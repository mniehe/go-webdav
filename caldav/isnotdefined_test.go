package caldav_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/mniehe/davkit/caldav"
)

const todoOnlyICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VTODO\r\nUID:chore\r\nDTSTAMP:20260801T000000Z\r\nSUMMARY:Chore\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"

// Two VCALENDAR objects concatenated into one PUT must be refused: the decoder
// stops at the first, so the second would be stored and served but invisible
// to every report, UID check and filter.
func TestPutRejectsMultipleCalendarObjects(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	first := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//EN\r\nBEGIN:VEVENT\r\nUID:first\r\nDTSTAMP:20260801T000000Z\r\nDTSTART:20260901T090000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	second := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//EN\r\nBEGIN:VEVENT\r\nUID:second\r\nDTSTAMP:20260801T000000Z\r\nDTSTART:20260902T090000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	w := put(h, "/alice/work/two.ics", first+second, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusForbidden, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "valid-calendar-data") {
		t.Errorf("body = %q, want the CALDAV:valid-calendar-data precondition", w.Body.String())
	}
	// A single object at the same name still succeeds.
	if w := put(h, "/alice/work/one.ics", first, nil); w.Code != http.StatusCreated {
		t.Errorf("single object: status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}
}

// comp-filter/is-not-defined must match a calendar object that lacks the named
// component and exclude one that has it. The old matcher made a name mismatch
// itself a positive result, so is-not-defined matched everything.
func TestQueryComponentIsNotDefined(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "event.ics", augustICS, "august") // has a VEVENT
	seedRaw(t, store, "alice", "todo.ics", todoOnlyICS, "chore") // has no VEVENT
	h := handlerFor(t, store, caldav.Config{})

	body := `<?xml version="1.0"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT">
    <C:is-not-defined/>
  </C:comp-filter></C:comp-filter></C:filter>
</C:calendar-query>`

	got := reportMS(t, h, "/alice/work/", body).hrefs()
	if len(got) != 1 || got[0] != "/alice/work/todo.ics" {
		t.Errorf("hrefs = %v, want only the object without a VEVENT", got)
	}
}

// A top-level is-not-defined on VCALENDAR matches nothing: every stored object
// is a VCALENDAR, so the component it asks to be absent is always present. This
// is the case matchCompFilter cannot reach — it exercises the guard in match
// itself.
func TestQueryTopLevelIsNotDefinedMatchesNothing(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "event.ics", augustICS, "august")
	h := handlerFor(t, store, caldav.Config{})

	body := `<?xml version="1.0"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"><C:is-not-defined/></C:comp-filter></C:filter>
</C:calendar-query>`

	if got := reportMS(t, h, "/alice/work/", body).hrefs(); len(got) != 0 {
		t.Errorf("hrefs = %v, want none: every object is a VCALENDAR", got)
	}
}

// The positive form still works: is-defined (no is-not-defined) matches only
// the object that has the component.
func TestQueryComponentIsDefined(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "event.ics", augustICS, "august")
	seedRaw(t, store, "alice", "todo.ics", todoOnlyICS, "chore")
	h := handlerFor(t, store, caldav.Config{})

	body := `<?xml version="1.0"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"/></C:comp-filter></C:filter>
</C:calendar-query>`

	got := reportMS(t, h, "/alice/work/", body).hrefs()
	if len(got) != 1 || got[0] != "/alice/work/event.ics" {
		t.Errorf("hrefs = %v, want only the object with a VEVENT", got)
	}
}
