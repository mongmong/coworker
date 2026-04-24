package workflow

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

func TestFreeformWorkflow_Name(t *testing.T) {
	t.Parallel()

	workflow := NewFreeformWorkflow(nil)
	if got, want := workflow.Name(), "freeform"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
}

func TestFreeformWorkflow_Dispatch(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	ctx := context.Background()
	tmpDir := t.TempDir()
	diffPath := filepath.Join(tmpDir, "diff.txt")
	specPath := filepath.Join(tmpDir, "spec.md")
	if err := os.WriteFile(diffPath, []byte("diff content"), 0o644); err != nil {
		t.Fatalf("write diff file: %v", err)
	}
	if err := os.WriteFile(specPath, []byte("spec content"), 0o644); err != nil {
		t.Fatalf("write spec file: %v", err)
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex")
	if _, err := os.Stat(mockBin); err != nil {
		t.Fatalf("mock binary not found: %v", err)
	}

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	dispatcher := &coding.Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     agent.NewCliAgent(mockBin),
		DB:        db,
	}

	workflow := NewFreeformWorkflow(dispatcher)
	jobID, err := workflow.Dispatch(ctx, &core.DispatchInput{
		Role: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": diffPath,
			"spec_path": specPath,
		},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if jobID == "" {
		t.Fatal("jobID should not be empty")
	}

	jobStore := store.NewJobStore(db, store.NewEventStore(db))
	job, err := jobStore.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Role != "reviewer.arch" {
		t.Errorf("job role = %q, want %q", job.Role, "reviewer.arch")
	}
	if job.State != core.JobStateComplete {
		t.Errorf("job state = %q, want %q", job.State, core.JobStateComplete)
	}
}

func TestFreeformWorkflow_Dispatch_MissingDispatcher(t *testing.T) {
	t.Parallel()

	workflow := NewFreeformWorkflow(nil)
	if _, err := workflow.Dispatch(context.Background(), &core.DispatchInput{
		Role: "reviewer.arch",
	}); err == nil {
		t.Fatal("expected error for missing dispatcher")
	}
}

func TestFreeformWorkflow_Dispatch_NilInput(t *testing.T) {
	t.Parallel()

	workflow := NewFreeformWorkflow(nil)
	if _, err := workflow.Dispatch(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil input")
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}
