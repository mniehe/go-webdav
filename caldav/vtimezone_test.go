package caldav

import (
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

// easternVTIMEZONE is US Eastern under a private TZID that time.LoadLocation
// cannot resolve, so the only way to read its times is the VTIMEZONE itself.
const easternVTIMEZONE = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//caldav//test//EN
BEGIN:VTIMEZONE
TZID:/example.test/Eastern
BEGIN:DAYLIGHT
TZOFFSETFROM:-0500
TZOFFSETTO:-0400
TZNAME:EDT
DTSTART:20070311T020000
RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU
END:DAYLIGHT
BEGIN:STANDARD
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
TZNAME:EST
DTSTART:20071104T020000
RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU
END:STANDARD
END:VTIMEZONE
END:VCALENDAR
`

func easternResolver(t *testing.T) *tzResolver {
	t.Helper()
	cal := parseCalendar(t, easternVTIMEZONE)
	var vtz *ical.Component
	for _, child := range cal.Children {
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
	return r
}

// easternEventCalendar is noon-to-1pm on 2026-01-15 in the private Eastern
// zone. In winter that is EST (-0500), so the true instant is 17:00–18:00Z.
const easternEventCalendar = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//caldav//test//EN
BEGIN:VTIMEZONE
TZID:/example.test/Eastern
BEGIN:DAYLIGHT
TZOFFSETFROM:-0500
TZOFFSETTO:-0400
TZNAME:EDT
DTSTART:20070311T020000
RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU
END:DAYLIGHT
BEGIN:STANDARD
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
TZNAME:EST
DTSTART:20071104T020000
RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
UID:eastern@example.test
DTSTAMP:20251201T000000Z
DTSTART;TZID=/example.test/Eastern:20260115T120000
DTEND;TZID=/example.test/Eastern:20260115T130000
SUMMARY:Noon Eastern
END:VEVENT
END:VCALENDAR
`

// The defect: go-ical resolves a TZID only through time.LoadLocation, so an
// event whose zone is defined by an embedded VTIMEZONE it cannot load fails
// outright. This pins that failure so the fix that removes it is discriminating.
func TestMatchFailsOnEmbeddedTimezoneWithoutResolution(t *testing.T) {
	comp := firstEvent(t, easternEventCalendar)
	_, err := matchCompTimeRange(toDate(t, "20260115T170000Z"), toDate(t, "20260115T173000Z"), comp)
	if err == nil {
		t.Fatal("expected a raw embedded-TZID event to fail time-range matching; go-ical cannot load the zone")
	}
}

func TestResolveObjectTimesEnablesEmbeddedTimezoneMatch(t *testing.T) {
	cal := parseCalendar(t, easternEventCalendar)
	resolved, err := resolveObjectTimes(cal, nil)
	if err != nil {
		t.Fatalf("resolveObjectTimes: %v", err)
	}

	var event *ical.Component
	for _, child := range resolved.Children {
		if child.Name == ical.CompEvent {
			event = child
		}
	}
	if event == nil {
		t.Fatal("resolved calendar has no VEVENT")
	}

	if got := event.Props.Get(ical.PropDateTimeStart); got.Value != "20260115T170000Z" {
		t.Errorf("DTSTART = %q, want 20260115T170000Z (noon EST is 17:00Z)", got.Value)
	}
	if tzid := event.Props.Get(ical.PropDateTimeStart).Params.Get(ical.ParamTimezoneID); tzid != "" {
		t.Errorf("resolved DTSTART still carries TZID=%q; it must be dropped once the value is UTC", tzid)
	}

	matched, err := matchCompTimeRange(toDate(t, "20260115T170000Z"), toDate(t, "20260115T173000Z"), event)
	if err != nil {
		t.Fatalf("matchCompTimeRange over the true window: %v", err)
	}
	if !matched {
		t.Error("resolved event did not match its true UTC interval")
	}

	naive, err := matchCompTimeRange(toDate(t, "20260115T120000Z"), toDate(t, "20260115T123000Z"), event)
	if err != nil {
		t.Fatalf("matchCompTimeRange over the naive window: %v", err)
	}
	if naive {
		t.Error("resolved event matched 12:00Z, the wall-clock read as if it were UTC; the zone offset was not applied")
	}
}

func TestQueryTimezoneSelection(t *testing.T) {
	defaultTZ, err := ParseTimezone([]byte(easternVTIMEZONE))
	if err != nil {
		t.Fatalf("ParseTimezone: %v", err)
	}
	noon := time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)

	t.Run("request timezone wins", func(t *testing.T) {
		r, err := queryTimezone(easternVTIMEZONE, Timezone{})
		if err != nil {
			t.Fatalf("queryTimezone: %v", err)
		}
		off, err := r.offsetAt(noon)
		if err != nil {
			t.Fatalf("offsetAt: %v", err)
		}
		if off != -5*time.Hour {
			t.Errorf("offset = %v, want EST from the request timezone", off)
		}
	})

	t.Run("falls back to the calendar default", func(t *testing.T) {
		r, err := queryTimezone("", defaultTZ)
		if err != nil {
			t.Fatalf("queryTimezone: %v", err)
		}
		if r == nil {
			t.Fatal("no resolver from the calendar default")
		}
		off, err := r.offsetAt(noon)
		if err != nil {
			t.Fatalf("offsetAt: %v", err)
		}
		if off != -5*time.Hour {
			t.Errorf("offset = %v, want EST from the calendar default", off)
		}
	})

	t.Run("no timezone at all is nil", func(t *testing.T) {
		r, err := queryTimezone("", Timezone{})
		if err != nil || r != nil {
			t.Errorf("queryTimezone(\"\", zero) = %v, %v; want nil, nil", r, err)
		}
	})

	t.Run("a malformed request timezone is a 400", func(t *testing.T) {
		if _, err := queryTimezone("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n", Timezone{}); err == nil {
			t.Error("expected an error for a timezone element with no VTIMEZONE")
		}
	})
}

func TestParseUTCOffset(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"+0000", 0, true},
		{"-0500", -5 * time.Hour, true},
		{"+0530", 5*time.Hour + 30*time.Minute, true},
		{"-0430", -(4*time.Hour + 30*time.Minute), true},
		{"+001530", 15*time.Minute + 30*time.Second, true},
		{"0500", 0, false},
		{"+05", 0, false},
		{"+05xx", 0, false},
	} {
		got, err := parseUTCOffset(tc.in)
		if tc.ok && err != nil {
			t.Errorf("parseUTCOffset(%q) = %v, want %v", tc.in, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Errorf("parseUTCOffset(%q) = %v, want an error", tc.in, got)
		}
		if tc.ok && got != tc.want {
			t.Errorf("parseUTCOffset(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The offsets are the load-bearing part: a wall clock in winter reads at
// standard offset, one in summer at daylight offset, and the switch happens at
// the RRULE-defined onset, not on the calendar quarter.
func TestResolverOffsetAt(t *testing.T) {
	r := easternResolver(t)
	naive := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse("20060102T150405", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct {
		name string
		wall string
		want time.Duration
	}{
		{"deep winter is EST", "20260115T120000", -5 * time.Hour},
		{"deep summer is EDT", "20260715T120000", -4 * time.Hour},
		{"noon before the spring onset is still EST", "20260308T000000", -5 * time.Hour},
		{"noon after the spring onset is EDT", "20260308T120000", -4 * time.Hour},
		{"before the autumn fall-back is EDT", "20261101T000000", -4 * time.Hour},
		{"after the autumn onset is EST", "20261101T120000", -5 * time.Hour},
		{"a year with no explicit onset still resolves", "20500115T120000", -5 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.offsetAt(naive(tc.wall))
			if err != nil {
				t.Fatalf("offsetAt: %v", err)
			}
			if got != tc.want {
				t.Errorf("offsetAt(%s) = %v, want %v", tc.wall, got, tc.want)
			}
		})
	}
}
