package caldav

import (
	"strings"
	"testing"
	"time"
)

// The VEVENT rows of the RFC 4791 §9.9 overlap table. The behaviour comes from
// go-ical's DateTimeEnd plus the instant/interval split in intervalOverlaps, so
// none of it is pinned by this package without these cases: a dependency bump
// could change which row a component lands on.
//
//	+---+---+---+---+-----------------------------------------------+
//	|DTE|DUR|>0 |DT |Condition                                      |
//	+---+---+---+---+-----------------------------------------------+
//	| Y | N | N | * | (start <  DTEND AND end > DTSTART)            |
//	| N | Y | Y | * | (start <  DTSTART+DURATION AND end > DTSTART) |
//	| N | Y | N | * | (start <= DTSTART AND end > DTSTART)          |
//	| N | N | N | Y | (start <= DTSTART AND end > DTSTART)          |
//	| N | N | N | N | (start <  DTSTART+P1D AND end > DTSTART)      |
//	+---+---+---+---+-----------------------------------------------+
func eventWith(t *testing.T, lines string) *calendarObject {
	t.Helper()
	ics := "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//Test//EN\nBEGIN:VEVENT\nUID:u1\nDTSTAMP:20260101T000000Z\n" +
		lines + "\nEND:VEVENT\nEND:VCALENDAR"
	return &calendarObject{Path: "/u/c/e.ics", Data: parseCalendar(t, ics)}
}

func timeRangeFilter(start, end time.Time) *compFilter {
	return &compFilter{
		Name:  "VCALENDAR",
		Comps: []compFilter{{Name: "VEVENT", Start: start, End: end}},
	}
}

func TestTimeRangeVEventTable(t *testing.T) {
	// The window is 12:00–13:00 on 2026-01-01.
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name  string
		lines string
		want  bool
	}{
		// Row 1: DTEND present. Half-open, so an event ending exactly at the
		// window start does not overlap and one starting exactly at the window
		// end does not either.
		{"dtend overlapping", "DTSTART:20260101T110000Z\nDTEND:20260101T123000Z", true},
		{"dtend ending before the window", "DTSTART:20260101T100000Z\nDTEND:20260101T110000Z", false},
		{"dtend ending exactly at the window start", "DTSTART:20260101T110000Z\nDTEND:20260101T120000Z", false},
		{"dtend starting exactly at the window end", "DTSTART:20260101T130000Z\nDTEND:20260101T140000Z", false},

		// Row 2: positive DURATION behaves like DTEND.
		{"positive duration reaching into the window", "DTSTART:20260101T113000Z\nDURATION:PT1H", true},
		{"positive duration ending before the window", "DTSTART:20260101T100000Z\nDURATION:PT30M", false},

		// Row 3: a zero-length DURATION is an instant, so the comparison against
		// the window start becomes inclusive. An instant exactly at the window
		// start overlaps; under interval semantics it would not.
		{"zero duration at the window start", "DTSTART:20260101T120000Z\nDURATION:PT0S", true},
		{"zero duration inside the window", "DTSTART:20260101T123000Z\nDURATION:PT0S", true},
		{"zero duration before the window", "DTSTART:20260101T110000Z\nDURATION:PT0S", false},
		{"zero duration at the window end", "DTSTART:20260101T130000Z\nDURATION:PT0S", false},

		// Row 4: a DATE-TIME DTSTART with neither property is also an instant.
		{"bare date-time at the window start", "DTSTART:20260101T120000Z", true},
		{"bare date-time inside the window", "DTSTART:20260101T123000Z", true},
		{"bare date-time before the window", "DTSTART:20260101T110000Z", false},

		// Row 5: a DATE DTSTART with neither property lasts one day, which is an
		// interval and not an instant.
		{"bare date covering the window", "DTSTART;VALUE=DATE:20260101", true},
		{"bare date on the previous day", "DTSTART;VALUE=DATE:20251230", false},
		{"bare date on the following day", "DTSTART;VALUE=DATE:20260102", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matched, err := matchObject(timeRangeFilter(start, end), eventWith(t, tc.lines))
			if err != nil {
				t.Fatalf("Match: %v", err)
			}
			if matched != tc.want {
				t.Errorf("matched = %v, want %v for:\n%s", matched, tc.want, strings.ReplaceAll(tc.lines, "\n", " | "))
			}
		})
	}
}

func componentWith(t *testing.T, name, lines string) *calendarObject {
	t.Helper()
	ics := "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//Test//EN\nBEGIN:" + name + "\nUID:u1\nDTSTAMP:20260101T000000Z\n" +
		lines + "\nEND:" + name + "\nEND:VCALENDAR"
	return &calendarObject{Path: "/u/c/e.ics", Data: parseCalendar(t, ics)}
}

func componentTimeRangeFilter(name string, start, end time.Time) *compFilter {
	return &compFilter{
		Name:  "VCALENDAR",
		Comps: []compFilter{{Name: name, Start: start, End: end}},
	}
}

func runTableCases(t *testing.T, name string, cases []struct {
	name  string
	lines string
	want  bool
},
) {
	t.Helper()
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matched, err := matchObject(componentTimeRangeFilter(name, start, end), componentWith(t, name, tc.lines))
			if err != nil {
				t.Fatalf("Match: %v", err)
			}
			if matched != tc.want {
				t.Errorf("matched = %v, want %v for:\n%s", matched, tc.want, strings.ReplaceAll(tc.lines, "\n", " | "))
			}
		})
	}
}

// RFC 4791 §9.9 VJOURNAL table. A DATE-TIME DTSTART is an instant, a DATE
// DTSTART lasts one day, and a VJOURNAL with no DTSTART never overlaps.
func TestTimeRangeVJournalTable(t *testing.T) {
	runTableCases(t, "VJOURNAL", []struct {
		name  string
		lines string
		want  bool
	}{
		{"date-time at the window start", "DTSTART:20260101T120000Z", true},
		{"date-time inside the window", "DTSTART:20260101T123000Z", true},
		{"date-time before the window", "DTSTART:20260101T110000Z", false},
		{"date-time at the window end", "DTSTART:20260101T130000Z", false},
		{"date covering the window", "DTSTART;VALUE=DATE:20260101", true},
		{"date on the previous day", "DTSTART;VALUE=DATE:20251231", false},
		{"no dtstart never overlaps", "SUMMARY:no start", false},
	})
}

// RFC 4791 §9.9 VFREEBUSY table. With DTSTART and DTEND the condition is
// (start <= DTEND AND end > DTSTART) — inclusive on DTEND, unlike VEVENT, so a
// period ending exactly at the window start still overlaps. Otherwise each
// FREEBUSY period is compared, and a component with neither never overlaps.
func TestTimeRangeVFreeBusyTable(t *testing.T) {
	runTableCases(t, "VFREEBUSY", []struct {
		name  string
		lines string
		want  bool
	}{
		{"dtstart and dtend spanning the window", "DTSTART:20260101T110000Z\nDTEND:20260101T123000Z", true},
		{"dtend exactly at the window start still overlaps", "DTSTART:20260101T100000Z\nDTEND:20260101T120000Z", true},
		{"ending before the window", "DTSTART:20260101T100000Z\nDTEND:20260101T113000Z", false},
		{"starting at the window end", "DTSTART:20260101T130000Z\nDTEND:20260101T140000Z", false},
		{"freebusy period overlapping", "FREEBUSY:20260101T113000Z/20260101T123000Z", true},
		{"freebusy period before the window", "FREEBUSY:20260101T100000Z/20260101T110000Z", false},
		{"neither dtstart-dtend nor freebusy", "SUMMARY:nothing", false},
	})
}

// RFC 4791 §9.9 VTODO table. Which row applies depends on which of DTSTART,
// DURATION, DUE, COMPLETED and CREATED the component carries, and the rows use
// a mix of inclusive and exclusive comparisons that no other component does.
// The last row is the surprising one: a VTODO carrying none of them overlaps
// every time range.
func TestTimeRangeVTodoTable(t *testing.T) {
	runTableCases(t, "VTODO", []struct {
		name  string
		lines string
		want  bool
	}{
		// Row 1: DTSTART and DURATION, no DUE.
		{"dtstart and duration reaching the window", "DTSTART:20260101T110000Z\nDURATION:PT2H", true},
		{"dtstart and duration ending before the window", "DTSTART:20260101T090000Z\nDURATION:PT1H", false},

		// Row 2: DTSTART and DUE.
		{"dtstart and due spanning the window", "DTSTART:20260101T110000Z\nDUE:20260101T123000Z", true},
		{"dtstart and due entirely before the window", "DTSTART:20260101T090000Z\nDUE:20260101T100000Z", false},

		// Row 3: DTSTART alone behaves as an instant.
		{"dtstart alone at the window start", "DTSTART:20260101T120000Z", true},
		{"dtstart alone before the window", "DTSTART:20260101T110000Z", false},

		// Row 4: DUE alone, inclusive at the window end.
		{"due inside the window", "DUE:20260101T123000Z", true},
		{"due exactly at the window end", "DUE:20260101T130000Z", true},
		{"due after the window", "DUE:20260101T140000Z", false},

		// Row 5: COMPLETED and CREATED together.
		{"completed and created inside the window", "CREATED:20260101T123000Z\nCOMPLETED:20260101T124500Z", true},
		{"completed and created after the window", "CREATED:20260101T140000Z\nCOMPLETED:20260101T150000Z", false},

		// Row 6: COMPLETED alone.
		{"completed inside the window", "COMPLETED:20260101T123000Z", true},
		{"completed before the window", "COMPLETED:20260101T110000Z", false},

		// Row 7: CREATED alone — only the upper bound is tested, so a task
		// created long before the window still overlaps it.
		{"created before the window", "CREATED:20260101T110000Z", true},
		{"created after the window", "CREATED:20260101T140000Z", false},

		// Row 8: none of the five properties.
		{"no timing properties at all", "SUMMARY:untimed task", true},
	})
}

// A stored object with a malformed FREEBUSY period must produce an error, not
// take the handler down. AGENTS.md forbids panics in production code, and a
// backend serving a damaged record would otherwise crash every query that
// touches it.
func TestMalformedFreeBusyPeriodIsAnError(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)

	for _, lines := range []string{
		"FREEBUSY:20260101T113000Z/",
		"FREEBUSY:",
		"FREEBUSY:20260101T113000Z",
		"FREEBUSY:/20260101T123000Z",
	} {
		t.Run(lines, func(t *testing.T) {
			matched, err := matchObject(componentTimeRangeFilter("VFREEBUSY", start, end), componentWith(t, "VFREEBUSY", lines))
			if err == nil && matched {
				t.Errorf("a malformed period matched the range instead of erroring")
			}
		})
	}
}
