package store

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func TestCreateRun(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ctx := context.Background()

	run := &core.Run{
		ID:        "run_test1",
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}

	if err := rs.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Verify run was created.
	got, err := rs.GetRun(ctx, "run_test1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ID != "run_test1" {
		t.Errorf("run ID = %q, want %q", got.ID, "run_test1")
	}
	if got.Mode != "interactive" {
		t.Errorf("run mode = %q, want %q", got.Mode, "interactive")
	}
	if got.State != core.RunStateActive {
		t.Errorf("run state = %q, want %q", got.State, core.RunStateActive)
	}

	// Verify event was written.
	events, err := es.ListEvents(ctx, "run_test1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Kind != core.EventRunCreated {
		t.Errorf("event kind = %q, want %q", events[0].Kind, core.EventRunCreated)
	}
}

func TestGetRun_NotFound(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ctx := context.Background()

	_, err := rs.GetRun(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent run, got nil")
	}
}

func TestCompleteRun(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ctx := context.Background()

	run := &core.Run{
		ID:        "run_complete",
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}

	if err := rs.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := rs.CompleteRun(ctx, "run_complete", core.RunStateCompleted); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}

	got, err := rs.GetRun(ctx, "run_complete")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != core.RunStateCompleted {
		t.Errorf("run state = %q, want %q", got.State, core.RunStateCompleted)
	}
	if got.EndedAt == nil {
		t.Error("run ended_at should be set")
	}

	// Verify two events: run.created + run.completed.
	events, err := es.ListEvents(ctx, "run_complete")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[1].Kind != core.EventRunCompleted {
		t.Errorf("second event kind = %q, want %q", events[1].Kind, core.EventRunCompleted)
	}
}

func TestRunStore_NewSpecColumnsRoundTrip(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ctx := context.Background()

	budget := 5.0
	in := &core.Run{
		ID:        "run_spec_columns",
		Mode:      "autopilot",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
		PRDPath:   "docs/prd.md",
		SpecPath:  "docs/spec.md",
		CostUSD:   1.25,
		BudgetUSD: &budget,
	}
	if err := rs.CreateRun(ctx, in); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	out, err := rs.GetRun(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if out.PRDPath != in.PRDPath {
		t.Errorf("PRDPath = %q, want %q", out.PRDPath, in.PRDPath)
	}
	if out.SpecPath != in.SpecPath {
		t.Errorf("SpecPath = %q, want %q", out.SpecPath, in.SpecPath)
	}
	if out.CostUSD != in.CostUSD {
		t.Errorf("CostUSD = %v, want %v", out.CostUSD, in.CostUSD)
	}
	if out.BudgetUSD == nil || *out.BudgetUSD != *in.BudgetUSD {
		t.Fatalf("BudgetUSD = %v, want %v", out.BudgetUSD, *in.BudgetUSD)
	}

	listed, err := rs.ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListRuns len = %d, want 1", len(listed))
	}
	if listed[0].PRDPath != in.PRDPath || listed[0].SpecPath != in.SpecPath ||
		listed[0].CostUSD != in.CostUSD ||
		listed[0].BudgetUSD == nil || *listed[0].BudgetUSD != *in.BudgetUSD {
		t.Errorf("ListRuns round-trip mismatch: got %+v, want %+v", listed[0], in)
	}
}

func TestRunStore_NilBudgetPreserved(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ctx := context.Background()

	in := &core.Run{
		ID:        "run_nil_budget",
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
		BudgetUSD: nil,
	}
	if err := rs.CreateRun(ctx, in); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	out, err := rs.GetRun(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if out.BudgetUSD != nil {
		t.Errorf("BudgetUSD = %v, want nil", *out.BudgetUSD)
	}
}
