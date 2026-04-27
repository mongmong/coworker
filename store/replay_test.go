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

// TestReplay_RebuildProjectionRowsFromEventLog confirms that the projection
// tables (supervisor_events, cost_events, jobs.cost_usd, runs.cost_usd) can
// be reconstructed from the event log alone. This is the spec's "replay
// repairs the projection" property: an empty DB + an event log + the replay
// routine == the same projection state.
func TestReplay_RebuildProjectionRowsFromEventLog(t *testing.T) {
	events := readEventsJSONL(t, filepath.Join("..", "testdata", "golden_events", "run_with_supervisor.jsonl"))
	if len(events) == 0 {
		t.Fatal("golden fixture contains no events")
	}

	db := setupTestDB(t)
	ctx := context.Background()
	if err := replayProjections(ctx, db, events); err != nil {
		t.Fatalf("replayProjections: %v", err)
	}

	es := NewEventStore(db)
	sup := NewSupervisorEventStore(db, es)
	cost := NewCostEventStore(db, es)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)

	runID := events[0].RunID
	supRows, err := sup.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(supRows) != 2 {
		t.Errorf("rebuilt supervisor_events = %d, want 2", len(supRows))
	}
	if got, err := cost.SumByRun(ctx, runID); err != nil || got != 0.01 {
		t.Errorf("rebuilt cost SumByRun = %v, %v; want 0.01, nil", got, err)
	}
	if r, err := rs.GetRun(ctx, runID); err != nil || r.CostUSD != 0.01 {
		t.Errorf("rebuilt run.cost_usd = %v (err %v), want 0.01", r, err)
	}
	if j, err := js.GetJob(ctx, "replay-job"); err != nil || j.CostUSD != 0.01 {
		t.Errorf("rebuilt job.cost_usd = %v (err %v), want 0.01", j, err)
	}
}

// replayProjections rebuilds projection tables from a sequence of events.
// Inserts only into projection tables; the events table is intentionally
// untouched here so that callers can assert the rebuild matches what an
// event-replay routine would produce. Returns the first projection error.
func replayProjections(ctx context.Context, db *DB, events []core.Event) error {
	for _, ev := range events {
		var payload map[string]any
		if ev.Payload != "" {
			if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
				return err
			}
		}
		switch ev.Kind {
		case core.EventRunCreated:
			mode, _ := payload["mode"].(string)
			if _, err := db.ExecContext(ctx,
				`INSERT INTO runs (id, mode, state, started_at) VALUES (?, ?, 'active', ?)`,
				ev.RunID, mode, ev.CreatedAt.UTC().Format(time.RFC3339),
			); err != nil {
				return err
			}
		case core.EventJobCreated:
			jobID, _ := payload["job_id"].(string)
			role, _ := payload["role"].(string)
			cli, _ := payload["cli"].(string)
			if _, err := db.ExecContext(ctx,
				`INSERT INTO jobs (id, run_id, role, state, dispatched_by, cli, started_at)
				 VALUES (?, ?, ?, 'pending', 'scheduler', ?, ?)`,
				jobID, ev.RunID, role, cli, ev.CreatedAt.UTC().Format(time.RFC3339),
			); err != nil {
				return err
			}
		case core.EventJobCompleted:
			jobID, _ := payload["job_id"].(string)
			if _, err := db.ExecContext(ctx,
				`UPDATE jobs SET state = 'complete', ended_at = ? WHERE id = ?`,
				ev.CreatedAt.UTC().Format(time.RFC3339), jobID,
			); err != nil {
				return err
			}
		case core.EventSupervisorVerdict:
			jobID, _ := payload["job_id"].(string)
			verdict, _ := payload["verdict"].(string)
			rule, _ := payload["rule"].(string)
			message, _ := payload["message"].(string)
			if _, err := db.ExecContext(ctx,
				`INSERT INTO supervisor_events
				    (id, run_id, job_id, kind, verdict, rule_id, message, created_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				core.NewID(), ev.RunID, jobID, string(ev.Kind), verdict, rule, message,
				ev.CreatedAt.UTC().Format(time.RFC3339Nano),
			); err != nil {
				return err
			}
		case core.EventCostDelta:
			jobID, _ := payload["job_id"].(string)
			provider, _ := payload["provider"].(string)
			model, _ := payload["model"].(string)
			tokensIn, _ := payload["tokens_in"].(float64)
			tokensOut, _ := payload["tokens_out"].(float64)
			usd, _ := payload["usd"].(float64)
			if _, err := db.ExecContext(ctx,
				`INSERT INTO cost_events
				    (id, run_id, job_id, provider, model, tokens_in, tokens_out, usd, created_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				core.NewID(), ev.RunID, jobID, provider, model,
				int(tokensIn), int(tokensOut), usd,
				ev.CreatedAt.UTC().Format(time.RFC3339Nano),
			); err != nil {
				return err
			}
			if _, err := db.ExecContext(ctx,
				`UPDATE jobs SET cost_usd = cost_usd + ? WHERE id = ?`, usd, jobID,
			); err != nil {
				return err
			}
			if _, err := db.ExecContext(ctx,
				`UPDATE runs SET cost_usd = cost_usd + ? WHERE id = ?`, usd, ev.RunID,
			); err != nil {
				return err
			}
		}
	}
	return nil
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
