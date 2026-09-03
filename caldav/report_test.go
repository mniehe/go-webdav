package caldav_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mniehe/davkit/caldav"
	"github.com/mniehe/davkit/caldavmem"
)

const (
	augustICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:august\r\nDTSTAMP:20260801T000000Z\r\n" +
		"DTSTART:20260810T090000Z\r\nDTEND:20260810T100000Z\r\nSUMMARY:August\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	octoberICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:october\r\nDTSTAMP:20260801T000000Z\r\n" +
		"DTSTART:20261010T090000Z\r\nDTEND:20261010T100000Z\r\nSUMMARY:October\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	weeklyICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:weekly\r\nDTSTAMP:20260801T000000Z\r\n" +
		"DTSTART:20260803T090000Z\r\nDTEND:20260803T093000Z\r\nRRULE:FREQ=WEEKLY\r\nSUMMARY:Weekly\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
)

func seedRaw(t *testing.T, store *caldavmem.Store, account caldav.AccountID, name, ics, uid string) {
	t.Helper()

	ref := caldav.ItemRef{
		Calendar: caldav.CalendarRef{Account: account, Calendar: caldav.MustSegment("work")},
		Item:     caldav.MustSegment(name),
	}
	req := caldav.StoreItemRequest{Content: []byte(ics), ContentID: uid, Kind: caldav.Event, MayCreate: true}
	if _, err := store.CompareAndStoreItem(context.Background(), ref, req); err != nil {
		t.Fatalf("seeding %s: %v", name, err)
	}
}

func report(t *testing.T, h *caldav.Handler, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	r := httptest.NewRequest("REPORT", target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func reportMS(t *testing.T, h *caldav.Handler, target, body string) multistatus {
	t.Helper()

	w := report(t, h, target, body)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("REPORT %s: status = %d, want %d\n%s", target, w.Code, http.StatusMultiStatus, w.Body.String())
	}
	var ms multistatus
	if err := xml.Unmarshal(w.Body.Bytes(), &ms); err != nil {
		t.Fatalf("decoding multistatus: %v\n%s", err, w.Body.String())
	}
	return ms
}

func timeRangeQuery(start, end string) string {
	return `<?xml version="1.0"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/><C:calendar-data/></D:prop>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VEVENT">
        <C:time-range start="` + start + `" end="` + end + `"/>
      </C:comp-filter>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`
}

func TestQueryMatchesByTimeRange(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "august.ics", augustICS, "august")
	seedRaw(t, store, "alice", "october.ics", octoberICS, "october")
	h := handlerFor(t, store, caldav.Config{})

	ms := reportMS(t, h, "/alice/work/", timeRangeQuery("20260801T000000Z", "20260901T000000Z"))

	if got := ms.hrefs(); len(got) != 1 || got[0] != "/alice/work/august.ics" {
		t.Fatalf("hrefs = %v, want only the August event", got)
	}
	resp := ms.at(t, "/alice/work/august.ics")
	if got := resp.value(t, davName("getetag")); got == "" {
		t.Error("no getetag on a query result")
	}
	if got := resp.value(t, caldavName("calendar-data")); !strings.Contains(got, "UID:august") {
		t.Errorf("calendar-data = %q, want the stored event", got)
	}
}

func TestQueryWithNoMatchesIsAnEmptyMultistatus(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "august.ics", augustICS, "august")
	h := handlerFor(t, store, caldav.Config{})

	ms := reportMS(t, h, "/alice/work/", timeRangeQuery("20270101T000000Z", "20270201T000000Z"))
	if len(ms.Responses) != 0 {
		t.Errorf("hrefs = %v, want none", ms.hrefs())
	}
}

func TestQueryExpandsRecurrencesInResponse(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "weekly.ics", weeklyICS, "weekly")
	h := handlerFor(t, store, caldav.Config{})

	body := `<?xml version="1.0"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <C:calendar-data>
      <C:expand start="20260801T000000Z" end="20260822T000000Z"/>
    </C:calendar-data>
  </D:prop>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VEVENT">
        <C:time-range start="20260801T000000Z" end="20260822T000000Z"/>
      </C:comp-filter>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`

	data := reportMS(t, h, "/alice/work/", body).
		at(t, "/alice/work/weekly.ics").value(t, caldavName("calendar-data"))

	// Three Mondays fall inside the window, so expansion replaces the rule with
	// three instances.
	if got := strings.Count(data, "BEGIN:VEVENT"); got != 3 {
		t.Errorf("expanded to %d instances, want 3:\n%s", got, data)
	}
	if strings.Contains(data, "RRULE") {
		t.Error("an expanded response must not carry the recurrence rule")
	}
}

func TestQueryExpansionDoesNotCorruptTheStoredItem(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "weekly.ics", weeklyICS, "weekly")
	h := handlerFor(t, store, caldav.Config{})

	body := `<?xml version="1.0"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <C:calendar-data>
      <C:expand start="20260801T000000Z" end="20260822T000000Z"/>
    </C:calendar-data>
  </D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"/></C:filter>
</C:calendar-query>`
	reportMS(t, h, "/alice/work/", body)

	// The expansion worked on the only parsed copy there is. A GET afterwards
	// must still serve the recurrence rule, not the flattened instances.
	if got := do(h, http.MethodGet, "/alice/work/weekly.ics").Body.String(); got != weeklyICS {
		t.Errorf("stored bytes changed after an expanding query:\n%q", got)
	}
}

func TestQueryProjectsRequestedProperties(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "august.ics", augustICS, "august")
	h := handlerFor(t, store, caldav.Config{})

	body := `<?xml version="1.0"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <C:calendar-data>
      <C:comp name="VCALENDAR">
        <C:comp name="VEVENT"><C:prop name="UID"/></C:comp>
      </C:comp>
    </C:calendar-data>
  </D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"/></C:filter>
</C:calendar-query>`

	data := reportMS(t, h, "/alice/work/", body).
		at(t, "/alice/work/august.ics").value(t, caldavName("calendar-data"))

	if !strings.Contains(data, "UID:august") {
		t.Errorf("calendar-data = %q, want the requested UID", data)
	}
	if strings.Contains(data, "SUMMARY") {
		t.Errorf("calendar-data = %q, includes a property the projection excluded", data)
	}
}

// overriddenWeeklyICS is a weekly Monday event with three overridden instances:
// the 10 Aug occurrence moved within August, the 17 Aug occurrence moved out to
// December, and the 5 Oct occurrence moved within October.
const overriddenWeeklyICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:weekly\r\nDTSTAMP:20260801T000000Z\r\n" +
	"DTSTART:20260803T090000Z\r\nDTEND:20260803T093000Z\r\nRRULE:FREQ=WEEKLY\r\nSUMMARY:Weekly\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:weekly\r\nRECURRENCE-ID:20260810T090000Z\r\nDTSTAMP:20260801T000000Z\r\n" +
	"DTSTART:20260810T110000Z\r\nDTEND:20260810T113000Z\r\nSUMMARY:MovedWithinAugust\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:weekly\r\nRECURRENCE-ID:20260817T090000Z\r\nDTSTAMP:20260801T000000Z\r\n" +
	"DTSTART:20261201T090000Z\r\nDTEND:20261201T093000Z\r\nSUMMARY:MovedToDecember\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:weekly\r\nRECURRENCE-ID:20261005T090000Z\r\nDTSTAMP:20260801T000000Z\r\n" +
	"DTSTART:20261005T110000Z\r\nDTEND:20261005T113000Z\r\nSUMMARY:MovedWithinOctober\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func limitRecurrenceQuery(start, end string) string {
	return `<?xml version="1.0"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <C:calendar-data>
      <C:limit-recurrence-set start="` + start + `" end="` + end + `"/>
    </C:calendar-data>
  </D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"/></C:filter>
</C:calendar-query>`
}

func TestQueryLimitRecurrenceSetKeepsOnlyImpactingOverrides(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "weekly.ics", overriddenWeeklyICS, "weekly")
	h := handlerFor(t, store, caldav.Config{})

	// RFC 4791 §9.6.6: the master is always returned; an overridden component
	// only when its current or original time impacts the window.
	data := reportMS(t, h, "/alice/work/", limitRecurrenceQuery("20260801T000000Z", "20260901T000000Z")).
		at(t, "/alice/work/weekly.ics").value(t, caldavName("calendar-data"))

	if !strings.Contains(data, "RRULE") {
		t.Errorf("calendar-data = %q, limit-recurrence-set must keep the master unexpanded", data)
	}
	if !strings.Contains(data, "MovedWithinAugust") {
		t.Errorf("calendar-data = %q, want the override currently inside the window", data)
	}
	if !strings.Contains(data, "MovedToDecember") {
		t.Errorf("calendar-data = %q, want the override whose original time was inside the window", data)
	}
	if strings.Contains(data, "MovedWithinOctober") {
		t.Errorf("calendar-data = %q, an override impacting the window in neither its current nor original time must be dropped", data)
	}
}

func TestQueryLimitRecurrenceSetExcludesExpand(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "weekly.ics", overriddenWeeklyICS, "weekly")
	h := handlerFor(t, store, caldav.Config{})

	// RFC 4791 §9.6: calendar-data allows (expand | limit-recurrence-set)?, not
	// both.
	body := `<?xml version="1.0"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <C:calendar-data>
      <C:expand start="20260801T000000Z" end="20260901T000000Z"/>
      <C:limit-recurrence-set start="20260801T000000Z" end="20260901T000000Z"/>
    </C:calendar-data>
  </D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"/></C:filter>
</C:calendar-query>`

	if w := report(t, h, "/alice/work/", body); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for expand combined with limit-recurrence-set", w.Code, http.StatusBadRequest)
	}
}

func TestQueryLimitRecurrenceSetRequiresAnOrderedWindow(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "weekly.ics", overriddenWeeklyICS, "weekly")
	h := handlerFor(t, store, caldav.Config{})

	if w := report(t, h, "/alice/work/", limitRecurrenceQuery("20260901T000000Z", "20260801T000000Z")); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for an inverted window", w.Code, http.StatusBadRequest)
	}
}

func TestQueryNoValueStripsTheRequestedValue(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "august.ics", augustICS, "august")
	h := handlerFor(t, store, caldav.Config{})

	// RFC 4791 §9.6.4: novalue="yes" asks for the property name and parameters
	// with a trailing ":" and no value data.
	body := `<?xml version="1.0"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <C:calendar-data>
      <C:comp name="VCALENDAR">
        <C:comp name="VEVENT">
          <C:prop name="UID"/>
          <C:prop name="SUMMARY" novalue="yes"/>
        </C:comp>
      </C:comp>
    </C:calendar-data>
  </D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"/></C:filter>
</C:calendar-query>`

	data := reportMS(t, h, "/alice/work/", body).
		at(t, "/alice/work/august.ics").value(t, caldavName("calendar-data"))

	if !strings.Contains(data, "UID:august") {
		t.Errorf("calendar-data = %q, want the requested UID with its value", data)
	}
	if !strings.Contains(data, "SUMMARY:") {
		t.Errorf("calendar-data = %q, want the SUMMARY name to survive novalue", data)
	}
	if strings.Contains(data, "SUMMARY:August") {
		t.Errorf("calendar-data = %q, novalue=yes must strip the value data", data)
	}
}

func TestQueryNoValueRejectsAnUnknownValue(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "august.ics", augustICS, "august")
	h := handlerFor(t, store, caldav.Config{})

	body := `<?xml version="1.0"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <C:calendar-data>
      <C:comp name="VCALENDAR">
        <C:comp name="VEVENT"><C:prop name="SUMMARY" novalue="maybe"/></C:comp>
      </C:comp>
    </C:calendar-data>
  </D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"/></C:filter>
</C:calendar-query>`

	if w := report(t, h, "/alice/work/", body); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for a novalue that is neither yes nor no", w.Code, http.StatusBadRequest)
	}
}

func TestQueryBoundsTheResultCount(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "august.ics", augustICS, "august")
	seedRaw(t, store, "alice", "october.ics", octoberICS, "october")
	h := handlerFor(t, store, caldav.Config{MaxSearchResults: 1})

	w := report(t, h, "/alice/work/", timeRangeQuery("20260101T000000Z", "20270101T000000Z"))
	if w.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want %d: a partial answer that looks complete is worse than a refusal", w.Code, http.StatusInsufficientStorage)
	}
	if !strings.Contains(w.Body.String(), "number-of-matches-within-limits") {
		t.Errorf("body = %q, want the DAV:number-of-matches-within-limits precondition", w.Body.String())
	}
}

func TestQueryFailsLoudlyOnUnparseableStoredContent(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "junk.ics", "not icalendar at all", "junk")
	h := handlerFor(t, store, caldav.Config{})

	// A read-only backend can hold bytes the library never validated. Matching
	// cannot be done on them, and silently skipping the item would report a
	// search as complete while omitting a member.
	w := report(t, h, "/alice/work/", timeRangeQuery("20260101T000000Z", "20270101T000000Z"))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestQueryRequiresViewDetails(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "august.ics", augustICS, "august")
	h := handlerFor(t, availabilityOnly{store}, caldav.Config{})

	w := report(t, h, "/alice/work/", timeRangeQuery("20260101T000000Z", "20270101T000000Z"))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if strings.Contains(w.Body.String(), "august") {
		t.Error("an item reached an actor allowed to see only busy times")
	}
}

func multigetBody(hrefs ...string) string {
	body := `<?xml version="1.0"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/><C:calendar-data/></D:prop>`
	for _, href := range hrefs {
		body += "<D:href>" + href + "</D:href>"
	}
	return body + `</C:calendar-multiget>`
}

func TestMultigetReturnsNamedItemsAndMissesAlike(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "august.ics", augustICS, "august")
	h := handlerFor(t, store, caldav.Config{})

	ms := reportMS(t, h, "/alice/work/", multigetBody("/alice/work/august.ics", "/alice/work/gone.ics"))

	found := ms.at(t, "/alice/work/august.ics")
	if got := found.value(t, caldavName("calendar-data")); !strings.Contains(got, "UID:august") {
		t.Errorf("calendar-data = %q, want the stored event", got)
	}
	missing := ms.at(t, "/alice/work/gone.ics")
	if missing.Status == "" || !strings.Contains(missing.Status, "404") {
		t.Errorf("missing href status = %q, want a 404 row", missing.Status)
	}
}

func TestMultigetConfinesHrefsToTheCollection(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "carol", "secret.ics", augustICS, "august")
	h := handlerFor(t, store, caldav.Config{})

	// RFC 4791 §7.9: hrefs name members of the request collection. An href into
	// another account's calendar must be refused per-row, never fetched.
	ms := reportMS(t, h, "/alice/work/", multigetBody("/carol/work/secret.ics", "/alice/../carol/work/secret.ics"))

	for _, resp := range ms.Responses {
		if resp.Status == "" || !strings.Contains(resp.Status, "403") {
			t.Errorf("%s: status = %q, want a 403 row", resp.Href, resp.Status)
		}
	}
	if body := multigetBody(); strings.Contains(body, "secret") {
		t.Fatal("fixture error")
	}
}

func TestSyncCollectionInitialSync(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "august.ics", augustICS, "august")
	h := handlerFor(t, store, caldav.Config{})

	ms := reportMS(t, h, "/alice/work/", syncBody(""))

	if !contains(ms.hrefs(), "/alice/work/august.ics") {
		t.Fatalf("hrefs = %v, want the seeded item", ms.hrefs())
	}
	if ms.SyncToken == "" {
		t.Fatal("no sync token, so the client has no position to resume from")
	}
}

func TestSyncCollectionReportsTheDelta(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "august.ics", augustICS, "august")
	h := handlerFor(t, store, caldav.Config{})

	token := reportMS(t, h, "/alice/work/", syncBody("")).SyncToken

	seedRaw(t, store, "alice", "october.ics", octoberICS, "october")
	ms := reportMS(t, h, "/alice/work/", syncBody(token))

	if got := ms.hrefs(); len(got) != 1 || got[0] != "/alice/work/october.ics" {
		t.Errorf("hrefs = %v, want only the item added since the token", got)
	}
	if ms.SyncToken == "" || ms.SyncToken == token {
		t.Errorf("token did not advance: %q", ms.SyncToken)
	}
}

func TestSyncCollectionReportsDeletionsAsNotFoundRows(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "august.ics", augustICS, "august")
	h := handlerFor(t, store, caldav.Config{})
	token := reportMS(t, h, "/alice/work/", syncBody("")).SyncToken

	if w := del(h, "/alice/work/august.ics", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}

	ms := reportMS(t, h, "/alice/work/", syncBody(token))
	row := ms.at(t, "/alice/work/august.ics")
	if row.Status == "" || !strings.Contains(row.Status, "404") {
		t.Errorf("deleted item status = %q, want a 404 row telling the client to drop it", row.Status)
	}
}

func TestSyncCollectionRefusesAForeignToken(t *testing.T) {
	store := newStore(t)
	h := handlerFor(t, store, caldav.Config{})

	// RFC 6578 §3.2: an unserviceable token MUST be DAV:valid-sync-token. A
	// silent full listing carries no deletions, so the client would keep
	// removed items forever.
	for name, token := range map[string]string{
		"garbage":          "not-a-token",
		"another calendar": reportMS(t, handlerFor(t, store, caldav.Config{}), "/alice/work/", syncBody("")).SyncToken + "0",
	} {
		t.Run(name, func(t *testing.T) {
			w := report(t, h, "/alice/work/", syncBody(token))
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
			}
			if !strings.Contains(w.Body.String(), "valid-sync-token") {
				t.Errorf("body = %q, want DAV:valid-sync-token", w.Body.String())
			}
		})
	}
}

func TestSyncCollectionRefusesPrunedHistory(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "august.ics", augustICS, "august")
	h := handlerFor(t, store, caldav.Config{})
	token := reportMS(t, h, "/alice/work/", syncBody("")).SyncToken

	seedRaw(t, store, "alice", "october.ics", octoberICS, "october")
	ref := caldav.CalendarRef{Account: "alice", Calendar: caldav.MustSegment("work")}
	if err := store.PruneHistory(context.Background(), ref, 99); err != nil {
		t.Fatalf("pruning: %v", err)
	}

	w := report(t, h, "/alice/work/", syncBody(token))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "valid-sync-token") {
		t.Errorf("status = %d body = %q, want 403 DAV:valid-sync-token", w.Code, w.Body.String())
	}
}

func TestSyncCollectionNeedsASyncingBackend(t *testing.T) {
	store := newStore(t)
	h := handlerFor(t, readOnlyBackend{store}, caldav.Config{})

	if w := report(t, h, "/alice/work/", syncBody("")); w.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotImplemented)
	}
}

func syncBody(token string) string {
	return `<?xml version="1.0"?>
<D:sync-collection xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:sync-token>` + token + `</D:sync-token>
  <D:sync-level>1</D:sync-level>
  <D:prop><D:getetag/></D:prop>
</D:sync-collection>`
}

func TestSupportedReportSetMatchesWhatIsDispatched(t *testing.T) {
	store := newStore(t)
	syncing := handlerFor(t, store, caldav.Config{})
	plain := handlerFor(t, readOnlyBackend{store}, caldav.Config{})

	ask := askFor(davName("supported-report-set"))
	full := propfind(t, syncing, "/alice/work/", "0", ask).at(t, "/alice/work/").value(t, davName("supported-report-set"))
	for _, want := range []string{"calendar-query", "calendar-multiget", "free-busy-query", "sync-collection"} {
		if !strings.Contains(full, want) {
			t.Errorf("syncing backend's report set = %q, missing %s", full, want)
		}
	}

	readonly := propfind(t, plain, "/alice/work/", "0", ask).at(t, "/alice/work/").value(t, davName("supported-report-set"))
	if strings.Contains(readonly, "sync-collection") {
		t.Errorf("report set = %q advertises sync-collection, which this backend answers 501", readonly)
	}
}

func TestReportOnAnAccountIsRefused(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	w := report(t, h, "/alice/", timeRangeQuery("20260101T000000Z", "20270101T000000Z"))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func contains(hrefs []string, want string) bool {
	for _, h := range hrefs {
		if h == want {
			return true
		}
	}
	return false
}
