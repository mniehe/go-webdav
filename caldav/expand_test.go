package caldav

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func parseICS(t *testing.T, data string) *ical.Calendar {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(strings.ReplaceAll(data, "\n", "\r\n"))).Decode()
	if err != nil {
		t.Fatalf("decoding fixture: %v", err)
	}
	return cal
}

func utc(y int, mo time.Month, d, h int) time.Time {
	return time.Date(y, mo, d, h, 0, 0, 0, time.UTC)
}

// componentsNamed returns every child component with the given name.
func componentsNamed(cal *ical.Calendar, name string) []*ical.Component {
	var out []*ical.Component
	for _, child := range cal.Children {
		if child.Name == name {
			out = append(out, child)
		}
	}
	return out
}

const dailyRecurring = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:daily-1
DTSTAMP:20260101T000000Z
DTSTART:20260101T090000Z
DTEND:20260101T100000Z
SUMMARY:Standup
RRULE:FREQ=DAILY;COUNT=10
END:VEVENT
END:VCALENDAR
`

// RFC 4791 §9.6.5: expand replaces the recurring component with one component
// per instance in the window, each carrying RECURRENCE-ID and no recurrence
// properties of its own.
func TestExpandProducesOneInstancePerOccurrence(t *testing.T) {
	cal := parseICS(t, dailyRecurring)

	out, err := expandCalendar(cal, &calendarExpandRequest{
		Start: utc(2026, time.January, 3, 0),
		End:   utc(2026, time.January, 6, 0),
	})
	if err != nil {
		t.Fatal(err)
	}

	events := componentsNamed(out, ical.CompEvent)
	if len(events) != 3 {
		t.Fatalf("expected 3 instances for a 3-day window, got %d", len(events))
	}

	wantStarts := []time.Time{utc(2026, time.January, 3, 9), utc(2026, time.January, 4, 9), utc(2026, time.January, 5, 9)}
	for i, ev := range events {
		if ev.Props.Get(ical.PropRecurrenceRule) != nil {
			t.Errorf("instance %d still carries an RRULE", i)
		}
		recID := ev.Props.Get(ical.PropRecurrenceID)
		if recID == nil {
			t.Errorf("instance %d has no RECURRENCE-ID", i)
			continue
		}
		got, err := recID.DateTime(time.UTC)
		if err != nil {
			t.Errorf("instance %d RECURRENCE-ID: %v", i, err)
			continue
		}
		if !got.Equal(wantStarts[i]) {
			t.Errorf("instance %d RECURRENCE-ID = %v, want %v", i, got, wantStarts[i])
		}

		start, err := (&ical.Event{Component: ev}).DateTimeStart(time.UTC)
		if err != nil {
			t.Errorf("instance %d DTSTART: %v", i, err)
			continue
		}
		if !start.Equal(wantStarts[i]) {
			t.Errorf("instance %d DTSTART = %v, want %v", i, start, wantStarts[i])
		}
	}
}

// Expansion preserves each instance's duration rather than copying the master's
// absolute end time onto every occurrence.
func TestExpandPreservesInstanceDuration(t *testing.T) {
	cal := parseICS(t, dailyRecurring)

	out, err := expandCalendar(cal, &calendarExpandRequest{
		Start: utc(2026, time.January, 3, 0),
		End:   utc(2026, time.January, 4, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	events := componentsNamed(out, ical.CompEvent)
	if len(events) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(events))
	}

	ev := &ical.Event{Component: events[0]}
	start, err := ev.DateTimeStart(time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	end, err := ev.DateTimeEnd(time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if got := end.Sub(start); got != time.Hour {
		t.Errorf("expected the master's 1h duration to carry over, got %v (%v..%v)", got, start, end)
	}
}

const recurringWithTimezone = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VTIMEZONE
TZID:America/Vancouver
BEGIN:STANDARD
DTSTART:19701101T020000
TZOFFSETFROM:-0700
TZOFFSETTO:-0800
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
UID:tz-1
DTSTAMP:20260101T000000Z
DTSTART:20260105T090000Z
DTEND:20260105T100000Z
SUMMARY:Zoned
RRULE:FREQ=DAILY;COUNT=3
END:VEVENT
END:VCALENDAR
`

// RFC 4791 §9.6.5: the expanded response carries no VTIMEZONE, because every
// time in it has been converted to UTC.
func TestExpandRemovesTimezoneComponents(t *testing.T) {
	cal := parseICS(t, recurringWithTimezone)

	out, err := expandCalendar(cal, &calendarExpandRequest{
		Start: utc(2026, time.January, 1, 0),
		End:   utc(2026, time.January, 30, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if tz := componentsNamed(out, ical.CompTimezone); len(tz) != 0 {
		t.Errorf("expected VTIMEZONE to be removed, got %d", len(tz))
	}
	if len(componentsNamed(out, ical.CompEvent)) == 0 {
		t.Error("the events themselves must survive")
	}
}

const recurringWithOverride = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:ovr-1
DTSTAMP:20260101T000000Z
DTSTART:20260101T090000Z
DTEND:20260101T100000Z
SUMMARY:Standup
RRULE:FREQ=DAILY;COUNT=5
END:VEVENT
BEGIN:VEVENT
UID:ovr-1
DTSTAMP:20260101T000000Z
RECURRENCE-ID:20260103T090000Z
DTSTART:20260103T140000Z
DTEND:20260103T150000Z
SUMMARY:Standup (moved)
END:VEVENT
END:VCALENDAR
`

// A modified instance replaces the generated one rather than appearing beside
// it, otherwise the client sees the occurrence twice.
func TestExpandOverrideReplacesGeneratedInstance(t *testing.T) {
	cal := parseICS(t, recurringWithOverride)

	out, err := expandCalendar(cal, &calendarExpandRequest{
		Start: utc(2026, time.January, 3, 0),
		End:   utc(2026, time.January, 4, 0),
	})
	if err != nil {
		t.Fatal(err)
	}

	events := componentsNamed(out, ical.CompEvent)
	if len(events) != 1 {
		t.Fatalf("expected the override to replace the generated instance, got %d events", len(events))
	}
	if summary := events[0].Props.Get(ical.PropSummary); summary == nil || summary.Value != "Standup (moved)" {
		t.Errorf("expected the override's SUMMARY, got %+v", summary)
	}
}

const nonRecurring = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:single-1
DTSTAMP:20260101T000000Z
DTSTART:20260110T090000Z
DTEND:20260110T100000Z
SUMMARY:One off
END:VEVENT
END:VCALENDAR
`

func TestExpandKeepsNonRecurringInsideWindow(t *testing.T) {
	out, err := expandCalendar(parseICS(t, nonRecurring), &calendarExpandRequest{
		Start: utc(2026, time.January, 1, 0),
		End:   utc(2026, time.February, 1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(componentsNamed(out, ical.CompEvent)); got != 1 {
		t.Errorf("expected the non-recurring event to be kept, got %d events", got)
	}
}

func TestExpandDropsNonRecurringOutsideWindow(t *testing.T) {
	out, err := expandCalendar(parseICS(t, nonRecurring), &calendarExpandRequest{
		Start: utc(2026, time.March, 1, 0),
		End:   utc(2026, time.April, 1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(componentsNamed(out, ical.CompEvent)); got != 0 {
		t.Errorf("expected the out-of-window event to be dropped, got %d events", got)
	}
}

// An unbounded high-frequency rule must not be allowed to materialise an
// unbounded instance list, however wide a window the client asks for.
func TestExpandBoundsTotalSizeNotJustInstanceCount(t *testing.T) {
	cal := parseICS(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:fat-1
DTSTAMP:20260101T000000Z
DTSTART:20260101T000000Z
DTEND:20260101T010000Z
SUMMARY:`+strings.Repeat("x", 200_000)+`
RRULE:FREQ=DAILY;COUNT=1000
END:VEVENT
END:VCALENDAR
`)

	// 1000 instances stays well under maxExpandedInstances, so only a byte-aware
	// budget can refuse this.
	_, err := expandCalendar(cal, &calendarExpandRequest{
		Start: utc(2026, time.January, 1, 0),
		End:   utc(2030, time.January, 1, 0),
	})
	if err == nil {
		t.Fatal("expected an expansion of ~200 MB to be refused")
	}
}

// Occurrences before the window are skipped without spending the emit budget,
// so only the iteration cap bounds the walk. Without it a rule dense enough and
// a window far enough away pin a goroutine for billions of iterations.
func TestExpandBoundsIterationsOverSkippedOccurrences(t *testing.T) {
	cal := parseICS(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:skip-1
DTSTAMP:20000101T000000Z
DTSTART:20000101T000000Z
DTEND:20000101T000100Z
SUMMARY:Distant
RRULE:FREQ=SECONDLY
END:VEVENT
END:VCALENDAR
`)

	done := make(chan error, 1)
	go func() {
		_, err := expandCalendar(cal, &calendarExpandRequest{
			Start: utc(2100, time.January, 1, 0),
			End:   utc(2100, time.January, 1, 1),
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a walk over billions of skipped occurrences to be refused")
		}
		if !strings.Contains(err.Error(), "iterations") {
			t.Errorf("error = %v, want the iteration cap to have stopped the walk", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the expansion did not terminate: nothing bounds the walk over skipped occurrences")
	}
}
