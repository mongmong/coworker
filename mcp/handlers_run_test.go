package mcp_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	mcpserver "github.com/chris/coworker/mcp"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// openTestDB opens an in-memory SQLite DB and registers cleanup.
func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open(:memory:): %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// createTestRun creates a run via RunStore and returns the run.
func createTestRun(t *testing.T, rs *store.RunStore, id, mode string) *core.Run {
	t.Helper()
	run := &core.Run{
		ID:        id,
		Mode:      mode,
		State:     core.RunStateActive,
		StartedAt: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
	}
	if err := rs.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("CreateRun(%q): %v", id, err)
	}
	return run
}

// newServerWithDB builds a Server wired to the given DB.
func newServerWithDB(t *testing.T, db *store.DB) *mcpserver.Server {
	t.Helper()
	s, err := mcpserver.NewServer(mcpserver.ServerConfig{DB: db})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return s
}

// callTool invokes one of the server's handler functions directly by building
// the store layer and calling the exported RunStatus / RunInspect helpers.
// Because the MCP SDK wraps handlers in a transport, we test the handler
// functions via the stores rather than going through the protocol layer.

// --- orch_run_status tests ---

func TestHandleRunStatus_HappyPath(t *testing.T) {
	db := openTestDB(t)
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)

	run := createTestRun(t, rs, "run_status_1", "interactive")
	_ = run

	s := newServerWithDB(t, db)
	_ = s // server wired — verify through handler directly

	// Call handler via the exported wrapper that the server uses internally.
	out, err := mcpserver.CallRunStatus(context.Background(), rs, "run_status_1")
	if err != nil {
		t.Fatalf("CallRunStatus: %v", err)
	}

	if out["run_id"] != "run_status_1" {
		t.Errorf("run_id = %q, want %q", out["run_id"], "run_status_1")
	}
	if out["mode"] != "interactive" {
		t.Errorf("mode = %q, want %q", out["mode"], "interactive")
	}
	if out["state"] != "active" {
		t.Errorf("state = %q, want %q", out["state"], "active")
	}
	if _, ok := out["started_at"]; !ok {
		t.Error("started_at missing from output")
	}
	if _, ok := out["ended_at"]; ok {
		t.Error("ended_at should be absent for an active run")
	}
}

func TestHandleRunStatus_CompletedRun(t *testing.T) {
	db := openTestDB(t)
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)

	createTestRun(t, rs, "run_done", "autopilot")
	if err := rs.CompleteRun(context.Background(), "run_done", core.RunStateCompleted); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}

	out, err := mcpserver.CallRunStatus(context.Background(), rs, "run_done")
	if err != nil {
		t.Fatalf("CallRunStatus: %v", err)
	}
	if out["state"] != "completed" {
		t.Errorf("state = %q, want %q", out["state"], "completed")
	}
	if _, ok := out["ended_at"]; !ok {
		t.Error("ended_at should be set for a completed run")
	}
}

func TestHandleRunStatus_MissingRunID(t *testing.T) {
	db := openTestDB(t)
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)

	_, err := mcpserver.CallRunStatus(context.Background(), rs, "")
	if err == nil {
		t.Fatal("expected error for empty run_id, got nil")
	}
}

func TestHandleRunStatus_NotFound(t *testing.T) {
	db := openTestDB(t)
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)

	_, err := mcpserver.CallRunStatus(context.Background(), rs, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent run, got nil")
	}
}

// --- orch_run_inspect tests ---

func TestHandleRunInspect_HappyPath(t *testing.T) {
	db := openTestDB(t)
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)

	createTestRun(t, rs, "run_inspect_1", "interactive")

	out, err := mcpserver.CallRunInspect(context.Background(), rs, es, "run_inspect_1")
	if err != nil {
		t.Fatalf("CallRunInspect: %v", err)
	}

	// Check run sub-object.
	runObj, ok := out["run"].(map[string]interface{})
	if !ok {
		t.Fatalf("run field missing or wrong type: %T", out["run"])
	}
	if runObj["id"] != "run_inspect_1" {
		t.Errorf("run.id = %q, want %q", runObj["id"], "run_inspect_1")
	}
	if runObj["mode"] != "interactive" {
		t.Errorf("run.mode = %q, want %q", runObj["mode"], "interactive")
	}

	// createTestRun writes one event (run.created).
	eventsRaw, ok := out["events"]
	if !ok {
		t.Fatal("events field missing")
	}
	events, ok := eventsRaw.([]interface{})
	if !ok {
		t.Fatalf("events wrong type: %T", eventsRaw)
	}
	if len(events) < 1 {
		t.Errorf("expected at least 1 event, got %d", len(events))
	}

	count, ok := out["event_count"].(float64)
	if !ok {
		t.Fatalf("event_count wrong type: %T", out["event_count"])
	}
	if int(count) != len(events) {
		t.Errorf("event_count = %d, want %d", int(count), len(events))
	}
}

func TestHandleRunInspect_EventsPopulated(t *testing.T) {
	db := openTestDB(t)
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)

	createTestRun(t, rs, "run_events", "autopilot")
	// Complete the run to add a second event.
	if err := rs.CompleteRun(context.Background(), "run_events", core.RunStateCompleted); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}

	out, err := mcpserver.CallRunInspect(context.Background(), rs, es, "run_events")
	if err != nil {
		t.Fatalf("CallRunInspect: %v", err)
	}

	events := out["events"].([]interface{})
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}

	count := int(out["event_count"].(float64))
	if count != 2 {
		t.Errorf("event_count = %d, want 2", count)
	}
}

func TestHandleRunInspect_MissingRunID(t *testing.T) {
	db := openTestDB(t)
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)

	_, err := mcpserver.CallRunInspect(context.Background(), rs, es, "")
	if err == nil {
		t.Fatal("expected error for empty run_id, got nil")
	}
}

func TestHandleRunInspect_NotFound(t *testing.T) {
	db := openTestDB(t)
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)

	_, err := mcpserver.CallRunInspect(context.Background(), rs, es, "ghost")
	if err == nil {
		t.Fatal("expected error for nonexistent run, got nil")
	}
}

func TestHandleRunInspect_EmptyEventsNotNull(t *testing.T) {
	// Manually insert a run without going through RunStore.CreateRun to bypass
	// the automatic run.created event and get zero events.
	db := openTestDB(t)
	_, err := db.Exec(
		`INSERT INTO runs (id, mode, state, started_at) VALUES (?, ?, ?, ?)`,
		"run_no_events", "interactive", "active",
		time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)

	out, err := mcpserver.CallRunInspect(context.Background(), rs, es, "run_no_events")
	if err != nil {
		t.Fatalf("CallRunInspect: %v", err)
	}

	// events must be a non-nil JSON array (not null).
	b, err := json.Marshal(out["events"])
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	if string(b) == "null" {
		t.Error("events field should be [] not null for a run with no events")
	}
}
