package store

import (
	"context"
	"errors"
	"testing"

	"github.com/chris/coworker/core"
)

func setupCheckpointStoreTest(t *testing.T) (*EventStore, *CheckpointStore, context.Context) {
	t.Helper()
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ctx := context.Background()
	createTestRun(t, rs, ctx, "run_checkpoint_store")
	return es, NewCheckpointStore(db, es), ctx
}

func TestCheckpointStore_CreateCheckpointWritesRowAndEvent(t *testing.T) {
	es, cs, ctx := setupCheckpointStoreTest(t)
	in := core.CheckpointRecord{
		ID:    "checkpoint-open",
		RunID: "run_checkpoint_store",
		Kind:  "spec-approved",
		Notes: "review spec",
	}
	if err := cs.CreateCheckpoint(ctx, in); err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}

	out, err := cs.GetCheckpoint(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if out.ID != in.ID || out.RunID != in.RunID || out.Kind != in.Kind || out.Notes != in.Notes {
		t.Fatalf("checkpoint round-trip mismatch: got %+v, want %+v", out, in)
	}
	if out.State != "open" {
		t.Errorf("State = %q, want open", out.State)
	}
	if out.PlanID != "" {
		t.Errorf("PlanID = %q, want empty for NULL", out.PlanID)
	}

	events, err := es.ListEvents(ctx, in.RunID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if events[len(events)-1].Kind != core.EventCheckpointOpened {
		t.Errorf("last event kind = %q, want %q", events[len(events)-1].Kind, core.EventCheckpointOpened)
	}
}

func TestCheckpointStore_CreateCheckpointWithPlanID(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ps := NewPlanStore(db, es)
	cs := NewCheckpointStore(db, es)
	ctx := context.Background()
	createTestRun(t, rs, ctx, "run_checkpoint_plan")
	if err := ps.CreatePlan(ctx, core.PlanRecord{
		ID: "plan-checkpoint", RunID: "run_checkpoint_plan", Number: 1, Title: "Checkpoint plan",
	}); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	if err := cs.CreateCheckpoint(ctx, core.CheckpointRecord{
		ID: "checkpoint-with-plan", RunID: "run_checkpoint_plan", PlanID: "plan-checkpoint", Kind: "plan-approved",
	}); err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}

	out, err := cs.GetCheckpoint(ctx, "checkpoint-with-plan")
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if out.PlanID != "plan-checkpoint" {
		t.Errorf("PlanID = %q, want plan-checkpoint", out.PlanID)
	}
}

func TestCheckpointStore_ResolveCheckpoint(t *testing.T) {
	es, cs, ctx := setupCheckpointStoreTest(t)
	if err := cs.CreateCheckpoint(ctx, core.CheckpointRecord{
		ID: "checkpoint-resolve", RunID: "run_checkpoint_store", Kind: "phase-clean",
	}); err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}

	if err := cs.ResolveCheckpoint(ctx, "checkpoint-resolve", "approve", "human", "looks good"); err != nil {
		t.Fatalf("ResolveCheckpoint: %v", err)
	}

	out, err := cs.GetCheckpoint(ctx, "checkpoint-resolve")
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if out.State != "resolved" || out.Decision != "approve" || out.DecidedBy != "human" || out.Notes != "looks good" {
		t.Errorf("resolved row mismatch: %+v", out)
	}
	if out.DecidedAt == nil {
		t.Fatal("DecidedAt is nil, want timestamp")
	}

	events, err := es.ListEvents(ctx, "run_checkpoint_store")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if events[len(events)-1].Kind != core.EventCheckpointResolved {
		t.Errorf("last event kind = %q, want %q", events[len(events)-1].Kind, core.EventCheckpointResolved)
	}
}

func TestCheckpointStore_ResolveCheckpointIdempotent(t *testing.T) {
	es, cs, ctx := setupCheckpointStoreTest(t)
	if err := cs.CreateCheckpoint(ctx, core.CheckpointRecord{
		ID: "checkpoint-idempotent", RunID: "run_checkpoint_store", Kind: "phase-clean",
	}); err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if err := cs.ResolveCheckpoint(ctx, "checkpoint-idempotent", "approve", "human", "once"); err != nil {
		t.Fatalf("ResolveCheckpoint first: %v", err)
	}
	before, err := es.ListEvents(ctx, "run_checkpoint_store")
	if err != nil {
		t.Fatalf("ListEvents before: %v", err)
	}

	if err := cs.ResolveCheckpoint(ctx, "checkpoint-idempotent", "reject", "human", "twice"); err != nil {
		t.Fatalf("ResolveCheckpoint second: %v", err)
	}

	after, err := es.ListEvents(ctx, "run_checkpoint_store")
	if err != nil {
		t.Fatalf("ListEvents after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("event count after idempotent resolve = %d, want %d", len(after), len(before))
	}
	out, err := cs.GetCheckpoint(ctx, "checkpoint-idempotent")
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if out.Decision != "approve" || out.Notes != "once" {
		t.Errorf("second resolve mutated row: %+v", out)
	}
}

func TestCheckpointStore_ResolveCheckpointMissing(t *testing.T) {
	_, cs, ctx := setupCheckpointStoreTest(t)
	if err := cs.ResolveCheckpoint(ctx, "missing", "approve", "human", ""); !errors.Is(err, ErrCheckpointNotFound) {
		t.Fatalf("ResolveCheckpoint error = %v, want ErrCheckpointNotFound", err)
	}
}

func TestCheckpointStore_ListCheckpointsByRunOrdersOpenBeforeResolved(t *testing.T) {
	_, cs, ctx := setupCheckpointStoreTest(t)
	for _, id := range []string{"checkpoint-resolved-a", "checkpoint-open", "checkpoint-resolved-b"} {
		if err := cs.CreateCheckpoint(ctx, core.CheckpointRecord{
			ID: id, RunID: "run_checkpoint_store", Kind: "phase-clean",
		}); err != nil {
			t.Fatalf("CreateCheckpoint(%s): %v", id, err)
		}
	}
	if err := cs.ResolveCheckpoint(ctx, "checkpoint-resolved-a", "approve", "human", "a"); err != nil {
		t.Fatalf("ResolveCheckpoint a: %v", err)
	}
	if err := cs.ResolveCheckpoint(ctx, "checkpoint-resolved-b", "reject", "human", "b"); err != nil {
		t.Fatalf("ResolveCheckpoint b: %v", err)
	}

	out, err := cs.ListCheckpointsByRun(ctx, "run_checkpoint_store")
	if err != nil {
		t.Fatalf("ListCheckpointsByRun: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3", len(out))
	}
	if out[0].ID != "checkpoint-open" {
		t.Errorf("first checkpoint = %q, want checkpoint-open", out[0].ID)
	}
	if out[1].State != "resolved" || out[2].State != "resolved" {
		t.Errorf("resolved checkpoints not ordered after open: %+v", out)
	}
}

func TestCheckpointStore_CreateCheckpointRollsBackEventOnProjectionFailure(t *testing.T) {
	es, cs, ctx := setupCheckpointStoreTest(t)
	if err := cs.CreateCheckpoint(ctx, core.CheckpointRecord{
		ID: "checkpoint-dup", RunID: "run_checkpoint_store", Kind: "phase-clean",
	}); err != nil {
		t.Fatalf("CreateCheckpoint first: %v", err)
	}
	before, err := es.ListEvents(ctx, "run_checkpoint_store")
	if err != nil {
		t.Fatalf("ListEvents before: %v", err)
	}

	err = cs.CreateCheckpoint(ctx, core.CheckpointRecord{
		ID: "checkpoint-dup", RunID: "run_checkpoint_store", Kind: "phase-clean",
	})
	if err == nil {
		t.Fatal("CreateCheckpoint duplicate succeeded, want error")
	}
	after, err := es.ListEvents(ctx, "run_checkpoint_store")
	if err != nil {
		t.Fatalf("ListEvents after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("event count after failed projection = %d, want %d", len(after), len(before))
	}
}
