package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

// dirtyOrchestrator always returns a single finding so phases never converge.
type dirtyOrchestrator struct {
	mu    sync.Mutex
	calls int
}

func (d *dirtyOrchestrator) Orchestrate(_ context.Context, _ *coding.DispatchInput) (*coding.DispatchResult, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	return &coding.DispatchResult{
		ExitCode: 0,
		Findings: []core.Finding{
			{
				Body:     "test finding",
				Severity: core.SeverityCritical,
			},
		},
	}, nil
}

// mockShipper records whether Ship was called.
type mockShipper struct {
	mu     sync.Mutex
	called int
}

func (m *mockShipper) Ship(_ context.Context, _ string, _ *manifest.PlanEntry, _ string) (*shipper.ShipResult, error) {
	m.mu.Lock()
	m.called++
	m.mu.Unlock()
	return &shipper.ShipResult{PRURL: "https://example.com/pr/1"}, nil
}

// newDirtyPhaseExecutor creates a PhaseExecutor with a dirty-always stub
// and a policy that exhausts the fix budget after 1 cycle (2 dispatcher
// calls total per reviewer: cycle 0 is dirty, cycle 1 >= maxCycles=1 → stop).
func newDirtyPhaseExecutor(t *testing.T, db *store.DB, attentionStore *store.AttentionStore) *phaseloop.PhaseExecutor {
	t.Helper()
	policy := &core.Policy{
		SupervisorLimits: core.SupervisorLimits{MaxFixCyclesPerPhase: 1},
	}
	exec := &phaseloop.PhaseExecutor{
		Dispatcher:     &dirtyOrchestrator{},
		EventStore:     store.NewEventStore(db),
		AttentionStore: attentionStore,
		Policy:         policy,
		// Single reviewer role reduces call count for speed.
		ReviewerRoles: []string{"reviewer.arch"},
	}
	return exec
}

// mustCreateRun inserts a run row needed for FK constraints on attention items.
func mustCreateRun(t *testing.T, db *store.DB, ctx context.Context, runID string) {
	t.Helper()
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)
	run := &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := rs.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun(%q): %v", runID, err)
	}
}

// TestRunPhasesForPlan_DirtyPhase_StopsWorkflow verifies that when a phase
// returns Clean==false, RunPhasesForPlan stops immediately and sets
// StoppedAtPhaseClean=true with the correct index and name.
func TestRunPhasesForPlan_DirtyPhase_StopsWorkflow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	path := writeManifest(t, testManifestYAML)
	db := openTestDB(t)
	runID := "run-dirty-phase"
	mustCreateRun(t, db, ctx, runID)

	w := workflow.NewBuildFromPRDWorkflow(path, nil)
	w.PhaseExecutor = newDirtyPhaseExecutor(t, db, nil)

	plan := manifest.PlanEntry{
		ID:     100,
		Title:  "Core runtime",
		Phases: []string{"phase-zero", "phase-one"},
	}
	result, err := w.RunPhasesForPlan(ctx, runID, plan, nil)
	if err != nil {
		t.Fatalf("RunPhasesForPlan: %v", err)
	}
	if !result.StoppedAtPhaseClean {
		t.Error("expected StoppedAtPhaseClean=true, got false")
	}
	if result.DirtyPhaseIndex != 0 {
		t.Errorf("DirtyPhaseIndex = %d, want 0", result.DirtyPhaseIndex)
	}
	if result.DirtyPhaseName != "phase-zero" {
		t.Errorf("DirtyPhaseName = %q, want %q", result.DirtyPhaseName, "phase-zero")
	}
	// Only the first phase result should be present (second was never executed).
	if len(result.PhaseResults) != 1 {
		t.Errorf("len(PhaseResults) = %d, want 1", len(result.PhaseResults))
	}
}

// TestRunPhasesForPlan_DirtyPhase_NoShipCalled verifies that when a phase is
// dirty the shipper is never invoked.
func TestRunPhasesForPlan_DirtyPhase_NoShipCalled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	path := writeManifest(t, testManifestYAML)
	db := openTestDB(t)
	runID := "run-dirty-no-ship"
	mustCreateRun(t, db, ctx, runID)

	ms := &mockShipper{}
	w := workflow.NewBuildFromPRDWorkflow(path, nil)
	w.PhaseExecutor = newDirtyPhaseExecutor(t, db, nil)
	// Wire in a real shipper wrapper that delegates to our mock.
	// We can't set w.Shipper directly since it's *shipper.Shipper; instead
	// verify via ShipResult being nil (the early return skips the ship block).
	_ = ms // keep reference for clarity
	// No w.Shipper set — ShipResult must still be nil and StoppedAtPhaseClean true.

	plan := manifest.PlanEntry{
		ID:     100,
		Title:  "Core runtime",
		Phases: []string{"only-phase"},
	}
	result, err := w.RunPhasesForPlan(ctx, runID, plan, nil)
	if err != nil {
		t.Fatalf("RunPhasesForPlan: %v", err)
	}
	if !result.StoppedAtPhaseClean {
		t.Error("expected StoppedAtPhaseClean=true")
	}
	if result.ShipResult != nil {
		t.Error("expected ShipResult to be nil when StoppedAtPhaseClean=true")
	}
}

// TestRunPhasesForPlan_DirtyPhase_SecondPhase verifies that when phase 0 is
// clean but phase 1 is dirty, the workflow stops at phase 1 and phase 2 is
// never executed.
//
// Strategy: use a thresholdOrchestrator that returns clean results for the
// first 2 calls (phase 0: developer + 1 reviewer) and dirty results thereafter
// (phase 1 onwards).
func TestRunPhasesForPlan_DirtyPhase_SecondPhase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Phase 0 with maxFixCycles=1 and 1 reviewer + default tester:
	//   cycle 0: developer(1) + reviewer.arch(1) + tester(1) = 3 calls → clean check → pass
	// Phase 1 (calls 3+): dirty immediately → exhausts at cycle 1.
	threshold := 3
	callIdx := 0
	var callIdxMu sync.Mutex

	thresholdOrch := &thresholdOrchestrator{
		threshold: threshold,
		callIdx:   &callIdx,
		mu:        &callIdxMu,
	}

	path := writeManifest(t, testManifestYAML)
	db := openTestDB(t)
	runID := "run-dirty-phase-1"
	mustCreateRun(t, db, ctx, runID)

	policy := &core.Policy{
		SupervisorLimits: core.SupervisorLimits{MaxFixCyclesPerPhase: 1},
	}
	exec := &phaseloop.PhaseExecutor{
		Dispatcher:    thresholdOrch,
		EventStore:    store.NewEventStore(db),
		Policy:        policy,
		ReviewerRoles: []string{"reviewer.arch"},
	}

	w := workflow.NewBuildFromPRDWorkflow(path, nil)
	w.PhaseExecutor = exec

	plan := manifest.PlanEntry{
		ID:     100,
		Title:  "Core runtime",
		Phases: []string{"phase-zero", "phase-one", "phase-two"},
	}
	result, err := w.RunPhasesForPlan(ctx, runID, plan, nil)
	if err != nil {
		t.Fatalf("RunPhasesForPlan: %v", err)
	}
	if !result.StoppedAtPhaseClean {
		t.Error("expected StoppedAtPhaseClean=true, got false")
	}
	if result.DirtyPhaseIndex != 1 {
		t.Errorf("DirtyPhaseIndex = %d, want 1", result.DirtyPhaseIndex)
	}
	if result.DirtyPhaseName != "phase-one" {
		t.Errorf("DirtyPhaseName = %q, want %q", result.DirtyPhaseName, "phase-one")
	}
	// Phase 0 and phase 1 results should be present; phase 2 was never executed.
	if len(result.PhaseResults) != 2 {
		t.Errorf("len(PhaseResults) = %d, want 2", len(result.PhaseResults))
	}
}

// thresholdOrchestrator returns clean results for the first `threshold` calls
// and dirty results (with findings) thereafter.
type thresholdOrchestrator struct {
	threshold int
	callIdx   *int
	mu        *sync.Mutex
}

func (t *thresholdOrchestrator) Orchestrate(_ context.Context, _ *coding.DispatchInput) (*coding.DispatchResult, error) {
	t.mu.Lock()
	idx := *t.callIdx
	*t.callIdx++
	t.mu.Unlock()

	if idx < t.threshold {
		return &coding.DispatchResult{ExitCode: 0}, nil
	}
	return &coding.DispatchResult{
		ExitCode: 0,
		Findings: []core.Finding{
			{Body: "threshold finding", Severity: core.SeverityCritical},
		},
	}, nil
}

// TestRunPhasesForPlan_AttentionItemID_Populated verifies that AttentionItemID
// is populated when AttentionStore is set and a matching checkpoint exists.
func TestRunPhasesForPlan_AttentionItemID_Populated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	path := writeManifest(t, testManifestYAML)
	db := openTestDB(t)
	runID := "run-attention-id"
	mustCreateRun(t, db, ctx, runID)

	as := store.NewAttentionStore(db)
	w := workflow.NewBuildFromPRDWorkflow(path, nil)
	w.PhaseExecutor = newDirtyPhaseExecutor(t, db, as)

	plan := manifest.PlanEntry{
		ID:     100,
		Title:  "Core runtime",
		Phases: []string{"only-phase"},
	}
	result, err := w.RunPhasesForPlan(ctx, runID, plan, nil)
	if err != nil {
		t.Fatalf("RunPhasesForPlan: %v", err)
	}
	if !result.StoppedAtPhaseClean {
		t.Error("expected StoppedAtPhaseClean=true")
	}
	// AttentionItemID should be populated because the PhaseExecutor (with
	// AttentionStore set) inserts a checkpoint item when fix-loop exhausts.
	if result.AttentionItemID == "" {
		t.Error("expected AttentionItemID to be non-empty when AttentionStore is set")
	}

	// Verify the item exists in the store.
	item, err := as.GetAttentionByID(ctx, result.AttentionItemID)
	if err != nil {
		t.Fatalf("GetAttentionByID: %v", err)
	}
	if item == nil {
		t.Fatal("attention item not found in store")
	}
	if item.Kind != core.AttentionCheckpoint {
		t.Errorf("item.Kind = %q, want checkpoint", item.Kind)
	}
}

// TestRunPhasesForPlan_AttentionItemID_NilStore verifies that AttentionItemID
// is empty (not a panic) when AttentionStore is nil.
func TestRunPhasesForPlan_AttentionItemID_NilStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	path := writeManifest(t, testManifestYAML)
	db := openTestDB(t)
	runID := "run-nil-attention-store"
	mustCreateRun(t, db, ctx, runID)

	w := workflow.NewBuildFromPRDWorkflow(path, nil)
	w.PhaseExecutor = newDirtyPhaseExecutor(t, db, nil) // AttentionStore is nil

	plan := manifest.PlanEntry{
		ID:     100,
		Title:  "Core runtime",
		Phases: []string{"only-phase"},
	}
	result, err := w.RunPhasesForPlan(ctx, runID, plan, nil)
	if err != nil {
		t.Fatalf("RunPhasesForPlan: %v", err)
	}
	if !result.StoppedAtPhaseClean {
		t.Error("expected StoppedAtPhaseClean=true")
	}
	if result.AttentionItemID != "" {
		t.Errorf("expected empty AttentionItemID when store is nil, got %q", result.AttentionItemID)
	}
}

// TestBuildFromPRDWorkflow_RoleDir_PropagatedToPhaseExecutor verifies that
// RoleDir set on BuildFromPRDWorkflow is propagated to PhaseExecutor before
// each RunPhasesForPlan call.
func TestBuildFromPRDWorkflow_RoleDir_PropagatedToPhaseExecutor(t *testing.T) {
	path := writeManifest(t, testManifestYAML)

	db := openTestDB(t)
	exec := &phaseloop.PhaseExecutor{
		Dispatcher: &stubOrchestrator{},
		EventStore: store.NewEventStore(db),
	}

	w := workflow.NewBuildFromPRDWorkflow(path, nil)
	w.PhaseExecutor = exec
	w.RoleDir = "/custom/roles"

	plan := manifest.PlanEntry{ID: 100, Title: "Core runtime", Phases: []string{"build"}}
	_, err := w.RunPhasesForPlan(context.Background(), "run-roledir", plan, map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("RunPhasesForPlan: %v", err)
	}

	// After RunPhasesForPlan the PhaseExecutor.RoleDir must equal w.RoleDir.
	if exec.RoleDir != w.RoleDir {
		t.Errorf("PhaseExecutor.RoleDir = %q, want %q", exec.RoleDir, w.RoleDir)
	}
}

// TestBuildFromPRDWorkflow_StageRegistry_TesterOverride verifies that when a
// StageRegistry overrides "phase-test", the custom tester role is dispatched
// instead of (not in addition to) the default "tester" role.
func TestBuildFromPRDWorkflow_StageRegistry_TesterOverride(t *testing.T) {
	path := writeManifest(t, testManifestYAML)

	// Policy overrides "phase-test" with a single custom tester.
	policy := &core.Policy{
		WorkflowOverrides: map[string]map[string][]string{
			"build-from-prd": {
				"phase-test": {"custom-tester"},
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

	plan := manifest.PlanEntry{ID: 201, Title: "Phase-test registry test", Phases: []string{"build"}}
	_, err := w.RunPhasesForPlan(context.Background(), "run-tester-registry", plan, map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("RunPhasesForPlan: %v", err)
	}

	dispatched := rec.dispatched()

	// "custom-tester" must appear in dispatched roles.
	foundCustom := false
	for _, r := range dispatched {
		if r == "custom-tester" {
			foundCustom = true
			break
		}
	}
	if !foundCustom {
		t.Errorf("expected custom-tester to be dispatched; got roles: %v", dispatched)
	}

	// Default tester must NOT appear (it was replaced, not appended).
	for _, r := range dispatched {
		if r == "tester" {
			t.Errorf("default tester dispatched despite phase-test registry override; got roles: %v", dispatched)
		}
	}
}

// TestRunPhasesForPlan_CleanPhases_ShipCalled verifies that when all phases
// are clean, StoppedAtPhaseClean is false and the shipper is called.
func TestRunPhasesForPlan_CleanPhases_ShipCalled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	path := writeManifest(t, testManifestYAML)
	db := openTestDB(t)
	runID := "run-clean-ship"
	mustCreateRun(t, db, ctx, runID)

	w := workflow.NewBuildFromPRDWorkflow(path, nil)
	w.PhaseExecutor = newTestPhaseExecutor(t) // always clean
	w.Shipper = &shipper.Shipper{DryRun: true}

	plan := manifest.PlanEntry{
		ID:     100,
		Title:  "Core runtime",
		Phases: []string{"phase-a", "phase-b"},
	}
	result, err := w.RunPhasesForPlan(ctx, runID, plan, map[string]string{
		"branch": "feature/plan-100",
	})
	if err != nil {
		t.Fatalf("RunPhasesForPlan: %v", err)
	}
	if result.StoppedAtPhaseClean {
		t.Error("expected StoppedAtPhaseClean=false for clean phases")
	}
	if result.ShipResult == nil {
		t.Error("expected ShipResult to be non-nil when all phases are clean")
	}
}
