package store

import (
	"context"
	"errors"
	"testing"

	"github.com/chris/coworker/core"
)

func setupPlanStoreTest(t *testing.T) (*EventStore, *PlanStore, context.Context) {
	t.Helper()
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ctx := context.Background()
	createTestRun(t, rs, ctx, "run_plan_store")
	return es, NewPlanStore(db, es), ctx
}

func TestPlanStore_CreatePlanRoundTripAndEvent(t *testing.T) {
	es, ps, ctx := setupPlanStoreTest(t)

	in := core.PlanRecord{
		ID:       "plan-1",
		RunID:    "run_plan_store",
		Number:   1,
		Title:    "Schema completion",
		BlocksOn: []int{100, 101},
		Branch:   "feature/plan-1",
		PRURL:    "https://example.com/pr/1",
	}
	if err := ps.CreatePlan(ctx, in); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	out, err := ps.GetPlan(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if out.ID != in.ID || out.RunID != in.RunID || out.Number != in.Number ||
		out.Title != in.Title || out.Branch != in.Branch || out.PRURL != in.PRURL {
		t.Fatalf("plan round-trip mismatch: got %+v, want %+v", out, in)
	}
	if out.State != "pending" {
		t.Errorf("State = %q, want pending", out.State)
	}
	if len(out.BlocksOn) != 2 || out.BlocksOn[0] != 100 || out.BlocksOn[1] != 101 {
		t.Errorf("BlocksOn = %v, want [100 101]", out.BlocksOn)
	}

	events, err := es.ListEvents(ctx, in.RunID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if events[len(events)-1].Kind != core.EventPlanCreated {
		t.Errorf("last event kind = %q, want %q", events[len(events)-1].Kind, core.EventPlanCreated)
	}
	if events[len(events)-1].CorrelationID != in.ID {
		t.Errorf("CorrelationID = %q, want %q", events[len(events)-1].CorrelationID, in.ID)
	}
}

func TestPlanStore_UpdatePlanState(t *testing.T) {
	es, ps, ctx := setupPlanStoreTest(t)
	if err := ps.CreatePlan(ctx, core.PlanRecord{
		ID: "plan-state", RunID: "run_plan_store", Number: 1, Title: "State",
	}); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	if err := ps.UpdatePlanState(ctx, "plan-state", "running"); err != nil {
		t.Fatalf("UpdatePlanState: %v", err)
	}

	out, err := ps.GetPlan(ctx, "plan-state")
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if out.State != "running" {
		t.Errorf("State = %q, want running", out.State)
	}

	events, err := es.ListEvents(ctx, "run_plan_store")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if events[len(events)-1].Kind != core.EventPlanStateChanged {
		t.Errorf("last event kind = %q, want %q", events[len(events)-1].Kind, core.EventPlanStateChanged)
	}
}

func TestPlanStore_UpdatePlanStateMissing(t *testing.T) {
	_, ps, ctx := setupPlanStoreTest(t)
	if err := ps.UpdatePlanState(ctx, "missing", "done"); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("UpdatePlanState error = %v, want ErrPlanNotFound", err)
	}
}

func TestPlanStore_UpdatePlanBranchAndPR(t *testing.T) {
	_, ps, ctx := setupPlanStoreTest(t)
	if err := ps.CreatePlan(ctx, core.PlanRecord{
		ID: "plan-branch", RunID: "run_plan_store", Number: 1, Title: "Branch",
	}); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	if err := ps.UpdatePlanBranchAndPR(ctx, "plan-branch", "feature/x", "https://example.com/pr/2"); err != nil {
		t.Fatalf("UpdatePlanBranchAndPR: %v", err)
	}

	out, err := ps.GetPlan(ctx, "plan-branch")
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if out.Branch != "feature/x" || out.PRURL != "https://example.com/pr/2" {
		t.Errorf("branch/pr = %q/%q, want feature/x/https://example.com/pr/2", out.Branch, out.PRURL)
	}
}

func TestPlanStore_ListPlansByRunOrdersByNumber(t *testing.T) {
	_, ps, ctx := setupPlanStoreTest(t)
	for _, p := range []core.PlanRecord{
		{ID: "plan-3", RunID: "run_plan_store", Number: 3, Title: "Third"},
		{ID: "plan-1", RunID: "run_plan_store", Number: 1, Title: "First"},
		{ID: "plan-2", RunID: "run_plan_store", Number: 2, Title: "Second"},
	} {
		if err := ps.CreatePlan(ctx, p); err != nil {
			t.Fatalf("CreatePlan(%s): %v", p.ID, err)
		}
	}

	out, err := ps.ListPlansByRun(ctx, "run_plan_store")
	if err != nil {
		t.Fatalf("ListPlansByRun: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3", len(out))
	}
	for i, want := range []int{1, 2, 3} {
		if out[i].Number != want {
			t.Errorf("out[%d].Number = %d, want %d", i, out[i].Number, want)
		}
	}
}

func TestPlanStore_GetPlanMissing(t *testing.T) {
	_, ps, ctx := setupPlanStoreTest(t)
	if _, err := ps.GetPlan(ctx, "missing"); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("GetPlan error = %v, want ErrPlanNotFound", err)
	}
}

func TestPlanStore_UniqueRunNumber(t *testing.T) {
	_, ps, ctx := setupPlanStoreTest(t)
	if err := ps.CreatePlan(ctx, core.PlanRecord{
		ID: "plan-a", RunID: "run_plan_store", Number: 1, Title: "A",
	}); err != nil {
		t.Fatalf("CreatePlan plan-a: %v", err)
	}
	if err := ps.CreatePlan(ctx, core.PlanRecord{
		ID: "plan-b", RunID: "run_plan_store", Number: 1, Title: "B",
	}); err == nil {
		t.Fatal("CreatePlan duplicate number succeeded, want constraint error")
	}
}

func TestPlanStore_CreatePlanRollsBackEventOnProjectionFailure(t *testing.T) {
	es, ps, ctx := setupPlanStoreTest(t)
	if err := ps.CreatePlan(ctx, core.PlanRecord{
		ID: "plan-a", RunID: "run_plan_store", Number: 1, Title: "A",
	}); err != nil {
		t.Fatalf("CreatePlan plan-a: %v", err)
	}
	before, err := es.ListEvents(ctx, "run_plan_store")
	if err != nil {
		t.Fatalf("ListEvents before: %v", err)
	}

	err = ps.CreatePlan(ctx, core.PlanRecord{
		ID: "plan-b", RunID: "run_plan_store", Number: 1, Title: "B",
	})
	if err == nil {
		t.Fatal("CreatePlan duplicate number succeeded, want error")
	}
	after, err := es.ListEvents(ctx, "run_plan_store")
	if err != nil {
		t.Fatalf("ListEvents after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("event count after failed projection = %d, want %d", len(after), len(before))
	}
}

func TestPlanStore_CreatePlanForeignKey(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ps := NewPlanStore(db, es)
	ctx := context.Background()

	err := ps.CreatePlan(ctx, core.PlanRecord{
		ID: "plan-no-run", RunID: "missing-run", Number: 1, Title: "No run",
	})
	if err == nil {
		t.Fatal("CreatePlan without run succeeded, want foreign key error")
	}
}

func TestPlanStore_CreatePlanWithExplicitState(t *testing.T) {
	_, ps, ctx := setupPlanStoreTest(t)
	in := core.PlanRecord{
		ID:       "plan-explicit-state",
		RunID:    "run_plan_store",
		Number:   1,
		Title:    "Explicit",
		State:    "running",
		BlocksOn: []int{7},
		Branch:   "feature/explicit",
	}
	if err := ps.CreatePlan(ctx, in); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	out, err := ps.GetPlan(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if out.State != "running" {
		t.Errorf("State = %q, want running", out.State)
	}
}
