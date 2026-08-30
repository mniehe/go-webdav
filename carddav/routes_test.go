package carddav

import (
	"context"
	"errors"
	"testing"
)

func TestDefaultRoutesParse(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		prefix string
		path   string
		want   Resource
	}{
		{"an account", "", "/alice/", AccountResource("alice")},
		{"an account without its trailing slash", "", "/alice", AccountResource("alice")},
		{"an address book", "", "/alice/work/", AddressBookResource(AddressBookRef{"alice", MustSegment("work")})},
		{"an address book without its trailing slash", "", "/alice/work", AddressBookResource(AddressBookRef{"alice", MustSegment("work")})},
		{"an item", "", "/alice/work/mtg.ics", ItemResource(ItemRef{AddressBookRef{"alice", MustSegment("work")}, MustSegment("mtg.ics")})},
		{"an escaped name", "", "/alice/Work%20&%20Home/", AddressBookResource(AddressBookRef{"alice", MustSegment("Work & Home")})},
		{"a non-ASCII name", "", "/alice/caf%C3%A9/", AddressBookResource(AddressBookRef{"alice", MustSegment("café")})},
		{"under a prefix", "/dav", "/dav/alice/work/", AddressBookResource(AddressBookRef{"alice", MustSegment("work")})},
		{"a prefix given without its slash", "dav", "/dav/alice/", AccountResource("alice")},
		{"a prefix given with a trailing slash", "/dav/", "/dav/alice/", AccountResource("alice")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DefaultRoutes(tc.prefix).Parse(ctx, tc.path)
			if err != nil {
				t.Fatalf("Parse(%q) = %v", tc.path, err)
			}
			if got != tc.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tc.path, got, tc.want)
			}
		})
	}
}

func TestDefaultRoutesRejects(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		prefix string
		path   string
	}{
		{"the root", "", "/"},
		{"an empty path", "", ""},
		{"a relative path", "", "alice/work/"},
		{"a path deeper than an item", "", "/alice/work/mtg.ics/attachment"},
		{"a trailing slash on an item", "", "/alice/work/mtg.ics/"},
		{"an empty segment", "", "/alice//work/"},
		{"a dot segment", "", "/alice/./work/"},
		{"an address book named by a dot segment", "", "/alice/./"},
		{"an account named by a parent segment", "", "/../alice/"},
		{"a parent segment", "", "/alice/../bob/"},

		// The one that matters. net/http hands ServeHTTP a decoded URL.Path, so
		// parsing that instead of the escaped form would split this into three
		// segments and answer with an item of an address book nobody named.
		{"a slash smuggled into a segment", "", "/alice/work%2Fevil/"},
		{"a slash smuggled into an account", "", "/alice%2Fwork/"},
		{"an encoded parent segment", "", "/alice/%2E%2E/bob/"},
		{"an encoded null byte", "", "/alice/wo%00rk/"},
		{"a malformed escape", "", "/alice/wo%zzrk/"},

		{"a path outside the prefix", "/dav", "/alice/work/"},
		{"a prefix matched mid-segment", "/dav", "/davx/alice/work/"},
		{"a prefix matched mid-segment at account depth", "/dav", "/davx/alice/"},
		{"the prefix alone", "/dav", "/dav/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DefaultRoutes(tc.prefix).Parse(ctx, tc.path)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("Parse(%q) = %+v, %v; want ErrNotFound", tc.path, got, err)
			}
		})
	}
}

func TestDefaultRoutesRoundTrip(t *testing.T) {
	ctx := context.Background()

	awkward := []string{
		"work", "Work & Home", "café", "a.b", "100%", "a b", "a+b", "a?b", "a#b",
		"a;b", "a:b", "a=b", "~tilde", "mtg.ics",
	}

	for _, prefix := range []string{"", "/dav", "/a/b"} {
		routes := DefaultRoutes(prefix)
		for _, name := range awkward {
			t.Run(prefix+"/"+name, func(t *testing.T) {
				seg := MustSegment(name)
				for _, want := range []Resource{
					AccountResource(AccountID(name)),
					AddressBookResource(AddressBookRef{AccountID(name), seg}),
					ItemResource(ItemRef{AddressBookRef{AccountID(name), seg}, seg}),
				} {
					href, err := routes.Href(ctx, want)
					if err != nil {
						t.Fatalf("Href(%+v) = %v", want, err)
					}
					got, err := routes.Parse(ctx, href)
					if err != nil {
						t.Fatalf("Parse(%q), which Href produced for %+v: %v", href, want, err)
					}
					if got != want {
						t.Errorf("Href then Parse of %+v gave %+v via %q", want, got, href)
					}
				}
			})
		}
	}
}

func TestDefaultRoutesHrefShape(t *testing.T) {
	ctx := context.Background()
	routes := DefaultRoutes("/dav")
	ref := AddressBookRef{Account: "alice", Book: MustSegment("work")}

	for _, tc := range []struct {
		res  Resource
		want string
	}{
		{AccountResource("alice"), "/dav/alice/"},
		{AddressBookResource(ref), "/dav/alice/work/"},
		{ItemResource(ItemRef{ref, MustSegment("mtg.ics")}), "/dav/alice/work/mtg.ics"},
	} {
		got, err := routes.Href(ctx, tc.res)
		if err != nil {
			t.Fatalf("Href(%+v) = %v", tc.res, err)
		}
		if got != tc.want {
			t.Errorf("Href(%+v) = %q, want %q — collections carry a trailing slash, items do not", tc.res, got, tc.want)
		}
	}
}

func TestDefaultRoutesHrefRefusesUnrenderable(t *testing.T) {
	ctx := context.Background()
	routes := DefaultRoutes("")
	ref := AddressBookRef{Account: "alice", Book: MustSegment("work")}

	for _, tc := range []struct {
		name string
		res  Resource
	}{
		{"an account that is not a segment", AccountResource("alice/bob")},
		{"an empty account", AccountResource("")},
		{"an account with a control character", AccountResource("alice\n")},
		{"an address book resource with no address book", Resource{Kind: KindAddressBook, Account: "alice"}},
		{"an item resource with no item", Resource{Kind: KindItem, Account: "alice", Book: ref.Book}},
		{"the zero resource", Resource{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if href, err := routes.Href(ctx, tc.res); err == nil {
				t.Errorf("Href(%+v) = %q, want an error", tc.res, href)
			}
		})
	}
}
