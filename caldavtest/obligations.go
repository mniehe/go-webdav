package caldavtest

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/mniehe/davkit/caldav"
)

func writerOf(t *testing.T, cfg *config, b caldav.Backend) caldav.ItemWriter {
	t.Helper()
	w, ok := b.(caldav.ItemWriter)
	cfg.need(t, CapItemWriter, ok)
	return w
}

func syncOf(t *testing.T, cfg *config, b caldav.Backend) caldav.SyncBackend {
	t.Helper()
	s, ok := b.(caldav.SyncBackend)
	cfg.need(t, CapSyncBackend, ok)
	return s
}

// createRequest is an unconditional write by an actor allowed to do either.
func createRequest(content []byte, contentID string) caldav.StoreItemRequest {
	return caldav.StoreItemRequest{
		Content:       content,
		ContentID:     contentID,
		Kind:          caldav.Event,
		Preconditions: caldav.Unconditional(),
		MayCreate:     true,
		MayReplace:    true,
	}
}

func mustStore(ctx context.Context, t *testing.T, w caldav.ItemWriter, ref caldav.ItemRef, content []byte, contentID string) caldav.StoreItemResult {
	t.Helper()
	res, err := w.CompareAndStoreItem(ctx, ref, createRequest(content, contentID))
	if err != nil {
		t.Fatalf("CompareAndStoreItem(%s): %v", ref.Item, err)
	}
	return res
}

// awkwardFixture seeds an item whose bytes a backend is tempted to normalise,
// so obligation 1 has something to check even without a writer.
func awkwardFixture() Fixture {
	f := BaseFixture()
	f.Calendars[0].Items = append(f.Calendars[0].Items, caldav.Item{
		Name:      caldav.MustSegment("awkward-seeded.ics"),
		ContentID: "awkward-seeded@example.test",
		Content:   awkwardEvent("awkward-seeded@example.test"),
	})
	return f
}

func testObligation1(t *testing.T, newHarness NewHarness, cfg *config) {
	ctx := context.Background()

	t.Run("BytesSurviveRoundTrip", func(t *testing.T) {
		b := setup(ctx, t, newHarness, awkwardFixture())
		want := awkwardEvent("awkward-seeded@example.test")

		got, err := b.GetItem(ctx, itemRef(WorkRef, "awkward-seeded.ics"))
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		assertSameBytes(t, "GetItem", want, got.Content)

		listed, _ := collect(ctx, t, b, WorkRef)
		assertSameBytes(t, "ListItems", want, listed["awkward-seeded.ics"].Content)
	})

	t.Run("WrittenBytesSurviveRoundTrip", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)

		const uid = "awkward-written@example.test"
		ref := itemRef(WorkRef, "awkward-written.ics")
		want := awkwardEvent(uid)
		mustStore(ctx, t, w, ref, awkwardEvent(uid), uid)

		got, err := b.GetItem(ctx, ref)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		assertSameBytes(t, "GetItem", want, got.Content)

		listed, _ := collect(ctx, t, b, WorkRef)
		assertSameBytes(t, "ListItems", want, listed["awkward-written.ics"].Content)
	})

	t.Run("ContentIsNotAliased", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)

		const uid = "aliased@example.test"
		ref := itemRef(WorkRef, "aliased.ics")
		want := event(uid, "Original")

		// Hand the backend a slice we still hold, then scribble on it. A
		// backend that kept the slice rather than a copy now serves whatever
		// the caller last wrote into it.
		handed := slices.Clone(want)
		mustStore(ctx, t, w, ref, handed, uid)
		for i := range handed {
			handed[i] = 'X'
		}

		got, err := b.GetItem(ctx, ref)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		assertSameBytes(t, "GetItem after the caller overwrote its slice", want, got.Content)
	})

	t.Run("ReturnedContentIsNotAliased", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())

		ref := itemRef(WorkRef, "standup.ics")
		first, err := b.GetItem(ctx, ref)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		want := slices.Clone(first.Content)
		for i := range first.Content {
			first.Content[i] = 'X'
		}

		second, err := b.GetItem(ctx, ref)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		assertSameBytes(t, "GetItem after a caller overwrote the slice it was handed", want, second.Content)
	})
}

func testObligation2(t *testing.T, newHarness NewHarness, cfg *config) {
	ctx := context.Background()

	t.Run("ReplaceRetiresOldContentID", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)

		const first, second = "first@example.test", "second@example.test"
		ref := itemRef(WorkRef, "moving.ics")

		mustStore(ctx, t, w, ref, event(first, "First"), first)
		mustStore(ctx, t, w, ref, event(second, "Second"), second)

		// The replacement gave up "first", so a different item may claim it.
		other := itemRef(WorkRef, "claimant.ics")
		if _, err := w.CompareAndStoreItem(ctx, other, createRequest(event(first, "Claimant"), first)); err != nil {
			t.Fatalf("claiming a content ID the replaced item gave up: %v, want success", err)
		}

		got, err := b.GetItem(ctx, ref)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if got.ContentID != second {
			t.Errorf("the replaced item still reports ContentID %q, want %q", got.ContentID, second)
		}
	})

	t.Run("DuplicateContentIDIsRefused", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)

		const uid = "shared@example.test"
		holder := itemRef(WorkRef, "holder.ics")
		mustStore(ctx, t, w, holder, event(uid, "Holder"), uid)

		_, err := w.CompareAndStoreItem(ctx, itemRef(WorkRef, "thief.ics"), createRequest(event(uid, "Thief"), uid))

		var dup *caldav.DuplicateContentIDError
		if !errors.As(err, &dup) {
			t.Fatalf("storing a content ID another item holds = %v, want a *caldav.DuplicateContentIDError", err)
		}
		if dup.Existing != holder.Item {
			t.Errorf("Existing = %q, want %q — the handler reports this href to the client", dup.Existing, holder.Item)
		}
		if errors.Is(err, caldav.ErrPreconditionFailed) {
			t.Error("the error is also ErrPreconditionFailed; a duplicate content ID is a different protocol response from a failed conditional write")
		}
	})

	t.Run("DeleteReleasesContentID", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)

		// Retiring on replace is not the same code path as retiring on delete,
		// and a uniqueness index left behind by a deletion locks a content ID
		// out of the calendar for good.
		const uid = "released@example.test"
		holder := itemRef(WorkRef, "holder.ics")
		mustStore(ctx, t, w, holder, event(uid, "Holder"), uid)

		if err := w.CompareAndDeleteItem(ctx, holder, caldav.Unconditional()); err != nil {
			t.Fatalf("CompareAndDeleteItem: %v", err)
		}
		if _, err := w.CompareAndStoreItem(ctx, itemRef(WorkRef, "claimant.ics"), createRequest(event(uid, "Claimant"), uid)); err != nil {
			t.Fatalf("claiming a content ID whose only holder was deleted = %v, want success", err)
		}
	})

	t.Run("ContentIDIsUniquePerCalendarOnly", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)

		// The seeded item in Carol's calendar already uses this one.
		const uid = "gym@example.test"
		if _, err := w.CompareAndStoreItem(ctx, itemRef(WorkRef, "same-uid.ics"), createRequest(event(uid, "Gym elsewhere"), uid)); err != nil {
			t.Fatalf("storing a content ID another calendar uses = %v, want success — uniqueness is per calendar", err)
		}
	})
}

func testObligation4(t *testing.T, newHarness NewHarness, cfg *config) {
	ctx := context.Background()

	t.Run("DeleteAppearsInChanges", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)
		s := syncOf(t, cfg, b)

		_, before := collect(ctx, t, b, WorkRef)

		ref := itemRef(WorkRef, "standup.ics")
		if err := w.CompareAndDeleteItem(ctx, ref, caldav.Unconditional()); err != nil {
			t.Fatalf("CompareAndDeleteItem: %v", err)
		}

		batch, err := s.ListChanges(ctx, WorkRef, before, 0)
		if err != nil {
			t.Fatalf("ListChanges: %v", err)
		}

		idx := slices.IndexFunc(batch.Changes, func(c caldav.Change) bool { return c.Item == ref.Item })
		if idx < 0 {
			t.Fatalf("ListChanges after deleting %s reported %v; a deletion leaves nothing in current state to notice, so it has to be logged", ref.Item, batch.Changes)
		}
		if !batch.Changes[idx].Deleted {
			t.Errorf("the change for %s has Deleted = false; the client will never remove it", ref.Item)
		}
	})

	t.Run("ChangesReplayOntoListing", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)
		s := syncOf(t, cfg, b)

		// A client's view: everything at the starting revision.
		start, startRev := collect(ctx, t, b, WorkRef)
		view := map[string]bool{}
		for name := range start {
			view[name] = true
		}

		const uid = "added@example.test"
		mustStore(ctx, t, w, itemRef(WorkRef, "added.ics"), event(uid, "Added"), uid)
		if err := w.CompareAndDeleteItem(ctx, itemRef(WorkRef, "review.ics"), caldav.Unconditional()); err != nil {
			t.Fatalf("CompareAndDeleteItem: %v", err)
		}

		batch, err := s.ListChanges(ctx, WorkRef, startRev, 0)
		if err != nil {
			t.Fatalf("ListChanges: %v", err)
		}
		if batch.HasMore {
			t.Fatal("ListChanges with no limit reported HasMore")
		}
		for _, c := range batch.Changes {
			if c.Deleted {
				delete(view, c.Item.String())
			} else {
				view[c.Item.String()] = true
			}
		}

		// Replaying the changes must land exactly where a fresh listing does.
		// This is the property clients actually depend on, and neither path
		// alone can demonstrate it.
		fresh, freshRev := collect(ctx, t, b, WorkRef)
		if len(view) != len(fresh) {
			t.Fatalf("replaying changes gave %d items, a fresh listing has %d", len(view), len(fresh))
		}
		for name := range fresh {
			if !view[name] {
				t.Errorf("a fresh listing has %s but replaying the changes did not produce it", name)
			}
		}
		if batch.CoveredThrough != freshRev {
			t.Errorf("CoveredThrough = %d but the listing that matches it is at revision %d", batch.CoveredThrough, freshRev)
		}
	})

	t.Run("EachItemAppearsOnce", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)
		s := syncOf(t, cfg, b)

		_, startRev := collect(ctx, t, b, WorkRef)

		// Three writes to one item, then a deletion: the final state over the
		// interval is one deletion, reported once.
		const uid = "churn@example.test"
		ref := itemRef(WorkRef, "churn.ics")
		for _, summary := range []string{"One", "Two", "Three"} {
			mustStore(ctx, t, w, ref, event(uid, summary), uid)
		}
		if err := w.CompareAndDeleteItem(ctx, ref, caldav.Unconditional()); err != nil {
			t.Fatalf("CompareAndDeleteItem: %v", err)
		}

		batch, err := s.ListChanges(ctx, WorkRef, startRev, 0)
		if err != nil {
			t.Fatalf("ListChanges: %v", err)
		}

		seen := 0
		for _, c := range batch.Changes {
			if c.Item == ref.Item {
				seen++
				if !c.Deleted {
					t.Errorf("the change for %s is not a deletion; it must report the final state over the interval", ref.Item)
				}
			}
		}
		if seen != 1 {
			t.Errorf("%s appears %d times in one batch, want 1", ref.Item, seen)
		}
	})

	t.Run("CoveredThroughIsResumable", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)
		s := syncOf(t, cfg, b)

		_, startRev := collect(ctx, t, b, WorkRef)

		const count = 6
		want := map[string]bool{}
		for i := range count {
			name := "batched-" + string(rune('a'+i)) + ".ics"
			uid := name + "@example.test"
			mustStore(ctx, t, w, itemRef(WorkRef, name), event(uid, "Batched"), uid)
			want[name] = true
		}

		// Page through with a limit well under the number of changes. Resuming
		// from CoveredThrough must deliver every change exactly once.
		got := map[string]int{}
		at := startRev
		for range count + 2 {
			batch, err := s.ListChanges(ctx, WorkRef, at, 2)
			if err != nil {
				t.Fatalf("ListChanges(after %d): %v", at, err)
			}
			for _, c := range batch.Changes {
				got[c.Item.String()]++
			}
			if batch.CoveredThrough < at {
				t.Fatalf("CoveredThrough went backwards: %d after asking from %d", batch.CoveredThrough, at)
			}
			at = batch.CoveredThrough
			if !batch.HasMore {
				break
			}
		}

		for name := range want {
			switch got[name] {
			case 1:
			case 0:
				t.Errorf("%s was never delivered; paging dropped it at a batch boundary", name)
			default:
				t.Errorf("%s was delivered %d times", name, got[name])
			}
		}
	})

	t.Run("PrunedHistoryIsTooOld", func(t *testing.T) {
		h, b := setupHarness(ctx, t, newHarness, BaseFixture())
		s := syncOf(t, cfg, b)
		p, ok := h.(Pruner)
		cfg.need(t, CapPruner, ok)

		w := writerOf(t, cfg, b)
		_, startRev := collect(ctx, t, b, WorkRef)

		const uid = "after-prune@example.test"
		mustStore(ctx, t, w, itemRef(WorkRef, "after-prune.ics"), event(uid, "After"), uid)
		_, nowRev := collect(ctx, t, b, WorkRef)

		if err := p.PruneHistory(ctx, WorkRef, nowRev); err != nil {
			t.Fatalf("PruneHistory: %v", err)
		}

		_, err := s.ListChanges(ctx, WorkRef, startRev, 0)
		if !errors.Is(err, caldav.ErrHistoryTooOld) {
			t.Fatalf("ListChanges from a pruned position = %v, want ErrHistoryTooOld — a batch that silently omits the pruned changes leaves the client wrong forever", err)
		}

		// A position at or after the prune point still works.
		if _, err := s.ListChanges(ctx, WorkRef, nowRev, 0); err != nil {
			t.Errorf("ListChanges from the prune point = %v, want success", err)
		}
	})
}

func testDurability(t *testing.T, newHarness NewHarness, cfg *config) {
	ctx := context.Background()

	t.Run("WritesSurviveReopen", func(t *testing.T) {
		h, b := setupHarness(ctx, t, newHarness, BaseFixture())
		r, ok := h.(Reopener)
		cfg.need(t, CapReopener, ok)
		w := writerOf(t, cfg, b)

		const uid = "durable@example.test"
		ref := itemRef(WorkRef, "durable.ics")
		want := event(uid, "Durable")
		mustStore(ctx, t, w, ref, event(uid, "Durable"), uid)

		reopened, err := r.Reopen(ctx)
		if err != nil {
			t.Fatalf("Reopen: %v", err)
		}
		got, err := reopened.GetItem(ctx, ref)
		if err != nil {
			t.Fatalf("GetItem after reopening: %v", err)
		}
		assertSameBytes(t, "GetItem after reopening", want, got.Content)
	})
}

func assertSameBytes(t *testing.T, what string, want, got []byte) {
	t.Helper()
	if bytes.Equal(want, got) {
		return
	}
	t.Errorf("%s returned different bytes from the ones stored\n want %d bytes: %q\n  got %d bytes: %q", what, len(want), want, len(got), got)
}
