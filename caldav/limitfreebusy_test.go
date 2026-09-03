package caldav_test

import (
	"strings"
	"testing"

	"github.com/mniehe/davkit/caldav"
)

const outOfWindowBusyICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
	"BEGIN:VFREEBUSY\r\nUID:busy\r\nDTSTAMP:20260801T000000Z\r\n" +
	"FREEBUSY:20261010T090000Z/20261010T100000Z\r\nEND:VFREEBUSY\r\nEND:VCALENDAR\r\n"

func TestQueryLimitFreeBusySetDropsAFreeBusyLeftWithNoValues(t *testing.T) {
	store := newStore(t)
	seedKind(t, store, "alice", "busy.ics", outOfWindowBusyICS, "busy", caldav.Availability)
	h := handlerFor(t, store, caldav.Config{})

	data := reportMS(t, h, "/alice/work/", limitFreeBusyQuery("20260801T000000Z", "20260901T000000Z")).
		at(t, "/alice/work/busy.ics").value(t, caldavName("calendar-data"))

	// RFC 4791 §9.6.7: the component stays, but a FREEBUSY with nothing left in
	// the window is removed rather than returned empty.
	if !strings.Contains(data, "BEGIN:VFREEBUSY") {
		t.Errorf("calendar-data = %q, the VFREEBUSY itself must survive", data)
	}
	if strings.Contains(data, "FREEBUSY:") {
		t.Errorf("calendar-data = %q, want no FREEBUSY property once every value fell outside the window", data)
	}
}

// mixedBusyICS holds the three component kinds an object can mix, so the
// limit's reach can be seen as well as its effect.
const mixedBusyICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
	"BEGIN:VTIMEZONE\r\nTZID:Test/Plus10\r\nBEGIN:STANDARD\r\nDTSTART:19700101T000000\r\n" +
	"TZOFFSETFROM:+1000\r\nTZOFFSETTO:+1000\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\nUID:mixed-event\r\nDTSTAMP:20260801T000000Z\r\n" +
	"DTSTART:20261111T090000Z\r\nDTEND:20261111T100000Z\r\nSUMMARY:UntouchedEvent\r\nEND:VEVENT\r\n" +
	"BEGIN:VFREEBUSY\r\nUID:mixed-busy\r\nDTSTAMP:20260801T000000Z\r\n" +
	"FREEBUSY:20260810T090000Z/20260810T100000Z,20261010T090000Z/20261010T100000Z\r\n" +
	"END:VFREEBUSY\r\nEND:VCALENDAR\r\n"

func TestQueryLimitFreeBusySetLeavesOtherComponentsAlone(t *testing.T) {
	store := newStore(t)
	seedKind(t, store, "alice", "mixed.ics", mixedBusyICS, "mixed", caldav.Availability)
	h := handlerFor(t, store, caldav.Config{})

	data := reportMS(t, h, "/alice/work/", limitFreeBusyQuery("20260801T000000Z", "20260901T000000Z")).
		at(t, "/alice/work/mixed.ics").value(t, caldavName("calendar-data"))

	if !strings.Contains(data, "SUMMARY:UntouchedEvent") || !strings.Contains(data, "20261111T090000Z") {
		t.Errorf("calendar-data = %q, limit-freebusy-set must not touch a VEVENT outside the window", data)
	}
	if !strings.Contains(data, "BEGIN:VTIMEZONE") {
		t.Errorf("calendar-data = %q, the object's own VTIMEZONE must survive", data)
	}
	if !strings.Contains(data, "20260810T090000Z/20260810T100000Z") {
		t.Errorf("calendar-data = %q, want the FREEBUSY period inside the window", data)
	}
	if strings.Contains(data, "20261010T090000Z") {
		t.Errorf("calendar-data = %q, a FREEBUSY period outside the window leaked", data)
	}
}
