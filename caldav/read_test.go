package caldav_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/mniehe/davkit/caldav"
	"github.com/mniehe/davkit/caldavmem"
)

const eventICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:standup\r\nDTSTAMP:20260801T000000Z\r\n" +
	"DTSTART:20260901T090000Z\r\nSUMMARY:Standup\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func seedItem(t *testing.T, store *caldavmem.Store, account caldav.AccountID, name string) {
	t.Helper()

	ref := caldav.ItemRef{
		Calendar: caldav.CalendarRef{Account: account, Calendar: caldav.MustSegment("work")},
		Item:     caldav.MustSegment(name),
	}
	req := caldav.StoreItemRequest{
		Content:   []byte(eventICS),
		ContentID: "standup", // the UID inside eventICS
		Kind:      caldav.Event,
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
		return httptest.NewRequest(http.MethodPut, t, strings.NewReader(eventICS))
	},
	http.MethodDelete: func(t string) *http.Request { return httptest.NewRequest(http.MethodDelete, t, http.NoBody) },
	"PROPFIND": func(t string) *http.Request {
		return xmlRequest("PROPFIND", t, `<?xml version="1.0"?><propfind xmlns="DAV:"><prop><displayname/></prop></propfind>`)
	},
	"PROPPATCH": func(t string) *http.Request {
		return xmlRequest("PROPPATCH", t, `<?xml version="1.0"?><propertyupdate xmlns="DAV:"><set><prop><displayname>x</displayname></prop></set></propertyupdate>`)
	},
	"MKCOL":      func(t string) *http.Request { return httptest.NewRequest("MKCOL", t, http.NoBody) },
	"MKCALENDAR": func(t string) *http.Request { return httptest.NewRequest("MKCALENDAR", t, http.NoBody) },
	"COPY":       func(t string) *http.Request { return transferRequest("COPY", t) },
	"MOVE":       func(t string) *http.Request { return transferRequest("MOVE", t) },
	"REPORT": func(t string) *http.Request {
		return xmlRequest("REPORT", t, `<?xml version="1.0"?><c:calendar-query xmlns:c="urn:ietf:params:xml:ns:caldav"/>`)
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
	assertAllowMatchesDispatch(t, func() *caldav.Handler {
		store := newStore(t)
		seedItem(t, store, "alice", "standup.ics")
		return handlerFor(t, store, caldav.Config{})
	}, "/alice/", "/alice/work/", "/alice/work/standup.ics")
}

// assertAllowMatchesDispatch probes every method against a fresh handler, so a
// method with side effects — a MOVE, a calendar DELETE — cannot poison the
// verdict on the methods after it.
func assertAllowMatchesDispatch(t *testing.T, fresh func() *caldav.Handler, targets ...string) {
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

func TestOptionsAdvertisesCalendarAccess(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, everyMethod[http.MethodOptions]("/alice/work/"))

	dav := splitHeaderList(w.Header().Get("DAV"))
	for _, want := range []string{"1", "3", "calendar-access"} {
		if !slices.Contains(dav, want) {
			t.Errorf("DAV = %q, missing %q", w.Header().Get("DAV"), want)
		}
	}
}

func TestOptionsDoesNotRevealWhetherAnItemExists(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, store, caldav.Config{})

	// Allow is a property of the collection, not of what happens to be in it. If
	// it narrowed for an absent item, a client could enumerate a calendar by
	// guessing names without ever being allowed to read one.
	assertOptionsIndistinguishable(t, h, "/alice/work/standup.ics", "/alice/work/absent.ics")
}

func TestOptionsDoesNotRevealAnotherAccountsCalendars(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	// carol's "work" exists and her "nowhere" does not. Alice has a share of
	// neither, so both must come back the same refusal.
	assertOptionsIndistinguishable(t, h, "/carol/work/", "/carol/nowhere/")
}

func assertOptionsIndistinguishable(t *testing.T, h *caldav.Handler, present, absent string) {
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
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, store, caldav.Config{})

	w := do(h, http.MethodGet, "/alice/work/standup.ics")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Body.String() != eventICS {
		t.Errorf("body = %q, want the bytes that were stored", w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/calendar") {
		t.Errorf("Content-Type = %q, want text/calendar", got)
	}
	if w.Header().Get("ETag") == "" {
		t.Error("no ETag, so a client cannot make its next write conditional")
	}
}

func TestGetAnswersAConditionalRequest(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, store, caldav.Config{})

	etag := do(h, http.MethodGet, "/alice/work/standup.ics").Header().Get("ETag")

	r := httptest.NewRequest(http.MethodGet, "/alice/work/standup.ics", http.NoBody)
	r.Header.Set("If-None-Match", etag)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNotModified {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotModified)
	}
}

func TestGetScopesTheETagToTheCalendarIncarnation(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	seedItem(t, store, "carol", "standup.ics")
	if err := store.Share(caldav.CalendarRef{Account: "carol", Calendar: caldav.MustSegment("work")}, "alice"); err != nil {
		t.Fatalf("sharing: %v", err)
	}
	h := handlerFor(t, store, caldav.Config{})

	// Identical content at the same revision in two calendars. An entity tag
	// that did not carry the calendar's identity would be the same string, and a
	// conditional write meant for one would apply to the other.
	mine := do(h, http.MethodGet, "/alice/work/standup.ics").Header().Get("ETag")
	theirs := do(h, http.MethodGet, "/carol/work/standup.ics").Header().Get("ETag")

	if mine == "" || theirs == "" {
		t.Fatalf("missing an ETag: %q and %q", mine, theirs)
	}
	if mine == theirs {
		t.Errorf("both calendars issued the ETag %q", mine)
	}
}

func TestGetOnAMissingItemIsNotFound(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	if w := do(h, http.MethodGet, "/alice/work/gone.ics"); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestGetServesAnItemInASharedCalendar(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "carol", "standup.ics")
	if err := store.Share(caldav.CalendarRef{Account: "carol", Calendar: caldav.MustSegment("work")}, "alice"); err != nil {
		t.Fatalf("sharing: %v", err)
	}
	h := handlerFor(t, store, caldav.Config{})

	if w := do(h, http.MethodGet, "/carol/work/standup.ics"); w.Code != http.StatusOK {
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

// availabilityOnly grants what a free-busy share grants: the actor may learn
// that the calendar is busy, never what the items say. caldavmem has no way to
// express that grant, and a permission check nothing exercises is one that can
// be deleted without a test noticing.
type availabilityOnly struct{ *caldavmem.Store }

func (availabilityOnly) CalendarPermissions(context.Context, caldav.Actor, caldav.CalendarRef) (caldav.CalendarPermissions, error) {
	return caldav.AvailabilityOnlyPermissions(), nil
}

func TestGetRefusesAnActorWhoMayOnlySeeBusyTimes(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, availabilityOnly{store}, caldav.Config{})

	w := do(h, http.MethodGet, "/alice/work/standup.ics")
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d: the stored bytes say what the event is", w.Code, http.StatusForbidden)
	}
	if strings.Contains(w.Body.String(), "Standup") {
		t.Error("the event's summary reached an actor allowed to see only busy times")
	}
}
