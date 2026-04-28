package store

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func setupCostStoreTest(t *testing.T) (*EventStore, *RunStore, *JobStore, *CostEventStore, context.Context, string, string) {
	t.Helper()
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)
	cs := NewCostEventStore(db, es)
	ctx := context.Background()
	runID := "run_cost_store"
	jobID := "job_cost_store"
	createTestRun(t, rs, ctx, runID)
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
	return es, rs, js, cs, ctx, runID, jobID
}

func TestCostEventStore_RecordCostWritesRowEventAndBumpsTotals(t *testing.T) {
	es, rs, js, cs, ctx, runID, jobID := setupCostStoreTest(t)
	sample := core.CostSample{
		Provider:  "anthropic",
		Model:     "claude-opus",
		TokensIn:  100,
		TokensOut: 50,
		USD:       0.25,
	}
	if err := cs.RecordCost(ctx, runID, jobID, sample); err != nil {
		t.Fatalf("RecordCost: %v", err)
	}

	rows, err := cs.ListByJob(ctx, jobID)
	if err != nil {
		t.Fatalf("ListByJob: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].Provider != sample.Provider || rows[0].Model != sample.Model ||
		rows[0].TokensIn != sample.TokensIn || rows[0].TokensOut != sample.TokensOut || rows[0].USD != sample.USD {
		t.Errorf("cost row mismatch: %+v", rows[0])
	}

	events, err := es.ListEvents(ctx, runID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if events[len(events)-1].Kind != core.EventCostDelta {
		t.Errorf("last event kind = %q, want %q", events[len(events)-1].Kind, core.EventCostDelta)
	}

	run, err := rs.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.CostUSD != sample.USD {
		t.Errorf("run.CostUSD = %v, want %v", run.CostUSD, sample.USD)
	}
	job, err := js.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.CostUSD != sample.USD {
		t.Errorf("job.CostUSD = %v, want %v", job.CostUSD, sample.USD)
	}
}

func TestCostEventStore_SumsAndAccumulation(t *testing.T) {
	_, rs, js, cs, ctx, runID, jobID := setupCostStoreTest(t)
	if got, err := cs.SumByRun(ctx, runID); err != nil || got != 0 {
		t.Fatalf("empty SumByRun = %v, %v; want 0, nil", got, err)
	}
	samples := []core.CostSample{
		{Provider: "openai", Model: "gpt-a", TokensIn: 10, TokensOut: 5, USD: 0.10},
		{Provider: "openai", Model: "gpt-a", TokensIn: 20, TokensOut: 7, USD: 0.15},
	}
	for _, sample := range samples {
		if err := cs.RecordCost(ctx, runID, jobID, sample); err != nil {
			t.Fatalf("RecordCost: %v", err)
		}
	}

	for _, tc := range []struct {
		name string
		got  func() (float64, error)
	}{
		{name: "run", got: func() (float64, error) { return cs.SumByRun(ctx, runID) }},
		{name: "job", got: func() (float64, error) { return cs.SumByJob(ctx, jobID) }},
	} {
		got, err := tc.got()
		if err != nil {
			t.Fatalf("SumBy%s: %v", tc.name, err)
		}
		if got != 0.25 {
			t.Errorf("SumBy%s = %v, want 0.25", tc.name, got)
		}
	}

	run, err := rs.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	job, err := js.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if run.CostUSD != 0.25 || job.CostUSD != 0.25 {
		t.Errorf("cumulative costs run/job = %v/%v, want 0.25/0.25", run.CostUSD, job.CostUSD)
	}
}

func TestCostEventStore_RollsBackEventOnProjectionFailure(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	cs := NewCostEventStore(db, es)
	ctx := context.Background()

	before, err := es.ListEvents(ctx, "missing-run")
	if err != nil {
		t.Fatalf("ListEvents before: %v", err)
	}
	err = cs.RecordCost(ctx, "missing-run", "missing-job", core.CostSample{
		Provider: "openai",
		Model:    "gpt",
		USD:      0.01,
	})
	if err == nil {
		t.Fatal("RecordCost with missing FK succeeded, want error")
	}
	after, err := es.ListEvents(ctx, "missing-run")
	if err != nil {
		t.Fatalf("ListEvents after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("event count after failed projection = %d, want %d", len(after), len(before))
	}
}

// TestCostEventStore_ConcurrentRecordCostMatchesFinalCumulative
// verifies that concurrent RecordCost calls for the same run produce
// event payloads whose cumulative_usd values, when sorted, form an
// arithmetic sequence summing to the final runs.cost_usd. Plan 137
// fixed a race where the pre-read happened outside the transaction;
// without the fix, multiple events could carry the same stale
// cumulative value.
func TestCostEventStore_ConcurrentRecordCostMatchesFinalCumulative(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)
	cs := NewCostEventStore(db, es)
	ctx := context.Background()

	runID := "run_concurrent_cost"
	if err := rs.CreateRun(ctx, &core.Run{
		ID: runID, Mode: "interactive", State: core.RunStateActive,
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	jobID := "job_concurrent_cost"
	if err := js.CreateJob(ctx, &core.Job{
		ID: jobID, RunID: runID, Role: "developer",
		State: core.JobStatePending, DispatchedBy: "test", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	// 10 concurrent RecordCost calls. Each adds 0.01.
	const N = 10
	const sample = 0.01
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if err := cs.RecordCost(ctx, runID, jobID, core.CostSample{
				Provider: "anthropic", Model: "opus", USD: sample,
			}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent RecordCost: %v", err)
	}

	// Final runs.cost_usd must be exactly N*sample.
	run, err := rs.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	expected := float64(N) * sample
	if run.CostUSD < expected-0.0001 || run.CostUSD > expected+0.0001 {
		t.Errorf("runs.cost_usd = %v, want %v (10 × 0.01)", run.CostUSD, expected)
	}

	// Each event payload's cumulative_usd is the post-bump cost
	// computed inside the same transaction. The set of cumulatives
	// must be {0.01, 0.02, ..., 0.10} (in some order).
	events, err := es.ListEvents(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	gotCumulatives := []float64{}
	for _, ev := range events {
		if ev.Kind != core.EventCostDelta {
			continue
		}
		var p struct {
			Cumulative float64 `json:"cumulative_usd"`
		}
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		gotCumulatives = append(gotCumulatives, p.Cumulative)
	}
	if len(gotCumulatives) != N {
		t.Fatalf("collected %d cost cumulatives, want %d", len(gotCumulatives), N)
	}
	sort.Float64s(gotCumulatives)
	for i, c := range gotCumulatives {
		want := float64(i+1) * sample
		if c < want-0.0001 || c > want+0.0001 {
			t.Errorf("cumulative[%d] = %v, want %v (cumulatives must form an arithmetic sequence under the in-transaction read fix)",
				i, c, want)
		}
	}
}

// TestCostEventStore_PayloadIncludesCumulativeAndBudget verifies the
// cost.delta event payload exposes cumulative_usd and budget_usd so live
// consumers (TUI, HTTP/SSE clients) can show totals without re-querying
// the runs row. Plan 130 (I11).
func TestCostEventStore_PayloadIncludesCumulativeAndBudget(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)
	cs := NewCostEventStore(db, es)
	ctx := context.Background()

	// Seed run with a budget; seed a job.
	runID := "run_payload_1"
	budget := 5.0
	if err := rs.CreateRun(ctx, &core.Run{
		ID: runID, Mode: "interactive", State: core.RunStateActive,
		StartedAt: time.Now(),
		BudgetUSD: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	jobID := "job_payload_1"
	if err := js.CreateJob(ctx, &core.Job{
		ID: jobID, RunID: runID, Role: "developer",
		State: core.JobStatePending, DispatchedBy: "test", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	// First cost sample. Cumulative should equal sample.USD; budget should
	// match the run's budget.
	if err := cs.RecordCost(ctx, runID, jobID, core.CostSample{
		Provider: "anthropic", Model: "claude", USD: 0.0042,
		TokensIn: 100, TokensOut: 50,
	}); err != nil {
		t.Fatal(err)
	}

	// Find the latest cost.delta event and decode the payload.
	events, err := es.ListEvents(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	var costEvent *core.Event
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == core.EventCostDelta {
			costEvent = &events[i]
			break
		}
	}
	if costEvent == nil {
		t.Fatal("no cost.delta event in log")
	}
	var payload struct {
		Cumulative float64 `json:"cumulative_usd"`
		BudgetUSD  float64 `json:"budget_usd"`
		USD        float64 `json:"usd"`
		TokensIn   int     `json:"tokens_in"`
		TokensOut  int     `json:"tokens_out"`
	}
	if err := json.Unmarshal([]byte(costEvent.Payload), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.USD != 0.0042 {
		t.Errorf("usd = %v, want 0.0042", payload.USD)
	}
	if payload.Cumulative != 0.0042 {
		t.Errorf("cumulative_usd = %v, want 0.0042 (first sample)", payload.Cumulative)
	}
	if payload.BudgetUSD != 5.0 {
		t.Errorf("budget_usd = %v, want 5.0", payload.BudgetUSD)
	}
	if payload.TokensIn != 100 || payload.TokensOut != 50 {
		t.Errorf("tokens = %d/%d, want 100/50", payload.TokensIn, payload.TokensOut)
	}

	// Second sample: cumulative should grow.
	if err := cs.RecordCost(ctx, runID, jobID, core.CostSample{
		Provider: "anthropic", Model: "claude", USD: 0.001,
	}); err != nil {
		t.Fatal(err)
	}
	events2, _ := es.ListEvents(ctx, runID)
	var lastPayload struct {
		Cumulative float64 `json:"cumulative_usd"`
	}
	for i := len(events2) - 1; i >= 0; i-- {
		if events2[i].Kind == core.EventCostDelta {
			_ = json.Unmarshal([]byte(events2[i].Payload), &lastPayload)
			break
		}
	}
	if lastPayload.Cumulative != 0.0052 {
		t.Errorf("second cumulative = %v, want 0.0052", lastPayload.Cumulative)
	}
}
