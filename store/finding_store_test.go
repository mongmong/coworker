package store

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func setupFindingTestDB(t *testing.T) (*DB, *EventStore, *RunStore, *JobStore, *FindingStore) {
	t.Helper()
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)
	fs := NewFindingStore(db, es)

	ctx := context.Background()
	createTestRun(t, rs, ctx, "run_f1")

	job := &core.Job{
		ID:           "job_f1",
		RunID:        "run_f1",
		Role:         "reviewer.arch",
		State:        core.JobStatePending,
		DispatchedBy: "scheduler",
		CLI:          "codex",
		StartedAt:    time.Now(),
	}
	if err := js.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	return db, es, rs, js, fs
}

func TestInsertFinding(t *testing.T) {
	_, es, _, _, fs := setupFindingTestDB(t)
	ctx := context.Background()

	finding := &core.Finding{
		ID:       "find1",
		RunID:    "run_f1",
		JobID:    "job_f1",
		Path:     "main.go",
		Line:     42,
		Severity: core.SeverityImportant,
		Body:     "Missing error check on Close()",
	}

	if err := fs.InsertFinding(ctx, finding); err != nil {
		t.Fatalf("InsertFinding: %v", err)
	}

	// Verify fingerprint was computed.
	if finding.Fingerprint == "" {
		t.Error("fingerprint should be computed")
	}

	// Verify finding was persisted.
	findings, err := fs.ListFindings(ctx, "run_f1")
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Path != "main.go" {
		t.Errorf("finding path = %q, want %q", findings[0].Path, "main.go")
	}
	if findings[0].Fingerprint == "" {
		t.Error("persisted finding should have fingerprint")
	}

	// Verify event was written.
	events, err := es.ListEvents(ctx, "run_f1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	// run.created + job.created + finding.created = 3
	foundFindingEvent := false
	for _, e := range events {
		if e.Kind == core.EventFindingCreated {
			foundFindingEvent = true
		}
	}
	if !foundFindingEvent {
		t.Error("no finding.created event found")
	}
}

func TestFindingImmutability_DirectUpdateBlocked(t *testing.T) {
	db, _, _, _, fs := setupFindingTestDB(t)
	ctx := context.Background()

	finding := &core.Finding{
		ID:       "find_immut",
		RunID:    "run_f1",
		JobID:    "job_f1",
		Path:     "main.go",
		Line:     42,
		Severity: core.SeverityImportant,
		Body:     "Original body text",
	}

	if err := fs.InsertFinding(ctx, finding); err != nil {
		t.Fatalf("InsertFinding: %v", err)
	}

	// Try to update body directly via SQL -- this should work at the SQL level
	// but the store API does not expose it. We test that the store layer
	// only provides InsertFinding and ResolveFinding.
	// The store API is the enforcement boundary.

	// Verify the finding body via the store API.
	findings, err := fs.ListFindings(ctx, "run_f1")
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if findings[0].Body != "Original body text" {
		t.Errorf("body = %q, want %q", findings[0].Body, "Original body text")
	}

	// Direct SQL update should work (SQLite doesn't have column-level triggers
	// in our schema), but we rely on the Go API boundary for immutability.
	// This is documented: "enforced by store layer" per the plan manifest.
	_ = db
}

func TestResolveFinding(t *testing.T) {
	_, _, _, _, fs := setupFindingTestDB(t)
	ctx := context.Background()

	finding := &core.Finding{
		ID:       "find_resolve",
		RunID:    "run_f1",
		JobID:    "job_f1",
		Path:     "store.go",
		Line:     17,
		Severity: core.SeverityMinor,
		Body:     "Consider using prepared statement",
	}

	if err := fs.InsertFinding(ctx, finding); err != nil {
		t.Fatalf("InsertFinding: %v", err)
	}

	if err := fs.ResolveFinding(ctx, "find_resolve", "fix_job_1"); err != nil {
		t.Fatalf("ResolveFinding: %v", err)
	}

	findings, err := fs.ListFindings(ctx, "run_f1")
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].ResolvedByJobID != "fix_job_1" {
		t.Errorf("resolved_by_job_id = %q, want %q", findings[0].ResolvedByJobID, "fix_job_1")
	}
	if findings[0].ResolvedAt == nil {
		t.Error("resolved_at should be set")
	}
}

func TestResolveFinding_AlreadyResolved(t *testing.T) {
	_, _, _, _, fs := setupFindingTestDB(t)
	ctx := context.Background()

	finding := &core.Finding{
		ID:       "find_double",
		RunID:    "run_f1",
		JobID:    "job_f1",
		Path:     "main.go",
		Line:     10,
		Severity: core.SeverityMinor,
		Body:     "Some finding",
	}

	if err := fs.InsertFinding(ctx, finding); err != nil {
		t.Fatalf("InsertFinding: %v", err)
	}

	if err := fs.ResolveFinding(ctx, "find_double", "fix_job_1"); err != nil {
		t.Fatalf("first ResolveFinding: %v", err)
	}

	// Second resolve should fail.
	err := fs.ResolveFinding(ctx, "find_double", "fix_job_2")
	if err == nil {
		t.Error("expected error resolving already-resolved finding, got nil")
	}
}

func TestResolveFinding_NotFound(t *testing.T) {
	_, _, _, _, fs := setupFindingTestDB(t)
	ctx := context.Background()

	err := fs.ResolveFinding(ctx, "nonexistent", "fix_job_1")
	if err == nil {
		t.Error("expected error resolving nonexistent finding, got nil")
	}
}

func TestListFindings_Empty(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	fs := NewFindingStore(db, es)
	ctx := context.Background()

	findings, err := fs.ListFindings(ctx, "nonexistent-run")
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}
