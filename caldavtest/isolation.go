package caldavtest

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/mniehe/davkit/caldav"
)

// AliceWorkRef and CarolWorkRef are two different calendars that share a name.
// Every scenario in this group turns on the difference between them.
var (
	AliceWorkRef = caldav.CalendarRef{Account: Alice, Calendar: caldav.MustSegment("work")}
	CarolWorkRef = caldav.CalendarRef{Account: Carol, Calendar: caldav.MustSegment("work")}

	sharedItem = caldav.MustSegment("shared-name.ics")
)

// IsolationFixture gives two accounts a calendar of the same name holding an
// item of the same name. Everything else in the suite uses distinct names,
// which is exactly what lets a backend keyed on the name alone pass.
func IsolationFixture() Fixture {
	calendar := func(ref caldav.CalendarRef, uid, summary string) FixtureCalendar {
		return FixtureCalendar{
			Ref:      ref,
			Settings: caldav.Calendar{Name: ref.Calendar, DisplayName: string(ref.Account) + " at work"},
			Items: []caldav.Item{{
				Name:      sharedItem,
				ContentID: uid,
				Content:   event(uid, summary),
			}},
		}
	}
	return Fixture{Calendars: []FixtureCalendar{
		calendar(AliceWorkRef, "alice-item@example.test", "Alice private"),
		calendar(CarolWorkRef, "carol-item@example.test", "Carol private"),
	}}
}

func aliceRef() caldav.ItemRef { return caldav.ItemRef{Calendar: AliceWorkRef, Item: sharedItem} }
func carolRef() caldav.ItemRef { return caldav.ItemRef{Calendar: CarolWorkRef, Item: sharedItem} }

// testIsolation pins the boundary between accounts.
//
// A backend that keys calendars by name alone, or items by name alone, passes
// every other group in this suite: nothing else here ever gives two accounts
// the same calendar name. The consequence of that mistake is one account
// reading or overwriting another's, which is the worst outcome this library
// has, so it gets its own group.
func testIsolation(t *testing.T, newHarness NewHarness, cfg *config) {
	ctx := context.Background()

	t.Run("SameNamesAreDifferentItems", func(t *testing.T) {
		b := setup(ctx, t, newHarness, IsolationFixture())

		alice, err := b.GetItem(ctx, aliceRef())
		if err != nil {
			t.Fatalf("GetItem(alice/work/%s): %v", sharedItem, err)
		}
		carol, err := b.GetItem(ctx, carolRef())
		if err != nil {
			t.Fatalf("GetItem(carol/work/%s): %v", sharedItem, err)
		}

		if alice.ContentID == carol.ContentID {
			t.Fatalf("both accounts' items report ContentID %q; one calendar is shadowing the other", alice.ContentID)
		}
		if !bytes.Contains(alice.Content, []byte("Alice private")) {
			t.Errorf("alice's item holds %q", alice.Content)
		}
		if bytes.Contains(alice.Content, []byte("Carol private")) {
			t.Error("alice's item returned carol's content")
		}
		if !bytes.Contains(carol.Content, []byte("Carol private")) {
			t.Errorf("carol's item holds %q", carol.Content)
		}
	})

	t.Run("SameNamesAreDifferentCalendars", func(t *testing.T) {
		b := setup(ctx, t, newHarness, IsolationFixture())

		aliceCal, err := b.GetCalendar(ctx, AliceWorkRef)
		if err != nil {
			t.Fatalf("GetCalendar(alice/work): %v", err)
		}
		carolCal, err := b.GetCalendar(ctx, CarolWorkRef)
		if err != nil {
			t.Fatalf("GetCalendar(carol/work): %v", err)
		}
		if aliceCal.DisplayName == carolCal.DisplayName {
			t.Errorf("both calendars report display name %q", aliceCal.DisplayName)
		}
		if aliceCal.ID == carolCal.ID {
			t.Errorf("two different calendars share CalendarID %q; every validator scoped by it now collides", aliceCal.ID)
		}

		for _, tc := range []struct {
			ref  caldav.CalendarRef
			want string
		}{{AliceWorkRef, "alice-item@example.test"}, {CarolWorkRef, "carol-item@example.test"}} {
			items, _ := collect(ctx, t, b, tc.ref)
			if len(items) != 1 {
				t.Fatalf("ListItems(%s/work) yielded %d items, want 1", tc.ref.Account, len(items))
			}
			if got := items[sharedItem.String()].ContentID; got != tc.want {
				t.Errorf("ListItems(%s/work) yielded ContentID %q, want %q", tc.ref.Account, got, tc.want)
			}
		}
	})

	t.Run("WritingOneLeavesTheOther", func(t *testing.T) {
		b := setup(ctx, t, newHarness, IsolationFixture())
		w := writerOf(t, cfg, b)

		before, err := b.GetItem(ctx, carolRef())
		if err != nil {
			t.Fatalf("GetItem(carol): %v", err)
		}

		const uid = "alice-replaced@example.test"
		mustStore(ctx, t, w, aliceRef(), event(uid, "Alice replaced"), uid)

		after, err := b.GetItem(ctx, carolRef())
		if err != nil {
			t.Fatalf("GetItem(carol) after writing alice's: %v", err)
		}
		if !bytes.Equal(before.Content, after.Content) {
			t.Error("writing alice's item changed carol's")
		}
		if before.Revision != after.Revision {
			t.Errorf("carol's item moved from revision %d to %d because alice wrote hers", before.Revision, after.Revision)
		}
	})

	t.Run("DeletingOneLeavesTheOther", func(t *testing.T) {
		b := setup(ctx, t, newHarness, IsolationFixture())
		w := writerOf(t, cfg, b)

		if err := w.CompareAndDeleteItem(ctx, aliceRef(), caldav.Unconditional()); err != nil {
			t.Fatalf("CompareAndDeleteItem(alice): %v", err)
		}

		if _, err := b.GetItem(ctx, carolRef()); err != nil {
			t.Fatalf("GetItem(carol) after deleting alice's same-named item: %v, want it still there", err)
		}
		if _, err := b.GetItem(ctx, aliceRef()); !errors.Is(err, caldav.ErrNotFound) {
			t.Errorf("GetItem(alice) after deleting it = %v, want ErrNotFound", err)
		}
	})

	t.Run("ContentIDsDoNotCollideAcrossAccounts", func(t *testing.T) {
		b := setup(ctx, t, newHarness, IsolationFixture())
		w := writerOf(t, cfg, b)

		// The ID carol's item already uses, claimed in alice's calendar of the
		// same name. Uniqueness is per calendar, and these are two calendars.
		const uid = "carol-item@example.test"
		ref := caldav.ItemRef{Calendar: AliceWorkRef, Item: caldav.MustSegment("borrowed.ics")}
		if _, err := w.CompareAndStoreItem(ctx, ref, createRequest(event(uid, "Borrowed"), uid)); err != nil {
			t.Fatalf("claiming a ContentID another account's same-named calendar uses = %v, want success", err)
		}
	})

	t.Run("ChangesDoNotCrossAccounts", func(t *testing.T) {
		b := setup(ctx, t, newHarness, IsolationFixture())
		w := writerOf(t, cfg, b)
		s := syncOf(t, cfg, b)

		_, carolStart := collect(ctx, t, b, CarolWorkRef)

		const uid = "alice-added@example.test"
		added := caldav.MustSegment("alice-added.ics")
		mustStore(ctx, t, w, caldav.ItemRef{Calendar: AliceWorkRef, Item: added}, event(uid, "Alice added"), uid)
		if err := w.CompareAndDeleteItem(ctx, aliceRef(), caldav.Unconditional()); err != nil {
			t.Fatalf("CompareAndDeleteItem(alice): %v", err)
		}

		batch, err := s.ListChanges(ctx, CarolWorkRef, carolStart, 0)
		if err != nil {
			t.Fatalf("ListChanges(carol/work): %v", err)
		}
		for _, c := range batch.Changes {
			t.Errorf("carol's change list reports %s (deleted=%t) after only alice's calendar changed", c.Item, c.Deleted)
		}
	})

	t.Run("DeletingOneCalendarLeavesTheOther", func(t *testing.T) {
		b := setup(ctx, t, newHarness, IsolationFixture())
		d, ok := b.(caldav.CalendarDeleter)
		cfg.need(t, CapCalendarDeleter, ok)

		if err := d.CompareAndDeleteCalendar(ctx, AliceWorkRef, caldav.Unconditional()); err != nil {
			t.Fatalf("CompareAndDeleteCalendar(alice/work): %v", err)
		}

		if _, err := b.GetCalendar(ctx, CarolWorkRef); err != nil {
			t.Fatalf("GetCalendar(carol/work) after deleting alice's same-named calendar: %v", err)
		}
		if _, err := b.GetItem(ctx, carolRef()); err != nil {
			t.Errorf("GetItem(carol) after deleting alice's same-named calendar: %v", err)
		}

		cals, err := b.ListCalendars(ctx, Carol)
		if err != nil {
			t.Fatalf("ListCalendars(carol): %v", err)
		}
		if !slices.ContainsFunc(cals, func(c caldav.Calendar) bool { return c.Name == CarolWorkRef.Calendar }) {
			t.Error("carol's calendar vanished when alice's same-named one was deleted")
		}
	})
}
