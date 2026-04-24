// Package integration contains integration tests that exercise the full
// dispatch pipeline with mock CLI binaries.
package integration

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// tests/integration/ is two levels below the repo root.
	return filepath.Dir(filepath.Dir(wd))
}

func TestInvokeReviewerArch_EndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex")
	if _, err := os.Stat(mockBin); err != nil {
		t.Fatalf("mock binary not found: %v", err)
	}

	// Use temp directory for database.
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open db: %v", err)
	}
	defer db.Close()

	d := &coding.Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     agent.NewCliAgent(mockBin),
		DB:        db,
	}

	ctx := context.Background()
	result, err := d.Orchestrate(ctx, &coding.DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": filepath.Join(repoRoot, "go.mod"),
			"spec_path": filepath.Join(repoRoot, "CLAUDE.md"),
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	// Verify results.
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

	// Verify finding 1.
	f1 := result.Findings[0]
	if f1.Path != "main.go" {
		t.Errorf("finding[0].path = %q, want %q", f1.Path, "main.go")
	}
	if f1.Line != 42 {
		t.Errorf("finding[0].line = %d, want 42", f1.Line)
	}
	if f1.Severity != core.SeverityImportant {
		t.Errorf("finding[0].severity = %q, want %q", f1.Severity, core.SeverityImportant)
	}

	// Verify finding 2.
	f2 := result.Findings[1]
	if f2.Path != "store.go" {
		t.Errorf("finding[1].path = %q, want %q", f2.Path, "store.go")
	}

	// Verify findings are persisted in DB with fingerprints.
	fs := store.NewFindingStore(db, store.NewEventStore(db))
	persisted, err := fs.ListFindings(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(persisted) != 2 {
		t.Errorf("persisted findings = %d, want 2", len(persisted))
	}
	for i, f := range persisted {
		if f.Fingerprint == "" {
			t.Errorf("persisted finding[%d] has empty fingerprint", i)
		}
	}

	// Verify run state.
	rs := store.NewRunStore(db, store.NewEventStore(db))
	run, err := rs.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.State != core.RunStateCompleted {
		t.Errorf("run state = %q, want %q", run.State, core.RunStateCompleted)
	}
	if run.EndedAt == nil {
		t.Error("run ended_at should be set")
	}

	// Verify job state.
	js := store.NewJobStore(db, store.NewEventStore(db))
	job, err := js.GetJob(ctx, result.JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.State != core.JobStateComplete {
		t.Errorf("job state = %q, want %q", job.State, core.JobStateComplete)
	}
	if job.Role != "reviewer.arch" {
		t.Errorf("job role = %q, want %q", job.Role, "reviewer.arch")
	}

	// Verify the durable event log against the normalized golden snapshot.
	goldenFile := filepath.Join(repoRoot, "testdata", "events", "invoke_reviewer_arch.golden.json")
	store.AssertGoldenEvents(t, db, result.RunID, goldenFile)
}
