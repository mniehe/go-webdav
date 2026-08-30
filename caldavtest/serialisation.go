package caldavtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mniehe/davkit/caldav"
)

// assertSerialised parks one operation inside its precondition check and starts
// a conflicting one, then requires the second not to finish until the first is
// released.
//
// Check is the only library code that runs inside a backend's transaction,
// which makes it the one place a test can hold a transaction open. A backend
// that really serialises never completes the second call in the window however
// long the window is, so this cannot flake in the passing direction; one that
// does not will finish it almost at once.
//
// The second operation's own result is not asserted: after a delete it is
// legitimately an error.
func assertSerialised(t *testing.T, what string, first func(caldav.Preconditions) error, second func() error) {
	t.Helper()

	inside := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var parked atomic.Bool

	pre := caldav.Unconditional().WithProbe(func(*caldav.Revision) {
		once.Do(func() {
			parked.Store(true)
			close(inside)
			<-release
			parked.Store(false)
		})
	})

	firstDone := make(chan error, 1)
	go func() { firstDone <- first(pre) }()

	select {
	case <-inside:
	case err := <-firstDone:
		t.Fatalf("the first %s finished without ever calling Check: %v", what, err)
	case <-time.After(5 * time.Second):
		t.Fatalf("the first %s never called Check", what)
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- second() }()

	select {
	case err := <-secondDone:
		t.Errorf("a second %s completed (%v) while the first was still inside its transaction; the two are not serialised, so one of them will be lost", what, err)
	case <-time.After(blockedFor):
		if !parked.Load() {
			t.Errorf("the first %s left Check without being released", what)
		}
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Errorf("the first %s = %v, want success", what, err)
	}
	select {
	case <-secondDone:
	case <-time.After(5 * time.Second):
		t.Errorf("the second %s never completed after the first released", what)
	}
}

// testSerialisation covers compare-and-mutate on the paths that do not go
// through CompareAndStoreItem: content-ID uniqueness across different names,
// and the calendar itself.
func testSerialisation(t *testing.T, newHarness NewHarness, cfg *config) {
	ctx := context.Background()

	t.Run("ConcurrentContentIDExactlyOneWins", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		w := writerOf(t, cfg, b)

		// Racing the same item name only exercises the target precondition. A
		// backend can get that exactly right and still enforce content-ID
		// uniqueness as an unlocked read-then-insert, which is a different
		// check on a different index — so race the same content ID under
		// different names instead.
		const racers = 8
		const uid = "contended-id@example.test"

		// A start channel only releases the racers; it does not make them
		// arrive together, and a write this cheap can finish before the next
		// goroutine is scheduled. The barrier holds every racer until all of
		// them are at it, which is what puts more than one inside the backend
		// at the same time.
		errs := make([]error, racers)
		var ready, done sync.WaitGroup
		ready.Add(racers)
		done.Add(racers)
		for i := range racers {
			go func() {
				defer done.Done()
				ref := itemRef(WorkRef, fmt.Sprintf("racer-%d.ics", i))
				req := createRequest(event(uid, "Contended"), uid)
				ready.Done()
				ready.Wait()
				_, errs[i] = w.CompareAndStoreItem(ctx, ref, req)
			}()
		}
		done.Wait()

		won := 0
		for i, err := range errs {
			var duplicate *caldav.DuplicateContentIDError
			switch {
			case err == nil:
				won++
			case errors.As(err, &duplicate):
			default:
				t.Errorf("racer %d = %v, want either success or a *caldav.DuplicateContentIDError", i, err)
			}
		}
		if won != 1 {
			t.Fatalf("%d of %d concurrent writes claimed content ID %s under different names, want exactly 1. Two calendar objects sharing a UID is a calendar no client can reconcile", won, racers, uid)
		}
	})

	t.Run("CalendarUpdateWaitsForAnotherTransaction", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		u, ok := b.(caldav.CalendarUpdater)
		cfg.need(t, CapCalendarUpdater, ok)

		firstName, secondName := "First", "Second"
		assertSerialised(t, "calendar update",
			func(pre caldav.Preconditions) error {
				_, err := u.CompareAndUpdateCalendar(ctx, WorkRef, caldav.CalendarPatch{DisplayName: &firstName}, pre)
				return err
			},
			func() error {
				_, err := u.CompareAndUpdateCalendar(ctx, WorkRef, caldav.CalendarPatch{DisplayName: &secondName}, caldav.Unconditional())
				return err
			})
	})

	t.Run("CalendarDeleteWaitsForAnotherTransaction", func(t *testing.T) {
		b := setup(ctx, t, newHarness, BaseFixture())
		d, ok := b.(caldav.CalendarDeleter)
		cfg.need(t, CapCalendarDeleter, ok)

		assertSerialised(t, "calendar delete",
			func(pre caldav.Preconditions) error {
				return d.CompareAndDeleteCalendar(ctx, WorkRef, pre)
			},
			func() error {
				return d.CompareAndDeleteCalendar(ctx, WorkRef, caldav.Unconditional())
			})
	})
}
