package caldavtest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mniehe/davkit/caldav"
)

// blockedFor is how long a write that should be waiting on another
// transaction is given to prove it is not. A backend that really serialises
// never completes in this window however long it is, so the test cannot flake
// in the passing direction; a backend that does not will finish almost at once.
const blockedFor = 100 * time.Millisecond

func testObligation3(t *testing.T, newHarness NewHarness, cfg *config) {
	ctx := context.Background()

	t.Run("PermissionSelectedByExistence", func(t *testing.T) {
		existing := itemRef(WorkRef, "standup.ics")
		missing := itemRef(WorkRef, "brand-new.ics")

		for _, tc := range []struct {
			name       string
			ref        caldav.ItemRef
			mayCreate  bool
			mayReplace bool
			refused    bool
		}{
			{"create refused when only replacing is allowed", missing, false, true, true},
			{"create allowed", missing, true, false, false},
			{"replace refused when only creating is allowed", existing, true, false, true},
			{"replace allowed", existing, false, true, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				b := setup(ctx, t, newHarness, BaseFixture())
				w := writerOf(t, cfg, b)

				const uid = "selected@example.test"
				req := createRequest(event(uid, "Selected"), uid)
				req.MayCreate, req.MayReplace = tc.mayCreate, tc.mayReplace

				_, err := w.CompareAndStoreItem(ctx, tc.ref, req)
				switch {
				case tc.refused && !errors.Is(err, caldav.ErrForbidden):
					t.Fatalf("= %v, want ErrForbidden: only the transaction knows whether the target exists, so only it can pick which permission applies", err)
				case tc.refused && errors.Is(err, caldav.ErrPreconditionFailed):
					t.Error("the refusal is also ErrPreconditionFailed; a missing permission and a failed conditional write are different responses")
				case !tc.refused && err != nil:
					t.Fatalf("= %v, want success", err)
				}
			})
		}
	})

	t.Run("ResultDescribesTheWrite", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)

		// The handler turns Created into the success status and Revision into
		// the ETag it hands back. A result that does not describe what was
		// actually committed makes both of those wrong, silently.
		const uid = "described@example.test"
		ref := itemRef(WorkRef, "described.ics")

		created, err := w.CompareAndStoreItem(ctx, ref, createRequest(event(uid, "First"), uid))
		if err != nil {
			t.Fatalf("CompareAndStoreItem: %v", err)
		}
		if !created.Created {
			t.Error("storing to a name that did not exist reported Created = false")
		}
		if created.Revision == 0 {
			t.Error("the write returned revision 0")
		}
		stored, err := b.GetItem(ctx, ref)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if created.Revision != stored.Revision {
			t.Errorf("the write returned revision %d, the stored item is at %d", created.Revision, stored.Revision)
		}

		replaced, err := w.CompareAndStoreItem(ctx, ref, createRequest(event(uid, "Second"), uid))
		if err != nil {
			t.Fatalf("CompareAndStoreItem: %v", err)
		}
		if replaced.Created {
			t.Error("storing over an existing item reported Created = true")
		}
		if replaced.Revision == created.Revision {
			t.Errorf("replacing the content left the revision at %d, so every client keeps its cached copy", replaced.Revision)
		}
		after, err := b.GetItem(ctx, ref)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if replaced.Revision != after.Revision {
			t.Errorf("the replace returned revision %d, the stored item is at %d", replaced.Revision, after.Revision)
		}
	})

	t.Run("StoreChecksPreconditions", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		existing, err := b.GetItem(ctx, itemRef(WorkRef, "standup.ics"))
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}

		for _, tc := range []struct {
			name string
			ref  caldav.ItemRef
			want *caldav.Revision
		}{
			{"create sees no current revision", itemRef(WorkRef, "unseen.ics"), nil},
			{"replace sees the current revision", itemRef(WorkRef, "standup.ics"), &existing.Revision},
		} {
			t.Run(tc.name, func(t *testing.T) {
				b := setup(ctx, t, newHarness, BaseFixture())
				w := writerOf(t, cfg, b)

				var calls int
				var saw *caldav.Revision
				const uid = "probed@example.test"
				req := createRequest(event(uid, "Probed"), uid)
				req.Preconditions = caldav.Unconditional().WithProbe(func(current *caldav.Revision) {
					calls++
					if current != nil {
						rev := *current
						saw = &rev
					}
				})

				if _, err := w.CompareAndStoreItem(ctx, tc.ref, req); err != nil {
					t.Fatalf("CompareAndStoreItem: %v", err)
				}
				if calls == 0 {
					t.Fatal("CompareAndStoreItem never called Preconditions.Check; a write that skips it applies whatever the client asked to guard against")
				}
				switch {
				case tc.want == nil && saw != nil:
					t.Errorf("Check saw revision %d for an item that did not exist", *saw)
				case tc.want != nil && saw == nil:
					t.Error("Check saw no current revision for an item that does exist")
				case tc.want != nil && saw != nil && *saw != *tc.want:
					t.Errorf("Check saw revision %d, the item was at %d — Check must run on state read inside the transaction", *saw, *tc.want)
				}
			})
		}
	})

	t.Run("DeleteChecksPreconditions", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)

		item, err := b.GetItem(ctx, itemRef(WorkRef, "standup.ics"))
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}

		var calls int
		var saw caldav.Revision
		pre := caldav.Unconditional().WithProbe(func(current *caldav.Revision) {
			calls++
			if current != nil {
				saw = *current
			}
		})
		if err := w.CompareAndDeleteItem(ctx, itemRef(WorkRef, "standup.ics"), pre); err != nil {
			t.Fatalf("CompareAndDeleteItem: %v", err)
		}
		if calls == 0 {
			t.Fatal("CompareAndDeleteItem never called Preconditions.Check; a conditional delete would remove whatever it found")
		}
		if saw != item.Revision {
			t.Errorf("Check saw revision %d, the item was at %d", saw, item.Revision)
		}
	})

	t.Run("StaleRevisionIsRefused", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)

		ref := itemRef(WorkRef, "standup.ics")
		before, err := b.GetItem(ctx, ref)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}

		// Someone else writes first. The revision our client is holding is now
		// the one it must not be allowed to overwrite from.
		const uid = "standup@example.test"
		mustStore(ctx, t, w, ref, event(uid, "Moved"), uid)

		stale := createRequest(event(uid, "Lost update"), uid)
		stale.Preconditions = caldav.IfRevision(before.Revision)
		if _, refused := w.CompareAndStoreItem(ctx, ref, stale); !errors.Is(refused, caldav.ErrPreconditionFailed) {
			t.Fatalf("writing from a stale revision = %v, want ErrPreconditionFailed — this is the lost update the whole compare-and-mutate contract exists to stop", err)
		}

		after, err := b.GetItem(ctx, ref)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if after.Revision == before.Revision {
			t.Error("the item is back at the revision the refused write held")
		}

		current := createRequest(event(uid, "Accepted"), uid)
		current.Preconditions = caldav.IfRevision(after.Revision)
		if _, err := w.CompareAndStoreItem(ctx, ref, current); err != nil {
			t.Errorf("writing from the current revision = %v, want success", err)
		}
	})

	t.Run("TargetStateIsChecked", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)

		const uid = "state@example.test"
		existing := itemRef(WorkRef, "standup.ics")
		missing := itemRef(WorkRef, "absent.ics")

		req := createRequest(event(uid, "State"), uid)
		req.Preconditions = caldav.IfTargetMissing()
		if _, err := w.CompareAndStoreItem(ctx, existing, req); !errors.Is(err, caldav.ErrPreconditionFailed) {
			t.Errorf("IfTargetMissing against an existing item = %v, want ErrPreconditionFailed", err)
		}

		req.Preconditions = caldav.IfTargetExists()
		if _, err := w.CompareAndStoreItem(ctx, missing, req); !errors.Is(err, caldav.ErrPreconditionFailed) {
			t.Errorf("IfTargetExists against a missing item = %v, want ErrPreconditionFailed", err)
		}

		if err := w.CompareAndDeleteItem(ctx, missing, caldav.Unconditional()); !errors.Is(err, caldav.ErrNotFound) {
			t.Errorf("deleting a missing item = %v, want ErrNotFound", err)
		}
	})

	t.Run("ConcurrentCreateExactlyOneWins", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)

		const racers = 8
		const uid = "raced@example.test"
		ref := itemRef(WorkRef, "raced.ics")

		errs := make([]error, racers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range racers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req := createRequest(event(uid, "Raced"), uid)
				req.Preconditions = caldav.IfTargetMissing()
				<-start
				_, errs[i] = w.CompareAndStoreItem(ctx, ref, req)
			}()
		}
		close(start)
		wg.Wait()

		won := 0
		for i, err := range errs {
			switch {
			case err == nil:
				won++
			case errors.Is(err, caldav.ErrPreconditionFailed):
			default:
				t.Errorf("racer %d = %v, want either success or ErrPreconditionFailed", i, err)
			}
		}
		if won != 1 {
			t.Fatalf("%d of %d concurrent IfTargetMissing writes succeeded, want exactly 1", won, racers)
		}

		if _, err := b.GetItem(ctx, ref); err != nil {
			t.Errorf("GetItem after the race: %v", err)
		}
	})

	t.Run("WriteWaitsForAnotherTransaction", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)

		const uid = "serialised@example.test"
		ref := itemRef(WorkRef, "serialised.ics")

		inside := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		var parked atomic.Bool

		first := createRequest(event(uid, "First"), uid)
		first.Preconditions = caldav.Unconditional().WithProbe(func(*caldav.Revision) {
			once.Do(func() {
				parked.Store(true)
				close(inside)
				<-release
				parked.Store(false)
			})
		})

		firstDone := make(chan error, 1)
		go func() { _, err := w.CompareAndStoreItem(ctx, ref, first); firstDone <- err }()

		select {
		case <-inside:
		case err := <-firstDone:
			t.Fatalf("the first write finished without ever calling Check: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("the first write never called Check")
		}

		secondDone := make(chan error, 1)
		go func() {
			_, err := w.CompareAndStoreItem(ctx, ref, createRequest(event(uid, "Second"), uid))
			secondDone <- err
		}()

		select {
		case err := <-secondDone:
			t.Errorf("a second write to %s completed (%v) while the first was still inside its transaction; the two are not serialised, so one of them will be lost", ref.Item, err)
		case <-time.After(blockedFor):
			if !parked.Load() {
				t.Error("the first write left Check without being released")
			}
		}

		close(release)
		if err := <-firstDone; err != nil {
			t.Errorf("the first write = %v, want success", err)
		}
		select {
		case err := <-secondDone:
			if err != nil {
				t.Errorf("the second write = %v, want success once the first released", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("the second write never completed after the first released")
		}
	})
}
