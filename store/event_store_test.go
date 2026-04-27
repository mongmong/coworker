package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

type recordingBus struct {
	published []*core.Event
}

func (b *recordingBus) Publish(event *core.Event) {
	b.published = append(b.published, event)
}

func (b *recordingBus) Subscribe(ch chan<- *core.Event) {}

func (b *recordingBus) Unsubscribe(ch chan<- *core.Event) {}

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

func TestWriteEventThenRow_PublishesCommittedEvent(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run_publish")

	bus := &recordingBus{}
	es := NewEventStore(db)
	es.Bus = bus

	ctx := context.Background()
	event := &core.Event{
		ID:            "evt_publish",
		RunID:         "run_publish",
		Kind:          core.EventRunCreated,
		SchemaVersion: 1,
		Payload:       `{"run_id":"run_publish"}`,
		CreatedAt:     time.Unix(20, 0).UTC(),
	}

	if err := es.WriteEventThenRow(ctx, event, nil); err != nil {
		t.Fatalf("WriteEventThenRow: %v", err)
	}

	if len(bus.published) != 1 {
		t.Fatalf("published events = %d, want 1", len(bus.published))
	}
	if bus.published[0] != event {
		t.Fatalf("published event pointer %p, want %p", bus.published[0], event)
	}
	if bus.published[0].ID != event.ID {
		t.Fatalf("published event ID = %q, want %q", bus.published[0].ID, event.ID)
	}
	if bus.published[0].Sequence != 1 {
		t.Fatalf("published event sequence = %d, want 1", bus.published[0].Sequence)
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

func TestWriteEventThenRow_ApplyFnFailsDoesNotPublish(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run_fail_publish")

	bus := &recordingBus{}
	es := NewEventStore(db)
	es.Bus = bus

	ctx := context.Background()
	event := &core.Event{
		ID:            "evt_fail_publish",
		RunID:         "run_fail_publish",
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		Payload:       `{"job_id":"job_fail_publish"}`,
		CreatedAt:     time.Unix(21, 0).UTC(),
	}

	err := es.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		return fmt.Errorf("simulated projection failure")
	})
	if err == nil {
		t.Fatal("expected error from failed applyFn, got nil")
	}

	if len(bus.published) != 0 {
		t.Fatalf("published events = %d, want 0", len(bus.published))
	}

	events, listErr := es.ListEvents(ctx, "run_fail_publish")
	if listErr != nil {
		t.Fatalf("ListEvents: %v", listErr)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events after rollback, got %d", len(events))
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

func TestWriteEventThenRow_NilBusDoesNotPublish(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run_nil_bus")

	es := NewEventStore(db)
	ctx := context.Background()
	event := &core.Event{
		ID:            "evt_nil_bus",
		RunID:         "run_nil_bus",
		Kind:          core.EventRunCreated,
		SchemaVersion: 1,
		Payload:       `{"run_id":"run_nil_bus"}`,
		CreatedAt:     time.Unix(22, 0).UTC(),
	}

	if err := es.WriteEventThenRow(ctx, event, nil); err != nil {
		t.Fatalf("WriteEventThenRow: %v", err)
	}

	events, err := es.ListEvents(ctx, "run_nil_bus")
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

func TestListEvents_CreatedAtParsedCorrectly(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run-ts-1")
	es := NewEventStore(db)
	ctx := context.Background()

	// Use a known timestamp with UTC offset to verify round-trip.
	knownTime := time.Date(2026, 4, 26, 15, 30, 0, 0, time.UTC)
	event := &core.Event{
		ID:            "evt-ts-1",
		RunID:         "run-ts-1",
		Kind:          core.EventRunCreated,
		SchemaVersion: 1,
		Payload:       `{}`,
		CreatedAt:     knownTime,
	}

	if err := es.WriteEventThenRow(ctx, event, nil); err != nil {
		t.Fatalf("WriteEventThenRow: %v", err)
	}

	events, err := es.ListEvents(ctx, "run-ts-1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	got := events[0].CreatedAt
	if got.IsZero() {
		t.Error("expected non-zero CreatedAt, got zero")
	}
	if !got.Equal(knownTime) {
		t.Errorf("CreatedAt = %v, want %v", got, knownTime)
	}
}

func TestListEvents_MalformedTimestamp_ReturnsError(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run-ts-bad")
	es := NewEventStore(db)
	ctx := context.Background()

	// Insert a row with a deliberately malformed timestamp via raw SQL.
	_, execErr := db.ExecContext(ctx,
		`INSERT INTO events (id, run_id, sequence, kind, schema_version, payload, created_at)
		 VALUES ('evt-ts-bad', 'run-ts-bad', 1, 'run.created', 1, '{}', 'not-a-timestamp')`,
	)
	if execErr != nil {
		t.Fatalf("insert malformed event: %v", execErr)
	}

	_, err := es.ListEvents(ctx, "run-ts-bad")
	if err == nil {
		t.Error("expected error for malformed timestamp, got nil")
	}
}
