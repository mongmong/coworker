package store

import (
	"context"
	"strings"
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

func TestFindingsImmutableTrigger_AllImmutableColumns(t *testing.T) {
	// Table-driven test: each protected column must be rejected by the trigger.
	tests := []struct {
		name  string
		query string
	}{
		{"path", "UPDATE findings SET path = 'tampered' WHERE id = 'find_immut'"},
		{"line", "UPDATE findings SET line = 999 WHERE id = 'find_immut'"},
		{"severity", "UPDATE findings SET severity = 'minor' WHERE id = 'find_immut'"},
		{"body", "UPDATE findings SET body = 'tampered body' WHERE id = 'find_immut'"},
		{"fingerprint", "UPDATE findings SET fingerprint = 'tampered-fp' WHERE id = 'find_immut'"},
		{"run_id", "UPDATE findings SET run_id = 'run_other' WHERE id = 'find_immut'"},
		{"job_id", "UPDATE findings SET job_id = 'job_other' WHERE id = 'find_immut'"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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

			_, err := db.ExecContext(ctx, tc.query)
			if err == nil {
				t.Fatalf("expected error updating immutable column %q, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), "immutable") && !strings.Contains(err.Error(), "resolved_by_job_id") {
				t.Errorf("expected 'immutable' in error, got: %v", err)
			}
		})
	}
}

func TestFindingsImmutableTrigger_ResolveAllowed(t *testing.T) {
	// ResolveFinding (which only updates resolved_by_job_id + resolved_at) must not be blocked.
	_, _, _, _, fs := setupFindingTestDB(t)
	ctx := context.Background()

	finding := &core.Finding{
		ID:       "find_resolve_trigger",
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

	// ResolveFinding must succeed (trigger must not block resolution updates).
	if err := fs.ResolveFinding(ctx, "find_resolve_trigger", "fix_job_trigger"); err != nil {
		t.Fatalf("ResolveFinding after trigger migration: %v", err)
	}

	findings, err := fs.ListFindings(ctx, "run_f1")
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].ResolvedByJobID != "fix_job_trigger" {
		t.Errorf("resolved_by_job_id = %q, want %q", findings[0].ResolvedByJobID, "fix_job_trigger")
	}
}

func TestFindingImmutability_DirectUpdateBlocked(t *testing.T) {
	// Legacy test kept for documentation: this now tests the SQL trigger path.
	db, _, _, _, fs := setupFindingTestDB(t)
	ctx := context.Background()

	finding := &core.Finding{
		ID:       "find_immut_legacy",
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

	// Direct SQL update on body should now be blocked by the trigger (migration 006).
	_, err := db.ExecContext(ctx, "UPDATE findings SET body = 'tampered' WHERE id = 'find_immut_legacy'")
	if err == nil {
		t.Fatal("expected trigger to block direct body UPDATE, got nil error")
	}
	if !strings.Contains(err.Error(), "immutable") && !strings.Contains(err.Error(), "resolved_by_job_id") {
		t.Errorf("expected 'immutable' in error, got: %v", err)
	}
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
