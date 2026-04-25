package mcp_test

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/core"
	mcpserver "github.com/chris/coworker/mcp"
	"github.com/chris/coworker/store"
)

// newStoresForWatchdog builds the worker and dispatch stores needed for watchdog tests.
func newStoresForWatchdog(t *testing.T, db *store.DB) (*store.WorkerStore, *store.DispatchStore) {
	t.Helper()
	es := store.NewEventStore(db)
	ws := store.NewWorkerStore(db, es)
	ds := store.NewDispatchStore(db, es)
	return ws, ds
}

// createTestWorkerRun creates a run for dispatch FK satisfaction.
func createTestWorkerRun(t *testing.T, db *store.DB, runID string) {
	t.Helper()
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)
	createTestRun(t, rs, runID, "interactive")
}

// backdateWorkerHeartbeat sets last_heartbeat_at on a worker to an old time,
// so watchdog stale/eviction triggers without wall-clock sleeping.
func backdateWorkerHeartbeat(t *testing.T, db *store.DB, handle string, age time.Duration) {
	t.Helper()
	old := time.Now().Add(-age).UTC().Format(time.RFC3339)
	_, err := db.ExecContext(context.Background(),
		`UPDATE workers SET last_heartbeat_at = ? WHERE handle = ?`,
		old, handle,
	)
	if err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}
}

// tickWatchdog calls the watchdog's internal tick once synchronously by running
// the watchdog for two tick intervals then cancelling. It is faster than
// relying on wall-clock time.
func tickWatchdogOnce(t *testing.T, ws *store.WorkerStore, ds *store.DispatchStore, cfg mcpserver.WatchdogConfig) {
	t.Helper()
	wd := mcpserver.NewHeartbeatWatchdog(ws, ds, cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go wd.Run(ctx)
	// Wait 2× interval so at least one tick fires.
	time.Sleep(cfg.Interval * 2)
	cancel()
}

func TestWatchdog_DefaultConfig(t *testing.T) {
	// Just verify the defaults resolve correctly via NewHeartbeatWatchdog.
	db := openTestDB(t)
	ws, ds := newStoresForWatchdog(t, db)

	cfg := mcpserver.WatchdogConfig{} // zero → defaults
	wd := mcpserver.NewHeartbeatWatchdog(ws, ds, cfg, nil)
	if wd == nil {
		t.Fatal("NewHeartbeatWatchdog returned nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()    // cancel immediately
	wd.Run(ctx) // must return promptly
}

func TestWatchdog_StaleDetection(t *testing.T) {
	db := openTestDB(t)
	ws, ds := newStoresForWatchdog(t, db)

	// Register a worker.
	reg, err := mcpserver.CallRegister(context.Background(), ws, "developer", "claude-code", "sess-w1", 100)
	if err != nil {
		t.Fatalf("CallRegister: %v", err)
	}
	handle, _ := reg["handle"].(string)

	// Backdate the heartbeat so the watchdog sees it as stale.
	// heartbeat timestamps are RFC3339 (second precision); use 60 s to be safe.
	backdateWorkerHeartbeat(t, db, handle, 60*time.Second)

	// Run watchdog with StaleAfter=30s (< 60s backdate) and long EvictAfter.
	cfg := mcpserver.WatchdogConfig{
		Interval:   20 * time.Millisecond,
		StaleAfter: 30 * time.Second,
		EvictAfter: 1 * time.Hour, // do not evict in this test
	}
	tickWatchdogOnce(t, ws, ds, cfg)

	// Worker should now be stale (not evicted — EvictAfter=1h).
	w, err := ws.GetWorker(context.Background(), handle)
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if w == nil {
		t.Fatal("worker not found")
	}
	if w.State == core.WorkerStateLive {
		t.Errorf("worker state = %q, expected stale or evicted", w.State)
	}
}

func TestWatchdog_EvictionAndRequeue(t *testing.T) {
	db := openTestDB(t)
	ws, ds := newStoresForWatchdog(t, db)

	// Create a run for the dispatch FK.
	createTestWorkerRun(t, db, "run_wd_evict")

	// Register a worker and backdate its heartbeat.
	reg, err := mcpserver.CallRegister(context.Background(), ws, "developer", "claude-code", "sess-w2", 200)
	if err != nil {
		t.Fatalf("CallRegister: %v", err)
	}
	handle, _ := reg["handle"].(string)
	backdateWorkerHeartbeat(t, db, handle, 60*time.Second)

	// Directly insert a leased dispatch associated with this worker.
	// (In production, Phase 4 will set worker_handle during dispatch routing.
	// Here we insert directly to test the requeue path without needing
	// the full Phase 4 dispatch routing.)
	dispatchID := "wd-evict-dispatch-001"
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO dispatches
			(id, run_id, role, inputs, state, worker_handle, created_at, leased_at)
		VALUES (?, ?, ?, ?, 'leased', ?, datetime('now'), datetime('now'))`,
		dispatchID, "run_wd_evict", "developer", `{}`, handle,
	)
	if err != nil {
		t.Fatalf("insert leased dispatch: %v", err)
	}

	// Run two watchdog passes: first marks live→stale, second marks stale→evicted.
	cfg := mcpserver.WatchdogConfig{
		Interval:   20 * time.Millisecond,
		StaleAfter: 30 * time.Second, // < 60s backdate
		EvictAfter: 30 * time.Second, // same cutoff — evict on the following tick
	}

	wd := mcpserver.NewHeartbeatWatchdog(ws, ds, cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go wd.Run(ctx)
	// Let 4 ticks fire: live→stale on tick 1, stale→evicted on tick 2.
	time.Sleep(cfg.Interval * 4)
	cancel()

	// Worker should be evicted.
	w, err := ws.GetWorker(context.Background(), handle)
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if w == nil {
		t.Fatal("worker not found after eviction")
	}
	if w.State != core.WorkerStateEvicted {
		t.Errorf("worker state = %q, want evicted", w.State)
	}

	// The leased dispatch should have been requeued (state = pending).
	final, err := ds.GetDispatch(context.Background(), dispatchID)
	if err != nil {
		t.Fatalf("GetDispatch: %v", err)
	}
	if final == nil {
		t.Fatal("dispatch not found after requeue")
	}
	if final.State != core.DispatchStatePending {
		t.Errorf("dispatch state = %q, want pending", final.State)
	}
}

func TestWatchdog_ActiveHeartbeatKeepsWorkerLive(t *testing.T) {
	db := openTestDB(t)
	ws, ds := newStoresForWatchdog(t, db)

	// Register a worker.
	reg, err := mcpserver.CallRegister(context.Background(), ws, "developer", "claude-code", "sess-w3", 300)
	if err != nil {
		t.Fatalf("CallRegister: %v", err)
	}
	handle, _ := reg["handle"].(string)

	// Use a 30-second StaleAfter — the worker just registered so its
	// last_heartbeat_at is now. The watchdog should NOT mark it stale because
	// last_heartbeat_at > now-30s.
	cfg := mcpserver.WatchdogConfig{
		Interval:   20 * time.Millisecond,
		StaleAfter: 30 * time.Second, // worker just registered; won't be stale
		EvictAfter: 30 * time.Second,
	}
	tickWatchdogOnce(t, ws, ds, cfg)

	// Worker should still be live — it is well within the StaleAfter window.
	w, err := ws.GetWorker(context.Background(), handle)
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if w == nil {
		t.Fatal("worker not found")
	}
	if w.State != core.WorkerStateLive {
		t.Errorf("worker state = %q, want live (recent heartbeat should prevent eviction)", w.State)
	}
}

func TestWatchdog_WiredInServer(t *testing.T) {
	// Verify the watchdog is created when DB is provided (indirectly via NewServer).
	db := openTestDB(t)
	s, err := mcpserver.NewServer(mcpserver.ServerConfig{
		DB: db,
		WatchdogConfig: mcpserver.WatchdogConfig{
			Interval:   100 * time.Millisecond,
			StaleAfter: 30 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
	// The server has a watchdog wired (no public accessor — just check it doesn't
	// panic; Run itself is not called here since it blocks on stdio transport).
}
