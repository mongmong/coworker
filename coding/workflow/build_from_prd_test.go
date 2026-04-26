package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/coding/manifest"
	"github.com/chris/coworker/coding/phaseloop"
	"github.com/chris/coworker/coding/shipper"
	"github.com/chris/coworker/coding/stages"
	"github.com/chris/coworker/coding/workflow"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// stubOrchestrator is a minimal Orchestrator stub for workflow-level tests.
type stubOrchestrator struct{}

func (s *stubOrchestrator) Orchestrate(_ context.Context, _ *coding.DispatchInput) (*coding.DispatchResult, error) {
	return &coding.DispatchResult{ExitCode: 0}, nil
}

// openTestDB opens an in-memory SQLite database for workflow tests.
func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open(:memory:): %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newTestPhaseExecutor creates a PhaseExecutor with a clean-always stub dispatcher.
func newTestPhaseExecutor(t *testing.T) *phaseloop.PhaseExecutor {
	t.Helper()
	db := openTestDB(t)
	return &phaseloop.PhaseExecutor{
		Dispatcher: &stubOrchestrator{},
		EventStore: store.NewEventStore(db),
	}
}

const testManifestYAML = `
spec_path: docs/specs/test.md
plans:
  - id: 100
    title: "Core runtime"
    phases: ["SQLite schema"]
    blocks_on: []
  - id: 101
    title: "Review workflow"
    phases: ["dispatch"]
    blocks_on: [100]
  - id: 102
    title: "TUI dashboard"
    phases: ["layout"]
    blocks_on: []
`

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

func TestBuildFromPRDWorkflow_Name(t *testing.T) {
	w := workflow.NewBuildFromPRDWorkflow("manifest.yaml", nil)
	if w.Name() != "build-from-prd" {
		t.Errorf("Name() = %q, want %q", w.Name(), "build-from-prd")
	}
}

func TestBuildFromPRDWorkflow_LoadManifest(t *testing.T) {
	path := writeManifest(t, testManifestYAML)
	w := workflow.NewBuildFromPRDWorkflow(path, nil)
	m, err := w.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(m.Plans) != 3 {
		t.Errorf("len(plans) = %d, want 3", len(m.Plans))
	}
}

func TestBuildFromPRDWorkflow_LoadManifest_EmptyPath(t *testing.T) {
	w := workflow.NewBuildFromPRDWorkflow("", nil)
	_, err := w.LoadManifest()
	if err == nil {
		t.Fatal("expected error for empty ManifestPath, got nil")
	}
}

func TestBuildFromPRDWorkflow_Schedule(t *testing.T) {
	path := writeManifest(t, testManifestYAML)
	policy := &core.Policy{
		ConcurrencyLimits: core.ConcurrencyLimits{MaxParallelPlans: 4},
	}
	w := workflow.NewBuildFromPRDWorkflow(path, policy)
	m, err := w.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}

	// No completed → only roots (100, 102) are ready; 101 blocks on 100.
	ready := w.Schedule(m, nil, nil)
	if len(ready) != 2 {
		t.Fatalf("want 2 ready plans, got %d", len(ready))
	}

	// After 100 complete → 101 becomes ready.
	ready = w.Schedule(m, map[int]bool{100: true}, nil)
	if len(ready) != 2 {
		t.Fatalf("want 2 after 100 done (101 + 102), got %d", len(ready))
	}
}

func TestBuildFromPRDWorkflow_Run_NoWorktrees(t *testing.T) {
	path := writeManifest(t, testManifestYAML)
	policy := &core.Policy{
		ConcurrencyLimits: core.ConcurrencyLimits{MaxParallelPlans: 4},
	}
	w := workflow.NewBuildFromPRDWorkflow(path, policy)
	// No WorktreeManager set → no worktrees created.

	result, err := w.Run(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.ReadyPlans) == 0 {
		t.Fatal("expected ready plans, got none")
	}
	if len(result.Worktrees) != 0 {
		t.Errorf("expected no worktrees (no manager), got %d", len(result.Worktrees))
	}
}

func TestBuildFromPRDWorkflow_Run_AllCompleted(t *testing.T) {
	path := writeManifest(t, testManifestYAML)
	w := workflow.NewBuildFromPRDWorkflow(path, nil)

	completed := map[int]bool{100: true, 101: true, 102: true}
	result, err := w.Run(context.Background(), completed, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.ReadyPlans) != 0 {
		t.Errorf("expected no ready plans when all completed, got %d", len(result.ReadyPlans))
	}
}

func TestBuildFromPRDWorkflow_PrepareWorktrees_SinglePlan(t *testing.T) {
	path := writeManifest(t, testManifestYAML)
	w := workflow.NewBuildFromPRDWorkflow(path, nil)
	// Even with a WorktreeManager, single plan → no worktrees.
	w.WorktreeManager = manifest.NewWorktreeManager("/repo", "/repo/.coworker/worktrees")

	plans := []manifest.PlanEntry{
		{ID: 100, Title: "Core runtime", Phases: []string{"p1"}, BlocksOn: nil},
	}
	worktrees, err := w.PrepareWorktrees(context.Background(), plans)
	if err != nil {
		t.Fatalf("PrepareWorktrees: %v", err)
	}
	if len(worktrees) != 0 {
		t.Errorf("expected no worktrees for single plan, got %d", len(worktrees))
	}
}

func TestBuildFromPRDWorkflow_RunPhasesForPlan_NilPhaseExecutor(t *testing.T) {
	path := writeManifest(t, testManifestYAML)
	w := workflow.NewBuildFromPRDWorkflow(path, nil)
	// PhaseExecutor is nil → must return an error.
	plan := manifest.PlanEntry{ID: 100, Title: "Core runtime", Phases: []string{"SQLite schema"}}
	_, err := w.RunPhasesForPlan(context.Background(), "run-1", plan, nil)
	if err == nil {
		t.Fatal("expected error when PhaseExecutor is nil, got nil")
	}
}

func TestBuildFromPRDWorkflow_RunPhasesForPlan_NoShipper(t *testing.T) {
	// Phases complete cleanly; no shipper → ShipResult should be nil.
	path := writeManifest(t, testManifestYAML)
	w := workflow.NewBuildFromPRDWorkflow(path, nil)
	w.PhaseExecutor = newTestPhaseExecutor(t)

	plan := manifest.PlanEntry{ID: 100, Title: "Core runtime", Phases: []string{"SQLite schema"}}
	result, err := w.RunPhasesForPlan(context.Background(), "run-no-ship", plan, map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("RunPhasesForPlan: %v", err)
	}
	if len(result.PhaseResults) != 1 {
		t.Errorf("expected 1 phase result, got %d", len(result.PhaseResults))
	}
	if result.ShipResult != nil {
		t.Error("expected nil ShipResult when no Shipper configured")
	}
}

func TestBuildFromPRDWorkflow_RunPhasesForPlan_WithShipper_DryRun(t *testing.T) {
	// Full smoke: manifest → schedule → phase loop (stub) → ship (dry-run).
	path := writeManifest(t, testManifestYAML)
	w := workflow.NewBuildFromPRDWorkflow(path, nil)
	w.PhaseExecutor = newTestPhaseExecutor(t)
	w.Shipper = &shipper.Shipper{DryRun: true}

	plan := manifest.PlanEntry{ID: 115, Title: "Shipper + workflow customization", Phases: []string{"build", "review"}}
	result, err := w.RunPhasesForPlan(context.Background(), "run-smoke", plan, map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
		"branch":    "feature/plan-115-shipper",
	})
	if err != nil {
		t.Fatalf("RunPhasesForPlan: %v", err)
	}

	// Two phases should have completed.
	if len(result.PhaseResults) != 2 {
		t.Errorf("expected 2 phase results, got %d", len(result.PhaseResults))
	}
	for i, pr := range result.PhaseResults {
		if !pr.Clean {
			t.Errorf("phase %d: expected Clean=true", i)
		}
	}

	// Ship step should have produced a result.
	if result.ShipResult == nil {
		t.Fatal("expected non-nil ShipResult when Shipper is configured")
	}
	if result.ShipResult.PRURL == "" {
		t.Error("expected non-empty PRURL from dry-run shipper")
	}
}

func TestBuildFromPRDWorkflow_RunPhasesForPlan_BranchFallback(t *testing.T) {
	// When "branch" key is absent from inputs, baseBranch() ("main") is used.
	path := writeManifest(t, testManifestYAML)
	w := workflow.NewBuildFromPRDWorkflow(path, nil)
	w.PhaseExecutor = newTestPhaseExecutor(t)
	w.Shipper = &shipper.Shipper{DryRun: true}

	plan := manifest.PlanEntry{ID: 100, Title: "Core runtime", Phases: []string{"p1"}}
	result, err := w.RunPhasesForPlan(context.Background(), "run-branch-fallback", plan, map[string]string{
		"diff_path": "/tmp/d.diff",
		"spec_path": "/tmp/s.md",
		// No "branch" key — should fall back to "main".
	})
	if err != nil {
		t.Fatalf("RunPhasesForPlan: %v", err)
	}
	if result.ShipResult == nil {
		t.Fatal("expected ShipResult even with branch fallback")
	}
}

// recordingOrchestrator captures the role name of every Orchestrate call.
type recordingOrchestrator struct {
	mu    sync.Mutex
	roles []string
}

func (r *recordingOrchestrator) Orchestrate(_ context.Context, input *coding.DispatchInput) (*coding.DispatchResult, error) {
	r.mu.Lock()
	r.roles = append(r.roles, input.RoleName)
	r.mu.Unlock()
	return &coding.DispatchResult{ExitCode: 0}, nil
}

func (r *recordingOrchestrator) dispatched() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.roles))
	copy(out, r.roles)
	return out
}

func TestBuildFromPRDWorkflow_StageRegistry_OverrideUsed(t *testing.T) {
	// When a StageRegistry overrides "phase-review", the overridden roles must
	// be dispatched by PhaseExecutor instead of the hardcoded defaults.
	path := writeManifest(t, testManifestYAML)

	// Build a policy that overrides phase-review to a single custom role.
	policy := &core.Policy{
		WorkflowOverrides: map[string]map[string][]string{
			"build-from-prd": {
				"phase-review": {"security-auditor"},
			},
		},
	}

	registry := stages.NewStageRegistry(
		stages.WorkflowBuildFromPRD,
		stages.DefaultStages,
		policy,
	)

	rec := &recordingOrchestrator{}
	db := openTestDB(t)
	exec := &phaseloop.PhaseExecutor{
		Dispatcher: rec,
		EventStore: store.NewEventStore(db),
	}

	w := workflow.NewBuildFromPRDWorkflow(path, nil)
	w.PhaseExecutor = exec
	w.StageRegistry = registry

	plan := manifest.PlanEntry{ID: 200, Title: "Registry override test", Phases: []string{"build"}}
	_, err := w.RunPhasesForPlan(context.Background(), "run-registry", plan, map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("RunPhasesForPlan: %v", err)
	}

	dispatched := rec.dispatched()

	// "security-auditor" must appear (the overridden role).
	found := false
	for _, r := range dispatched {
		if r == "security-auditor" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected security-auditor to be dispatched; got roles: %v", dispatched)
	}

	// Default reviewer roles must NOT appear (they were replaced, not appended).
	for _, r := range dispatched {
		if r == "reviewer.arch" || r == "reviewer.frontend" {
			t.Errorf("default reviewer role %q dispatched despite registry override; got roles: %v", r, dispatched)
		}
	}
}
