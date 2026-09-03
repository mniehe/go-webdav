package caldavtest

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/mniehe/davkit/caldav"
)

func testCapabilities(t *testing.T, newHarness NewHarness, cfg *config) {
	ctx := context.Background()

	t.Run("CreateCalendar", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		c, ok := b.(caldav.CalendarCreator)
		cfg.need(t, CapCalendarCreator, ok)

		name := caldav.MustSegment("side-project")
		created, err := c.CompareAndCreateCalendar(ctx, Alice, caldav.CreateCalendarRequest{
			Name:        name,
			DisplayName: "Side project",
			Accepts:     caldav.OnlyItemKinds(caldav.Task),
		}, caldav.IfTargetMissing())
		if err != nil {
			t.Fatalf("CreateCalendar: %v", err)
		}
		if created.Name != name {
			t.Errorf("CreateCalendar returned Name %q, want %q", created.Name, name)
		}

		ref := caldav.CalendarRef{Account: Alice, Calendar: name}
		got, err := b.GetCalendar(ctx, ref)
		if err != nil {
			t.Fatalf("GetCalendar after creating it: %v", err)
		}
		if got.DisplayName != "Side project" {
			t.Errorf("DisplayName = %q, want %q", got.DisplayName, "Side project")
		}
		if got.Accepts.Allows(caldav.Event) {
			t.Error("the new calendar accepts events, but it was created accepting only tasks")
		}

		cals, err := b.ListCalendars(ctx, Alice)
		if err != nil {
			t.Fatalf("ListCalendars: %v", err)
		}
		if !slices.ContainsFunc(cals, func(c caldav.Calendar) bool { return c.Name == name }) {
			t.Error("the new calendar is not in ListCalendars")
		}
	})

	t.Run("UpdateCalendarPatchesOnlyWhatIsSet", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		u, ok := b.(caldav.CalendarUpdater)
		cfg.need(t, CapCalendarUpdater, ok)

		display := "Work, renamed"
		empty := ""
		updated, err := u.CompareAndUpdateCalendar(ctx, WorkRef, caldav.CalendarPatch{
			DisplayName: &display,
			Description: &empty,
		}, caldav.Unconditional())
		if err != nil {
			t.Fatalf("CompareAndUpdateCalendar: %v", err)
		}
		if updated.DisplayName != display {
			t.Errorf("DisplayName = %q, want %q", updated.DisplayName, display)
		}
		if updated.Description != "" {
			t.Errorf("Description = %q, want it cleared — a non-nil pointer to the empty string means set, not untouched", updated.Description)
		}
		if updated.Name != WorkRef.Calendar {
			t.Errorf("Name = %q, want %q — a patch cannot move a calendar", updated.Name, WorkRef.Calendar)
		}

		got, err := b.GetCalendar(ctx, WorkRef)
		if err != nil {
			t.Fatalf("GetCalendar: %v", err)
		}
		if got.DisplayName != display || got.Description != "" {
			t.Errorf("GetCalendar reports %q/%q, the update returned %q/%q", got.DisplayName, got.Description, display, "")
		}
	})

	t.Run("UpdateCalendarChecksPreconditions", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		u, ok := b.(caldav.CalendarUpdater)
		cfg.need(t, CapCalendarUpdater, ok)

		cal, err := b.GetCalendar(ctx, WorkRef)
		if err != nil {
			t.Fatalf("GetCalendar: %v", err)
		}

		display := "Renamed"
		_, err = u.CompareAndUpdateCalendar(ctx, WorkRef, caldav.CalendarPatch{DisplayName: &display}, caldav.IfRevision(cal.Revision+1000))
		if !errors.Is(err, caldav.ErrPreconditionFailed) {
			t.Fatalf("updating from a revision the calendar is not at = %v, want ErrPreconditionFailed", err)
		}

		after, err := b.GetCalendar(ctx, WorkRef)
		if err != nil {
			t.Fatalf("GetCalendar: %v", err)
		}
		if after.DisplayName == display {
			t.Error("the refused update was applied anyway")
		}
	})

	t.Run("DeleteCalendarTakesItsItems", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		d, ok := b.(caldav.CalendarDeleter)
		cfg.need(t, CapCalendarDeleter, ok)

		if err := d.CompareAndDeleteCalendar(ctx, WorkRef, caldav.Unconditional()); err != nil {
			t.Fatalf("CompareAndDeleteCalendar: %v", err)
		}

		if _, err := b.GetCalendar(ctx, WorkRef); !errors.Is(err, caldav.ErrNotFound) {
			t.Errorf("GetCalendar after deleting = %v, want ErrNotFound", err)
		}
		if _, err := b.GetItem(ctx, itemRef(WorkRef, "standup.ics")); !errors.Is(err, caldav.ErrParentNotFound) {
			t.Errorf("GetItem in a deleted calendar = %v, want ErrParentNotFound", err)
		}

		cals, err := b.ListCalendars(ctx, Alice)
		if err != nil {
			t.Fatalf("ListCalendars: %v", err)
		}
		if slices.ContainsFunc(cals, func(c caldav.Calendar) bool { return c.Name == WorkRef.Calendar }) {
			t.Error("the deleted calendar is still listed")
		}
	})

	t.Run("SharesReportTheViewers", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		s, ok := b.(caldav.SharingBackend)
		cfg.need(t, CapSharingBackend, ok)

		shares, err := s.Shares(ctx, WorkRef)
		if err != nil {
			t.Fatalf("Shares: %v", err)
		}
		idx := slices.IndexFunc(shares, func(sh caldav.Share) bool { return sh.Account == Bob })
		if idx < 0 {
			t.Fatalf("Shares returned %v, want one for bob", shares)
		}
		if !shares[idx].Permissions.ViewDetails {
			t.Error("bob's share does not grant ViewDetails, but he can read the calendar")
		}
		if slices.ContainsFunc(shares, func(sh caldav.Share) bool { return sh.Account == Alice }) {
			t.Error("Shares includes the owner; owning a calendar is not a share of it")
		}

		if own, err := s.Shares(ctx, PersonalRef); err != nil {
			t.Errorf("Shares on an unshared calendar = %v, want an empty list", err)
		} else if len(own) != 0 {
			t.Errorf("Shares on an unshared calendar returned %v", own)
		}
	})
}

// testCalendarLifecycle covers what happens around a calendar's creation and
// destruction, which is where identity has to be got right.
func testCalendarLifecycle(t *testing.T, newHarness NewHarness, cfg *config) {
	ctx := context.Background()

	t.Run("RecreatedCalendarIsANewCalendar", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		c, ok := b.(caldav.CalendarCreator)
		cfg.need(t, CapCalendarCreator, ok)
		d, ok := b.(caldav.CalendarDeleter)
		cfg.need(t, CapCalendarDeleter, ok)

		before, err := b.GetCalendar(ctx, WorkRef)
		if err != nil {
			t.Fatalf("GetCalendar: %v", err)
		}
		if before.ID == "" {
			t.Fatal("the calendar has no CalendarID; every entity tag and sync token the handler issues is scoped by it")
		}

		if deleted := d.CompareAndDeleteCalendar(ctx, WorkRef, caldav.Unconditional()); deleted != nil {
			t.Fatalf("CompareAndDeleteCalendar: %v", deleted)
		}
		after, err := c.CompareAndCreateCalendar(ctx, WorkRef.Account,
			caldav.CreateCalendarRequest{Name: WorkRef.Calendar}, caldav.IfTargetMissing())
		if err != nil {
			t.Fatalf("recreating the calendar: %v", err)
		}

		if after.ID == before.ID {
			t.Errorf("the recreated calendar reuses CalendarID %q. Its revisions start over, so a client holding an If-Match from before the deletion will overwrite an item it has never seen, and a stale sync position will silently skip the new calendar's early changes", after.ID)
		}
	})

	t.Run("DuplicateCalendarIsRefused", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		c, ok := b.(caldav.CalendarCreator)
		cfg.need(t, CapCalendarCreator, ok)

		_, err := c.CompareAndCreateCalendar(ctx, WorkRef.Account,
			caldav.CreateCalendarRequest{Name: WorkRef.Calendar}, caldav.Unconditional())
		if !errors.Is(err, caldav.ErrAlreadyExists) {
			t.Fatalf("creating a calendar over an existing one = %v, want ErrAlreadyExists — a name collision is an ordinary answer with its own protocol response, not a server error", err)
		}

		existing, err := b.GetCalendar(ctx, WorkRef)
		if err != nil {
			t.Fatalf("GetCalendar: %v", err)
		}
		if existing.DisplayName != "Work" {
			t.Errorf("the refused creation overwrote the existing calendar: display name is now %q", existing.DisplayName)
		}
	})

	t.Run("ConcurrentCreateExactlyOneWins", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		c, ok := b.(caldav.CalendarCreator)
		cfg.need(t, CapCalendarCreator, ok)

		const racers = 8
		name := caldav.MustSegment("contended")
		errs := make([]error, racers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range racers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = c.CompareAndCreateCalendar(ctx, Alice,
					caldav.CreateCalendarRequest{Name: name}, caldav.IfTargetMissing())
			}()
		}
		close(start)
		wg.Wait()

		won := 0
		for i, err := range errs {
			switch {
			case err == nil:
				won++
			case errors.Is(err, caldav.ErrPreconditionFailed), errors.Is(err, caldav.ErrAlreadyExists):
			default:
				t.Errorf("racer %d = %v, want success, ErrPreconditionFailed or ErrAlreadyExists", i, err)
			}
		}
		if won != 1 {
			t.Errorf("%d of %d concurrent creations of the same calendar succeeded, want exactly 1", won, racers)
		}
	})

	t.Run("ReturnedCalendarsAreNotAliased", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		u, ok := b.(caldav.CalendarUpdater)
		cfg.need(t, CapCalendarUpdater, ok)

		order := 1
		if _, err := u.CompareAndUpdateCalendar(ctx, WorkRef, caldav.CalendarPatch{SortOrder: caldav.SetValue(order)}, caldav.Unconditional()); err != nil {
			t.Fatalf("CompareAndUpdateCalendar: %v", err)
		}

		// Calendar.SortOrder is an exported pointer, so returning the struct by
		// value still hands the caller a way into storage.
		got, err := b.GetCalendar(ctx, WorkRef)
		if err != nil {
			t.Fatalf("GetCalendar: %v", err)
		}
		if got.SortOrder == nil {
			t.Fatal("the sort order that was just set came back nil")
		}
		before := got.Revision
		*got.SortOrder = 999

		again, err := b.GetCalendar(ctx, WorkRef)
		if err != nil {
			t.Fatalf("GetCalendar: %v", err)
		}
		if again.SortOrder != nil && *again.SortOrder == 999 {
			t.Errorf("writing through the returned pointer changed stored state, with no write, no precondition check and no revision bump (still %d)", before)
		}
	})
}
