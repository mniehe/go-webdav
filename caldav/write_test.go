package caldav_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mniehe/davkit/caldav"
	"github.com/mniehe/davkit/caldavmem"
)

const (
	reviewICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:review\r\nDTSTAMP:20260801T000000Z\r\n" +
		"DTSTART:20260902T100000Z\r\nSUMMARY:Review\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	todoICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VTODO\r\nUID:chore\r\nDTSTAMP:20260801T000000Z\r\n" +
		"SUMMARY:Chore\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
)

func put(h *caldav.Handler, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "text/calendar")
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func del(h *caldav.Handler, target string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodDelete, target, http.NoBody)
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPutCreatesAnItem(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	w := put(h, "/alice/work/review.ics", reviewICS, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}
	if w.Header().Get("ETag") == "" {
		t.Error("no ETag, so the client cannot make its next write conditional")
	}

	got := do(h, http.MethodGet, "/alice/work/review.ics")
	if got.Body.String() != reviewICS {
		t.Errorf("stored bytes differ from what was sent:\n%q", got.Body.String())
	}
}

func TestPutReplaceIsConditional(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, store, caldav.Config{})
	current := do(h, http.MethodGet, "/alice/work/standup.ics").Header().Get("ETag")

	replaced := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:standup.ics\r\nDTSTAMP:20260801T000000Z\r\n" +
		"DTSTART:20260901T093000Z\r\nSUMMARY:Standup moved\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	w := put(h, "/alice/work/standup.ics", replaced, map[string]string{"If-Match": current})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusNoContent, w.Body.String())
	}
	fresh := w.Header().Get("ETag")
	if fresh == "" || fresh == current {
		t.Errorf("ETag after replace = %q, want a new one (had %q)", fresh, current)
	}

	// The write moved the revision on, so the tag the client used is now stale
	// and the same request must not apply twice.
	if w := put(h, "/alice/work/standup.ics", replaced, map[string]string{"If-Match": current}); w.Code != http.StatusPreconditionFailed {
		t.Errorf("stale If-Match: status = %d, want %d", w.Code, http.StatusPreconditionFailed)
	}

	// The tag just issued has to work as the next If-Match, or the client is
	// stranded: it holds a validator no write will ever match.
	again := strings.Replace(replaced, "moved", "moved again", 1)
	if w := put(h, "/alice/work/standup.ics", again, map[string]string{"If-Match": fresh}); w.Code != http.StatusNoContent {
		t.Errorf("If-Match with the returned ETag: status = %d, want %d\n%s", w.Code, http.StatusNoContent, w.Body.String())
	}
}

func TestPutIfNoneMatchStarRefusesOverwrite(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, store, caldav.Config{})

	if w := put(h, "/alice/work/standup.ics", reviewICS, map[string]string{"If-None-Match": "*"}); w.Code != http.StatusPreconditionFailed {
		t.Errorf("over an existing item: status = %d, want %d", w.Code, http.StatusPreconditionFailed)
	}
	if w := put(h, "/alice/work/review.ics", reviewICS, map[string]string{"If-None-Match": "*"}); w.Code != http.StatusCreated {
		t.Errorf("at a free name: status = %d, want %d", w.Code, http.StatusCreated)
	}
}

func TestPutRejectsUnparseableContent(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	w := put(h, "/alice/work/junk.ics", "this is not icalendar", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if !strings.Contains(w.Body.String(), "valid-calendar-data") {
		t.Errorf("body = %q, want the CALDAV:valid-calendar-data precondition", w.Body.String())
	}
}

func TestPutRejectsAnObjectBreakingResourceRules(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	// RFC 4791 §4.1: one resource holds components of one kind, sharing one
	// UID, and never a METHOD — that is what separates stored data from a
	// scheduling message.
	tests := map[string]string{
		"a METHOD property": "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:a\r\nDTSTAMP:20260801T000000Z\r\n" +
			"DTSTART:20260901T090000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		"two component kinds": "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:a\r\nDTSTAMP:20260801T000000Z\r\n" +
			"DTSTART:20260901T090000Z\r\nEND:VEVENT\r\nBEGIN:VTODO\r\nUID:a\r\nDTSTAMP:20260801T000000Z\r\nSUMMARY:x\r\nEND:VTODO\r\nEND:VCALENDAR\r\n",
		"two UIDs": "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:a\r\nDTSTAMP:20260801T000000Z\r\n" +
			"DTSTART:20260901T090000Z\r\nEND:VEVENT\r\nBEGIN:VEVENT\r\nUID:b\r\nDTSTAMP:20260801T000000Z\r\n" +
			"DTSTART:20260902T090000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		"no components": "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nEND:VCALENDAR\r\n",
		"a missing UID": "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\n" +
			"DTSTART:20260901T090000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			w := put(h, "/alice/work/bad.ics", body, nil)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusForbidden, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "valid-calendar-object-resource") {
				t.Errorf("body = %q, want the CALDAV:valid-calendar-object-resource precondition", w.Body.String())
			}
		})
	}
}

func TestPutAcceptsRecurrenceOverridesSharingAUID(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	// A master plus an overridden instance is two VEVENTs with one UID — the
	// ordinary shape of an edited recurring event, not a rule violation.
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:weekly\r\nDTSTAMP:20260801T000000Z\r\n" +
		"DTSTART:20260901T090000Z\r\nRRULE:FREQ=WEEKLY\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:weekly\r\nDTSTAMP:20260801T000000Z\r\nRECURRENCE-ID:20260908T090000Z\r\n" +
		"DTSTART:20260908T100000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	if w := put(h, "/alice/work/weekly.ics", body, nil); w.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}
}

func TestPutRejectsAKindTheCalendarRefuses(t *testing.T) {
	store := newStore(t)
	req := caldav.CreateCalendarRequest{
		Name:    caldav.MustSegment("events"),
		Accepts: caldav.OnlyItemKinds(caldav.Event),
	}
	if _, err := store.CompareAndCreateCalendar(t.Context(), "alice", req, caldav.Unconditional()); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	h := handlerFor(t, store, caldav.Config{})

	w := put(h, "/alice/events/chore.ics", todoICS, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if !strings.Contains(w.Body.String(), "supported-calendar-component") {
		t.Errorf("body = %q, want the CALDAV:supported-calendar-component precondition", w.Body.String())
	}
}

func TestPutRefusesAUIDHeldByAnotherItem(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics") // holds UID "standup"
	h := handlerFor(t, store, caldav.Config{})

	w := put(h, "/alice/work/other.ics", eventICS, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusConflict, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no-uid-conflict") {
		t.Errorf("body = %q, want the CALDAV:no-uid-conflict precondition", w.Body.String())
	}
}

func TestPutRejectsAForeignContentType(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	r := httptest.NewRequest(http.MethodPut, "/alice/work/review.ics", strings.NewReader(reviewICS))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnsupportedMediaType)
	}
}

func TestPutBoundsTheBody(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	oversized := "BEGIN:VCALENDAR\r\n" + strings.Repeat("X-PAD:y\r\n", (10<<20)/9+1)
	w := put(h, "/alice/work/huge.ics", oversized, nil)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
	if !strings.Contains(w.Body.String(), "max-resource-size") {
		t.Errorf("body = %q, want the CALDAV:max-resource-size precondition", w.Body.String())
	}
}

func TestPutRequiresWritePermission(t *testing.T) {
	store := newStore(t)
	if err := store.Share(caldav.CalendarRef{Account: "carol", Calendar: caldav.MustSegment("work")}, "alice"); err != nil {
		t.Fatalf("sharing: %v", err)
	}
	h := handlerFor(t, store, caldav.Config{})

	// The share is view-only: alice can see the calendar, so the refusal is an
	// honest 403 rather than a concealing 404.
	if w := put(h, "/carol/work/review.ics", reviewICS, nil); w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestPutOnACollectionIsRefused(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	for _, target := range []string{"/alice/", "/alice/work/"} {
		if w := put(h, target, reviewICS, nil); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want %d", target, w.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestWritesNeedAWritingBackend(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, readOnlyBackend{store}, caldav.Config{})

	if w := put(h, "/alice/work/review.ics", reviewICS, nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT: status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
	if w := del(h, "/alice/work/standup.ics", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestAllowShrinksWithTheBackendsCapabilities(t *testing.T) {
	assertAllowMatchesDispatch(t, func() *caldav.Handler {
		store := newStore(t)
		seedItem(t, store, "alice", "standup.ics")
		return handlerFor(t, readOnlyBackend{store}, caldav.Config{})
	}, "/alice/", "/alice/work/", "/alice/work/standup.ics")
}

func TestDeleteRemovesAnItem(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, store, caldav.Config{})

	if w := del(h, "/alice/work/standup.ics", nil); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusNoContent, w.Body.String())
	}
	if w := do(h, http.MethodGet, "/alice/work/standup.ics"); w.Code != http.StatusNotFound {
		t.Errorf("after delete: GET = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestDeleteIsConditional(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, store, caldav.Config{})
	current := do(h, http.MethodGet, "/alice/work/standup.ics").Header().Get("ETag")

	stale := strings.Replace(current, `-`, `-f`, 1)
	if w := del(h, "/alice/work/standup.ics", map[string]string{"If-Match": stale}); w.Code != http.StatusPreconditionFailed {
		t.Errorf("stale If-Match: status = %d, want %d", w.Code, http.StatusPreconditionFailed)
	}
	if w := do(h, http.MethodGet, "/alice/work/standup.ics"); w.Code != http.StatusOK {
		t.Errorf("the refused delete removed the item anyway: GET = %d", w.Code)
	}

	if w := del(h, "/alice/work/standup.ics", map[string]string{"If-Match": current}); w.Code != http.StatusNoContent {
		t.Errorf("current If-Match: status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

func TestDeleteOfAMissingItemIsNotFound(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	if w := del(h, "/alice/work/gone.ics", nil); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestDeleteRequiresPermission(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "carol", "standup.ics")
	if err := store.Share(caldav.CalendarRef{Account: "carol", Calendar: caldav.MustSegment("work")}, "alice"); err != nil {
		t.Fatalf("sharing: %v", err)
	}
	h := handlerFor(t, store, caldav.Config{})

	if w := del(h, "/carol/work/standup.ics", nil); w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if w := do(h, http.MethodGet, "/carol/work/standup.ics"); w.Code != http.StatusOK {
		t.Errorf("the refused delete removed the item anyway: GET = %d", w.Code)
	}
}

// createOnly may add items but never replace one. Which of the two applies
// depends on whether the target exists — a fact only the backend's transaction
// knows — so this grant is what proves the handler hands both flags over
// faithfully instead of collapsing them.
type createOnly struct{ *caldavmem.Store }

func (createOnly) CalendarPermissions(context.Context, caldav.Actor, caldav.CalendarRef) (caldav.CalendarPermissions, error) {
	return caldav.CalendarPermissions{ViewDetails: true, CreateItems: true}, nil
}

func TestPutPermissionIsSelectedByExistence(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, createOnly{store}, caldav.Config{})

	if w := put(h, "/alice/work/review.ics", reviewICS, nil); w.Code != http.StatusCreated {
		t.Errorf("creating: status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}
	replaced := strings.Replace(eventICS, "Standup", "Standup moved", 1)
	if w := put(h, "/alice/work/standup.ics", replaced, nil); w.Code != http.StatusForbidden {
		t.Errorf("replacing: status = %d, want %d", w.Code, http.StatusForbidden)
	}
}
