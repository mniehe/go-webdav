package caldav

import "slices"

type preconditionKind uint8

const (
	unconditional preconditionKind = iota
	targetExists
	targetMissing
	atRevision
	notAtRevision
)

// Preconditions is what the client expects to find before its write applies.
// Invalid combinations are unconstructable: there is no way to ask for a target
// that both exists and does not.
//
// The zero value is Unconditional.
type Preconditions struct {
	kind  preconditionKind
	revs  []Revision
	probe func(current *Revision)
}

// Unconditional applies the write whatever the current state.
func Unconditional() Preconditions { return Preconditions{} }

// IfTargetExists requires the target to exist, at any revision.
func IfTargetExists() Preconditions { return Preconditions{kind: targetExists} }

// IfTargetMissing requires the target not to exist.
func IfTargetMissing() Preconditions { return Preconditions{kind: targetMissing} }

// IfRevision requires the target to exist at one of these revisions. This is
// the common case: a client that fetched an item and is now updating it sends
// the revision it saw, and the write must not apply if anything changed since.
func IfRevision(revs ...Revision) Preconditions {
	return Preconditions{kind: atRevision, revs: slices.Clone(revs)}
}

// IfNotRevision requires the target to be absent, or to be at none of these
// revisions.
func IfNotRevision(revs ...Revision) Preconditions {
	return Preconditions{kind: notAtRevision, revs: slices.Clone(revs)}
}

// WithProbe returns a copy of p that calls fn each time Check runs, before
// evaluating anything.
//
// Check is the only library code that runs inside a backend's transaction, so
// this is the one place a test can observe one. The backend conformance suite
// uses it to count calls and to hold a transaction open while a conflicting
// write runs; ordinary servers have no reason to.
func (p Preconditions) WithProbe(fn func(current *Revision)) Preconditions {
	p.probe = fn
	return p
}

// Check evaluates the preconditions against the state you just read, inside
// your transaction. Pass nil when the target does not exist. Works the same for
// items and calendars.
//
// A failure is ErrPreconditionFailed. Return it unwrapped.
func (p Preconditions) Check(current *Revision) error {
	if p.probe != nil {
		p.probe(current)
	}

	switch p.kind {
	case unconditional:
		return nil
	case targetExists:
		if current == nil {
			return ErrPreconditionFailed
		}
	case targetMissing:
		if current != nil {
			return ErrPreconditionFailed
		}
	case atRevision:
		if current == nil || !slices.Contains(p.revs, *current) {
			return ErrPreconditionFailed
		}
	case notAtRevision:
		if current != nil && slices.Contains(p.revs, *current) {
			return ErrPreconditionFailed
		}
	}
	return nil
}
