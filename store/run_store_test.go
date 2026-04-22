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
