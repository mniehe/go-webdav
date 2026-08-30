package caldav

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/mniehe/davkit/internal"
)

func decodeCal(t *testing.T, s string) *ical.Calendar {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(s)).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return cal
}

func weekly(uid, dtstart string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:" + dtstart + "\r\nDTEND:" + dtstart +
		"\r\nRRULE:FREQ=WEEKLY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func vevent(name string, start, end time.Time) *compFilter {
	return &compFilter{Name: "VCALENDAR", Comps: []compFilter{{Name: name, Start: start, End: end}}}
}

func TestMatchNilDataReturnsError(t *testing.T) {
	if _, err := matchObject(&compFilter{Name: "VCALENDAR"}, &calendarObject{Path: "/x.ics"}); err == nil {
		t.Fatal("expected an error for a calendar object with no data, got nil")
	}
	// Filter must not panic either.
	if _, err := filterObjects(&calendarQuery{}, []calendarObject{{Path: "/x.ics"}}); err == nil {
		t.Fatal("expected Filter to surface the error")
	}
}

func TestOpenEndedTimeRangeIncludesRecurring(t *testing.T) {
	cal := decodeCal(t, weekly("rec1", "20260101T090000Z"))
	co := &calendarObject{Path: "/e.ics", Data: cal}

	// "everything from mid-2026 onward" — no end bound. The weekly event has
	// occurrences after this, so it must match.
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	matched, err := matchObject(vevent("VEVENT", start, time.Time{}), co)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Error("open-ended time-range dropped a recurring event")
	}
}

func TestOmittedStartTimeRangeBounded(t *testing.T) {
	cal := decodeCal(t, weekly("rec1", "20260101T090000Z"))
	co := &calendarObject{Path: "/e.ics", Data: cal}

	// "everything up to 2025" — event starts in 2026, so it must NOT match.
	end := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	matched, err := matchObject(vevent("VEVENT", time.Time{}, end), co)
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Error("omitted-start time-range matched an out-of-range event")
	}
}

func TestHighFrequencyRuleTerminates(t *testing.T) {
	// FREQ=SECONDLY with a range far after the start would materialise billions
	// of occurrences under the old code. The bounded walk must return promptly.
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:s1\r\n" +
		"DTSTAMP:20200101T000000Z\r\nDTSTART:20200101T000000Z\r\nDTEND:20200101T000001Z\r\n" +
		"RRULE:FREQ=SECONDLY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	cal := decodeCal(t, ics)
	co := &calendarObject{Path: "/s.ics", Data: cal}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	// The walk must terminate (the test timeout catches a hang) and must say it
	// could not answer. Reporting a match instead would return an event the
	// filter never established was in range.
	matched, err := matchObject(vevent("VEVENT", start, end), co)
	if matched {
		t.Error("time-range reported a match it never established")
	}
	var httpErr *internal.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v, want an *internal.HTTPError", err)
	}
	if httpErr.Code != http.StatusInsufficientStorage {
		t.Errorf("status = %d, want %d", httpErr.Code, http.StatusInsufficientStorage)
	}
}
