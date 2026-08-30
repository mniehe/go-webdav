package caldavtest

import (
	"context"
	"strings"
	"testing"

	"github.com/mniehe/davkit/caldav"
)

// Fixture is the state a harness must put in place before a scenario runs.
type Fixture struct {
	Calendars []FixtureCalendar
}

// FixtureCalendar is one seeded calendar.
type FixtureCalendar struct {
	Ref      caldav.CalendarRef
	Settings caldav.Calendar
	Items    []caldav.Item

	// Viewers are accounts other than the owner that may read the calendar. The
	// suite expects the harness to grant the owner OwnerPermissions, each viewer
	// ViewOnlyPermissions, and everyone else nothing — that is the only way a
	// suite can test an Authorizer whose policy is otherwise the application's.
	Viewers []caldav.AccountID
}

// Item revisions in a fixture are ignored: the backend assigns its own.

// The accounts and calendars every scenario works with.
var (
	Alice = caldav.AccountID("alice")
	Bob   = caldav.AccountID("bob")
	Carol = caldav.AccountID("carol")

	WorkRef     = caldav.CalendarRef{Account: Alice, Calendar: caldav.MustSegment("work")}
	PersonalRef = caldav.CalendarRef{Account: Carol, Calendar: caldav.MustSegment("personal")}

	// MissingRef names a calendar no fixture ever seeds.
	MissingRef = caldav.CalendarRef{Account: Alice, Calendar: caldav.MustSegment("no-such-calendar")}
)

func itemRef(cal caldav.CalendarRef, name string) caldav.ItemRef {
	return caldav.ItemRef{Calendar: cal, Item: caldav.MustSegment(name)}
}

// event builds a minimal valid VEVENT under the given UID.
func event(uid, summary string) []byte {
	return []byte(strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//caldavtest//EN",
		"BEGIN:VEVENT",
		"UID:" + uid,
		"DTSTAMP:20260101T000000Z",
		"DTSTART:20260101T090000Z",
		"DTEND:20260101T100000Z",
		"SUMMARY:" + summary,
		"END:VEVENT",
		"END:VCALENDAR",
		"",
	}, "\r\n"))
}

// awkwardEvent is valid iCalendar that a backend is tempted to normalise:
// CRLF line endings, a folded line, non-ASCII text, an unknown X- property
// carrying a parameter, an escaped newline, and no trailing CRLF. Obligation 1
// says every byte of it comes back.
func awkwardEvent(uid string) []byte {
	return []byte(strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//caldavtest//Awkward//EN",
		"BEGIN:VEVENT",
		"UID:" + uid,
		"DTSTAMP:20260101T000000Z",
		"DTSTART:20260101T090000Z",
		"DTEND:20260101T100000Z",
		"SUMMARY:Café — déjeuner with a summary long enough that it has to b",
		" e folded across two content lines",
		"DESCRIPTION:first line\\nsecond line",
		"X-CALDAVTEST-CUSTOM;X-CALDAVTEST-PARAM=odd value:kept verbatim",
		"END:VEVENT",
		"END:VCALENDAR",
	}, "\r\n"))
}

// BaseFixture is the state most scenarios start from: one calendar Alice owns
// and Bob may read, and one belonging to Carol that neither may see.
func BaseFixture() Fixture {
	return Fixture{
		Calendars: []FixtureCalendar{
			{
				Ref: WorkRef,
				Settings: caldav.Calendar{
					Name:        WorkRef.Calendar,
					DisplayName: "Work",
					Description: "Alice at work",
					Accepts:     caldav.AllItemKinds(),
				},
				Viewers: []caldav.AccountID{Bob},
				Items: []caldav.Item{
					{Name: caldav.MustSegment("standup.ics"), ContentID: "standup@example.test", Content: event("standup@example.test", "Standup")},
					{Name: caldav.MustSegment("review.ics"), ContentID: "review@example.test", Content: event("review@example.test", "Review")},
				},
			},
			{
				Ref: PersonalRef,
				Settings: caldav.Calendar{
					Name:        PersonalRef.Calendar,
					DisplayName: "Personal",
					Accepts:     caldav.OnlyItemKinds(caldav.Event),
				},
				Items: []caldav.Item{
					{Name: caldav.MustSegment("gym.ics"), ContentID: "gym@example.test", Content: event("gym@example.test", "Gym")},
				},
			},
		},
	}
}

// setup builds a harness, seeds it and returns the backend.
func setup(ctx context.Context, t *testing.T, newHarness NewHarness, f Fixture) caldav.Backend {
	t.Helper()
	_, backend := setupHarness(ctx, t, newHarness, f)
	return backend
}

func setupHarness(ctx context.Context, t *testing.T, newHarness NewHarness, f Fixture) (Harness, caldav.Backend) {
	t.Helper()

	h, err := newHarness(ctx, t)
	if err != nil {
		t.Fatalf("building the harness: %v", err)
	}
	t.Cleanup(h.Close)

	if seedErr := h.Seed(ctx, f); seedErr != nil {
		t.Fatalf("seeding the fixture: %v", seedErr)
	}

	backend, err := h.Backend(ctx)
	if err != nil {
		t.Fatalf("opening the backend: %v", err)
	}
	if backend == nil {
		t.Fatal("harness returned a nil backend")
	}
	return h, backend
}

// collect drains ListItems into a map keyed by item name.
func collect(ctx context.Context, t *testing.T, b caldav.Backend, ref caldav.CalendarRef) (map[string]caldav.Item, caldav.Revision) {
	t.Helper()

	items := map[string]caldav.Item{}
	rev, err := b.ListItems(ctx, ref, func(item caldav.Item) bool {
		if _, dup := items[item.Name.String()]; dup {
			t.Errorf("ListItems yielded %q twice", item.Name)
		}
		items[item.Name.String()] = item
		return true
	})
	if err != nil {
		t.Fatalf("ListItems(%s): %v", ref.Calendar, err)
	}
	return items, rev
}
