package caldav

import (
	"bytes"
	"strings"
	"testing"
	"time"

	// The suite asserts what happens for a zone that observes daylight saving,
	// so the zone database has to be present wherever the tests run.
	_ "time/tzdata"
)

func vtimezone(body ...string) []byte {
	return []byte(strings.Join(append(append([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//caldav//test//EN",
	}, body...), "END:VCALENDAR", ""), "\r\n"))
}

var utcTimezone = vtimezone(
	"BEGIN:VTIMEZONE",
	"TZID:UTC",
	"BEGIN:STANDARD",
	"DTSTART:19700101T000000",
	"TZNAME:UTC",
	"TZOFFSETFROM:+0000",
	"TZOFFSETTO:+0000",
	"END:STANDARD",
	"END:VTIMEZONE",
)

func TestParseTimezone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    []byte
		valid bool
	}{
		{"one VTIMEZONE", utcTimezone, true},

		{"not iCalendar at all", []byte("this is not iCalendar"), false},
		{"no VTIMEZONE", vtimezone(), false},
		{"an event instead", vtimezone(
			"BEGIN:VEVENT", "UID:x@example.test", "DTSTAMP:20260101T000000Z", "END:VEVENT",
		), false},
		{"two VTIMEZONEs", vtimezone(
			"BEGIN:VTIMEZONE", "TZID:UTC", "END:VTIMEZONE",
			"BEGIN:VTIMEZONE", "TZID:Etc/GMT", "END:VTIMEZONE",
		), false},
		{"no TZID", vtimezone(
			"BEGIN:VTIMEZONE", "END:VTIMEZONE",
		), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tz, err := ParseTimezone(tc.in)
			switch {
			case tc.valid && err != nil:
				t.Fatalf("ParseTimezone = %v, want it accepted", err)
			case !tc.valid && err == nil:
				t.Fatal("ParseTimezone accepted it, want an error")
			case !tc.valid:
				return
			}
			if !bytes.Equal(tz.Bytes(), tc.in) {
				t.Error("Bytes() did not return what was parsed")
			}
			if tz.IsZero() {
				t.Error("a parsed timezone reports IsZero")
			}
		})
	}
}

func TestZeroTimezone(t *testing.T) {
	var zero Timezone
	if !zero.IsZero() {
		t.Error("the zero Timezone does not report IsZero")
	}
	if len(zero.Bytes()) != 0 {
		t.Error("the zero Timezone has bytes")
	}
}

func TestTimezoneBytesDoNotAlias(t *testing.T) {
	source := append([]byte(nil), utcTimezone...)
	tz, err := ParseTimezone(source)
	if err != nil {
		t.Fatalf("ParseTimezone: %v", err)
	}

	for i := range source {
		source[i] = 'X'
	}
	if !bytes.Equal(tz.Bytes(), utcTimezone) {
		t.Error("ParseTimezone kept the caller's slice")
	}

	handed := tz.Bytes()
	for i := range handed {
		handed[i] = 'X'
	}
	if !bytes.Equal(tz.Bytes(), utcTimezone) {
		t.Error("Bytes() handed out the stored slice")
	}
}

func TestTimezoneForFixedOffset(t *testing.T) {
	for _, tc := range []struct {
		name   string
		loc    *time.Location
		offset string
	}{
		{"UTC", time.UTC, "TZOFFSETTO:+0000"},
		{"a fixed positive offset", time.FixedZone("test", 5*3600+30*60), "TZOFFSETTO:+0530"},
		{"a fixed negative offset", time.FixedZone("test", -8*3600), "TZOFFSETTO:-0800"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tz, err := TimezoneFor(tc.loc)
			if err != nil {
				t.Fatalf("TimezoneFor: %v", err)
			}
			if !strings.Contains(string(tz.Bytes()), tc.offset) {
				t.Errorf("the definition does not carry %q:\n%s", tc.offset, tz.Bytes())
			}

			// Whatever it produced has to survive the library's own parser, or
			// it is not a definition a client could be given either.
			if _, err := ParseTimezone(tz.Bytes()); err != nil {
				t.Errorf("TimezoneFor produced something ParseTimezone rejects: %v", err)
			}
		})
	}
}

func TestTimezoneForRefusesADaylightSavingZone(t *testing.T) {
	loc, err := time.LoadLocation("America/Vancouver")
	if err != nil {
		t.Fatalf("loading a zone that observes daylight saving: %v", err)
	}
	if _, err := TimezoneFor(loc); err == nil {
		t.Error("TimezoneFor accepted a zone that changes offset; the definition it can build has no transition rules, so every recurring event after the next transition would be an hour out")
	}
}

func TestTimezoneForRejectsNil(t *testing.T) {
	if _, err := TimezoneFor(nil); err == nil {
		t.Error("TimezoneFor(nil) was accepted")
	}
}
