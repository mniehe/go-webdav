// Package caldavtest is the conformance suite for caldav backends.
//
// A backend supplies a Harness — something that can seed known state and hand
// back a Backend over it — and Conformance does the rest:
//
//	func TestConformance(t *testing.T) {
//		caldavtest.Conformance(t, newHarness, caldavtest.RequireAll())
//	}
//
// The suite tests the four obligations only storage can meet, described on
// caldav.Backend. Everything else the library does itself, and a backend
// cannot get it wrong.
//
// # Optional interfaces
//
// Capabilities a backend does not implement are skipped, and every skip names
// the Require option that turns it into a failure. A backend that implements
// everything should run with RequireAll, so dropping an interface by accident
// reddens rather than quietly shrinking the suite. Where an optional path can
// be checked against a required one — a write against a read, a change list
// against a full listing — the suite does that instead of trusting either.
//
// # Compatibility
//
// This suite grows. A scenario added later that fails a backend which used to
// pass is a pre-existing bug being found, not a compatibility break: the
// obligations it tests have not changed since the first release.
package caldavtest

import (
	"context"
	"testing"

	"github.com/mniehe/davkit/caldav"
)

// NewHarness builds a harness over empty storage. It is called once per
// scenario, so scenarios cannot see each other's writes.
type NewHarness func(ctx context.Context, t *testing.T) (Harness, error)

// Harness is a backend plus the two things a test needs that the backend
// interface deliberately does not offer: a way to put known state in place, and
// a way to tear it down.
type Harness interface {
	// Seed puts the fixture in place. It may be called only once, before
	// Backend.
	Seed(ctx context.Context, f Fixture) error

	// Backend returns the backend over the seeded state. Repeated calls may
	// return the same value.
	Backend(ctx context.Context) (caldav.Backend, error)

	// Close releases whatever the harness holds. It must be safe to call twice.
	Close()
}

// Reopener is a harness whose storage outlives the process handle, which is
// what lets the suite check that a write was durable rather than merely
// visible. In-memory backends will not implement it.
type Reopener interface {
	// Reopen discards the current backend and returns a fresh one over the same
	// storage.
	Reopen(ctx context.Context) (caldav.Backend, error)
}

// Pruner is a harness that can discard change history on demand. Without it
// caldav.ErrHistoryTooOld is unreachable from a test, and the branch a client
// depends on to recover from a stale sync position goes unexercised.
type Pruner interface {
	// PruneHistory discards change records below a revision. Afterwards
	// ListChanges must report ErrHistoryTooOld for any earlier position.
	PruneHistory(ctx context.Context, ref caldav.CalendarRef, before caldav.Revision) error
}

// Capability names an interface a backend or harness may implement. Failures
// print the identifier so it is clear what to go and write.
type Capability string

const (
	CapItemWriter      Capability = "caldav.ItemWriter"
	CapSyncBackend     Capability = "caldav.SyncBackend"
	CapCalendarCreator Capability = "caldav.CalendarCreator"
	CapCalendarUpdater Capability = "caldav.CalendarUpdater"
	CapCalendarDeleter Capability = "caldav.CalendarDeleter"
	CapSharingBackend  Capability = "caldav.SharingBackend"
	CapReopener        Capability = "caldavtest.Reopener"
	CapPruner          Capability = "caldavtest.Pruner"
)

// allCapabilities is what RequireAll requires, and the source of the constant
// names printed in skip messages.
var allCapabilities = map[Capability]string{
	CapItemWriter:      "CapItemWriter",
	CapSyncBackend:     "CapSyncBackend",
	CapCalendarCreator: "CapCalendarCreator",
	CapCalendarUpdater: "CapCalendarUpdater",
	CapCalendarDeleter: "CapCalendarDeleter",
	CapSharingBackend:  "CapSharingBackend",
	CapReopener:        "CapReopener",
	CapPruner:          "CapPruner",
}

// Option configures a run.
type Option func(*config)

type config struct {
	required map[Capability]bool
}

// Require turns the skip for each named capability into a failure.
func Require(caps ...Capability) Option {
	return func(c *config) {
		for _, capability := range caps {
			c.required[capability] = true
		}
	}
}

// RequireAll requires every capability. A backend that implements the lot
// should run this way, so an interface dropped by accident reddens instead of
// quietly shrinking the suite.
func RequireAll() Option {
	return func(c *config) {
		for capability := range allCapabilities {
			c.required[capability] = true
		}
	}
}

// verdict is what to do about a capability, separated from the reporting so
// the guard itself can be tested: a guard that skipped where it should fail
// would shrink the suite silently, and look exactly like a green run.
type verdict uint8

const (
	runScenario verdict = iota
	skipScenario
	failScenario
)

func (c *config) verdict(capability Capability, implemented bool) verdict {
	switch {
	case implemented:
		return runScenario
	case c.required[capability]:
		return failScenario
	default:
		return skipScenario
	}
}

// need runs, skips or fails the scenario. A skip always names the option that
// would have made it a failure.
func (c *config) need(t *testing.T, capability Capability, implemented bool) {
	t.Helper()
	name := allCapabilities[capability]
	switch c.verdict(capability, implemented) {
	case failScenario:
		t.Fatalf("backend does not implement %s, and caldavtest.Require(caldavtest.%s) demands it", capability, name)
	case skipScenario:
		t.Skipf("backend does not implement %s; pass caldavtest.Require(caldavtest.%s) to make this a failure", capability, name)
	case runScenario:
	}
}

// Conformance runs the whole suite against newHarness.
func Conformance(t *testing.T, newHarness NewHarness, opts ...Option) {
	t.Helper()

	cfg := &config{required: map[Capability]bool{}}
	for _, opt := range opts {
		opt(cfg)
	}

	for _, group := range []struct {
		name string
		run  func(*testing.T, NewHarness, *config)
	}{
		{"Reading", testReading},
		{"Authorizer", testAuthorizer},
		{"Isolation", testIsolation},
		{"Obligation1", testObligation1},
		{"Obligation2", testObligation2},
		{"Obligation3", testObligation3},
		{"Obligation4", testObligation4},
		{"Capabilities", testCapabilities},
		{"CalendarLifecycle", testCalendarLifecycle},
		{"Consistency", testConsistency},
		{"Serialisation", testSerialisation},
		{"Settings", testSettings},
		{"Durability", testDurability},
	} {
		t.Run(group.name, func(t *testing.T) {
			group.run(t, newHarness, cfg)
		})
	}
}
