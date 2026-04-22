package store

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func createTestRun(t *testing.T, rs *RunStore, ctx context.Context, runID string) {
	t.Helper()
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

func TestCreateJob(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_j1")

	job := &core.Job{
		ID:           "job1",
		RunID:        "run_j1",
		Role:         "reviewer.arch",
		State:        core.JobStatePending,
		DispatchedBy: "user",
		CLI:          "codex",
		StartedAt:    time.Now(),
	}

	if err := js.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	got, err := js.GetJob(ctx, "job1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Role != "reviewer.arch" {
		t.Errorf("job role = %q, want %q", got.Role, "reviewer.arch")
	}
	if got.State != core.JobStatePending {
		t.Errorf("job state = %q, want %q", got.State, core.JobStatePending)
	}
	if got.CLI != "codex" {
		t.Errorf("job CLI = %q, want %q", got.CLI, "codex")
	}

	// Verify events: run.created + job.created.
	events, err := es.ListEvents(ctx, "run_j1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[1].Kind != core.EventJobCreated {
		t.Errorf("event kind = %q, want %q", events[1].Kind, core.EventJobCreated)
	}
}

func TestUpdateJobState_Complete(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_j2")

	job := &core.Job{
		ID:           "job2",
		RunID:        "run_j2",
		Role:         "reviewer.arch",
		State:        core.JobStatePending,
		DispatchedBy: "scheduler",
		CLI:          "codex",
		StartedAt:    time.Now(),
	}
	if err := js.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	if err := js.UpdateJobState(ctx, "job2", core.JobStateComplete); err != nil {
		t.Fatalf("UpdateJobState: %v", err)
	}

	got, err := js.GetJob(ctx, "job2")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.State != core.JobStateComplete {
		t.Errorf("job state = %q, want %q", got.State, core.JobStateComplete)
	}
	if got.EndedAt == nil {
		t.Error("job ended_at should be set for complete state")
	}

	// Verify events: run.created + job.created + job.completed.
	events, err := es.ListEvents(ctx, "run_j2")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[2].Kind != core.EventJobCompleted {
		t.Errorf("event kind = %q, want %q", events[2].Kind, core.EventJobCompleted)
	}
}

func TestUpdateJobState_Failed(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_j3")

	job := &core.Job{
		ID:           "job3",
		RunID:        "run_j3",
		Role:         "reviewer.arch",
		State:        core.JobStatePending,
		DispatchedBy: "scheduler",
		CLI:          "codex",
		StartedAt:    time.Now(),
	}
	if err := js.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	if err := js.UpdateJobState(ctx, "job3", core.JobStateFailed); err != nil {
		t.Fatalf("UpdateJobState: %v", err)
	}

	got, err := js.GetJob(ctx, "job3")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.State != core.JobStateFailed {
		t.Errorf("job state = %q, want %q", got.State, core.JobStateFailed)
	}

	events, err := es.ListEvents(ctx, "run_j3")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if events[2].Kind != core.EventJobFailed {
		t.Errorf("event kind = %q, want %q", events[2].Kind, core.EventJobFailed)
	}
}

func TestGetJob_NotFound(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	js := NewJobStore(db, es)
	ctx := context.Background()

	_, err := js.GetJob(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent job, got nil")
	}
}
