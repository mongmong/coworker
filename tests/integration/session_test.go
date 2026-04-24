package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding/roles"
	"github.com/chris/coworker/coding/session"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

func TestSessionLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRootForIntegration(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex")
	if _, err := os.Stat(mockBin); err != nil {
		t.Fatalf("mock binary not found: %v", err)
	}

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "session.lock")
	dbPath := filepath.Join(tmpDir, "state.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open db: %v", err)
	}
	defer db.Close()

	eventStore := store.NewEventStore(db)
	runStore := store.NewRunStore(db, eventStore)
	jobStore := store.NewJobStore(db, eventStore)
	sm := &session.Manager{
		RunStore: runStore,
		LockPath: lockPath,
	}

	runID, err := sm.StartSession()
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	if runID == "" {
		t.Fatal("expected non-empty run ID")
	}

	if got, err := sm.CurrentSession(); err != nil {
		t.Fatalf("CurrentSession() error after start = %v", err)
	} else if got != runID {
		t.Errorf("CurrentSession() = %q, want %q", got, runID)
	}

	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("session lock should exist: %v", err)
	}

	// Invoke a role in the active session run.
	role, err := roles.LoadRole(filepath.Join(repoRoot, "coding", "roles"), "reviewer.arch")
	if err != nil {
		t.Fatalf("LoadRole(): %v", err)
	}

	tmpl, err := roles.LoadPromptTemplate(filepath.Join(repoRoot, "coding"), role.PromptTemplate)
	if err != nil {
		t.Fatalf("LoadPromptTemplate(): %v", err)
	}

	prompt, err := roles.RenderPrompt(tmpl, map[string]string{
		"DiffPath": filepath.Join(repoRoot, "go.mod"),
		"SpecPath": filepath.Join(repoRoot, "CLAUDE.md"),
	})
	if err != nil {
		t.Fatalf("RenderPrompt(): %v", err)
	}

	job := &core.Job{
		ID:           core.NewID(),
		RunID:        runID,
		Role:         role.Name,
		State:        core.JobStatePending,
		DispatchedBy: "user",
		CLI:          role.CLI,
		StartedAt:    time.Now(),
	}
	if err := jobStore.CreateJob(context.Background(), job); err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}

	if err := jobStore.UpdateJobState(context.Background(), job.ID, core.JobStateDispatched); err != nil {
		t.Fatalf("UpdateJobState(dispatched) error = %v", err)
	}

	handle, err := agent.NewCliAgent(mockBin).Dispatch(context.Background(), job, prompt)
	if err != nil {
		t.Fatalf("agent dispatch: %v", err)
	}

	result, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatalf("agent wait: %v", err)
	}

	finalState := core.JobStateFailed
	if result.ExitCode == 0 {
		finalState = core.JobStateComplete
	}
	if err := jobStore.UpdateJobState(context.Background(), job.ID, finalState); err != nil {
		t.Fatalf("UpdateJobState(%s) error = %v", finalState, err)
	}

	retrievedRun, err := runStore.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if retrievedRun.State != core.RunStateActive {
		t.Fatalf("run state = %q, want %q", retrievedRun.State, core.RunStateActive)
	}

	retrievedJob, err := jobStore.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if got, want := retrievedJob.RunID, runID; got != want {
		t.Fatalf("job run_id = %q, want %q", got, want)
	}

	events, err := eventStore.ListEvents(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected events for active session run")
	}

	if err := sm.EndSession(); err != nil {
		t.Fatalf("EndSession() error = %v", err)
	}

	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("expected session lock removed, err = %v", err)
	}

	endedRun, err := runStore.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun() after end error = %v", err)
	}
	if endedRun.State != core.RunStateCompleted {
		t.Fatalf("run state after end = %q, want %q", endedRun.State, core.RunStateCompleted)
	}

	if _, err := sm.CurrentSession(); !errors.Is(err, session.ErrNoActiveSession) {
		t.Fatalf("CurrentSession() after end = %v, want %q", err, session.ErrNoActiveSession)
	}
}

func findRepoRootForIntegration(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// tests/integration is two levels below repo root.
	return filepath.Dir(filepath.Dir(wd))
}
