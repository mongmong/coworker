package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func setupTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func insertTestRun(t *testing.T, db *DB, runID string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO runs (id, mode, state, started_at) VALUES (?, 'interactive', 'active', ?)`,
		runID, time.Now().Format("2006-01-02T15:04:05Z"),
	)
	if err != nil {
		t.Fatalf("insert test run: %v", err)
	}
}

func TestWriteEventThenRow_WritesEventAndApplies(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run1")
	es := NewEventStore(db)
	ctx := context.Background()

	applyCalled := false
	event := &core.Event{
		ID:            "evt1",
		RunID:         "run1",
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		Payload:       `{"job_id":"j1"}`,
		CreatedAt:     time.Now(),
	}

	err := es.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		applyCalled = true
		_, err := tx.Exec(`INSERT INTO jobs (id, run_id, role, state, dispatched_by, cli, started_at)
			VALUES ('j1', 'run1', 'reviewer.arch', 'pending', 'scheduler', 'codex', ?)`,
			time.Now().Format("2006-01-02T15:04:05Z"))
		return err
	})
	if err != nil {
		t.Fatalf("WriteEventThenRow: %v", err)
	}

	if !applyCalled {
		t.Error("applyFn was not called")
	}

	// Verify event was written.
	events, err := es.ListEvents(ctx, "run1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].ID != "evt1" {
		t.Errorf("event ID = %q, want %q", events[0].ID, "evt1")
	}
	if events[0].Sequence != 1 {
		t.Errorf("event sequence = %d, want 1", events[0].Sequence)
	}

	// Verify projection (job) was written.
	var jobID string
	err = db.QueryRow("SELECT id FROM jobs WHERE id = 'j1'").Scan(&jobID)
	if err != nil {
		t.Errorf("job not found: %v", err)
	}
}

func TestWriteEventThenRow_ApplyFnFailsRollsBackBoth(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run1")
	es := NewEventStore(db)
	ctx := context.Background()

	event := &core.Event{
		ID:            "evt_fail",
		RunID:         "run1",
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		Payload:       `{"test":"fail"}`,
		CreatedAt:     time.Now(),
	}

	err := es.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		return fmt.Errorf("simulated projection failure")
	})
	if err == nil {
		t.Fatal("expected error from failed applyFn, got nil")
	}

	// Both event and projection should be rolled back.
	events, err := es.ListEvents(ctx, "run1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events after rollback, got %d", len(events))
	}
}

func TestWriteEventThenRow_NilApplyFn(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run1")
	es := NewEventStore(db)
	ctx := context.Background()

	event := &core.Event{
		ID:            "evt_noproject",
		RunID:         "run1",
		Kind:          core.EventRunCreated,
		SchemaVersion: 1,
		Payload:       `{}`,
		CreatedAt:     time.Now(),
	}

	err := es.WriteEventThenRow(ctx, event, nil)
	if err != nil {
		t.Fatalf("WriteEventThenRow with nil applyFn: %v", err)
	}

	events, err := es.ListEvents(ctx, "run1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestWriteEventThenRow_SequenceAutoIncrement(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run1")
	es := NewEventStore(db)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		event := &core.Event{
			ID:            fmt.Sprintf("evt_%d", i),
			RunID:         "run1",
			Kind:          core.EventJobCreated,
			SchemaVersion: 1,
			Payload:       fmt.Sprintf(`{"i":%d}`, i),
			CreatedAt:     time.Now(),
		}
		if err := es.WriteEventThenRow(ctx, event, nil); err != nil {
			t.Fatalf("write event %d: %v", i, err)
		}
	}

	events, err := es.ListEvents(ctx, "run1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	for i, e := range events {
		if e.Sequence != i+1 {
			t.Errorf("event %d sequence = %d, want %d", i, e.Sequence, i+1)
		}
	}
}

func TestWriteEventIdempotent_DuplicateKeySkips(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run1")
	es := NewEventStore(db)
	ctx := context.Background()

	event1 := &core.Event{
		ID:             "evt_idem1",
		RunID:          "run1",
		Kind:           core.EventJobCreated,
		SchemaVersion:  1,
		IdempotencyKey: "unique-key-1",
		Payload:        `{"first":true}`,
		CreatedAt:      time.Now(),
	}

	written, err := es.WriteEventIdempotent(ctx, event1, nil)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if !written {
		t.Error("first write should return true")
	}

	// Second write with same idempotency key should be skipped.
	event2 := &core.Event{
		ID:             "evt_idem2",
		RunID:          "run1",
		Kind:           core.EventJobCreated,
		SchemaVersion:  1,
		IdempotencyKey: "unique-key-1",
		Payload:        `{"second":true}`,
		CreatedAt:      time.Now(),
	}

	written, err = es.WriteEventIdempotent(ctx, event2, nil)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if written {
		t.Error("second write with same key should return false")
	}

	// Only one event should exist.
	events, err := es.ListEvents(ctx, "run1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].ID != "evt_idem1" {
		t.Errorf("event ID = %q, want %q", events[0].ID, "evt_idem1")
	}
}

func TestWriteEventIdempotent_EmptyKeyReturnsError(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ctx := context.Background()

	event := &core.Event{
		ID:            "evt_nokey",
		RunID:         "run1",
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		Payload:       `{}`,
		CreatedAt:     time.Now(),
	}

	_, err := es.WriteEventIdempotent(ctx, event, nil)
	if err == nil {
		t.Error("expected error for empty idempotency key, got nil")
	}
}

func TestListEvents_EmptyRun(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ctx := context.Background()

	events, err := es.ListEvents(ctx, "nonexistent-run")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}
