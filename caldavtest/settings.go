package caldavtest

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/mniehe/davkit/caldav"
)

// furnishedRef is a calendar seeded with every settable field set to something
// distinguishable, so a field silently dropped in storage shows up.
var furnishedRef = caldav.CalendarRef{Account: Alice, Calendar: caldav.MustSegment("furnished")}

// testSettings covers a calendar's own fields surviving storage.
//
// Not every field is reachable from here. CreateCalendarRequest carries no
// SortOrder and no MaxItemSize, so a fixture cannot seed them: SortOrder is
// reached through CalendarUpdater instead, and MaxItemSize cannot be set over
// this interface at all. That last one is an interface gap, not a coverage gap,
// and the suite says so rather than pretending to test it.
func testSettings(t *testing.T, newHarness NewHarness, cfg *config) {
	ctx := context.Background()

	timezone, err := caldav.TimezoneFor(time.UTC)
	if err != nil {
		t.Fatalf("building a timezone for the fixture: %v", err)
	}

	furnished := Fixture{Calendars: []FixtureCalendar{{
		Ref: furnishedRef,
		Settings: caldav.Calendar{
			Name:        furnishedRef.Calendar,
			DisplayName: "Furnished",
			Description: "every settable field carries a distinguishable value",
			Color:       "#ff8800",
			Timezone:    timezone,
			Accepts:     caldav.OnlyItemKinds(caldav.Task, caldav.Note),
		},
	}}}

	assertFields := func(t *testing.T, what string, got caldav.Calendar) {
		t.Helper()

		if got.DisplayName != "Furnished" {
			t.Errorf("%s: DisplayName = %q, want %q", what, got.DisplayName, "Furnished")
		}
		if got.Description == "" {
			t.Errorf("%s: Description was dropped", what)
		}
		if got.Color != "#ff8800" {
			t.Errorf("%s: Color = %q, want %q — clients render the calendar with it", what, got.Color, "#ff8800")
		}
		if !bytes.Equal(got.Timezone.Bytes(), timezone.Bytes()) {
			t.Errorf("%s: the timezone did not round-trip; every floating time in the calendar is then resolved against the wrong zone", what)
		}
		if got.Accepts.Allows(caldav.Event) {
			t.Errorf("%s: Accepts allows events, but the calendar was created accepting only tasks and notes — a dropped restriction lets clients store what the calendar refuses", what)
		}
		if !got.Accepts.Allows(caldav.Task) || !got.Accepts.Allows(caldav.Note) {
			t.Errorf("%s: Accepts rejects a kind the calendar was created with: %v", what, got.Accepts.Kinds())
		}
	}

	t.Run("CalendarFieldsRoundTrip", func(t *testing.T) {
		b := setup(ctx, t, newHarness, furnished)

		got, err := b.GetCalendar(ctx, furnishedRef)
		if err != nil {
			t.Fatalf("GetCalendar: %v", err)
		}
		assertFields(t, "GetCalendar", got)

		// The two read paths are separate code in most backends, and a listing
		// that reports a thinner calendar than a fetch is a real and quiet bug.
		cals, err := b.ListCalendars(ctx, Alice)
		if err != nil {
			t.Fatalf("ListCalendars: %v", err)
		}
		idx := slices.IndexFunc(cals, func(c caldav.Calendar) bool { return c.Name == furnishedRef.Calendar })
		if idx < 0 {
			t.Fatalf("ListCalendars did not return %s", furnishedRef.Calendar)
		}
		assertFields(t, "ListCalendars", cals[idx])
	})

	t.Run("SortOrderRoundTrips", func(t *testing.T) {
		b := setup(ctx, t, newHarness, furnished)
		u, ok := b.(caldav.CalendarUpdater)
		cfg.need(t, CapCalendarUpdater, ok)

		order := 42
		updated, err := u.CompareAndUpdateCalendar(ctx, furnishedRef, caldav.CalendarPatch{SortOrder: caldav.SetValue(order)}, caldav.Unconditional())
		if err != nil {
			t.Fatalf("CompareAndUpdateCalendar: %v", err)
		}
		if updated.SortOrder == nil || *updated.SortOrder != order {
			t.Fatalf("the update returned SortOrder %v, want %d", updated.SortOrder, order)
		}

		got, err := b.GetCalendar(ctx, furnishedRef)
		if err != nil {
			t.Fatalf("GetCalendar: %v", err)
		}
		if got.SortOrder == nil {
			t.Fatal("SortOrder came back nil after being set; the client's calendar list falls back to whatever order storage happens to produce")
		}
		if *got.SortOrder != order {
			t.Errorf("SortOrder = %d, want %d", *got.SortOrder, order)
		}
	})
}
