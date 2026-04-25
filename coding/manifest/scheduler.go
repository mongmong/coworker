package manifest

import (
	"github.com/chris/coworker/core"
)

const defaultMaxParallelPlans = 2

// DAGScheduler determines which plans in a manifest are ready to run,
// given the current state of completed and active plans.
//
// DAGScheduler is pure logic — it has no store imports and performs no I/O.
// It can be used in unit tests without any database setup.
type DAGScheduler struct {
	Manifest *PlanManifest
	Policy   *core.Policy
}

// NewDAGScheduler creates a scheduler from the given manifest and policy.
// Either may be nil; nil Policy means the default concurrency cap is used.
func NewDAGScheduler(m *PlanManifest, p *core.Policy) *DAGScheduler {
	return &DAGScheduler{Manifest: m, Policy: p}
}

// MaxParallelPlans returns the effective concurrency cap from policy.
// Falls back to defaultMaxParallelPlans if policy is nil or unset.
func (s *DAGScheduler) MaxParallelPlans() int {
	if s.Policy != nil && s.Policy.ConcurrencyLimits.MaxParallelPlans > 0 {
		return s.Policy.ConcurrencyLimits.MaxParallelPlans
	}
	return defaultMaxParallelPlans
}

// ReadyPlans returns the plans that are eligible to start now.
//
// A plan is eligible when:
//  1. It is not in completed (already done).
//  2. It is not in active (currently running).
//  3. All IDs listed in its BlocksOn are in completed.
//
// The returned slice is bounded so that len(active) + len(result) does not
// exceed MaxParallelPlans(). If active already equals or exceeds the cap,
// ReadyPlans returns nil.
//
// Both completed and active may be nil, which is treated as empty sets.
func (s *DAGScheduler) ReadyPlans(completed map[int]bool, active map[int]bool) []PlanEntry {
	if s.Manifest == nil {
		return nil
	}

	cap := s.MaxParallelPlans()
	slots := cap - len(active)
	if slots <= 0 {
		return nil
	}

	var ready []PlanEntry
	for _, p := range s.Manifest.Plans {
		if len(ready) >= slots {
			break
		}
		if completed[p.ID] || active[p.ID] {
			continue
		}
		if !allCompleted(p.BlocksOn, completed) {
			continue
		}
		ready = append(ready, p)
	}
	return ready
}

// allCompleted reports whether every id in deps is present in the completed set.
func allCompleted(deps []int, completed map[int]bool) bool {
	for _, id := range deps {
		if !completed[id] {
			return false
		}
	}
	return true
}
