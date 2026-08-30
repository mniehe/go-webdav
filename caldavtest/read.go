package caldavtest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/mniehe/davkit/caldav"
)

func testReading(t *testing.T, newHarness NewHarness, _ *config) {
	ctx := context.Background()

	t.Run("ListCalendarsReturnsOwnOnly", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())

		cals, err := b.ListCalendars(ctx, Alice)
		if err != nil {
			t.Fatalf("ListCalendars(alice): %v", err)
		}
		if len(cals) != 1 {
			t.Fatalf("ListCalendars(alice) returned %d calendars, want 1", len(cals))
		}
		if got := cals[0].Name.String(); got != WorkRef.Calendar.String() {
			t.Errorf("ListCalendars(alice) returned %q, want %q", got, WorkRef.Calendar)
		}

		// Bob may read Alice's calendar, but it is not his: a shared calendar
		// lives under its owner and is discovered by the application.
		bobs, err := b.ListCalendars(ctx, Bob)
		if err != nil {
			t.Fatalf("ListCalendars(bob): %v", err)
		}
		if len(bobs) != 0 {
			t.Errorf("ListCalendars(bob) returned %d calendars, want 0 — a share is not a listing", len(bobs))
		}
	})

	t.Run("GetCalendarReportsSettings", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())

		cal, err := b.GetCalendar(ctx, WorkRef)
		if err != nil {
			t.Fatalf("GetCalendar(alice/work): %v", err)
		}
		if cal.Name != WorkRef.Calendar {
			t.Errorf("Name = %q, want %q", cal.Name, WorkRef.Calendar)
		}
		if cal.DisplayName != "Work" {
			t.Errorf("DisplayName = %q, want %q", cal.DisplayName, "Work")
		}
		if cal.Description != "Alice at work" {
			t.Errorf("Description = %q, want %q", cal.Description, "Alice at work")
		}

		// Carol's calendar was seeded accepting only events, and a set that is
		// not round-tripped would silently let clients store tasks.
		personal, err := b.GetCalendar(ctx, PersonalRef)
		if err != nil {
			t.Fatalf("GetCalendar(carol/personal): %v", err)
		}
		if !personal.Accepts.Allows(caldav.Event) {
			t.Error("Accepts rejects events, but the fixture accepts them")
		}
		if personal.Accepts.Allows(caldav.Task) {
			t.Error("Accepts allows tasks, but the fixture accepts only events")
		}
	})

	t.Run("MissingCalendarIsNotFound", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())

		_, err := b.GetCalendar(ctx, MissingRef)
		if !errors.Is(err, caldav.ErrNotFound) {
			t.Fatalf("GetCalendar of a missing calendar = %v, want ErrNotFound", err)
		}
		if errors.Is(err, caldav.ErrParentNotFound) {
			t.Error("the error is also ErrParentNotFound; a calendar's parent is the account, which does exist")
		}

		_, err = b.ListItems(ctx, MissingRef, func(caldav.Item) bool { return true })
		if !errors.Is(err, caldav.ErrNotFound) {
			t.Errorf("ListItems of a missing calendar = %v, want ErrNotFound", err)
		}
	})

	t.Run("MissingItemIsNotFound", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())

		_, err := b.GetItem(ctx, itemRef(WorkRef, "no-such-item.ics"))
		if !errors.Is(err, caldav.ErrNotFound) {
			t.Fatalf("GetItem of a missing item = %v, want ErrNotFound", err)
		}
		if errors.Is(err, caldav.ErrParentNotFound) {
			t.Error("the error is also ErrParentNotFound, but the calendar exists — the handler cannot tell the two cases apart")
		}
	})

	t.Run("ItemInMissingCalendarIsParentNotFound", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())

		_, err := b.GetItem(ctx, itemRef(MissingRef, "anything.ics"))
		if !errors.Is(err, caldav.ErrParentNotFound) {
			t.Fatalf("GetItem under a missing calendar = %v, want ErrParentNotFound", err)
		}
	})

	t.Run("ListItemsAgreesWithGetItem", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())

		listed, rev := collect(ctx, t, b, WorkRef)
		if len(listed) != 2 {
			t.Fatalf("ListItems yielded %d items, want 2", len(listed))
		}
		if rev == 0 {
			t.Error("ListItems returned revision 0 for a calendar holding items")
		}

		for name, want := range listed {
			got, err := b.GetItem(ctx, itemRef(WorkRef, name))
			if err != nil {
				t.Fatalf("GetItem(%s): %v", name, err)
			}
			if got.Name != want.Name {
				t.Errorf("GetItem(%s).Name = %q, want %q", name, got.Name, want.Name)
			}
			if got.ContentID != want.ContentID {
				t.Errorf("GetItem(%s).ContentID = %q, ListItems said %q", name, got.ContentID, want.ContentID)
			}
			if got.Revision != want.Revision {
				t.Errorf("GetItem(%s).Revision = %d, ListItems said %d", name, got.Revision, want.Revision)
			}
			if !bytes.Equal(got.Content, want.Content) {
				t.Errorf("GetItem(%s) and ListItems returned different content", name)
			}
		}
	})

	t.Run("ListItemsStopsOnFalse", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())

		yielded := 0
		if _, err := b.ListItems(ctx, WorkRef, func(caldav.Item) bool {
			yielded++
			return false
		}); err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if yielded != 1 {
			t.Errorf("yield was called %d times after returning false, want 1", yielded)
		}
	})
}

func testAuthorizer(t *testing.T, newHarness NewHarness, _ *config) {
	ctx := context.Background()

	t.Run("PermissionsFollowOwnershipAndShares", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())

		for _, tc := range []struct {
			name  string
			actor caldav.AccountID
			want  caldav.CalendarPermissions
		}{
			{"owner", Alice, caldav.OwnerPermissions()},
			{"viewer", Bob, caldav.ViewOnlyPermissions()},
			{"stranger", Carol, caldav.CalendarPermissions{}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, err := b.CalendarPermissions(ctx, caldav.Actor{Account: tc.actor}, WorkRef)
				if err != nil {
					t.Fatalf("CalendarPermissions(%s): %v", tc.actor, err)
				}
				if got.Normalised() != tc.want.Normalised() {
					t.Errorf("CalendarPermissions(%s) = %+v, want %+v", tc.actor, got, tc.want)
				}
			})
		}
	})

	t.Run("NoPermissionIsNotAnError", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())

		got, err := b.CalendarPermissions(ctx, caldav.Actor{Account: Carol}, WorkRef)
		if err != nil {
			t.Fatalf("CalendarPermissions for an unrelated actor returned %v; the zero value is the answer, not an error", err)
		}
		if got.Any() {
			t.Errorf("an unrelated actor got %+v, want nothing", got)
		}
	})

	t.Run("AccountPermissionsAreOwnOnly", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())

		own, err := b.AccountPermissions(ctx, caldav.Actor{Account: Alice}, Alice)
		if err != nil {
			t.Fatalf("AccountPermissions(alice, alice): %v", err)
		}
		if !own.ListCalendars {
			t.Error("alice may not list her own calendars")
		}

		other, err := b.AccountPermissions(ctx, caldav.Actor{Account: Bob}, Alice)
		if err != nil {
			t.Fatalf("AccountPermissions(bob, alice): %v", err)
		}
		if other.Any() {
			t.Errorf("bob got %+v over alice's account; a share grants nothing over the calendar list", other)
		}
	})
}
