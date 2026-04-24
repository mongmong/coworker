package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chris/coworker/coding/policy"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

func TestAttentionQueue(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	eventStore := store.NewEventStore(db)
	runStore := store.NewRunStore(db, eventStore)
	jobStore := store.NewJobStore(db, eventStore)
	attentionStore := store.NewAttentionStore(db)

	runID := core.NewID()
	if err := runStore.CreateRun(ctx, &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	job := &core.Job{
		ID:           core.NewID(),
		RunID:        runID,
		Role:         "reviewer.arch",
		State:        core.JobStatePending,
		DispatchedBy: "scheduler",
		CLI:          "codex",
		StartedAt:    time.Now(),
	}
	if err := jobStore.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}
	if err := jobStore.UpdateJobState(ctx, job.ID, core.JobStateDispatched); err != nil {
		t.Fatalf("UpdateJobState(dispatched) error = %v", err)
	}
	if err := jobStore.UpdateJobState(ctx, job.ID, core.JobStateComplete); err != nil {
		t.Fatalf("UpdateJobState(complete) error = %v", err)
	}

	item := &core.AttentionItem{
		RunID:    runID,
		Kind:     core.AttentionPermission,
		Source:   "permission-checker",
		Question: "Can this change be shipped?",
		Options:  []string{"yes", "no"},
		JobID:    job.ID,
	}
	if err := attentionStore.InsertAttention(ctx, item); err != nil {
		t.Fatalf("InsertAttention() error = %v", err)
	}

	pending, err := attentionStore.ListUnansweredByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListUnansweredByRun() error = %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("len(pending) = %d, want 1", len(pending))
	}

	if err := attentionStore.AnswerAttention(ctx, item.ID, "yes", "pipeline"); err != nil {
		t.Fatalf("AnswerAttention() error = %v", err)
	}

	pending, err = attentionStore.ListUnansweredByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListUnansweredByRun() after answer error = %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("len(pending) after answer = %d, want 0", len(pending))
	}

	resolved, err := attentionStore.GetAttentionByID(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetAttentionByID() error = %v", err)
	}
	if resolved.Answer != "yes" {
		t.Errorf("resolved.Answer = %q, want %q", resolved.Answer, "yes")
	}
	if resolved.AnsweredBy != "pipeline" {
		t.Errorf("resolved.AnsweredBy = %q, want %q", resolved.AnsweredBy, "pipeline")
	}

	events, err := eventStore.ListEvents(ctx, runID)
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(events) < 4 {
		t.Fatalf("len(events) = %d, want >=4", len(events))
	}

	hasJobCreated := false
	hasJobCompleted := false
	hasRunCreated := false
	for _, event := range events {
		switch event.Kind {
		case core.EventJobCreated:
			hasJobCreated = true
		case core.EventJobCompleted:
			hasJobCompleted = true
		case core.EventRunCreated:
			hasRunCreated = true
		}
	}
	if !hasRunCreated {
		t.Error("expected run.created event")
	}
	if !hasJobCreated {
		t.Error("expected job.created event")
	}
	if !hasJobCompleted {
		t.Error("expected job.completed event")
	}
}

func TestPolicyLoading_RepoMerge(t *testing.T) {
	t.Parallel()

	policyYAML := `
permissions:
  on_undeclared: warn
supervisor_limits:
  max_retries_per_job: 5
`

	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(policyPath, []byte(policyYAML), 0600); err != nil {
		t.Fatalf("write policy.yaml: %v", err)
	}

	loaded, err := (&policy.PolicyLoader{RepoConfigPath: policyPath}).LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy() error = %v", err)
	}

	if loaded.PermissionPolicy.OnUndeclared != "warn" {
		t.Fatalf("PermissionPolicy.OnUndeclared = %q, want %q", loaded.PermissionPolicy.OnUndeclared, "warn")
	}
	if loaded.Source["permissions.on_undeclared"] != "repo "+policyPath {
		t.Fatalf("permissions.on_undeclared source = %q, want %q", loaded.Source["permissions.on_undeclared"], "repo "+policyPath)
	}
	if loaded.SupervisorLimits.MaxRetriesPerJob != 5 {
		t.Fatalf("supervisor_limits.max_retries_per_job = %d, want 5", loaded.SupervisorLimits.MaxRetriesPerJob)
	}
	if got := loaded.Checkpoints["ready-to-ship"]; got == "" {
		t.Fatalf("checkpoint ready-to-ship missing after merge")
	}
	if got := loaded.Source["checkpoints.ready-to-ship"]; got != "built-in default" {
		t.Fatalf("checkpoint ready-to-ship source = %q, want built-in default", got)
	}
}

