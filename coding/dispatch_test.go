package coding

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// coding/ is one level below the repo root.
	return filepath.Dir(wd)
}

func TestOrchestrate_WithMockCodex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex")
	if _, err := os.Stat(mockBin); err != nil {
		t.Fatalf("mock binary not found: %v", err)
	}

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	d := &Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     agent.NewCliAgent(mockBin),
		DB:        db,
	}

	ctx := context.Background()
	result, err := d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	if result.RunID == "" {
		t.Error("run ID should not be empty")
	}
	if result.JobID == "" {
		t.Error("job ID should not be empty")
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}
	if len(result.Findings) != 2 {
		t.Fatalf("findings count = %d, want 2", len(result.Findings))
	}

	// Verify findings were persisted.
	findingStore := store.NewFindingStore(db, store.NewEventStore(db))
	findings, err := findingStore.ListFindings(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(findings) != 2 {
		t.Errorf("persisted findings = %d, want 2", len(findings))
	}

	// Verify run was completed.
	runStore := store.NewRunStore(db, store.NewEventStore(db))
	run, err := runStore.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.State != core.RunStateCompleted {
		t.Errorf("run state = %q, want %q", run.State, core.RunStateCompleted)
	}

	// Verify job was completed.
	jobStore := store.NewJobStore(db, store.NewEventStore(db))
	job, err := jobStore.GetJob(ctx, result.JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.State != core.JobStateComplete {
		t.Errorf("job state = %q, want %q", job.State, core.JobStateComplete)
	}

	// Verify event log.
	events, err := store.NewEventStore(db).ListEvents(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	// Expected events: run.created, job.created, job.leased (dispatched),
	// finding.created x2, job.completed, run.completed = 7
	if len(events) != 7 {
		t.Errorf("event count = %d, want 7", len(events))
		for i, e := range events {
			t.Logf("  event[%d]: seq=%d kind=%s", i, e.Sequence, e.Kind)
		}
	}
}

func TestOrchestrate_MissingRequiredInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex")

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	d := &Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     agent.NewCliAgent(mockBin),
		DB:        db,
	}

	ctx := context.Background()
	_, err = d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			// Missing spec_path.
		},
	})
	if err == nil {
		t.Error("expected error for missing required input, got nil")
	}
}

func TestOrchestrate_InvalidRole(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	d := &Dispatcher{
		RoleDir:   "/nonexistent",
		PromptDir: "/nonexistent",
		Agent:     agent.NewCliAgent("/bin/true"),
		DB:        db,
	}

	ctx := context.Background()
	_, err = d.Orchestrate(ctx, &DispatchInput{
		RoleName: "nonexistent.role",
		Inputs:   map[string]string{},
	})
	if err == nil {
		t.Error("expected error for invalid role, got nil")
	}
}

func TestSnakeToPascal(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"diff_path", "DiffPath"},
		{"spec_path", "SpecPath"},
		{"simple", "Simple"},
		{"a_b_c", "ABC"},
	}
	for _, tt := range tests {
		got := snakeToPascal(tt.input)
		if got != tt.want {
			t.Errorf("snakeToPascal(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
