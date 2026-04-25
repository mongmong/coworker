package coding_test

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// openRouterTestDB opens an in-memory SQLite DB for router tests.
func openRouterTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newRouterStores creates the event, run, worker, and dispatch stores.
func newRouterStores(t *testing.T, db *store.DB) (*store.EventStore, *store.RunStore, *store.WorkerStore, *store.DispatchStore) {
	t.Helper()
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)
	ws := store.NewWorkerStore(db, es)
	ds := store.NewDispatchStore(db, es)
	return es, rs, ws, ds
}

// createRouterRun inserts a run row needed for dispatch FK.
func createRouterRun(t *testing.T, rs *store.RunStore, runID string) {
	t.Helper()
	run := &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := rs.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("CreateRun(%q): %v", runID, err)
	}
}

// registerWorker inserts a live worker for the given role.
func registerWorker(t *testing.T, ws *store.WorkerStore, role, cli string) *core.Worker {
	t.Helper()
	w := &core.Worker{
		Handle:       core.NewID(),
		Role:         role,
		PID:          1000,
		SessionID:    "tmux-1",
		CLI:          cli,
		RegisteredAt: time.Now(),
	}
	if err := ws.Register(context.Background(), w); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return w
}

// newRouter creates a DispatchRouter backed by the given stores.
func newRouter(ws *store.WorkerStore, ds *store.DispatchStore) *coding.DispatchRouter {
	return coding.NewDispatchRouter(ws, ds, nil)
}

// newTemplateDispatch builds a minimal Dispatch template for testing Route().
func newTemplateDispatch(runID, role string) *core.Dispatch {
	return &core.Dispatch{
		RunID:  runID,
		Role:   role,
		Prompt: "do the thing",
		Inputs: map[string]interface{}{"key": "val"},
	}
}

// ---- single concurrency tests -------------------------------------------------

func TestDispatchRouter_SingleConcurrency_NoWorkers(t *testing.T) {
	db := openRouterTestDB(t)
	_, rs, ws, ds := newRouterStores(t, db)
	createRouterRun(t, rs, "run_r_s0")

	router := newRouter(ws, ds)
	tmpl := newTemplateDispatch("run_r_s0", "coder.impl")

	result, err := router.Route(context.Background(), tmpl, "single")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}

	if result.Mode != coding.RouteModeEphemeral {
		t.Errorf("Mode = %q, want ephemeral", result.Mode)
	}
	if len(result.Workers) != 0 {
		t.Errorf("Workers = %v, want empty", result.Workers)
	}
	// Ephemeral routing creates one dispatch row with NULL worker_handle.
	if len(result.DispatchIDs) != 1 {
		t.Fatalf("DispatchIDs count = %d, want 1", len(result.DispatchIDs))
	}
}

func TestDispatchRouter_SingleConcurrency_OneWorker(t *testing.T) {
	db := openRouterTestDB(t)
	_, rs, ws, ds := newRouterStores(t, db)
	createRouterRun(t, rs, "run_r_s1")

	w := registerWorker(t, ws, "coder.impl", "claude-code")
	router := newRouter(ws, ds)
	tmpl := newTemplateDispatch("run_r_s1", "coder.impl")

	result, err := router.Route(context.Background(), tmpl, "single")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}

	if result.Mode != coding.RouteModeWorker {
		t.Errorf("Mode = %q, want worker", result.Mode)
	}
	if len(result.Workers) != 1 {
		t.Fatalf("Workers count = %d, want 1", len(result.Workers))
	}
	if result.Workers[0] != w.Handle {
		t.Errorf("Workers[0] = %q, want %q", result.Workers[0], w.Handle)
	}
	if len(result.DispatchIDs) != 1 {
		t.Fatalf("DispatchIDs count = %d, want 1", len(result.DispatchIDs))
	}
}

func TestDispatchRouter_SingleConcurrency_MultiWorker(t *testing.T) {
	db := openRouterTestDB(t)
	_, rs, ws, ds := newRouterStores(t, db)
	createRouterRun(t, rs, "run_r_s2")

	// Register two workers; oldest first (small sleep to ensure distinct timestamps).
	w1 := registerWorker(t, ws, "coder.impl", "claude-code")
	time.Sleep(2 * time.Millisecond)
	_ = registerWorker(t, ws, "coder.impl", "codex")

	router := newRouter(ws, ds)
	tmpl := newTemplateDispatch("run_r_s2", "coder.impl")

	result, err := router.Route(context.Background(), tmpl, "single")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}

	if result.Mode != coding.RouteModeWorker {
		t.Errorf("Mode = %q, want worker", result.Mode)
	}
	// Single concurrency — only one dispatch, to the OLDEST worker.
	if len(result.Workers) != 1 {
		t.Fatalf("Workers count = %d, want 1", len(result.Workers))
	}
	if result.Workers[0] != w1.Handle {
		t.Errorf("Workers[0] = %q, want oldest worker %q", result.Workers[0], w1.Handle)
	}
	if len(result.DispatchIDs) != 1 {
		t.Fatalf("DispatchIDs count = %d, want 1", len(result.DispatchIDs))
	}
}

// ---- many concurrency tests ---------------------------------------------------

func TestDispatchRouter_ManyConcurrency_NoWorkers(t *testing.T) {
	db := openRouterTestDB(t)
	_, rs, ws, ds := newRouterStores(t, db)
	createRouterRun(t, rs, "run_r_m0")

	router := newRouter(ws, ds)
	tmpl := newTemplateDispatch("run_r_m0", "reviewer.arch")

	result, err := router.Route(context.Background(), tmpl, "many")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}

	if result.Mode != coding.RouteModeEphemeral {
		t.Errorf("Mode = %q, want ephemeral", result.Mode)
	}
	if len(result.Workers) != 0 {
		t.Errorf("Workers = %v, want empty", result.Workers)
	}
	// One ephemeral dispatch created.
	if len(result.DispatchIDs) != 1 {
		t.Fatalf("DispatchIDs count = %d, want 1", len(result.DispatchIDs))
	}
}

func TestDispatchRouter_ManyConcurrency_TwoWorkers(t *testing.T) {
	db := openRouterTestDB(t)
	_, rs, ws, ds := newRouterStores(t, db)
	createRouterRun(t, rs, "run_r_m2")

	w1 := registerWorker(t, ws, "reviewer.arch", "claude-code")
	time.Sleep(2 * time.Millisecond)
	w2 := registerWorker(t, ws, "reviewer.arch", "codex")

	router := newRouter(ws, ds)
	tmpl := newTemplateDispatch("run_r_m2", "reviewer.arch")

	result, err := router.Route(context.Background(), tmpl, "many")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}

	if result.Mode != coding.RouteModeWorker {
		t.Errorf("Mode = %q, want worker", result.Mode)
	}
	if len(result.Workers) != 2 {
		t.Fatalf("Workers count = %d, want 2", len(result.Workers))
	}
	if len(result.DispatchIDs) != 2 {
		t.Fatalf("DispatchIDs count = %d, want 2", len(result.DispatchIDs))
	}

	// Both workers should appear in the result.
	workerSet := map[string]bool{w1.Handle: true, w2.Handle: true}
	for _, h := range result.Workers {
		if !workerSet[h] {
			t.Errorf("unexpected handle %q in Workers", h)
		}
	}
}

// ---- dispatch row verification tests -----------------------------------------

func TestDispatchRouter_EnqueueSetsWorkerHandle(t *testing.T) {
	db := openRouterTestDB(t)
	_, rs, ws, ds := newRouterStores(t, db)
	createRouterRun(t, rs, "run_r_wh")

	w := registerWorker(t, ws, "coder.impl", "claude-code")
	router := newRouter(ws, ds)
	tmpl := newTemplateDispatch("run_r_wh", "coder.impl")

	result, err := router.Route(context.Background(), tmpl, "single")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if result.Mode != coding.RouteModeWorker {
		t.Fatalf("Mode = %q, want worker", result.Mode)
	}

	// Verify the dispatch row in the DB has worker_handle set correctly.
	dispatch, err := ds.GetDispatch(context.Background(), result.DispatchIDs[0])
	if err != nil {
		t.Fatalf("GetDispatch: %v", err)
	}
	if dispatch == nil {
		t.Fatal("dispatch not found")
	}
	if dispatch.WorkerHandle != w.Handle {
		t.Errorf("dispatch.WorkerHandle = %q, want %q", dispatch.WorkerHandle, w.Handle)
	}
}

func TestDispatchRouter_EphemeralDispatchNullHandle(t *testing.T) {
	db := openRouterTestDB(t)
	_, rs, ws, ds := newRouterStores(t, db)
	createRouterRun(t, rs, "run_r_ep")

	// No workers registered — should produce an ephemeral dispatch.
	router := newRouter(ws, ds)
	tmpl := newTemplateDispatch("run_r_ep", "coder.impl")

	result, err := router.Route(context.Background(), tmpl, "single")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if result.Mode != coding.RouteModeEphemeral {
		t.Fatalf("Mode = %q, want ephemeral", result.Mode)
	}
	if len(result.DispatchIDs) != 1 {
		t.Fatalf("DispatchIDs count = %d, want 1", len(result.DispatchIDs))
	}

	// The created dispatch row should have an empty worker_handle (NULL in DB).
	dispatch, err := ds.GetDispatch(context.Background(), result.DispatchIDs[0])
	if err != nil {
		t.Fatalf("GetDispatch: %v", err)
	}
	if dispatch == nil {
		t.Fatal("dispatch not found")
	}
	if dispatch.WorkerHandle != "" {
		t.Errorf("dispatch.WorkerHandle = %q, want empty (NULL)", dispatch.WorkerHandle)
	}

	// An ephemeral caller (handle="") should be able to claim it.
	claimed, err := ds.ClaimNextDispatch(context.Background(), "coder.impl", "")
	if err != nil {
		t.Fatalf("ClaimNextDispatch: %v", err)
	}
	if claimed == nil {
		t.Fatal("ephemeral caller could not claim the NULL-handle dispatch")
	}
	if claimed.ID != dispatch.ID {
		t.Errorf("claimed ID = %q, want %q", claimed.ID, dispatch.ID)
	}
}

// ---- unknown concurrency ------------------------------------------------------

func TestDispatchRouter_UnknownConcurrency_ReturnsError(t *testing.T) {
	db := openRouterTestDB(t)
	_, rs, ws, ds := newRouterStores(t, db)
	createRouterRun(t, rs, "run_r_bad")

	router := newRouter(ws, ds)
	tmpl := newTemplateDispatch("run_r_bad", "coder.impl")

	_, err := router.Route(context.Background(), tmpl, "fanned-out")
	if err == nil {
		t.Fatal("expected error for unknown concurrency, got nil")
	}
}

// ---- registered worker handle filtering (ClaimNextDispatch) ------------------

func TestClaimNextDispatch_HandleFilter_WorkerOnly(t *testing.T) {
	// Verify that a registered worker (handle != "") can claim a targeted dispatch
	// but an ephemeral caller (handle == "") cannot claim that targeted dispatch.
	db := openRouterTestDB(t)
	_, rs, ws, ds := newRouterStores(t, db)
	createRouterRun(t, rs, "run_r_hf")

	w := registerWorker(t, ws, "coder.impl", "claude-code")
	router := newRouter(ws, ds)
	tmpl := newTemplateDispatch("run_r_hf", "coder.impl")

	// Route → dispatch targeted to w.Handle.
	result, err := router.Route(context.Background(), tmpl, "single")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if result.Mode != coding.RouteModeWorker {
		t.Fatalf("Mode = %q, want worker", result.Mode)
	}

	// Ephemeral caller should NOT be able to claim the worker-targeted dispatch.
	claimed, err := ds.ClaimNextDispatch(context.Background(), "coder.impl", "")
	if err != nil {
		t.Fatalf("ClaimNextDispatch ephemeral: %v", err)
	}
	if claimed != nil {
		t.Error("ephemeral caller should not be able to claim a worker-targeted dispatch")
	}

	// The registered worker CAN claim it.
	claimed, err = ds.ClaimNextDispatch(context.Background(), "coder.impl", w.Handle)
	if err != nil {
		t.Fatalf("ClaimNextDispatch worker: %v", err)
	}
	if claimed == nil {
		t.Fatal("registered worker could not claim its targeted dispatch")
	}
	if claimed.ID != result.DispatchIDs[0] {
		t.Errorf("claimed ID = %q, want %q", claimed.ID, result.DispatchIDs[0])
	}
}

func TestClaimNextDispatch_HandleFilter_EphemeralPickedUpByRegistered(t *testing.T) {
	// A registered worker CAN claim a NULL-handle dispatch (ephemeral fallback).
	db := openRouterTestDB(t)
	_, rs, ws, ds := newRouterStores(t, db)
	createRouterRun(t, rs, "run_r_hf2")

	w := registerWorker(t, ws, "coder.impl", "claude-code")

	// Enqueue a NULL-handle dispatch directly (simulating an ephemeral dispatch
	// created before the worker registered).
	d := &core.Dispatch{
		RunID:  "run_r_hf2",
		Role:   "coder.impl",
		Prompt: "do work",
		Inputs: map[string]interface{}{},
		// WorkerHandle: "" → NULL
	}
	if err := ds.EnqueueDispatch(context.Background(), d); err != nil {
		t.Fatalf("EnqueueDispatch: %v", err)
	}

	// Registered worker should be able to claim the NULL-handle dispatch.
	claimed, err := ds.ClaimNextDispatch(context.Background(), "coder.impl", w.Handle)
	if err != nil {
		t.Fatalf("ClaimNextDispatch: %v", err)
	}
	if claimed == nil {
		t.Fatal("registered worker could not claim NULL-handle dispatch")
	}
	if claimed.ID != d.ID {
		t.Errorf("claimed ID = %q, want %q", claimed.ID, d.ID)
	}
}
