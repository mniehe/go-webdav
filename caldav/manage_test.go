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

func mkcalendar(h *caldav.Handler, target, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("MKCALENDAR", target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/xml")
	} else {
		r.Body = http.NoBody
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func proppatch(h *caldav.Handler, target, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("PROPPATCH", target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestMkcalendarCreatesAConfiguredCalendar(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	body := `<?xml version="1.0"?>
<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="http://apple.com/ns/ical/">
  <D:set><D:prop>
    <D:displayname>Team events</D:displayname>
    <C:calendar-description>Shared team calendar</C:calendar-description>
    <A:calendar-color>#3B82F6FF</A:calendar-color>
    <C:supported-calendar-component-set><C:comp name="VEVENT"/></C:supported-calendar-component-set>
  </D:prop></D:set>
</C:mkcalendar>`

	w := mkcalendar(h, "/alice/team/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}
	// RFC 4791 §5.3.1: the response is uncacheable.
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}

	resp := propfind(t, h, "/alice/team/", "0", askFor(
		davName("displayname"), caldavName("calendar-description"),
		appleName("calendar-color"), caldavName("supported-calendar-component-set"))).
		at(t, "/alice/team/")
	if got := resp.value(t, davName("displayname")); got != "Team events" {
		t.Errorf("displayname = %q", got)
	}
	if got := resp.value(t, caldavName("calendar-description")); got != "Shared team calendar" {
		t.Errorf("calendar-description = %q", got)
	}
	if got := resp.value(t, appleName("calendar-color")); got != "#3B82F6FF" {
		t.Errorf("calendar-color = %q", got)
	}
	comps := resp.value(t, caldavName("supported-calendar-component-set"))
	if !strings.Contains(comps, "VEVENT") || strings.Contains(comps, "VTODO") {
		t.Errorf("component set = %q, want VEVENT only", comps)
	}

	// The component restriction is enforced, not just echoed.
	if w := put(h, "/alice/team/chore.ics", todoICS, nil); w.Code != http.StatusForbidden {
		t.Errorf("PUT of a refused kind: status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestMkcalendarWithoutABodyCreatesADefaultCalendar(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	if w := mkcalendar(h, "/alice/plain/", ""); w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}
	// An unconfigured calendar accepts everything.
	if w := put(h, "/alice/plain/chore.ics", todoICS, nil); w.Code != http.StatusCreated {
		t.Errorf("PUT into the new calendar: status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}
}

func TestMkcalendarOverAnExistingCalendarIsRefused(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	// RFC 4791 §5.3.1.1: the request URI must be unmapped, reported with the
	// DAV:resource-must-be-null precondition rather than a silent 201 that
	// reads as a successful create.
	w := mkcalendar(h, "/alice/work/", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if !strings.Contains(w.Body.String(), "resource-must-be-null") {
		t.Errorf("body = %q, want DAV:resource-must-be-null", w.Body.String())
	}
}

func TestMkcalendarTargetsOnlyACalendarPath(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	for _, target := range []string{"/alice/", "/alice/work/deep.ics"} {
		if w := mkcalendar(h, target, ""); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want %d", target, w.Code, http.StatusMethodNotAllowed)
		}
	}
}

// listOnly may enumerate the account's calendars but not add to them.
type listOnly struct{ *caldavmem.Store }

func (listOnly) AccountPermissions(context.Context, caldav.Actor, caldav.AccountID) (caldav.AccountPermissions, error) {
	return caldav.AccountPermissions{ListCalendars: true}, nil
}

func TestMkcalendarRequiresTheCreatePermission(t *testing.T) {
	h := handlerFor(t, listOnly{newStore(t)}, caldav.Config{})

	if w := mkcalendar(h, "/alice/new/", ""); w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestMkcalendarConcealsAForeignAccount(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	if w := mkcalendar(h, "/carol/new/", ""); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestMkcalendarNeedsACreatingBackend(t *testing.T) {
	h := handlerFor(t, readOnlyBackend{newStore(t)}, caldav.Config{})

	if w := mkcalendar(h, "/alice/new/", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestProppatchUpdatesCalendarSettings(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	body := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="http://apple.com/ns/ical/">
  <D:set><D:prop>
    <D:displayname>Renamed</D:displayname>
    <A:calendar-color>#FF0000</A:calendar-color>
    <A:calendar-order>7</A:calendar-order>
  </D:prop></D:set>
</D:propertyupdate>`

	w := proppatch(h, "/alice/work/", body)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}

	resp := propfind(t, h, "/alice/work/", "0", askFor(
		davName("displayname"), appleName("calendar-color"), appleName("calendar-order"))).
		at(t, "/alice/work/")
	if got := resp.value(t, davName("displayname")); got != "Renamed" {
		t.Errorf("displayname = %q", got)
	}
	if got := resp.value(t, appleName("calendar-color")); got != "#FF0000" {
		t.Errorf("calendar-color = %q", got)
	}
	if got := resp.value(t, appleName("calendar-order")); got != "7" {
		t.Errorf("calendar-order = %q", got)
	}
}

func TestProppatchRemoveClearsAProperty(t *testing.T) {
	store := newStore(t)
	h := handlerFor(t, store, caldav.Config{})

	set := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:set><D:prop><C:calendar-description>doomed</C:calendar-description></D:prop></D:set>
</D:propertyupdate>`
	if w := proppatch(h, "/alice/work/", set); w.Code != http.StatusMultiStatus {
		t.Fatalf("setting up: %d\n%s", w.Code, w.Body.String())
	}

	remove := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:remove><D:prop><C:calendar-description/></D:prop></D:remove>
</D:propertyupdate>`
	if w := proppatch(h, "/alice/work/", remove); w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}

	resp := propfind(t, h, "/alice/work/", "0", askFor(caldavName("calendar-description"))).at(t, "/alice/work/")
	if code, reported := resp.found(caldavName("calendar-description")); reported && code == http.StatusOK {
		t.Error("calendar-description survived its removal")
	}
}

func TestProppatchRemoveClearsCalendarOrder(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	set := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:A="http://apple.com/ns/ical/">
  <D:set><D:prop><A:calendar-order>7</A:calendar-order></D:prop></D:set>
</D:propertyupdate>`
	if w := proppatch(h, "/alice/work/", set); w.Code != http.StatusMultiStatus {
		t.Fatalf("setting up: %d\n%s", w.Code, w.Body.String())
	}

	remove := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:A="http://apple.com/ns/ical/">
  <D:remove><D:prop><A:calendar-order/></D:prop></D:remove>
</D:propertyupdate>`
	w := proppatch(h, "/alice/work/", remove)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "403") {
		t.Fatalf("body = %q, removing calendar-order was refused", w.Body.String())
	}

	resp := propfind(t, h, "/alice/work/", "0", askFor(appleName("calendar-order"))).at(t, "/alice/work/")
	if code, reported := resp.found(appleName("calendar-order")); reported && code == http.StatusOK {
		t.Error("calendar-order survived its removal")
	}
}

func TestProppatchIsAtomic(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	// RFC 4918 §9.2: instructions apply together or not at all. The dead
	// property is refused 403, and the displayname — valid on its own — must
	// report 424 and stay unapplied.
	body := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:X="http://example.invalid/">
  <D:set><D:prop>
    <D:displayname>Half-applied</D:displayname>
    <X:invented>x</X:invented>
  </D:prop></D:set>
</D:propertyupdate>`

	w := proppatch(h, "/alice/work/", body)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "424") {
		t.Errorf("body = %q, want a 424 Failed Dependency propstat", w.Body.String())
	}

	got := propfind(t, h, "/alice/work/", "0", askFor(davName("displayname"))).
		at(t, "/alice/work/").value(t, davName("displayname"))
	if got == "Half-applied" {
		t.Error("a failed PROPPATCH applied one of its instructions anyway")
	}
}

func TestProppatchRejectsAnInvalidColor(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	body := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:A="http://apple.com/ns/ical/">
  <D:set><D:prop><A:calendar-color>chartreuse</A:calendar-color></D:prop></D:set>
</D:propertyupdate>`

	w := proppatch(h, "/alice/work/", body)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "409") {
		t.Errorf("body = %q, want a 409 propstat for the malformed value", w.Body.String())
	}
	got := propfind(t, h, "/alice/work/", "0", allProp).at(t, "/alice/work/")
	if code, reported := got.found(appleName("calendar-color")); reported && code == http.StatusOK {
		t.Error("the malformed colour was applied")
	}
}

func TestProppatchTargetsOnlyACalendar(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, store, caldav.Config{})

	body := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>x</D:displayname></D:prop></D:set></D:propertyupdate>`
	for _, target := range []string{"/alice/", "/alice/work/standup.ics"} {
		if w := proppatch(h, target, body); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want %d", target, w.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestProppatchRequiresUpdateSettings(t *testing.T) {
	store := newStore(t)
	if err := store.Share(caldav.CalendarRef{Account: "carol", Calendar: caldav.MustSegment("work")}, "alice"); err != nil {
		t.Fatalf("sharing: %v", err)
	}
	h := handlerFor(t, store, caldav.Config{})

	body := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>mine now</D:displayname></D:prop></D:set></D:propertyupdate>`
	if w := proppatch(h, "/carol/work/", body); w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestProppatchNeedsAnUpdatingBackend(t *testing.T) {
	h := handlerFor(t, readOnlyBackend{newStore(t)}, caldav.Config{})

	body := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>x</D:displayname></D:prop></D:set></D:propertyupdate>`
	if w := proppatch(h, "/alice/work/", body); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestDeleteRemovesACalendarAndItsItems(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, store, caldav.Config{})

	if w := del(h, "/alice/work/", nil); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusNoContent, w.Body.String())
	}
	for _, target := range []string{"/alice/work/", "/alice/work/standup.ics"} {
		if w := do(h, http.MethodGet, target); w.Code != http.StatusNotFound {
			t.Errorf("%s after delete: status = %d, want %d", target, w.Code, http.StatusNotFound)
		}
	}
}

func TestDeleteCalendarRequiresItsOwnPermission(t *testing.T) {
	// An editor may change every item and still not delete the calendar.
	h := handlerFor(t, editorOnly{newStore(t)}, caldav.Config{})

	if w := del(h, "/alice/work/", nil); w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestDeleteCalendarNeedsADeletingBackend(t *testing.T) {
	h := handlerFor(t, readOnlyBackend{newStore(t)}, caldav.Config{})

	if w := del(h, "/alice/work/", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestDeleteOnAnAccountIsRefused(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	if w := del(h, "/alice/", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestMkcalendarHonoursTheSortOrder(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	// MKCALENDAR accepts calendar-order and answers 201, so the calendar has to
	// come back carrying it — a 201 for a property that was dropped tells the
	// client a write happened that did not.
	body := `<?xml version="1.0"?>
<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="http://apple.com/ns/ical/">
  <D:set><D:prop><A:calendar-order>7</A:calendar-order></D:prop></D:set>
</C:mkcalendar>`

	w := mkcalendar(h, "/alice/ordered/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}

	resp := propfind(t, h, "/alice/ordered/", "0", askFor(appleName("calendar-order"))).at(t, "/alice/ordered/")
	if got := resp.value(t, appleName("calendar-order")); got != "7" {
		t.Errorf("calendar-order = %q, want 7", got)
	}
}
