package store

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func TestInsertArtifact(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)
	as := NewArtifactStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_a1")

	job := &core.Job{
		ID:           "job_a1",
		RunID:        "run_a1",
		Role:         "reviewer.arch",
		State:        core.JobStatePending,
		DispatchedBy: "scheduler",
		CLI:          "codex",
		StartedAt:    time.Now(),
	}
	if err := js.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	artifact := &core.Artifact{
		ID:    "art1",
		JobID: "job_a1",
		Kind:  "log",
		Path:  ".coworker/runs/run_a1/jobs/job_a1.jsonl",
	}

	if err := as.InsertArtifact(ctx, artifact, "run_a1"); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	// Verify artifact was persisted.
	var path string
	err := db.QueryRow("SELECT path FROM artifacts WHERE id = 'art1'").Scan(&path)
	if err != nil {
		t.Fatalf("query artifact: %v", err)
	}
	if path != ".coworker/runs/run_a1/jobs/job_a1.jsonl" {
		t.Errorf("artifact path = %q, want %q", path, ".coworker/runs/run_a1/jobs/job_a1.jsonl")
	}

	// Verify event was written.
	events, err := es.ListEvents(ctx, "run_a1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	foundArtifactEvent := false
	for _, e := range events {
		if e.Kind == core.EventArtifactCreated {
			foundArtifactEvent = true
		}
	}
	if !foundArtifactEvent {
		t.Error("no artifact.created event found")
	}
}
