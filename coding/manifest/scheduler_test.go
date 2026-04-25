package manifest_test

import (
	"testing"

	"github.com/chris/coworker/coding/manifest"
	"github.com/chris/coworker/core"
)

// buildManifest constructs a PlanManifest from inline entries for testing.
func buildManifest(entries []manifest.PlanEntry) *manifest.PlanManifest {
	return &manifest.PlanManifest{
		SpecPath: "docs/specs/test.md",
		Plans:    entries,
	}
}

func ids(entries []manifest.PlanEntry) []int {
	result := make([]int, len(entries))
	for i, e := range entries {
		result[i] = e.ID
	}
	return result
}

func makePolicy(maxParallel int) *core.Policy {
	return &core.Policy{
		ConcurrencyLimits: core.ConcurrencyLimits{
			MaxParallelPlans: maxParallel,
		},
	}
}

// TestScheduler_NoCompletedOrActive: all root plans (no deps) should be ready.
func TestScheduler_NoCompletedOrActive(t *testing.T) {
	m := buildManifest([]manifest.PlanEntry{
		{ID: 100, Title: "Plan A", Phases: []string{"p1"}, BlocksOn: nil},
		{ID: 101, Title: "Plan B", Phases: []string{"p1"}, BlocksOn: nil},
		{ID: 102, Title: "Plan C", Phases: []string{"p1"}, BlocksOn: []int{100}},
	})
	s := manifest.NewDAGScheduler(m, makePolicy(4))
	ready := s.ReadyPlans(nil, nil)
	got := ids(ready)
	// Only root plans: 100 and 101. 102 blocks on 100.
	if len(got) != 2 {
		t.Fatalf("want 2 ready plans, got %d: %v", len(got), got)
	}
	if got[0] != 100 || got[1] != 101 {
		t.Errorf("want [100 101], got %v", got)
	}
}

// TestScheduler_BlockingDepsRespected: 101 blocks on 100 → 101 not ready until 100 done.
func TestScheduler_BlockingDepsRespected(t *testing.T) {
	m := buildManifest([]manifest.PlanEntry{
		{ID: 100, Title: "Plan A", Phases: []string{"p1"}, BlocksOn: nil},
		{ID: 101, Title: "Plan B", Phases: []string{"p1"}, BlocksOn: []int{100}},
	})
	s := manifest.NewDAGScheduler(m, makePolicy(4))

	ready := s.ReadyPlans(nil, nil)
	if ids := ids(ready); len(ids) != 1 || ids[0] != 100 {
		t.Fatalf("before 100 done, want [100], got %v", ids)
	}

	// Mark 100 complete.
	completed := map[int]bool{100: true}
	ready = s.ReadyPlans(completed, nil)
	if ids := ids(ready); len(ids) != 1 || ids[0] != 101 {
		t.Fatalf("after 100 done, want [101], got %v", ids)
	}
}

// TestScheduler_CapFromPolicy: cap of 2 with 1 active → 1 slot.
func TestScheduler_CapFromPolicy(t *testing.T) {
	m := buildManifest([]manifest.PlanEntry{
		{ID: 100, Title: "Plan A", Phases: []string{"p1"}, BlocksOn: nil},
		{ID: 101, Title: "Plan B", Phases: []string{"p1"}, BlocksOn: nil},
		{ID: 102, Title: "Plan C", Phases: []string{"p1"}, BlocksOn: nil},
	})
	s := manifest.NewDAGScheduler(m, makePolicy(2))

	active := map[int]bool{100: true}
	ready := s.ReadyPlans(nil, active)
	// cap=2, active=1 → slots=1 → only one ready plan returned
	if len(ready) != 1 {
		t.Fatalf("want 1 ready plan (1 slot), got %d: %v", len(ready), ids(ready))
	}
	if ready[0].ID != 101 {
		t.Errorf("want plan 101, got %v", ready[0].ID)
	}
}

// TestScheduler_CapExhausted: active at cap → no slots → nil returned.
func TestScheduler_CapExhausted(t *testing.T) {
	m := buildManifest([]manifest.PlanEntry{
		{ID: 100, Title: "Plan A", Phases: []string{"p1"}, BlocksOn: nil},
		{ID: 101, Title: "Plan B", Phases: []string{"p1"}, BlocksOn: nil},
		{ID: 102, Title: "Plan C", Phases: []string{"p1"}, BlocksOn: nil},
	})
	s := manifest.NewDAGScheduler(m, makePolicy(2))

	active := map[int]bool{100: true, 101: true}
	ready := s.ReadyPlans(nil, active)
	if ready != nil {
		t.Fatalf("want nil when cap exhausted, got %v", ids(ready))
	}
}

// TestScheduler_AllCompleted: returns empty when every plan is done.
func TestScheduler_AllCompleted(t *testing.T) {
	m := buildManifest([]manifest.PlanEntry{
		{ID: 100, Title: "Plan A", Phases: []string{"p1"}, BlocksOn: nil},
		{ID: 101, Title: "Plan B", Phases: []string{"p1"}, BlocksOn: []int{100}},
	})
	s := manifest.NewDAGScheduler(m, makePolicy(4))

	completed := map[int]bool{100: true, 101: true}
	ready := s.ReadyPlans(completed, nil)
	if len(ready) != 0 {
		t.Fatalf("want empty when all completed, got %v", ids(ready))
	}
}

// TestScheduler_SingleDepChain: 100 → 101 → 102 releases one at a time.
func TestScheduler_SingleDepChain(t *testing.T) {
	m := buildManifest([]manifest.PlanEntry{
		{ID: 100, Title: "Plan A", Phases: []string{"p1"}, BlocksOn: nil},
		{ID: 101, Title: "Plan B", Phases: []string{"p1"}, BlocksOn: []int{100}},
		{ID: 102, Title: "Plan C", Phases: []string{"p1"}, BlocksOn: []int{101}},
	})
	s := manifest.NewDAGScheduler(m, makePolicy(4))

	ready := s.ReadyPlans(nil, nil)
	if ids := ids(ready); len(ids) != 1 || ids[0] != 100 {
		t.Fatalf("step 0: want [100], got %v", ids)
	}

	ready = s.ReadyPlans(map[int]bool{100: true}, nil)
	if ids := ids(ready); len(ids) != 1 || ids[0] != 101 {
		t.Fatalf("step 1: want [101], got %v", ids)
	}

	ready = s.ReadyPlans(map[int]bool{100: true, 101: true}, nil)
	if ids := ids(ready); len(ids) != 1 || ids[0] != 102 {
		t.Fatalf("step 2: want [102], got %v", ids)
	}
}

// TestScheduler_Diamond: 100 and 101 are roots; 102 blocks on both.
func TestScheduler_Diamond(t *testing.T) {
	m := buildManifest([]manifest.PlanEntry{
		{ID: 100, Title: "Plan A", Phases: []string{"p1"}, BlocksOn: nil},
		{ID: 101, Title: "Plan B", Phases: []string{"p1"}, BlocksOn: nil},
		{ID: 102, Title: "Plan C", Phases: []string{"p1"}, BlocksOn: []int{100, 101}},
	})
	s := manifest.NewDAGScheduler(m, makePolicy(4))

	// Initial: 100 and 101 ready; 102 blocked.
	ready := s.ReadyPlans(nil, nil)
	if len(ready) != 2 {
		t.Fatalf("want 2 roots, got %d: %v", len(ready), ids(ready))
	}

	// Only 100 done → 102 still blocked.
	ready = s.ReadyPlans(map[int]bool{100: true}, nil)
	if len(ready) != 1 || ready[0].ID != 101 {
		t.Fatalf("only 100 done: want [101], got %v", ids(ready))
	}

	// Both done → 102 ready.
	ready = s.ReadyPlans(map[int]bool{100: true, 101: true}, nil)
	if len(ready) != 1 || ready[0].ID != 102 {
		t.Fatalf("both done: want [102], got %v", ids(ready))
	}
}

// TestScheduler_DefaultCap: nil policy uses default cap of 2.
func TestScheduler_DefaultCap(t *testing.T) {
	s := manifest.NewDAGScheduler(nil, nil)
	if s.MaxParallelPlans() != 2 {
		t.Errorf("default cap should be 2, got %d", s.MaxParallelPlans())
	}
}

// TestScheduler_NilManifest: nil manifest returns nil.
func TestScheduler_NilManifest(t *testing.T) {
	s := manifest.NewDAGScheduler(nil, makePolicy(4))
	ready := s.ReadyPlans(nil, nil)
	if ready != nil {
		t.Fatalf("want nil for nil manifest, got %v", ready)
	}
}
