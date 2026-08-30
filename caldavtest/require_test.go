package caldavtest

import "testing"

// TestCapabilityGuard pins the difference between skipping and failing. A guard
// that skipped where Require says fail would shrink the suite silently, which
// is the failure mode the capability mechanism exists to prevent — and it would
// look exactly like a green run.
func TestCapabilityGuard(t *testing.T) {
	for _, tc := range []struct {
		name        string
		opts        []Option
		implemented bool
		want        verdict
	}{
		{"implemented", nil, true, runScenario},
		{"implemented and required", []Option{RequireAll()}, true, runScenario},
		{"absent and not required", nil, false, skipScenario},
		{"absent and required by name", []Option{Require(CapItemWriter)}, false, failScenario},
		{"absent and required by RequireAll", []Option{RequireAll()}, false, failScenario},
		{"a different capability required", []Option{Require(CapSyncBackend)}, false, skipScenario},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config{required: map[Capability]bool{}}
			for _, opt := range tc.opts {
				opt(cfg)
			}
			if got := cfg.verdict(CapItemWriter, tc.implemented); got != tc.want {
				t.Errorf("verdict = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestEveryCapabilityHasAName keeps skip messages actionable: a capability
// missing from allCapabilities prints an empty constant name, and RequireAll
// silently stops requiring it.
func TestEveryCapabilityHasAName(t *testing.T) {
	for _, capability := range []Capability{
		CapItemWriter, CapSyncBackend, CapCalendarCreator, CapCalendarUpdater,
		CapCalendarDeleter, CapSharingBackend, CapReopener, CapPruner,
	} {
		if allCapabilities[capability] == "" {
			t.Errorf("%s has no entry in allCapabilities", capability)
		}
	}
}
