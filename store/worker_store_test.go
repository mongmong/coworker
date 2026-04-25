package store

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

// newTestWorker builds a minimal Worker for use in tests.
func newTestWorker(role, cli string) *core.Worker {
	return &core.Worker{
		Handle:    core.NewID(),
		Role:      role,
		PID:       1234,
		SessionID: "tmux-session-1",
		CLI:       cli,
	}
}

func TestWorkerStore_Register(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	w := newTestWorker("coder.impl", "claude-code")
	if err := ws.Register(ctx, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Handle must remain set.
	if w.Handle == "" {
		t.Error("Handle should not be empty after Register")
	}
	// State must be live.
	if w.State != core.WorkerStateLive {
		t.Errorf("State = %q, want live", w.State)
	}
	// RegisteredAt must be set.
	if w.RegisteredAt.IsZero() {
		t.Error("RegisteredAt should be set")
	}

	// Verify we can read it back.
	got, err := ws.GetWorker(ctx, w.Handle)
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if got == nil {
		t.Fatal("expected worker, got nil")
	}
	if got.Role != "coder.impl" {
		t.Errorf("Role = %q, want coder.impl", got.Role)
	}
	if got.CLI != "claude-code" {
		t.Errorf("CLI = %q, want claude-code", got.CLI)
	}
	if got.State != core.WorkerStateLive {
		t.Errorf("State = %q, want live", got.State)
	}
}

func TestWorkerStore_Register_EmitsEvent(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	w := newTestWorker("reviewer.arch", "codex")
	if err := ws.Register(ctx, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Worker events have no run_id; query by kind directly.
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events WHERE kind = ? AND correlation_id = ?",
		string(core.EventWorkerRegistered), w.Handle,
	).Scan(&count); err != nil {
		t.Fatalf("query events: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 worker.registered event, got %d", count)
	}
}

func TestWorkerStore_Heartbeat(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	w := newTestWorker("coder.impl", "claude-code")
	if err := ws.Register(ctx, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Record the time before heartbeat; the heartbeat timestamp must be >= this.
	beforeHeartbeat := time.Now().Truncate(time.Second)

	if err := ws.Heartbeat(ctx, w.Handle); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	got, err := ws.GetWorker(ctx, w.Handle)
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if got.State != core.WorkerStateLive {
		t.Errorf("State after heartbeat = %q, want live", got.State)
	}
	// LastHeartbeat should be at or after the moment we started the heartbeat.
	if got.LastHeartbeat.Before(beforeHeartbeat) {
		t.Errorf("LastHeartbeat (%v) should not be before beforeHeartbeat (%v)",
			got.LastHeartbeat, beforeHeartbeat)
	}

	// Verify worker.heartbeat event was emitted.
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events WHERE kind = ? AND correlation_id = ?",
		string(core.EventWorkerHeartbeat), w.Handle,
	).Scan(&count); err != nil {
		t.Fatalf("query events: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 worker.heartbeat event, got %d", count)
	}
}

func TestWorkerStore_Heartbeat_NotFound(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	err := ws.Heartbeat(ctx, "nonexistent-handle")
	if err == nil {
		t.Error("expected error for nonexistent handle, got nil")
	}
}

func TestWorkerStore_Heartbeat_EvictedWorker(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	w := newTestWorker("coder.impl", "claude-code")
	if err := ws.Register(ctx, w); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := ws.Deregister(ctx, w.Handle); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	// Heartbeat on evicted worker should fail.
	err := ws.Heartbeat(ctx, w.Handle)
	if err == nil {
		t.Error("expected error for heartbeat on evicted worker, got nil")
	}
}

func TestWorkerStore_Deregister(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	w := newTestWorker("reviewer.arch", "codex")
	if err := ws.Register(ctx, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := ws.Deregister(ctx, w.Handle); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	got, err := ws.GetWorker(ctx, w.Handle)
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if got == nil {
		t.Fatal("expected worker after deregister, got nil")
	}
	if got.State != core.WorkerStateEvicted {
		t.Errorf("State = %q, want evicted", got.State)
	}

	// Verify worker.deregistered event was emitted.
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events WHERE kind = ? AND correlation_id = ?",
		string(core.EventWorkerDeregistered), w.Handle,
	).Scan(&count); err != nil {
		t.Fatalf("query events: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 worker.deregistered event, got %d", count)
	}
}

func TestWorkerStore_Deregister_AlreadyEvicted(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	w := newTestWorker("coder.impl", "claude-code")
	if err := ws.Register(ctx, w); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := ws.Deregister(ctx, w.Handle); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	// Second deregister should fail.
	err := ws.Deregister(ctx, w.Handle)
	if err == nil {
		t.Error("expected error for double-deregister, got nil")
	}
}

func TestWorkerStore_LiveWorkersForRole(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	// Register two workers for same role, one for different role.
	w1 := newTestWorker("coder.impl", "claude-code")
	w2 := newTestWorker("coder.impl", "codex")
	w3 := newTestWorker("reviewer.arch", "claude-code")

	for _, w := range []*core.Worker{w1, w2, w3} {
		if err := ws.Register(ctx, w); err != nil {
			t.Fatalf("Register %s: %v", w.Handle, err)
		}
		// Small sleep to ensure distinct registered_at for ordering test.
		time.Sleep(2 * time.Millisecond)
	}

	live, err := ws.LiveWorkersForRole(ctx, "coder.impl")
	if err != nil {
		t.Fatalf("LiveWorkersForRole: %v", err)
	}
	if len(live) != 2 {
		t.Fatalf("expected 2 live workers for coder.impl, got %d", len(live))
	}
	// Should be ordered by registered_at ASC (oldest first).
	if live[0].Handle != w1.Handle {
		t.Errorf("first live worker = %q, want %q (oldest)", live[0].Handle, w1.Handle)
	}
	if live[1].Handle != w2.Handle {
		t.Errorf("second live worker = %q, want %q", live[1].Handle, w2.Handle)
	}

	// reviewer.arch should have exactly one.
	reviewerLive, err := ws.LiveWorkersForRole(ctx, "reviewer.arch")
	if err != nil {
		t.Fatalf("LiveWorkersForRole(reviewer.arch): %v", err)
	}
	if len(reviewerLive) != 1 {
		t.Fatalf("expected 1 live worker for reviewer.arch, got %d", len(reviewerLive))
	}
}

func TestWorkerStore_LiveWorkersForRole_ExcludesEvicted(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	w := newTestWorker("coder.impl", "claude-code")
	if err := ws.Register(ctx, w); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := ws.Deregister(ctx, w.Handle); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	live, err := ws.LiveWorkersForRole(ctx, "coder.impl")
	if err != nil {
		t.Fatalf("LiveWorkersForRole: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("expected 0 live workers after eviction, got %d", len(live))
	}
}

func TestWorkerStore_MarkStale(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	w := newTestWorker("coder.impl", "claude-code")
	if err := ws.Register(ctx, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Mark stale using a cutoff in the future (so last_heartbeat_at < cutoff).
	cutoff := time.Now().Add(1 * time.Hour)
	stale, err := ws.MarkStale(ctx, cutoff)
	if err != nil {
		t.Fatalf("MarkStale: %v", err)
	}
	if len(stale) != 1 || stale[0] != w.Handle {
		t.Errorf("MarkStale returned %v, want [%s]", stale, w.Handle)
	}

	got, err := ws.GetWorker(ctx, w.Handle)
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if got.State != core.WorkerStateStale {
		t.Errorf("State = %q, want stale", got.State)
	}
}

func TestWorkerStore_MarkStale_NoOpWhenFresh(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	w := newTestWorker("coder.impl", "claude-code")
	if err := ws.Register(ctx, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Cutoff in the past — worker's heartbeat is after cutoff, so not stale.
	cutoff := time.Now().Add(-1 * time.Hour)
	stale, err := ws.MarkStale(ctx, cutoff)
	if err != nil {
		t.Fatalf("MarkStale: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("expected no stale workers, got %v", stale)
	}
}

func TestWorkerStore_MarkEvicted(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	w := newTestWorker("coder.impl", "claude-code")
	if err := ws.Register(ctx, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Transition to stale first.
	cutoff := time.Now().Add(1 * time.Hour)
	if _, err := ws.MarkStale(ctx, cutoff); err != nil {
		t.Fatalf("MarkStale: %v", err)
	}

	// Now evict.
	evicted, err := ws.MarkEvicted(ctx, cutoff)
	if err != nil {
		t.Fatalf("MarkEvicted: %v", err)
	}
	if len(evicted) != 1 || evicted[0] != w.Handle {
		t.Errorf("MarkEvicted returned %v, want [%s]", evicted, w.Handle)
	}

	got, err := ws.GetWorker(ctx, w.Handle)
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if got.State != core.WorkerStateEvicted {
		t.Errorf("State = %q, want evicted", got.State)
	}

	// Verify worker.deregistered event was emitted.
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events WHERE kind = ? AND correlation_id = ?",
		string(core.EventWorkerDeregistered), w.Handle,
	).Scan(&count); err != nil {
		t.Fatalf("query events: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 worker.deregistered event from MarkEvicted, got %d", count)
	}
}

func TestWorkerStore_MarkEvicted_OnlyFromStale(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	w := newTestWorker("coder.impl", "claude-code")
	if err := ws.Register(ctx, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Try to evict directly without going through stale — should return nothing.
	cutoff := time.Now().Add(1 * time.Hour)
	evicted, err := ws.MarkEvicted(ctx, cutoff)
	if err != nil {
		t.Fatalf("MarkEvicted: %v", err)
	}
	if len(evicted) != 0 {
		t.Errorf("expected no evictions from live worker, got %v", evicted)
	}

	// Worker should still be live (MarkStale didn't run first).
	got, err := ws.GetWorker(ctx, w.Handle)
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if got.State != core.WorkerStateLive {
		t.Errorf("State = %q, want live", got.State)
	}
}

func TestWorkerStore_MarkEvicted_NoPhantomEventWhenWorkerRecovers(t *testing.T) {
	// Regression test: if a worker heartbeats between MarkStale's SELECT and
	// MarkEvicted's UPDATE, the Heartbeat call re-sets state to 'live'. The
	// subsequent MarkEvicted UPDATE should affect 0 rows and must NOT commit a
	// phantom worker.deregistered event.
	db := setupTestDB(t)
	es := NewEventStore(db)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	w := newTestWorker("coder.impl", "claude-code")
	if err := ws.Register(ctx, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Mark the worker stale.
	cutoff := time.Now().Add(1 * time.Hour)
	if _, err := ws.MarkStale(ctx, cutoff); err != nil {
		t.Fatalf("MarkStale: %v", err)
	}

	// Simulate the worker recovering by sending a heartbeat (state → live).
	if err := ws.Heartbeat(ctx, w.Handle); err != nil {
		t.Fatalf("Heartbeat (recovery): %v", err)
	}

	// MarkEvicted: the UPDATE will affect 0 rows because state is now 'live'.
	// The worker should NOT appear in the returned evicted list, and no
	// worker.deregistered event should be committed.
	evicted, err := ws.MarkEvicted(ctx, cutoff)
	if err != nil {
		t.Fatalf("MarkEvicted: %v", err)
	}
	for _, h := range evicted {
		if h == w.Handle {
			t.Errorf("recovered worker %q appeared in evicted list", w.Handle)
		}
	}

	// Worker should still be live.
	got, err := ws.GetWorker(ctx, w.Handle)
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if got.State != core.WorkerStateLive {
		t.Errorf("state = %q, want live (worker recovered)", got.State)
	}

	// Exactly one worker.deregistered event should exist (none — the worker
	// never actually got evicted).
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events WHERE kind = ? AND correlation_id = ?",
		string(core.EventWorkerDeregistered), w.Handle,
	).Scan(&count); err != nil {
		t.Fatalf("query events: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 worker.deregistered events for recovered worker, got %d", count)
	}
}

func TestWorkerStore_GetWorker_NotFound(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	got, err := ws.GetWorker(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetWorker unexpected error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent worker")
	}
}

func TestWorkerStore_RequeueLeasedDispatches(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ds := NewDispatchStore(db, es)
	ws := NewWorkerStore(db, es)
	ctx := context.Background()

	// Set up a run and a dispatch.
	createTestRun(t, rs, ctx, "run_w1")

	d := &core.Dispatch{
		RunID:  "run_w1",
		Role:   "coder.impl",
		Inputs: map[string]interface{}{},
	}
	if err := ds.EnqueueDispatch(ctx, d); err != nil {
		t.Fatalf("EnqueueDispatch: %v", err)
	}

	// Claim the dispatch (sets it to leased).
	claimed, err := ds.ClaimNextDispatch(ctx, "coder.impl", "")
	if err != nil {
		t.Fatalf("ClaimNextDispatch: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected claimed dispatch")
	}

	// Manually set worker_handle on the dispatch to simulate a registered worker.
	workerHandle := "test-worker-handle"
	_, err = db.ExecContext(ctx,
		"UPDATE dispatches SET worker_handle = ? WHERE id = ?",
		workerHandle, claimed.ID,
	)
	if err != nil {
		t.Fatalf("set worker_handle: %v", err)
	}

	// Register a worker and requeue its dispatches.
	w := &core.Worker{Handle: workerHandle, Role: "coder.impl", CLI: "claude-code"}
	if err := ws.Register(ctx, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := ws.RequeueLeasedDispatches(ctx, workerHandle, ds); err != nil {
		t.Fatalf("RequeueLeasedDispatches: %v", err)
	}

	// Dispatch should now be pending again.
	got, err := ds.GetDispatch(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("GetDispatch: %v", err)
	}
	if got.State != core.DispatchStatePending {
		t.Errorf("state after requeue = %q, want pending", got.State)
	}
	if got.WorkerHandle != "" {
		t.Errorf("worker_handle after requeue = %q, want empty", got.WorkerHandle)
	}
}
