package store

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func TestReplay_RebuildProjectionsFromEventLog(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)
	sup := NewSupervisorEventStore(db, es)
	cost := NewCostEventStore(db, es)

	ctx := context.Background()
	runID := "replay-run"
	jobID := "replay-job"
	if err := rs.CreateRun(ctx, &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := js.CreateJob(ctx, &core.Job{
		ID:           jobID,
		RunID:        runID,
		Role:         "developer",
		State:        core.JobStatePending,
		DispatchedBy: "scheduler",
		StartedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := sup.RecordVerdict(ctx, runID, jobID, core.RuleResult{
		RuleName: "r-A",
		Passed:   true,
		Message:  "ok",
	}); err != nil {
		t.Fatalf("RecordVerdict r-A: %v", err)
	}
	if err := sup.RecordVerdict(ctx, runID, jobID, core.RuleResult{
		RuleName: "r-B",
		Passed:   false,
		Message:  "bad",
	}); err != nil {
		t.Fatalf("RecordVerdict r-B: %v", err)
	}
	if err := cost.RecordCost(ctx, runID, jobID, core.CostSample{
		Provider:  "anthropic",
		Model:     "opus",
		TokensIn:  100,
		TokensOut: 50,
		USD:       0.01,
	}); err != nil {
		t.Fatalf("RecordCost: %v", err)
	}
	if err := js.UpdateJobState(ctx, jobID, core.JobStateComplete); err != nil {
		t.Fatalf("UpdateJobState: %v", err)
	}

	events, err := es.ListEvents(ctx, runID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if os.Getenv("COWORKER_REGEN") == "1" {
		writeEventsJSONL(t, filepath.Join("..", "testdata", "golden_events", "run_with_supervisor.jsonl"), events)
	}

	supRows, err := sup.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(supRows) != 2 {
		t.Fatalf("supervisor_events = %d, want 2", len(supRows))
	}
	if got, err := cost.SumByRun(ctx, runID); err != nil || got != 0.01 {
		t.Fatalf("cost SumByRun = %v, %v; want 0.01, nil", got, err)
	}
	runRow, err := rs.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if runRow.CostUSD != 0.01 {
		t.Errorf("run.cost_usd = %v, want 0.01", runRow.CostUSD)
	}
	jobRow, err := js.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if jobRow.CostUSD != 0.01 {
		t.Errorf("job.cost_usd = %v, want 0.01", jobRow.CostUSD)
	}

	assertReplayShape(t, events)
}

func TestReplay_GoldenEventsRoundTrip(t *testing.T) {
	events := readEventsJSONL(t, filepath.Join("..", "testdata", "golden_events", "run_with_supervisor.jsonl"))
	if len(events) == 0 {
		t.Fatal("golden fixture contains no events")
	}

	db := setupTestDB(t)
	es := NewEventStore(db)
	ctx := context.Background()
	for i := range events {
		ev := events[i]
		if err := es.WriteEventThenRow(ctx, &ev, nil); err != nil {
			t.Fatalf("WriteEventThenRow event %d (%s): %v", i, ev.Kind, err)
		}
	}
	out, err := es.ListEvents(ctx, events[0].RunID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(out) != len(events) {
		t.Fatalf("round-trip events = %d, want %d", len(out), len(events))
	}
	assertReplayShape(t, out)
}

func assertReplayShape(t *testing.T, events []core.Event) {
	t.Helper()
	var haveJobCreated, haveJobCompleted bool
	var supervisorCount, costCount int
	for _, e := range events {
		switch e.Kind {
		case core.EventJobCreated:
			haveJobCreated = true
		case core.EventJobCompleted:
			haveJobCompleted = true
		case core.EventSupervisorVerdict:
			supervisorCount++
		case core.EventCostDelta:
			costCount++
		}
	}
	if !haveJobCreated || !haveJobCompleted || supervisorCount != 2 || costCount != 1 {
		t.Fatalf("event shape: job.created=%v job.completed=%v supervisor=%d cost=%d; want true true 2 1",
			haveJobCreated, haveJobCompleted, supervisorCount, costCount)
	}
}

func writeEventsJSONL(t *testing.T, path string, events []core.Event) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir fixture dir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatalf("close fixture: %v", err)
		}
	}()
	enc := json.NewEncoder(f)
	for _, event := range events {
		if err := enc.Encode(event); err != nil {
			t.Fatalf("encode event: %v", err)
		}
	}
}

func readEventsJSONL(t *testing.T, path string) []core.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatalf("close fixture: %v", err)
		}
	}()

	var events []core.Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event core.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	return events
}
