package carddav

import (
	"errors"
	"testing"
)

func rev(r Revision) *Revision { return &r }

func TestPreconditionsCheck(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pre     Preconditions
		current *Revision
		pass    bool
	}{
		{"zero value is unconditional, absent", Preconditions{}, nil, true},
		{"zero value is unconditional, present", Preconditions{}, rev(7), true},
		{"unconditional, absent", Unconditional(), nil, true},
		{"unconditional, present", Unconditional(), rev(7), true},

		{"exists, absent", IfTargetExists(), nil, false},
		{"exists, present", IfTargetExists(), rev(7), true},
		{"missing, absent", IfTargetMissing(), nil, true},
		{"missing, present", IfTargetMissing(), rev(7), false},

		{"revision matches", IfRevision(7), rev(7), true},
		{"revision is one of several", IfRevision(5, 7, 9), rev(7), true},
		{"revision differs", IfRevision(7), rev(8), false},
		{"revision but absent", IfRevision(7), nil, false},
		{"no revisions listed", IfRevision(), rev(7), false},

		{"not revision, differs", IfNotRevision(7), rev(8), true},
		{"not revision, matches", IfNotRevision(7), rev(7), false},
		{"not revision, one of several matches", IfNotRevision(5, 7), rev(7), false},
		{"not revision, absent", IfNotRevision(7), nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.pre.Check(tc.current)
			switch {
			case tc.pass && err != nil:
				t.Fatalf("Check = %v, want it to pass", err)
			case !tc.pass && err == nil:
				t.Fatal("Check passed, want ErrPreconditionFailed")
			case !tc.pass && !errors.Is(err, ErrPreconditionFailed):
				t.Fatalf("Check = %v, want ErrPreconditionFailed", err)
			}
		})
	}
}

func TestIfRevisionCopiesItsArgument(t *testing.T) {
	revs := []Revision{7}
	pre := IfRevision(revs...)
	revs[0] = 8

	if err := pre.Check(rev(7)); err != nil {
		t.Errorf("Check against the revision passed in = %v; the caller's slice was aliased", err)
	}
	if err := pre.Check(rev(8)); err == nil {
		t.Error("Check accepted the revision the caller wrote into its slice afterwards")
	}
}

func TestProbeSeesWhatCheckSees(t *testing.T) {
	var calls int
	var saw []*Revision
	pre := IfTargetExists().WithProbe(func(current *Revision) {
		calls++
		saw = append(saw, current)
	})

	if err := pre.Check(nil); err == nil {
		t.Error("Check passed for a missing target")
	}
	if err := pre.Check(rev(3)); err != nil {
		t.Errorf("Check = %v, want it to pass", err)
	}

	if calls != 2 {
		t.Fatalf("the probe ran %d times, want 2 — it must run even when the check fails", calls)
	}
	if saw[0] != nil {
		t.Errorf("the probe saw revision %d for a missing target", *saw[0])
	}
	if saw[1] == nil || *saw[1] != 3 {
		t.Errorf("the probe saw %v, want revision 3", saw[1])
	}
}

func TestWithProbeLeavesTheOriginalAlone(t *testing.T) {
	original := IfTargetExists()
	var called bool
	probed := original.WithProbe(func(*Revision) { called = true })

	if err := original.Check(rev(1)); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if called {
		t.Error("WithProbe attached the probe to the value it was called on, not to a copy")
	}
	if err := probed.Check(rev(1)); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !called {
		t.Error("the returned value did not carry the probe")
	}
}
