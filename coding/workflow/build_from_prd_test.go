package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chris/coworker/coding/manifest"
	"github.com/chris/coworker/coding/workflow"
	"github.com/chris/coworker/core"
)

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
