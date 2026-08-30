package caldav

import "testing"

// A property may appear more than once in a component. RFC 4791 section 9.7.2
// matches a prop-filter against the property of the named type, so a component
// carrying several of them matches when one of them satisfies the filter.
const repeatedAttendeeICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:u1
DTSTAMP:20260101T000000Z
DTSTART:20260101T100000Z
ATTENDEE;PARTSTAT=ACCEPTED:mailto:alice@example.com
ATTENDEE;PARTSTAT=DECLINED:mailto:bob@example.com
END:VEVENT
END:VCALENDAR`

func repeatedAttendeeObject(t *testing.T) *calendarObject {
	t.Helper()
	return &calendarObject{Path: "/u/c/e.ics", Data: parseCalendar(t, repeatedAttendeeICS)}
}

func attendeePropFilter(f *propFilter) *compFilter {
	f.Name = "ATTENDEE"
	return &compFilter{
		Name:  "VCALENDAR",
		Comps: []compFilter{{Name: "VEVENT", Props: []propFilter{*f}}},
	}
}

// Only the first ATTENDEE is examined today, so a filter naming the second one
// reports no match and the object is withheld from a legitimate query.
func TestPropFilterMatchesALaterOccurrence(t *testing.T) {
	matched, err := matchObject(attendeePropFilter(&propFilter{TextMatch: &textMatch{Text: "bob@example.com"}}), repeatedAttendeeObject(t))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched {
		t.Error("a text-match against the second ATTENDEE did not match")
	}
}

// The same, through a param-filter: the second ATTENDEE is the one that carries
// PARTSTAT=DECLINED.
func TestPropFilterMatchesParamOnALaterOccurrence(t *testing.T) {
	filter := propFilter{ParamFilter: []paramFilter{{Name: "PARTSTAT", TextMatch: &textMatch{Text: "DECLINED"}}}}
	matched, err := matchObject(attendeePropFilter(&filter), repeatedAttendeeObject(t))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched {
		t.Error("a param-filter satisfied by the second ATTENDEE did not match")
	}
}

// Conditions must be satisfied by one occurrence, not collected across several.
// PARTSTAT=ACCEPTED holds for alice and the address matches bob, so no single
// ATTENDEE satisfies both and the object must not match.
func TestPropFilterConditionsMustHoldOnOneOccurrence(t *testing.T) {
	filter := propFilter{
		ParamFilter: []paramFilter{{Name: "PARTSTAT", TextMatch: &textMatch{Text: "ACCEPTED"}}},
		TextMatch:   &textMatch{Text: "bob@example.com"},
	}
	matched, err := matchObject(attendeePropFilter(&filter), repeatedAttendeeObject(t))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if matched {
		t.Error("conditions satisfied by different ATTENDEEs were treated as a match")
	}
}

// carddav returns false when is-not-defined names a property that is present;
// caldav has no such guard, so it falls through to "property exists" and
// reports a match. That returns objects the client asked to exclude.
func TestPropFilterIsNotDefinedRejectsAPresentProperty(t *testing.T) {
	matched, err := matchObject(attendeePropFilter(&propFilter{IsNotDefined: true}), repeatedAttendeeObject(t))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if matched {
		t.Error("is-not-defined matched a component whose ATTENDEE is defined")
	}
}

// The guard for the tests above: a filter naming the first occurrence, and one
// naming a property that genuinely is absent, must keep their answers.
func TestPropFilterKeepsSimpleAnswers(t *testing.T) {
	matched, err := matchObject(attendeePropFilter(&propFilter{TextMatch: &textMatch{Text: "alice@example.com"}}), repeatedAttendeeObject(t))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched {
		t.Error("a text-match against the first ATTENDEE stopped matching")
	}

	matched, err = matchObject(attendeePropFilter(&propFilter{TextMatch: &textMatch{Text: "carol@example.com"}}), repeatedAttendeeObject(t))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if matched {
		t.Error("a text-match matching no ATTENDEE reported a match")
	}

	absent := &compFilter{
		Name:  "VCALENDAR",
		Comps: []compFilter{{Name: "VEVENT", Props: []propFilter{{Name: "ORGANIZER", IsNotDefined: true}}}},
	}
	matched, err = matchObject(absent, repeatedAttendeeObject(t))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched {
		t.Error("is-not-defined stopped matching a genuinely absent property")
	}
}

// A parameter may carry several values. carddav already matches any one of them;
// caldav reads only the first, so a filter naming a later value reports no match.
func TestParamFilterMatchesALaterValue(t *testing.T) {
	object := func(t *testing.T) *calendarObject {
		t.Helper()
		cal := parseCalendar(t, repeatedAttendeeICS)
		cal.Children[0].Props.Get("ATTENDEE").Params["MEMBER"] = []string{"mailto:staff@example.com", "mailto:board@example.com"}
		return &calendarObject{Path: "/u/c/e.ics", Data: cal}
	}

	filter := propFilter{ParamFilter: []paramFilter{{Name: "MEMBER", TextMatch: &textMatch{Text: "mailto:board@example.com"}}}}
	matched, err := matchObject(attendeePropFilter(&filter), object(t))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched {
		t.Error("a param-filter naming the second MEMBER value did not match")
	}

	// The guard: a value present in neither entry must still not match.
	absent := propFilter{ParamFilter: []paramFilter{{Name: "MEMBER", TextMatch: &textMatch{Text: "mailto:nobody@example.com"}}}}
	matched, err = matchObject(attendeePropFilter(&absent), object(t))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if matched {
		t.Error("a param-filter matching no MEMBER value reported a match")
	}
}
