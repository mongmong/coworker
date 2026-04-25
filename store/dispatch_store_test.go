package store

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

// newTestDispatch builds a minimal Dispatch for use in tests.
func newTestDispatch(runID, role string) *core.Dispatch {
	return &core.Dispatch{
		RunID: runID,
		Role:  role,
		Inputs: map[string]interface{}{
			"key": "value",
		},
	}
}

func TestEnqueueDispatch_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ds := NewDispatchStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_d1")

	d := newTestDispatch("run_d1", "reviewer.arch")
	if err := ds.EnqueueDispatch(ctx, d); err != nil {
		t.Fatalf("EnqueueDispatch: %v", err)
	}

	if d.ID == "" {
		t.Error("ID should be auto-assigned")
	}
	if d.State != core.DispatchStatePending {
		t.Errorf("state = %q, want pending", d.State)
	}

	// Verify dispatch.queued event.
	events, err := es.ListEvents(ctx, "run_d1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	// run.created + dispatch.queued
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[1].Kind != core.EventDispatchQueued {
		t.Errorf("event kind = %q, want dispatch.queued", events[1].Kind)
	}
}

func TestClaimNextDispatch_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ds := NewDispatchStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_d2")

	d := newTestDispatch("run_d2", "reviewer.arch")
	if err := ds.EnqueueDispatch(ctx, d); err != nil {
		t.Fatalf("EnqueueDispatch: %v", err)
	}

	claimed, err := ds.ClaimNextDispatch(ctx, "reviewer.arch", "")
	if err != nil {
		t.Fatalf("ClaimNextDispatch: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected a dispatch, got nil")
	}
	if claimed.ID != d.ID {
		t.Errorf("dispatch id = %q, want %q", claimed.ID, d.ID)
	}
	if claimed.State != core.DispatchStateLeased {
		t.Errorf("state = %q, want leased", claimed.State)
	}
	if claimed.LeasedAt == nil {
		t.Error("leased_at should be set")
	}
	if claimed.Role != "reviewer.arch" {
		t.Errorf("role = %q, want reviewer.arch", claimed.Role)
	}

	// Verify dispatch.leased event.
	events, err := es.ListEvents(ctx, "run_d2")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	// run.created + dispatch.queued + dispatch.leased
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[2].Kind != core.EventDispatchLeased {
		t.Errorf("event kind = %q, want dispatch.leased", events[2].Kind)
	}
}

func TestClaimNextDispatch_IdleWhenEmpty(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ds := NewDispatchStore(db, es)
	ctx := context.Background()

	claimed, err := ds.ClaimNextDispatch(ctx, "reviewer.arch", "")
	if err != nil {
		t.Fatalf("ClaimNextDispatch on empty queue: %v", err)
	}
	if claimed != nil {
		t.Errorf("expected nil, got dispatch %q", claimed.ID)
	}
}

func TestClaimNextDispatch_WrongRoleReturnsNil(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ds := NewDispatchStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_d3")

	d := newTestDispatch("run_d3", "reviewer.arch")
	if err := ds.EnqueueDispatch(ctx, d); err != nil {
		t.Fatalf("EnqueueDispatch: %v", err)
	}

	claimed, err := ds.ClaimNextDispatch(ctx, "coder.impl", "")
	if err != nil {
		t.Fatalf("ClaimNextDispatch: %v", err)
	}
	if claimed != nil {
		t.Error("expected nil for wrong role, got a dispatch")
	}
}

func TestClaimNextDispatch_OldestFirst(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ds := NewDispatchStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_d4")

	// Enqueue two dispatches; first enqueued should be claimed first.
	d1 := &core.Dispatch{
		RunID:     "run_d4",
		Role:      "reviewer.arch",
		CreatedAt: time.Now().Add(-1 * time.Minute), // older
		Inputs:    map[string]interface{}{},
	}
	d2 := &core.Dispatch{
		RunID:  "run_d4",
		Role:   "reviewer.arch",
		Inputs: map[string]interface{}{},
	}

	if err := ds.EnqueueDispatch(ctx, d1); err != nil {
		t.Fatalf("EnqueueDispatch d1: %v", err)
	}
	if err := ds.EnqueueDispatch(ctx, d2); err != nil {
		t.Fatalf("EnqueueDispatch d2: %v", err)
	}

	claimed, err := ds.ClaimNextDispatch(ctx, "reviewer.arch", "")
	if err != nil {
		t.Fatalf("ClaimNextDispatch: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected a dispatch, got nil")
	}
	if claimed.ID != d1.ID {
		t.Errorf("claimed dispatch id = %q, want %q (oldest)", claimed.ID, d1.ID)
	}
}

func TestCompleteDispatch_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ds := NewDispatchStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_d5")

	d := newTestDispatch("run_d5", "reviewer.arch")
	if err := ds.EnqueueDispatch(ctx, d); err != nil {
		t.Fatalf("EnqueueDispatch: %v", err)
	}

	claimed, err := ds.ClaimNextDispatch(ctx, "reviewer.arch", "")
	if err != nil {
		t.Fatalf("ClaimNextDispatch: %v", err)
	}

	outputs := map[string]interface{}{
		"exit_code": 0,
		"summary":   "all good",
	}
	if err := ds.CompleteDispatch(ctx, claimed.ID, outputs); err != nil {
		t.Fatalf("CompleteDispatch: %v", err)
	}

	// Verify state via GetDispatch.
	got, err := ds.GetDispatch(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("GetDispatch: %v", err)
	}
	if got == nil {
		t.Fatal("expected dispatch, got nil")
	}
	if got.State != core.DispatchStateCompleted {
		t.Errorf("state = %q, want completed", got.State)
	}
	if got.CompletedAt == nil {
		t.Error("completed_at should be set")
	}

	// Verify dispatch.completed event.
	events, err := es.ListEvents(ctx, "run_d5")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	// run.created + dispatch.queued + dispatch.leased + dispatch.completed
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}
	if events[3].Kind != core.EventDispatchCompleted {
		t.Errorf("event kind = %q, want dispatch.completed", events[3].Kind)
	}
}

func TestCompleteDispatch_NotFound(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ds := NewDispatchStore(db, es)
	ctx := context.Background()

	err := ds.CompleteDispatch(ctx, "nonexistent", nil)
	if err == nil {
		t.Error("expected error for nonexistent dispatch, got nil")
	}
}

func TestExpireLeases(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ds := NewDispatchStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_d6")

	d := newTestDispatch("run_d6", "reviewer.arch")
	if err := ds.EnqueueDispatch(ctx, d); err != nil {
		t.Fatalf("EnqueueDispatch: %v", err)
	}

	claimed, err := ds.ClaimNextDispatch(ctx, "reviewer.arch", "")
	if err != nil {
		t.Fatalf("ClaimNextDispatch: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected claimed dispatch")
	}

	// ExpireLeases with a zero timeout should expire the lease immediately.
	if err := ds.ExpireLeases(ctx, 0); err != nil {
		t.Fatalf("ExpireLeases: %v", err)
	}

	// Dispatch should be reset to pending.
	got, err := ds.GetDispatch(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("GetDispatch: %v", err)
	}
	if got.State != core.DispatchStatePending {
		t.Errorf("state after expiry = %q, want pending", got.State)
	}

	// Verify dispatch.expired event.
	events, err := es.ListEvents(ctx, "run_d6")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	// run.created + dispatch.queued + dispatch.leased + dispatch.expired
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}
	if events[3].Kind != core.EventDispatchExpired {
		t.Errorf("event kind = %q, want dispatch.expired", events[3].Kind)
	}
}

func TestExpireLeases_SkipsNonExpired(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ds := NewDispatchStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_d7")

	d := newTestDispatch("run_d7", "reviewer.arch")
	if err := ds.EnqueueDispatch(ctx, d); err != nil {
		t.Fatalf("EnqueueDispatch: %v", err)
	}

	claimed, err := ds.ClaimNextDispatch(ctx, "reviewer.arch", "")
	if err != nil {
		t.Fatalf("ClaimNextDispatch: %v", err)
	}

	// Use a long timeout — should NOT expire the recently leased dispatch.
	if err := ds.ExpireLeases(ctx, 30*time.Minute); err != nil {
		t.Fatalf("ExpireLeases: %v", err)
	}

	got, err := ds.GetDispatch(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("GetDispatch: %v", err)
	}
	if got.State != core.DispatchStateLeased {
		t.Errorf("state = %q, want leased (should not have expired)", got.State)
	}
}

func TestGetDispatch_NotFound(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ds := NewDispatchStore(db, es)
	ctx := context.Background()

	got, err := ds.GetDispatch(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetDispatch unexpected error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent dispatch")
	}
}

func TestFullLeaseCycle(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ds := NewDispatchStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_d8")

	// Enqueue.
	d := &core.Dispatch{
		RunID:  "run_d8",
		Role:   "coder.impl",
		Prompt: "implement feature X",
		Inputs: map[string]interface{}{"spec": "spec.md"},
	}
	if err := ds.EnqueueDispatch(ctx, d); err != nil {
		t.Fatalf("EnqueueDispatch: %v", err)
	}

	// Claim.
	claimed, err := ds.ClaimNextDispatch(ctx, "coder.impl", "")
	if err != nil {
		t.Fatalf("ClaimNextDispatch: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected dispatch")
	}
	if claimed.Prompt != "implement feature X" {
		t.Errorf("prompt = %q, want %q", claimed.Prompt, "implement feature X")
	}

	// Idle check — no more pending dispatches for this role.
	idle, err := ds.ClaimNextDispatch(ctx, "coder.impl", "")
	if err != nil {
		t.Fatalf("ClaimNextDispatch (idle): %v", err)
	}
	if idle != nil {
		t.Error("expected nil (idle) when all dispatches are leased")
	}

	// Complete.
	if err := ds.CompleteDispatch(ctx, claimed.ID, map[string]interface{}{"status": "done"}); err != nil {
		t.Fatalf("CompleteDispatch: %v", err)
	}

	// Verify final state.
	final, err := ds.GetDispatch(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("GetDispatch: %v", err)
	}
	if final.State != core.DispatchStateCompleted {
		t.Errorf("final state = %q, want completed", final.State)
	}
}
