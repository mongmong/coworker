package mcp_test

import (
	"context"
	"testing"

	mcpserver "github.com/chris/coworker/mcp"
	"github.com/chris/coworker/store"
)

// newWorkerStore creates a WorkerStore backed by an in-memory test DB.
func newWorkerStore(t *testing.T, db *store.DB) *store.WorkerStore {
	t.Helper()
	es := store.NewEventStore(db)
	return store.NewWorkerStore(db, es)
}

// ---- orch_register tests ----------------------------------------------------

func TestHandleRegister_HappyPath(t *testing.T) {
	db := openTestDB(t)
	ws := newWorkerStore(t, db)

	out, err := mcpserver.CallRegister(context.Background(), ws, "developer", "claude-code", "sess-1", 1234)
	if err != nil {
		t.Fatalf("CallRegister: %v", err)
	}

	if out["status"] != "registered" {
		t.Errorf("status = %q, want %q", out["status"], "registered")
	}
	handle, ok := out["handle"].(string)
	if !ok || handle == "" {
		t.Errorf("handle missing or empty: %v", out["handle"])
	}
}

func TestHandleRegister_MissingRole(t *testing.T) {
	db := openTestDB(t)
	ws := newWorkerStore(t, db)

	_, err := mcpserver.CallRegister(context.Background(), ws, "", "claude-code", "sess-1", 1234)
	if err == nil {
		t.Fatal("expected error for empty role, got nil")
	}
}

func TestHandleRegister_MissingCLI(t *testing.T) {
	db := openTestDB(t)
	ws := newWorkerStore(t, db)

	_, err := mcpserver.CallRegister(context.Background(), ws, "developer", "", "sess-1", 1234)
	if err == nil {
		t.Fatal("expected error for empty cli, got nil")
	}
}

func TestHandleRegister_HandleIsUnique(t *testing.T) {
	db := openTestDB(t)
	ws := newWorkerStore(t, db)

	out1, err := mcpserver.CallRegister(context.Background(), ws, "developer", "claude-code", "sess-1", 1001)
	if err != nil {
		t.Fatalf("CallRegister 1: %v", err)
	}
	out2, err := mcpserver.CallRegister(context.Background(), ws, "developer", "claude-code", "sess-2", 1002)
	if err != nil {
		t.Fatalf("CallRegister 2: %v", err)
	}

	h1, _ := out1["handle"].(string)
	h2, _ := out2["handle"].(string)
	if h1 == h2 {
		t.Errorf("expected unique handles, both returned %q", h1)
	}
}

// ---- orch_heartbeat tests ---------------------------------------------------

func TestHandleHeartbeat_HappyPath(t *testing.T) {
	db := openTestDB(t)
	ws := newWorkerStore(t, db)

	reg, err := mcpserver.CallRegister(context.Background(), ws, "developer", "claude-code", "sess-1", 1234)
	if err != nil {
		t.Fatalf("CallRegister: %v", err)
	}
	handle, _ := reg["handle"].(string)

	out, err := mcpserver.CallHeartbeat(context.Background(), ws, handle)
	if err != nil {
		t.Fatalf("CallHeartbeat: %v", err)
	}
	if out["status"] != "ok" {
		t.Errorf("status = %q, want %q", out["status"], "ok")
	}
}

func TestHandleHeartbeat_MissingHandle(t *testing.T) {
	db := openTestDB(t)
	ws := newWorkerStore(t, db)

	_, err := mcpserver.CallHeartbeat(context.Background(), ws, "")
	if err == nil {
		t.Fatal("expected error for empty handle, got nil")
	}
}

func TestHandleHeartbeat_UnknownHandle(t *testing.T) {
	db := openTestDB(t)
	ws := newWorkerStore(t, db)

	_, err := mcpserver.CallHeartbeat(context.Background(), ws, "nonexistent-handle")
	if err == nil {
		t.Fatal("expected error for unknown handle, got nil")
	}
}

func TestHandleHeartbeat_EvictedWorkerRejected(t *testing.T) {
	db := openTestDB(t)
	ws := newWorkerStore(t, db)

	// Register then immediately deregister (evict).
	reg, err := mcpserver.CallRegister(context.Background(), ws, "developer", "claude-code", "sess-1", 1234)
	if err != nil {
		t.Fatalf("CallRegister: %v", err)
	}
	handle, _ := reg["handle"].(string)

	if _, err := mcpserver.CallDeregister(context.Background(), ws, handle); err != nil {
		t.Fatalf("CallDeregister: %v", err)
	}

	// Heartbeat on evicted worker must fail.
	_, err = mcpserver.CallHeartbeat(context.Background(), ws, handle)
	if err == nil {
		t.Fatal("expected error for heartbeat on evicted worker, got nil")
	}
}

// ---- orch_deregister tests --------------------------------------------------

func TestHandleDeregister_HappyPath(t *testing.T) {
	db := openTestDB(t)
	ws := newWorkerStore(t, db)

	reg, err := mcpserver.CallRegister(context.Background(), ws, "developer", "claude-code", "sess-1", 1234)
	if err != nil {
		t.Fatalf("CallRegister: %v", err)
	}
	handle, _ := reg["handle"].(string)

	out, err := mcpserver.CallDeregister(context.Background(), ws, handle)
	if err != nil {
		t.Fatalf("CallDeregister: %v", err)
	}
	if out["status"] != "deregistered" {
		t.Errorf("status = %q, want %q", out["status"], "deregistered")
	}
}

func TestHandleDeregister_MissingHandle(t *testing.T) {
	db := openTestDB(t)
	ws := newWorkerStore(t, db)

	_, err := mcpserver.CallDeregister(context.Background(), ws, "")
	if err == nil {
		t.Fatal("expected error for empty handle, got nil")
	}
}

func TestHandleDeregister_UnknownHandle(t *testing.T) {
	db := openTestDB(t)
	ws := newWorkerStore(t, db)

	_, err := mcpserver.CallDeregister(context.Background(), ws, "nonexistent-handle")
	if err == nil {
		t.Fatal("expected error for unknown handle, got nil")
	}
}

// ---- full register → heartbeat → deregister cycle --------------------------

func TestRegistry_FullCycle(t *testing.T) {
	db := openTestDB(t)
	ws := newWorkerStore(t, db)

	// Step 1: register.
	reg, err := mcpserver.CallRegister(context.Background(), ws, "developer", "claude-code", "sess-abc", 5678)
	if err != nil {
		t.Fatalf("CallRegister: %v", err)
	}
	if reg["status"] != "registered" {
		t.Fatalf("register status = %q, want registered", reg["status"])
	}
	handle, _ := reg["handle"].(string)
	if handle == "" {
		t.Fatal("handle is empty after register")
	}

	// Step 2: heartbeat.
	hb, err := mcpserver.CallHeartbeat(context.Background(), ws, handle)
	if err != nil {
		t.Fatalf("CallHeartbeat: %v", err)
	}
	if hb["status"] != "ok" {
		t.Errorf("heartbeat status = %q, want ok", hb["status"])
	}

	// Step 3: deregister.
	dereg, err := mcpserver.CallDeregister(context.Background(), ws, handle)
	if err != nil {
		t.Fatalf("CallDeregister: %v", err)
	}
	if dereg["status"] != "deregistered" {
		t.Errorf("deregister status = %q, want deregistered", dereg["status"])
	}

	// Step 4: heartbeat after deregister must fail.
	_, err = mcpserver.CallHeartbeat(context.Background(), ws, handle)
	if err == nil {
		t.Fatal("expected error for heartbeat after deregister, got nil")
	}
}

// ---- stub server tests for registry tools -----------------------------------

func TestHandleRegister_NilWorkerStore_ReturnsStub(t *testing.T) {
	s, err := mcpserver.NewServer(mcpserver.ServerConfig{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	tools := s.Tools()
	found := false
	for _, name := range tools {
		if name == "orch_register" {
			found = true
			break
		}
	}
	if !found {
		t.Error("orch_register tool should be registered even without a WorkerStore")
	}
}
