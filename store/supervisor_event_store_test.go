package store

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func setupSupervisorStoreTest(t *testing.T) (*EventStore, *SupervisorEventStore, context.Context, string, string) {
	t.Helper()
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)
	ctx := context.Background()
	runID := "run_supervisor_store"
	jobID := "job_supervisor_store"
	createTestRun(t, rs, ctx, runID)
	if err := js.CreateJob(ctx, &core.Job{
		ID:           jobID,
		RunID:        runID,
		Role:         "reviewer.arch",
		State:        core.JobStatePending,
		DispatchedBy: "scheduler",
		StartedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	return es, NewSupervisorEventStore(db, es), ctx, runID, jobID
}

func TestSupervisorEventStore_RecordVerdictWritesRowAndEvent(t *testing.T) {
	es, ss, ctx, runID, jobID := setupSupervisorStoreTest(t)
	if err := ss.RecordVerdict(ctx, runID, jobID, core.RuleResult{
		RuleName: "findings_have_path",
		Passed:   true,
		Message:  "ok",
	}); err != nil {
		t.Fatalf("RecordVerdict: %v", err)
	}

	rows, err := ss.ListByJob(ctx, jobID)
	if err != nil {
		t.Fatalf("ListByJob: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].Verdict != "pass" || rows[0].RuleID != "findings_have_path" || rows[0].Message != "ok" {
		t.Errorf("row mismatch: %+v", rows[0])
	}

	events, err := es.ListEvents(ctx, runID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if events[len(events)-1].Kind != core.EventSupervisorVerdict {
		t.Errorf("last event kind = %q, want %q", events[len(events)-1].Kind, core.EventSupervisorVerdict)
	}
}

func TestSupervisorEventStore_RecordVerdictValues(t *testing.T) {
	_, ss, ctx, runID, jobID := setupSupervisorStoreTest(t)
	cases := []struct {
		name   string
		result core.RuleResult
		want   string
	}{
		{name: "pass", result: core.RuleResult{RuleName: "pass", Passed: true}, want: "pass"},
		{name: "fail", result: core.RuleResult{RuleName: "fail", Passed: false}, want: "fail"},
		{name: "skipped", result: core.RuleResult{RuleName: "skip", Passed: false, Skipped: true}, want: "skipped"},
	}
	for _, tc := range cases {
		if err := ss.RecordVerdict(ctx, runID, jobID, tc.result); err != nil {
			t.Fatalf("RecordVerdict(%s): %v", tc.name, err)
		}
	}
	rows, err := ss.ListByJob(ctx, jobID)
	if err != nil {
		t.Fatalf("ListByJob: %v", err)
	}
	if len(rows) != len(cases) {
		t.Fatalf("len(rows) = %d, want %d", len(rows), len(cases))
	}
	for i, tc := range cases {
		if rows[i].Verdict != tc.want {
			t.Errorf("rows[%d].Verdict = %q, want %q", i, rows[i].Verdict, tc.want)
		}
	}
}

func TestSupervisorEventStore_ListByRunAndJob(t *testing.T) {
	_, ss, ctx, runID, jobID := setupSupervisorStoreTest(t)
	if err := ss.RecordVerdict(ctx, runID, jobID, core.RuleResult{RuleName: "r1", Passed: true}); err != nil {
		t.Fatalf("RecordVerdict r1: %v", err)
	}
	if err := ss.RecordVerdict(ctx, runID, jobID, core.RuleResult{RuleName: "r2", Passed: false}); err != nil {
		t.Fatalf("RecordVerdict r2: %v", err)
	}

	byRun, err := ss.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	byJob, err := ss.ListByJob(ctx, jobID)
	if err != nil {
		t.Fatalf("ListByJob: %v", err)
	}
	if len(byRun) != 2 || len(byJob) != 2 {
		t.Fatalf("counts byRun/byJob = %d/%d, want 2/2", len(byRun), len(byJob))
	}
	if byRun[0].CreatedAt.After(byRun[1].CreatedAt) {
		t.Errorf("ListByRun not ordered by created_at: %v after %v", byRun[0].CreatedAt, byRun[1].CreatedAt)
	}
}

func TestSupervisorEventStore_RollsBackEventOnProjectionFailure(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ss := NewSupervisorEventStore(db, es)
	ctx := context.Background()

	before, err := es.ListEvents(ctx, "missing-run")
	if err != nil {
		t.Fatalf("ListEvents before: %v", err)
	}
	err = ss.RecordVerdict(ctx, "missing-run", "missing-job", core.RuleResult{RuleName: "r1", Passed: true})
	if err == nil {
		t.Fatal("RecordVerdict with missing FK succeeded, want error")
	}
	after, err := es.ListEvents(ctx, "missing-run")
	if err != nil {
		t.Fatalf("ListEvents after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("event count after failed projection = %d, want %d", len(after), len(before))
	}
}
