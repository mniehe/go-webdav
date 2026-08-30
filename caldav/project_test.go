package caldav

import (
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

const projectionSource = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
CALSCALE:GREGORIAN
BEGIN:VTIMEZONE
TZID:Europe/London
BEGIN:STANDARD
DTSTART:19701025T020000
TZOFFSETFROM:+0100
TZOFFSETTO:+0000
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
UID:uid-1
DTSTAMP:20260101T000000Z
DTSTART:20260101T100000Z
DTEND:20260101T110000Z
SUMMARY:Quarterly review
DESCRIPTION:Salary bands and headcount
LOCATION:Room 3
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER:-PT15M
END:VALARM
END:VEVENT
END:VCALENDAR`

func projectionObject(t *testing.T) calendarObject {
	t.Helper()
	return calendarObject{Path: "/u/c/e.ics", Data: parseCalendar(t, projectionSource)}
}

func componentNamed(comp *ical.Component, name string) *ical.Component {
	for _, child := range comp.Children {
		if child.Name == name {
			return child
		}
	}
	return nil
}

// RFC 4791 §9.6.1: a calendar-data naming components and properties asks for
// exactly those. Returning more discloses event content the client's request
// deliberately excluded.
func TestFilterProjectsRequestedProperties(t *testing.T) {
	query := &calendarQuery{
		CompFilter: compFilter{Name: "VCALENDAR"},
		CompRequest: calendarCompRequest{
			Name: "VCALENDAR",
			Comps: []calendarCompRequest{{
				Name:  "VEVENT",
				Props: []string{"UID"},
			}},
		},
	}

	out, err := filterObjects(query, []calendarObject{projectionObject(t)})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d objects, want 1", len(out))
	}

	event := componentNamed(out[0].Data.Component, ical.CompEvent)
	if event == nil {
		t.Fatal("the projection dropped the VEVENT entirely")
	}
	if got := event.Props.Get("UID"); got == nil || got.Value != "uid-1" {
		t.Error("the requested UID was not returned")
	}
	for _, name := range []string{"SUMMARY", "DESCRIPTION", "LOCATION"} {
		if event.Props.Get(name) != nil {
			t.Errorf("%s was returned although only UID was requested", name)
		}
	}
	// RFC 4791 §9.6: the result must still be a valid iCalendar object.
	if event.Props.Get(ical.PropDateTimeStamp) == nil {
		t.Error("DTSTAMP was dropped, leaving an invalid VEVENT")
	}
	root := out[0].Data.Component
	for _, name := range []string{ical.PropVersion, ical.PropProductID} {
		if root.Props.Get(name) == nil {
			t.Errorf("%s was dropped, leaving an invalid VCALENDAR", name)
		}
	}
}

// A named comp list excludes the components it does not name.
func TestFilterProjectsRequestedComponents(t *testing.T) {
	query := &calendarQuery{
		CompFilter: compFilter{Name: "VCALENDAR"},
		CompRequest: calendarCompRequest{
			Name: "VCALENDAR",
			Comps: []calendarCompRequest{{
				Name:     "VEVENT",
				AllProps: true,
			}},
		},
	}

	out, err := filterObjects(query, []calendarObject{projectionObject(t)})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}

	root := out[0].Data.Component
	if componentNamed(root, ical.CompTimezone) != nil {
		t.Error("VTIMEZONE was returned although only VEVENT was requested")
	}
	event := componentNamed(root, ical.CompEvent)
	if event == nil {
		t.Fatal("VEVENT was dropped")
	}
	if componentNamed(event, "VALARM") != nil {
		t.Error("VALARM was returned although the VEVENT named no sub-components")
	}
	if event.Props.Get("SUMMARY") == nil {
		t.Error("allprop dropped SUMMARY")
	}
}

// allcomp and allprop ask for everything, and no projection at all returns the
// object untouched.
func TestFilterProjectionPassthrough(t *testing.T) {
	tests := []struct {
		name string
		req  calendarCompRequest
	}{
		{"allprop and allcomp", calendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true}},
		{"no calendar-data requested", calendarCompRequest{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			query := &calendarQuery{CompFilter: compFilter{Name: "VCALENDAR"}, CompRequest: tc.req}
			out, err := filterObjects(query, []calendarObject{projectionObject(t)})
			if err != nil {
				t.Fatalf("Filter: %v", err)
			}
			root := out[0].Data.Component
			if componentNamed(root, ical.CompTimezone) == nil {
				t.Error("VTIMEZONE was dropped")
			}
			event := componentNamed(root, ical.CompEvent)
			if event == nil {
				t.Fatal("VEVENT was dropped")
			}
			if event.Props.Get("DESCRIPTION") == nil {
				t.Error("DESCRIPTION was dropped")
			}
			if componentNamed(event, "VALARM") == nil {
				t.Error("VALARM was dropped")
			}
		})
	}
}

// The projection must not mutate the caller's objects: a Backend commonly hands
// back slices into its own cache.
func TestFilterKeepsRecurrencePropertiesForExpansion(t *testing.T) {
	recurring := parseCalendar(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:uid-r
DTSTAMP:20251201T090000Z
DTSTART:20251201T100000Z
DTEND:20251201T110000Z
SUMMARY:Standup
RRULE:FREQ=DAILY
END:VEVENT
END:VCALENDAR
`)

	query := &calendarQuery{
		CompFilter: compFilter{Name: "VCALENDAR"},
		CompRequest: calendarCompRequest{
			Name: "VCALENDAR",
			Comps: []calendarCompRequest{{
				Name:  "VEVENT",
				Props: []string{"UID", "DTSTART", "SUMMARY"},
			}},
			Expand: &calendarExpandRequest{
				Start: utc(2026, time.January, 1, 0),
				End:   utc(2026, time.February, 1, 0),
			},
		},
	}

	out, err := filterObjects(query, []calendarObject{{Path: "/u/c/r.ics", Data: recurring}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d objects, want 1", len(out))
	}

	event := componentNamed(out[0].Data.Component, ical.CompEvent)
	if event == nil {
		t.Fatal("the projected object has no VEVENT")
	}
	if event.Props.Get(ical.PropRecurrenceRule) == nil {
		t.Fatal("RRULE was projected away, so the server's expansion would yield no instances")
	}

	expanded, err := expandCalendar(out[0].Data, query.CompRequest.Expand)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(expanded.Children); n != 31 {
		t.Errorf("expansion produced %d instances, want 31", n)
	}
}

// A comp tree rooted at anything but VCALENDAR matches nothing, so the
// projection would silently return the whole object.

// Expansion runs before projection, so the copy the projection takes cannot
// protect the Backend's object from it. Expanding in place replaces a stored
// recurring event with the handful of instances one client happened to ask for.
