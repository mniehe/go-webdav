package caldav_test

import (
	"strings"
	"testing"

	"github.com/mniehe/davkit/caldav"
)

// dueOnlyTodoICS is a weekly VTODO carrying a DUE and no DTSTART, with the
// 5 October instance overridden to a DUE inside August.
const dueOnlyTodoICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
	"BEGIN:VTODO\r\nUID:chore\r\nDTSTAMP:20260801T000000Z\r\nDUE:20260803T090000Z\r\nRRULE:FREQ=WEEKLY\r\n" +
	"SUMMARY:Chore\r\nEND:VTODO\r\n" +
	"BEGIN:VTODO\r\nUID:chore\r\nRECURRENCE-ID:20261005T090000Z\r\nDTSTAMP:20260801T000000Z\r\n" +
	"DUE:20260810T090000Z\r\nSUMMARY:MovedIntoAugust\r\nEND:VTODO\r\n" +
	"END:VCALENDAR\r\n"

func TestQueryLimitRecurrenceSetKeepsATodoDueInsideTheWindow(t *testing.T) {
	store := newStore(t)
	seedKind(t, store, "alice", "chore.ics", dueOnlyTodoICS, "chore", caldav.Task)
	h := handlerFor(t, store, caldav.Config{})

	// RFC 4791 §9.6.6 judges the override with the same logic as
	// CALDAV:time-range, and §9.9 gives a VTODO with only a DUE its own row.
	data := reportMS(t, h, "/alice/work/", limitRecurrenceQuery("20260801T000000Z", "20260901T000000Z")).
		at(t, "/alice/work/chore.ics").value(t, caldavName("calendar-data"))

	if !strings.Contains(data, "MovedIntoAugust") {
		t.Errorf("calendar-data = %q, want the override whose current DUE is inside the window", data)
	}
}

// spanningTodoICS is a yearly VTODO spanning July to September, with its first
// instance overridden out to December.
const spanningTodoICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
	"BEGIN:VTODO\r\nUID:span\r\nDTSTAMP:20260801T000000Z\r\nDTSTART:20260701T090000Z\r\nDUE:20260901T090000Z\r\n" +
	"RRULE:FREQ=YEARLY\r\nSUMMARY:Span\r\nEND:VTODO\r\n" +
	"BEGIN:VTODO\r\nUID:span\r\nRECURRENCE-ID:20260701T090000Z\r\nDTSTAMP:20260801T000000Z\r\n" +
	"DTSTART:20261201T090000Z\r\nDUE:20261215T090000Z\r\nSUMMARY:MovedToDecember\r\nEND:VTODO\r\n" +
	"END:VCALENDAR\r\n"

func TestQueryLimitRecurrenceSetKeepsATodoWhoseOriginalSpanCoversTheWindow(t *testing.T) {
	store := newStore(t)
	seedKind(t, store, "alice", "span.ics", spanningTodoICS, "span", caldav.Task)
	h := handlerFor(t, store, caldav.Config{})

	// The RECURRENCE-ID alone falls before the window; only the span the master
	// gives that instance reaches into it.
	data := reportMS(t, h, "/alice/work/", limitRecurrenceQuery("20260801T000000Z", "20260901T000000Z")).
		at(t, "/alice/work/span.ics").value(t, caldavName("calendar-data"))

	if !strings.Contains(data, "MovedToDecember") {
		t.Errorf("calendar-data = %q, want the override whose original span covers the window", data)
	}
}

// orphanOverrideICS is a legal detached instance: one VEVENT carrying a
// RECURRENCE-ID with no master alongside it.
const orphanOverrideICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:orphan\r\nDTSTAMP:20260801T000000Z\r\nRECURRENCE-ID:20261005T090000Z\r\n" +
	"DTSTART:20261005T110000Z\r\nDTEND:20261005T113000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func TestQueryShapingAwayEveryComponentStillAnswers(t *testing.T) {
	// RFC 4791 §9.6 allows the calendar-data a report returns to be invalid per
	// its media type. A shaped object left with no components is one of those
	// cases, and refusing to encode it poisons the whole multistatus.
	bodies := map[string]string{
		"limit-recurrence-set": limitRecurrenceQuery("20260801T000000Z", "20260901T000000Z"),
		"expand":               expandQuery("20260801T000000Z", "20260901T000000Z"),
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			seedRaw(t, store, "alice", "orphan.ics", orphanOverrideICS, "orphan")
			h := handlerFor(t, store, caldav.Config{})

			ms := reportMS(t, h, "/alice/work/", body)
			if got := ms.hrefs(); len(got) != 1 || got[0] != "/alice/work/orphan.ics" {
				t.Fatalf("hrefs = %v, want a row for the item", got)
			}
		})
	}
}
