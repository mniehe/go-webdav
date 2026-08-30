package caldav

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func parseCalendar(t *testing.T, data string) *ical.Calendar {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(data)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

func firstEvent(t *testing.T, data string) *ical.Component {
	t.Helper()
	for _, child := range parseCalendar(t, data).Children {
		if child.Name == ical.CompEvent {
			return child
		}
	}
	t.Fatal("no VEVENT in test data")
	return nil
}

// RFC 4791 §9.9 defines VEVENT time-range matching on the event interval:
// (DTSTART < end) AND (effective DTEND > start). An event that spans the whole
// window overlaps it, so it matches even though neither bound falls inside.
func TestTimeRangeMatchesWindowInsideOccurrence(t *testing.T) {
	tests := []struct {
		name       string
		event      string
		start, end string
	}{
		{
			name: "recurring event, window strictly inside an occurrence",
			event: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:inside-recurring
DTSTAMP:20251201T000000Z
DTSTART:20260101T100000Z
DTEND:20260101T120000Z
RRULE:FREQ=DAILY;COUNT=3
SUMMARY:Long daily meeting
END:VEVENT
END:VCALENDAR`,
			start: "20260102T110000Z",
			end:   "20260102T113000Z",
		},
		{
			name: "recurring event with DURATION, window strictly inside",
			event: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:inside-duration
DTSTAMP:20251201T000000Z
DTSTART:20260101T100000Z
DURATION:PT2H
RRULE:FREQ=DAILY;COUNT=3
SUMMARY:Long daily meeting
END:VEVENT
END:VCALENDAR`,
			start: "20260102T110000Z",
			end:   "20260102T113000Z",
		},
		{
			name: "single event exactly spanning the window",
			event: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:exact-span
DTSTAMP:20251201T000000Z
DTSTART:20260101T100000Z
DTEND:20260101T120000Z
SUMMARY:Exactly the window
END:VEVENT
END:VCALENDAR`,
			start: "20260101T100000Z",
			end:   "20260101T120000Z",
		},
		{
			name: "zero-duration event at the window start",
			event: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:zero-duration
DTSTAMP:20251201T000000Z
DTSTART:20260101T100000Z
SUMMARY:Instant at the lower bound
END:VEVENT
END:VCALENDAR`,
			start: "20260101T100000Z",
			end:   "20260101T120000Z",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			comp := firstEvent(t, tc.event)
			matched, err := matchCompTimeRange(toDate(t, tc.start), toDate(t, tc.end), comp)
			if err != nil {
				t.Fatalf("matchCompTimeRange: %v", err)
			}
			if !matched {
				t.Error("time-range did not match an event overlapping the window; RFC 4791 §9.9 matches on the event interval, not on DTSTART alone")
			}
		})
	}
}

// An event whose interval ends before the window starts, or starts after it
// ends, must not match — the interval test has to reject as well as accept.
func TestTimeRangeRejectsDisjointOccurrence(t *testing.T) {
	event := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:disjoint
DTSTAMP:20251201T000000Z
DTSTART:20260101T100000Z
DTEND:20260101T120000Z
RRULE:FREQ=DAILY;COUNT=3
SUMMARY:Long daily meeting
END:VEVENT
END:VCALENDAR`

	tests := []struct{ name, start, end string }{
		{"window entirely before every occurrence", "20251231T000000Z", "20251231T235900Z"},
		{"window entirely after every occurrence", "20260201T000000Z", "20260201T010000Z"},
		{"window abutting an occurrence end", "20260101T120000Z", "20260101T130000Z"},
		{"window abutting an occurrence start", "20260101T080000Z", "20260101T100000Z"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			comp := firstEvent(t, event)
			matched, err := matchCompTimeRange(toDate(t, tc.start), toDate(t, tc.end), comp)
			if err != nil {
				t.Fatalf("matchCompTimeRange: %v", err)
			}
			if matched {
				t.Error("time-range matched an event that does not overlap the window")
			}
		})
	}
}

// The iteration cap exists to bound work, not to answer the question. Returning
// true once it trips reports calendar data the client's filter excluded.
func TestTimeRangeDoesNotInventMatchAtIterationCap(t *testing.T) {
	// Every occurrence is over long before the window opens, but there are far
	// more of them than maxRecurrenceIterations.
	event := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:secondly
DTSTAMP:20251201T000000Z
DTSTART:20260101T000000Z
DTEND:20260101T000001Z
RRULE:FREQ=SECONDLY;COUNT=200000
SUMMARY:High frequency rule
END:VEVENT
END:VCALENDAR`

	comp := firstEvent(t, event)
	start := toDate(t, "20260501T000000Z")
	end := toDate(t, "20260501T010000Z")

	matched, err := matchCompTimeRange(start, end, comp)
	if matched {
		t.Error("time-range reported a match for a window no occurrence reaches; the iteration cap must not answer true")
	}
	if err == nil && !matched {
		return // a correct negative answer is also acceptable
	}
	if err == nil {
		t.Fatal("expected either a correct negative or an explicit error")
	}
}

// RFC 4791 §9.6.5 expands the instances *in* the range, not the instances
// *starting in* it.
func TestExpandIncludesOccurrenceContainingWindow(t *testing.T) {
	cal := parseCalendar(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:expand-inside
DTSTAMP:20251201T000000Z
DTSTART:20260101T100000Z
DTEND:20260101T120000Z
RRULE:FREQ=DAILY;COUNT=3
SUMMARY:Long daily meeting
END:VEVENT
END:VCALENDAR`)

	req := &calendarExpandRequest{
		Start: toDate(t, "20260102T110000Z"),
		End:   toDate(t, "20260102T113000Z"),
	}

	expanded, err := expandCalendar(cal, req)
	if err != nil {
		t.Fatalf("expandCalendar: %v", err)
	}

	var instances []*ical.Component
	for _, child := range expanded.Children {
		if child.Name == ical.CompEvent {
			instances = append(instances, child)
		}
	}
	if len(instances) != 1 {
		t.Fatalf("got %d expanded instances, want 1 (the occurrence containing the window)", len(instances))
	}

	recurrenceID, err := instances[0].Props.Get(ical.PropRecurrenceID).DateTime(time.UTC)
	if err != nil {
		t.Fatalf("RECURRENCE-ID: %v", err)
	}
	if want := toDate(t, "20260102T100000Z"); !recurrenceID.Equal(want) {
		t.Errorf("RECURRENCE-ID = %v, want %v", recurrenceID, want)
	}
}

// RFC 4791 §9.9 matches a DATE-TIME property when start <= value < end, so the
// lower bound is inclusive and the upper bound is not.
func TestPropTimeRangeBoundsAreHalfOpen(t *testing.T) {
	comp := firstEvent(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:prop-bounds
DTSTAMP:20260101T100000Z
DTSTART:20260101T100000Z
DTEND:20260101T110000Z
SUMMARY:Boundary
END:VEVENT
END:VCALENDAR`)
	field := comp.Props.Get(ical.PropDateTimeStamp)

	t.Run("value at the lower bound matches", func(t *testing.T) {
		matched, err := matchPropTimeRange(toDate(t, "20260101T100000Z"), toDate(t, "20260101T120000Z"), field)
		if err != nil {
			t.Fatalf("matchPropTimeRange: %v", err)
		}
		if !matched {
			t.Error("property value equal to the window start did not match; the lower bound is inclusive")
		}
	})

	t.Run("value at the upper bound does not match", func(t *testing.T) {
		matched, err := matchPropTimeRange(toDate(t, "20260101T080000Z"), toDate(t, "20260101T100000Z"), field)
		if err != nil {
			t.Fatalf("matchPropTimeRange: %v", err)
		}
		if matched {
			t.Error("property value equal to the window end matched; the upper bound is exclusive")
		}
	})
}
