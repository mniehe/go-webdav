package carddav

import (
	"errors"
	"fmt"
	"testing"
)

var (
	workScope     = bookScope("book-1")
	personalScope = bookScope("book-2")
)

func TestCalendarScopeFollowsTheIncarnation(t *testing.T) {
	if workScope == personalScope {
		t.Fatal("two address books share a scope")
	}
	if bookScope("book-1") != workScope {
		t.Error("the same address book hashed to two different scopes")
	}

	// The point of scoping by ID rather than by owner and name: a address book
	// deleted and recreated at the same reference is a different address book, its
	// revisions start over, and validators issued before the deletion must stop
	// matching.
	recreated := bookScope("book-3")
	if recreated == workScope {
		t.Error("a recreated address book reused the deleted one's scope, so a stale If-Match still matches")
	}
}

func TestETagRoundTrip(t *testing.T) {
	item := Item{Name: MustSegment("mtg.ics"), Revision: 42, Content: []byte("BEGIN:VCALENDAR")}

	tag := etagFor(workScope, item).String()
	if tag[0] != '"' || tag[len(tag)-1] != '"' {
		t.Fatalf("etagFor produced %s, which is not a quoted entity tag", tag)
	}

	rev, ok := parseETag(workScope, tag)
	if !ok {
		t.Fatalf("parseETag rejected %s, which etagFor just produced", tag)
	}
	if rev != item.Revision {
		t.Errorf("parseETag recovered revision %d, want %d", rev, item.Revision)
	}

	if _, ok := parseETag(personalScope, tag); ok {
		t.Error("a tag issued for one address book parsed against another; a client copying one across would silently satisfy a precondition")
	}
}

func TestETagChangesWithTheRevision(t *testing.T) {
	body := []byte("BEGIN:VCALENDAR")
	first := etagFor(workScope, Item{Revision: 1, Content: body})
	second := etagFor(workScope, Item{Revision: 2, Content: body})
	if first == second {
		t.Error("two revisions of an item share an entity tag, so a client would never refetch it")
	}
}

func TestETagFallsBackToTheContentHash(t *testing.T) {
	first := etagFor(workScope, Item{Content: []byte("one")}).String()
	second := etagFor(workScope, Item{Content: []byte("two")}).String()

	if first == second {
		t.Error("a revisionless backend gave two different bodies the same tag")
	}
	if _, ok := parseETag(workScope, first); ok {
		t.Error("a content hash parsed back as a revision")
	}
	if again := etagFor(workScope, Item{Content: []byte("one")}).String(); again != first {
		t.Error("the same body hashed to two different tags")
	}
}

func TestParseETagRejects(t *testing.T) {
	valid := etagFor(workScope, Item{Revision: 42}).String()

	for _, tc := range []struct {
		name string
		tag  string
	}{
		{"empty", ""},
		{"unquoted", valid[1 : len(valid)-1]},
		{"weak", "W/" + valid},
		{"a bare quote", `"`},
		{"no revision", `"` + scopeText(workScope) + `"`},
		{"a revision that is not hex", `"` + scopeText(workScope) + `-zz"`},
		{"a foreign scope", `"0000000000000000-2a"`},
		{"another server's tag", `"686897696a7c876b7e"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rev, ok := parseETag(workScope, tc.tag); ok {
				t.Errorf("parseETag(%q) = %d, true; want it rejected", tc.tag, rev)
			}
		})
	}
}

func TestPreconditionsFromHeaders(t *testing.T) {
	ours := etagFor(workScope, Item{Revision: 42}).String()
	other := etagFor(workScope, Item{Revision: 43}).String()
	foreign := etagFor(personalScope, Item{Revision: 42}).String()

	at42, at43 := Revision(42), Revision(43)

	for _, tc := range []struct {
		name        string
		ifMatch     string
		ifNoneMatch string
		// pass lists the states the write must be allowed in, fail the states it
		// must not. nil stands for a missing target.
		pass []*Revision
		fail []*Revision
	}{
		{"no headers", "", "", []*Revision{nil, &at42, &at43}, nil},
		{"If-Match star", "*", "", []*Revision{&at42}, []*Revision{nil}},
		{"If-None-Match star", "", "*", []*Revision{nil}, []*Revision{&at42}},
		{"If-Match our tag", ours, "", []*Revision{&at42}, []*Revision{nil, &at43}},
		{"If-Match a list", ours + ", " + other, "", []*Revision{&at42, &at43}, []*Revision{nil}},
		{"If-None-Match our tag", "", ours, []*Revision{nil, &at43}, []*Revision{&at42}},

		// A tag we never issued must not be quietly ignored. For If-Match that
		// means nothing satisfies it; for If-None-Match, everything does.
		{"If-Match a foreign tag", foreign, "", nil, []*Revision{nil, &at42, &at43}},
		{"If-Match a weak tag", "W/" + ours, "", nil, []*Revision{nil, &at42}},
		{"If-None-Match a foreign tag", "", foreign, []*Revision{nil, &at42, &at43}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pre, err := preconditionsFrom(workScope, tc.ifMatch, tc.ifNoneMatch)
			if err != nil {
				t.Fatalf("preconditionsFrom: %v", err)
			}
			for _, current := range tc.pass {
				if err := pre.Check(current); err != nil {
					t.Errorf("Check(%s) = %v, want it to pass", describe(current), err)
				}
			}
			for _, current := range tc.fail {
				if err := pre.Check(current); !errors.Is(err, ErrPreconditionFailed) {
					t.Errorf("Check(%s) = %v, want ErrPreconditionFailed", describe(current), err)
				}
			}
		})
	}
}

func TestPreconditionsRefusesBothHeaders(t *testing.T) {
	ours := etagFor(workScope, Item{Revision: 42}).String()
	if _, err := preconditionsFrom(workScope, ours, "*"); err == nil {
		t.Error("If-Match and If-None-Match together were accepted; Preconditions cannot express the conjunction, so one of the two guards would be dropped")
	}
}

func TestSyncTokenRoundTrip(t *testing.T) {
	token := syncTokenFor(workScope, 42)

	rev, ok := parseSyncToken(workScope, token)
	if !ok {
		t.Fatalf("parseSyncToken rejected %q, which syncTokenFor just produced", token)
	}
	if rev != 42 {
		t.Errorf("recovered revision %d, want 42", rev)
	}

	if _, ok := parseSyncToken(personalScope, token); ok {
		t.Error("a token issued for one address book parsed against another, so a client could sync one address book from another's position")
	}
}

func TestParseSyncTokenRejects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token string
	}{
		{"empty, meaning initial sync", ""},
		{"another server's token", "http://example.test/ns/sync/42"},
		{"the prefix alone", syncURNPrefix},
		{"no revision", syncURNPrefix + scopeText(workScope)},
		{"a revision that is not hex", syncURNPrefix + scopeText(workScope) + ":zz"},
		{"a foreign scope", syncURNPrefix + "0000000000000000:2a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rev, ok := parseSyncToken(workScope, tc.token); ok {
				t.Errorf("parseSyncToken(%q) = %d, true; want it rejected", tc.token, rev)
			}
		})
	}
}

func describe(rev *Revision) string {
	if rev == nil {
		return "a missing target"
	}
	return fmt.Sprintf("revision %d", *rev)
}
