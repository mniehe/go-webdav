package caldav_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/mniehe/davkit/caldav"
)

// embeddedTZICS is noon–1pm on 2026-01-15 in a private Eastern zone whose
// definition travels with the object. In winter that is EST (-0500), so the
// true instant is 17:00–18:00Z. time.LoadLocation cannot resolve the TZID, so
// before resolution a time-range query over this object failed outright.
const embeddedTZICS = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//test//EN\r\n" +
	"BEGIN:VTIMEZONE\r\n" +
	"TZID:/example.test/Eastern\r\n" +
	"BEGIN:DAYLIGHT\r\n" +
	"TZOFFSETFROM:-0500\r\n" +
	"TZOFFSETTO:-0400\r\n" +
	"TZNAME:EDT\r\n" +
	"DTSTART:20070311T020000\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU\r\n" +
	"END:DAYLIGHT\r\n" +
	"BEGIN:STANDARD\r\n" +
	"TZOFFSETFROM:-0400\r\n" +
	"TZOFFSETTO:-0500\r\n" +
	"TZNAME:EST\r\n" +
	"DTSTART:20071104T020000\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU\r\n" +
	"END:STANDARD\r\n" +
	"END:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:eastern\r\n" +
	"DTSTAMP:20251201T000000Z\r\n" +
	"DTSTART;TZID=/example.test/Eastern:20260115T120000\r\n" +
	"DTEND;TZID=/example.test/Eastern:20260115T130000\r\n" +
	"SUMMARY:Noon Eastern\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func TestQueryResolvesEmbeddedTimezone(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "eastern.ics", embeddedTZICS, "eastern")
	h := handlerFor(t, store, caldav.Config{})

	// The window is the object's true UTC interval. A naive read of the
	// wall clock would place the event at 12:00Z and miss this entirely.
	ms := reportMS(t, h, "/alice/work/", timeRangeQuery("20260115T170000Z", "20260115T180000Z"))
	if got := ms.hrefs(); len(got) != 1 || got[0] != "/alice/work/eastern.ics" {
		t.Fatalf("hrefs = %v, want the Eastern event matched at its true UTC interval", got)
	}

	// The returned calendar-data must be the bytes as stored: resolution is for
	// matching, not a rewrite of what the client gets back.
	resp := ms.at(t, "/alice/work/eastern.ics")
	data := resp.value(t, caldavName("calendar-data"))
	if !strings.Contains(data, "TZID=/example.test/Eastern") {
		t.Errorf("calendar-data lost its original TZID; resolution must not mutate the stored object:\n%s", data)
	}
}

func TestQueryDoesNotMatchEmbeddedTimezoneNaively(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "eastern.ics", embeddedTZICS, "eastern")
	h := handlerFor(t, store, caldav.Config{})

	// 12:00–12:30Z is the wall clock read as if it were UTC. Matching it would
	// mean the zone offset was ignored.
	ms := reportMS(t, h, "/alice/work/", timeRangeQuery("20260115T120000Z", "20260115T123000Z"))
	if got := ms.hrefs(); len(got) != 0 {
		t.Fatalf("hrefs = %v, want no match: 12:00Z is the wall clock, not the resolved instant", got)
	}
}

// floatingNoonICS has a DTSTART with neither a Z nor a TZID: a floating time,
// read against whatever zone the query names, defaulting to UTC.
const floatingNoonICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:floating\r\nDTSTAMP:20251201T000000Z\r\n" +
	"DTSTART:20260115T120000\r\nDTEND:20260115T130000\r\nSUMMARY:Floating noon\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

// easternTimezoneSpec is the VTIMEZONE a request supplies to fix the meaning of
// floating times, under a private TZID Go cannot load.
const easternTimezoneSpec = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\nPRODID:-//test//EN\r\n" +
	"BEGIN:VTIMEZONE\r\nTZID:/example.test/Eastern\r\n" +
	"BEGIN:DAYLIGHT\r\nTZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\nTZNAME:EDT\r\n" +
	"DTSTART:20070311T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU\r\nEND:DAYLIGHT\r\n" +
	"BEGIN:STANDARD\r\nTZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\nTZNAME:EST\r\n" +
	"DTSTART:20071104T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU\r\nEND:STANDARD\r\n" +
	"END:VTIMEZONE\r\nEND:VCALENDAR\r\n"

func timeRangeQueryTZ(start, end, timezone string) string {
	return `<?xml version="1.0"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/><C:calendar-data/></D:prop>
  <C:timezone>` + timezone + `</C:timezone>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VEVENT">
        <C:time-range start="` + start + `" end="` + end + `"/>
      </C:comp-filter>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`
}

func TestQueryTimezoneResolvesFloatingTimes(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "floating.ics", floatingNoonICS, "floating")
	h := handlerFor(t, store, caldav.Config{})

	// Floating noon in Eastern is 17:00Z. With the timezone supplied it matches
	// the 17:00 window; read as UTC it would sit at 12:00Z and miss it.
	ms := reportMS(t, h, "/alice/work/", timeRangeQueryTZ("20260115T170000Z", "20260115T180000Z", easternTimezoneSpec))
	if got := ms.hrefs(); len(got) != 1 || got[0] != "/alice/work/floating.ics" {
		t.Fatalf("hrefs = %v, want the floating event resolved into the window by the query timezone", got)
	}
}

func TestQueryFloatingDefaultsToUTC(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "floating.ics", floatingNoonICS, "floating")
	h := handlerFor(t, store, caldav.Config{})

	// With no timezone, a floating noon is UTC noon and matches the 12:00 window.
	ms := reportMS(t, h, "/alice/work/", timeRangeQuery("20260115T120000Z", "20260115T130000Z"))
	if got := ms.hrefs(); len(got) != 1 || got[0] != "/alice/work/floating.ics" {
		t.Fatalf("hrefs = %v, want the floating event read as UTC when no timezone is given", got)
	}
}

func TestFreeBusyResolvesEmbeddedTimezone(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "eastern.ics", embeddedTZICS, "eastern")
	h := handlerFor(t, store, caldav.Config{})

	w := report(t, h, "/alice/work/", freeBusyQuery("20260115T000000Z", "20260116T000000Z"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusOK, w.Body.String())
	}
	// Noon EST is 17:00Z; the busy period must land at the resolved instant, not
	// the wall clock.
	if body := w.Body.String(); !strings.Contains(body, "20260115T170000Z/20260115T180000Z") {
		t.Errorf("free-busy did not report the resolved 17:00Z/18:00Z period:\n%s", body)
	}
}
