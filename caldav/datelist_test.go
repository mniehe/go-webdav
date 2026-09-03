package caldav

import (
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

// multiEXDATECalendar excludes two of the five daily occurrences with a single
// EXDATE, which RFC 5545 §3.8.5.1 allows.
const multiEXDATECalendar = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//caldav//test//EN
BEGIN:VEVENT
UID:multi-exdate
DTSTAMP:20251201T000000Z
DTSTART:20260101T100000Z
DTEND:20260101T120000Z
SUMMARY:Daily standup
RRULE:FREQ=DAILY;COUNT=5
EXDATE:20260102T100000Z,20260104T100000Z
END:VEVENT
END:VCALENDAR
`

func TestTimeRangeReadsAMultiValueEXDATE(t *testing.T) {
	comp := firstEvent(t, multiEXDATECalendar)
	for _, tc := range []struct {
		name       string
		start, end string
		want       bool
	}{
		{"the first excluded instance is gone", "20260102T100000Z", "20260102T120000Z", false},
		{"the second excluded instance is gone", "20260104T100000Z", "20260104T120000Z", false},
		{"the instance between them remains", "20260103T100000Z", "20260103T120000Z", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matched, err := matchCompTimeRange(toDate(t, tc.start), toDate(t, tc.end), comp)
			if err != nil {
				t.Fatalf("matchCompTimeRange: %v", err)
			}
			if matched != tc.want {
				t.Errorf("matchCompTimeRange = %v, want %v", matched, tc.want)
			}
		})
	}
}

func TestExpandReadsAMultiValueEXDATE(t *testing.T) {
	out, err := expandCalendar(parseICS(t, multiEXDATECalendar), &calendarExpandRequest{
		Start: utc(2026, time.January, 1, 0),
		End:   utc(2026, time.January, 6, 0),
	})
	if err != nil {
		t.Fatalf("expandCalendar: %v", err)
	}

	events := componentsNamed(out, ical.CompEvent)
	want := []time.Time{utc(2026, time.January, 1, 10), utc(2026, time.January, 3, 10), utc(2026, time.January, 5, 10)}
	if len(events) != len(want) {
		t.Fatalf("expanded %d instances, want %d", len(events), len(want))
	}
	for i, ev := range events {
		start, err := (&ical.Event{Component: ev}).DateTimeStart(time.UTC)
		if err != nil {
			t.Fatalf("instance %d DTSTART: %v", i, err)
		}
		if !start.Equal(want[i]) {
			t.Errorf("instance %d DTSTART = %v, want %v", i, start, want[i])
		}
	}
}

// rdateVTIMEZONE lists its onsets with RDATE instead of an RRULE, and packs two
// of the standard-time onsets into one property.
const rdateVTIMEZONE = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//caldav//test//EN
BEGIN:VTIMEZONE
TZID:/example.test/Listed
BEGIN:STANDARD
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
TZNAME:EST
DTSTART:20251102T020000
RDATE:20261101T020000,20271107T020000
END:STANDARD
BEGIN:DAYLIGHT
TZOFFSETFROM:-0500
TZOFFSETTO:-0400
TZNAME:EDT
DTSTART:20260308T020000
RDATE:20270314T020000
END:DAYLIGHT
END:VTIMEZONE
END:VCALENDAR
`

func TestResolverReadsAMultiValueRDATE(t *testing.T) {
	var vtz *ical.Component
	for _, child := range parseCalendar(t, rdateVTIMEZONE).Children {
		if child.Name == ical.CompTimezone {
			vtz = child
		}
	}
	if vtz == nil {
		t.Fatal("no VTIMEZONE in test data")
	}
	r, err := newTZResolver(vtz)
	if err != nil {
		t.Fatalf("newTZResolver: %v", err)
	}

	for _, tc := range []struct {
		name string
		wall string
		want time.Duration
	}{
		{"after the listed 2027 daylight onset", "20270701T120000", -4 * time.Hour},
		{"after the second onset listed in one RDATE", "20271201T120000", -5 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wall, err := time.Parse(localDateTimeLayout, tc.wall)
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.offsetAt(wall)
			if err != nil {
				t.Fatalf("offsetAt: %v", err)
			}
			if got != tc.want {
				t.Errorf("offsetAt(%s) = %v, want %v", tc.wall, got, tc.want)
			}
		})
	}
}
