package carddav_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/mniehe/davkit/carddav"
	"github.com/mniehe/davkit/carddavmem"
)

const standupVCF = "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:standup\r\nFN:Stan Dupp\r\nEND:VCARD\r\n"

func seedItem(t *testing.T, store *carddavmem.Store, account carddav.AccountID, name string) {
	t.Helper()

	ref := carddav.ItemRef{
		Book: carddav.AddressBookRef{Account: account, Book: carddav.MustSegment("work")},
		Item: carddav.MustSegment(name),
	}
	req := carddav.StoreItemRequest{
		Content:   []byte(standupVCF),
		ContentID: "standup", // the UID inside standupVCF
		MayCreate: true,
	}
	if _, err := store.CompareAndStoreItem(context.Background(), ref, req); err != nil {
		t.Fatalf("seeding %s: %v", name, err)
	}
}

// everyMethod is the request surface a client can reach the adapter over. Every
// entry is built so that it arrives at the adapter rather than being refused by
// internal.Handler for a missing header or body, because the point of the drift
// test is what the adapter answers.
var everyMethod = map[string]func(target string) *http.Request{
	http.MethodOptions: func(t string) *http.Request { return httptest.NewRequest(http.MethodOptions, t, http.NoBody) },
	http.MethodGet:     func(t string) *http.Request { return httptest.NewRequest(http.MethodGet, t, http.NoBody) },
	http.MethodHead:    func(t string) *http.Request { return httptest.NewRequest(http.MethodHead, t, http.NoBody) },
	http.MethodPut: func(t string) *http.Request {
		return httptest.NewRequest(http.MethodPut, t, strings.NewReader(standupVCF))
	},
	http.MethodDelete: func(t string) *http.Request { return httptest.NewRequest(http.MethodDelete, t, http.NoBody) },
	"PROPFIND": func(t string) *http.Request {
		return xmlRequest("PROPFIND", t, `<?xml version="1.0"?><propfind xmlns="DAV:"><prop><displayname/></prop></propfind>`)
	},
	"PROPPATCH": func(t string) *http.Request {
		return xmlRequest("PROPPATCH", t, `<?xml version="1.0"?><propertyupdate xmlns="DAV:"><set><prop><displayname>x</displayname></prop></set></propertyupdate>`)
	},
	"MKCOL": func(t string) *http.Request { return httptest.NewRequest("MKCOL", t, http.NoBody) },

	"COPY": func(t string) *http.Request { return transferRequest("COPY", t) },
	"MOVE": func(t string) *http.Request { return transferRequest("MOVE", t) },
	"REPORT": func(t string) *http.Request {
		return xmlRequest("REPORT", t, `<?xml version="1.0"?><c:addressbook-query xmlns:c="urn:ietf:params:xml:ns:carddav"/>`)
	},
	"UNSUPPORTED": func(t string) *http.Request { return httptest.NewRequest("UNSUPPORTED", t, http.NoBody) },
}

func xmlRequest(method, target, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/xml")
	return r
}

func transferRequest(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, http.NoBody)
	r.Header.Set("Destination", "/alice/work/moved.ics")
	return r
}

// TestAllowMatchesWhatIsActuallyServed is the guard against the Allow header
// and the dispatch drifting apart. Advertising a method that answers 405, or
// serving one that is not advertised, are both failures here.
func TestAllowMatchesWhatIsActuallyServed(t *testing.T) {
	assertAllowMatchesDispatch(t, func() *carddav.Handler {
		store := newStore(t)
		seedItem(t, store, "alice", "standup.vcf")
		return handlerFor(t, store, carddav.Config{})
	}, "/alice/", "/alice/work/", "/alice/work/standup.vcf")
}

// assertAllowMatchesDispatch probes every method against a fresh handler, so a
// method with side effects — a MOVE, a calendar DELETE — cannot poison the
// verdict on the methods after it.
func assertAllowMatchesDispatch(t *testing.T, fresh func() *carddav.Handler, targets ...string) {
	t.Helper()

	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			w := httptest.NewRecorder()
			fresh().ServeHTTP(w, everyMethod[http.MethodOptions](target))
			if w.Code != http.StatusNoContent {
				t.Fatalf("OPTIONS status = %d, want %d", w.Code, http.StatusNoContent)
			}
			advertised := splitHeaderList(w.Header().Get("Allow"))

			for method, build := range everyMethod {
				if method == "UNSUPPORTED" {
					continue
				}
				got := httptest.NewRecorder()
				fresh().ServeHTTP(got, build(target))

				served := got.Code != http.StatusMethodNotAllowed
				if listed := slices.Contains(advertised, method); listed != served {
					t.Errorf("%s: Allow lists it = %v, but it is served = %v (status %d); Allow was %q",
						method, listed, served, got.Code, w.Header().Get("Allow"))
				}
			}
		})
	}
}

func TestOptionsAdvertisesAddressbookSupport(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, everyMethod[http.MethodOptions]("/alice/work/"))

	dav := splitHeaderList(w.Header().Get("DAV"))
	for _, want := range []string{"1", "3", "addressbook"} {
		if !slices.Contains(dav, want) {
			t.Errorf("DAV = %q, missing %q", w.Header().Get("DAV"), want)
		}
	}
}

func TestOptionsDoesNotRevealWhetherAnItemExists(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, store, carddav.Config{})

	// Allow is a property of the collection, not of what happens to be in it. If
	// it narrowed for an absent item, a client could enumerate a calendar by
	// guessing names without ever being allowed to read one.
	assertOptionsIndistinguishable(t, h, "/alice/work/standup.vcf", "/alice/work/absent.ics")
}

func TestOptionsDoesNotRevealAnotherAccountsCalendars(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	// carol's "work" exists and her "nowhere" does not. Alice has a share of
	// neither, so both must come back the same refusal.
	assertOptionsIndistinguishable(t, h, "/carol/work/", "/carol/nowhere/")
}

func assertOptionsIndistinguishable(t *testing.T, h *carddav.Handler, present, absent string) {
	t.Helper()

	existing := httptest.NewRecorder()
	h.ServeHTTP(existing, everyMethod[http.MethodOptions](present))
	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, everyMethod[http.MethodOptions](absent))

	if existing.Code != missing.Code {
		t.Errorf("status differs by existence: %s is %d, %s is %d", present, existing.Code, absent, missing.Code)
	}
	for _, header := range []string{"Allow", "DAV"} {
		if existing.Header().Get(header) != missing.Header().Get(header) {
			t.Errorf("%s differs by existence: %q vs %q",
				header, existing.Header().Get(header), missing.Header().Get(header))
		}
	}
}

func TestGetServesTheStoredBytes(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, store, carddav.Config{})

	w := do(h, http.MethodGet, "/alice/work/standup.vcf")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Body.String() != standupVCF {
		t.Errorf("body = %q, want the bytes that were stored", w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/vcard") {
		t.Errorf("Content-Type = %q, want text/vcard", got)
	}
	if w.Header().Get("ETag") == "" {
		t.Error("no ETag, so a client cannot make its next write conditional")
	}
}

func TestGetAnswersAConditionalRequest(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, store, carddav.Config{})

	etag := do(h, http.MethodGet, "/alice/work/standup.vcf").Header().Get("ETag")

	r := httptest.NewRequest(http.MethodGet, "/alice/work/standup.vcf", http.NoBody)
	r.Header.Set("If-None-Match", etag)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNotModified {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotModified)
	}
}

func TestGetScopesTheETagToTheCalendarIncarnation(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	seedItem(t, store, "carol", "standup.vcf")
	if err := store.Share(carddav.AddressBookRef{Account: "carol", Book: carddav.MustSegment("work")}, "alice"); err != nil {
		t.Fatalf("sharing: %v", err)
	}
	h := handlerFor(t, store, carddav.Config{})

	// Identical content at the same revision in two calendars. An entity tag
	// that did not carry the calendar's identity would be the same string, and a
	// conditional write meant for one would apply to the other.
	mine := do(h, http.MethodGet, "/alice/work/standup.vcf").Header().Get("ETag")
	theirs := do(h, http.MethodGet, "/carol/work/standup.vcf").Header().Get("ETag")

	if mine == "" || theirs == "" {
		t.Fatalf("missing an ETag: %q and %q", mine, theirs)
	}
	if mine == theirs {
		t.Errorf("both calendars issued the ETag %q", mine)
	}
}

func TestGetOnAMissingItemIsNotFound(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	if w := do(h, http.MethodGet, "/alice/work/gone.ics"); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestGetServesAnItemInASharedCalendar(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "carol", "standup.vcf")
	if err := store.Share(carddav.AddressBookRef{Account: "carol", Book: carddav.MustSegment("work")}, "alice"); err != nil {
		t.Fatalf("sharing: %v", err)
	}
	h := handlerFor(t, store, carddav.Config{})

	if w := do(h, http.MethodGet, "/carol/work/standup.vcf"); w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func splitHeaderList(value string) []string {
	if value == "" {
		return nil
	}
	fields := strings.Split(value, ",")
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
	}
	return fields
}
