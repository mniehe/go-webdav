package caldavtest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mniehe/davkit/caldav"
)

// replayChanges applies a batch to a set of item names, the way a client keeps
// its local copy up to date between polls.
func replayChanges(view map[string]bool, changes []caldav.Change) {
	for _, c := range changes {
		if c.Deleted {
			delete(view, c.Item.String())
		} else {
			view[c.Item.String()] = true
		}
	}
}

// assertViewMatches requires a replayed client view to agree with a fresh
// listing. This is the property clients actually depend on, and neither the
// listing nor the change log can demonstrate it alone.
func assertViewMatches(t *testing.T, what string, view map[string]bool, fresh map[string]caldav.Item) {
	t.Helper()

	for name := range fresh {
		if !view[name] {
			t.Errorf("%s: a fresh listing holds %s, but replaying the changes never produced it. A client that started from this position never learns the item exists", what, name)
		}
	}
	for name := range view {
		if _, ok := fresh[name]; !ok {
			t.Errorf("%s: replaying the changes left %s in the client's copy, but a fresh listing does not have it. A client that started from this position keeps a deleted item for good", what, name)
		}
	}
}

// testConsistency covers the places where the listing and the change log have
// to agree with each other. Each of these is a way for a client to end up
// permanently wrong while every individual call looks correct.
func testConsistency(t *testing.T, newHarness NewHarness, cfg *config) {
	ctx := context.Background()

	t.Run("ListRevisionDescribesWhatWasYielded", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)
		s := syncOf(t, cfg, b)

		// Hold the listing open after its first item and write underneath it.
		// A backend that reads its revision after iterating, rather than in the
		// same transaction, returns a revision covering a write it never
		// yielded — and a client storing that revision as its sync position
		// never hears about that write again.
		inside := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once

		view := map[string]bool{}
		var listRev caldav.Revision
		listDone := make(chan error, 1)
		go func() {
			rev, err := b.ListItems(ctx, WorkRef, func(item caldav.Item) bool {
				view[item.Name.String()] = true
				once.Do(func() {
					close(inside)
					<-release
				})
				return true
			})
			listRev = rev
			listDone <- err
		}()

		select {
		case <-inside:
		case err := <-listDone:
			t.Fatalf("ListItems returned without yielding anything: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("ListItems never yielded an item")
		}

		const uid = "raced-in@example.test"
		writeDone := make(chan error, 1)
		go func() {
			_, err := w.CompareAndStoreItem(ctx, itemRef(WorkRef, "raced-in.ics"),
				createRequest(event(uid, "Raced in"), uid))
			writeDone <- err
		}()

		// Let the write reach storage and either land or block on it. Waiting
		// longer can only help a correct backend; an incorrect one needs the
		// write to have landed for the inconsistency to exist at all.
		time.Sleep(blockedFor)
		close(release)

		if err := <-listDone; err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if err := <-writeDone; err != nil {
			t.Fatalf("the write racing the listing: %v", err)
		}

		batch, err := s.ListChanges(ctx, WorkRef, listRev, 0)
		if err != nil {
			t.Fatalf("ListChanges from the revision ListItems returned: %v", err)
		}
		replayChanges(view, batch.Changes)

		fresh, _ := collect(ctx, t, b, WorkRef)
		assertViewMatches(t, "after a write raced a listing", view, fresh)
	})

	t.Run("ChangesSurviveReopen", func(t *testing.T) {
		h, b := setupHarness(ctx, t, newHarness, BaseFixture())
		r, ok := h.(Reopener)
		cfg.need(t, CapReopener, ok)
		w := writerOf(t, cfg, b)
		syncOf(t, cfg, b)

		start, startRev := collect(ctx, t, b, WorkRef)
		view := map[string]bool{}
		for name := range start {
			view[name] = true
		}

		const uid = "durable-added@example.test"
		mustStore(ctx, t, w, itemRef(WorkRef, "durable-added.ics"), event(uid, "Added"), uid)
		if err := w.CompareAndDeleteItem(ctx, itemRef(WorkRef, "review.ics"), caldav.Unconditional()); err != nil {
			t.Fatalf("CompareAndDeleteItem: %v", err)
		}

		reopened, err := r.Reopen(ctx)
		if err != nil {
			t.Fatalf("Reopen: %v", err)
		}
		rs, ok := reopened.(caldav.SyncBackend)
		if !ok {
			t.Fatal("the reopened backend no longer implements caldav.SyncBackend")
		}

		// The items surviving a restart is not the same promise as the log
		// surviving one. A backend can commit items durably and keep its change
		// records in memory, and every client that was mid-sync stays wrong
		// until something makes it refetch.
		batch, err := rs.ListChanges(ctx, WorkRef, startRev, 0)
		if err != nil {
			t.Fatalf("ListChanges from a position taken before the reopen = %v; the change log did not survive the restart", err)
		}
		replayChanges(view, batch.Changes)

		fresh, _ := collect(ctx, t, reopened, WorkRef)
		assertViewMatches(t, "after reopening", view, fresh)
	})

	t.Run("DeleteThenRecreateIsOneChange", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)
		s := syncOf(t, cfg, b)

		_, startRev := collect(ctx, t, b, WorkRef)

		// The mirror of the delete-last case: an item removed and put back
		// within one interval is present at the end of it, and a batch that
		// reports the deletion instead makes the client throw away an item that
		// exists.
		ref := itemRef(WorkRef, "standup.ics")
		if err := w.CompareAndDeleteItem(ctx, ref, caldav.Unconditional()); err != nil {
			t.Fatalf("CompareAndDeleteItem: %v", err)
		}
		const uid = "standup-again@example.test"
		mustStore(ctx, t, w, ref, event(uid, "Standup again"), uid)

		batch, err := s.ListChanges(ctx, WorkRef, startRev, 0)
		if err != nil {
			t.Fatalf("ListChanges: %v", err)
		}

		seen := 0
		for _, c := range batch.Changes {
			if c.Item != ref.Item {
				continue
			}
			seen++
			if c.Deleted {
				t.Errorf("%s is reported deleted, but it was recreated before the interval ended; the client drops an item that is still there", ref.Item)
			}
		}
		if seen != 1 {
			t.Errorf("%s appears %d times in one batch, want 1 — a batch reports each item once, in its final state over the interval", ref.Item, seen)
		}
	})
}
