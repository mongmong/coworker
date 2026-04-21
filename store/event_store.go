package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/chris/coworker/core"
)

// EventStore handles event persistence with the event-log-before-state invariant.
type EventStore struct {
	db *DB
}

// NewEventStore creates an EventStore backed by the given DB.
func NewEventStore(db *DB) *EventStore {
	return &EventStore{db: db}
}

// WriteEventThenRow writes the event first, then calls applyFn within
// the same transaction to update projection tables. This enforces the
// event-log-before-state invariant from the spec.
//
// The sequence number is auto-assigned as MAX(sequence)+1 for the run.
// If applyFn is nil, only the event is written.
func (s *EventStore) WriteEventThenRow(ctx context.Context, event *core.Event, applyFn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Auto-assign sequence number.
	var seq int
	err = tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(sequence), 0) + 1 FROM events WHERE run_id = ?",
		event.RunID,
	).Scan(&seq)
	if err != nil {
		return fmt.Errorf("compute sequence: %w", err)
	}
	event.Sequence = seq

	// Write the event first (event-log-before-state invariant).
	_, err = tx.ExecContext(ctx,
		`INSERT INTO events (id, run_id, sequence, kind, schema_version,
			idempotency_key, causation_id, correlation_id, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID,
		event.RunID,
		event.Sequence,
		string(event.Kind),
		event.SchemaVersion,
		nullableString(event.IdempotencyKey),
		nullableString(event.CausationID),
		nullableString(event.CorrelationID),
		event.Payload,
		event.CreatedAt.Format("2006-01-02T15:04:05Z"),
	)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}

	// Then apply the projection update.
	if applyFn != nil {
		if err := applyFn(tx); err != nil {
			return fmt.Errorf("apply projection: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// WriteEventIdempotent writes an event with an idempotency key.
// If the key already exists, the write is silently skipped (no error).
// Returns true if the event was written, false if it was a duplicate.
func (s *EventStore) WriteEventIdempotent(ctx context.Context, event *core.Event, applyFn func(tx *sql.Tx) error) (bool, error) {
	if event.IdempotencyKey == "" {
		return false, fmt.Errorf("idempotency key must not be empty")
	}

	// Check if already exists.
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events WHERE idempotency_key = ?",
		event.IdempotencyKey,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check idempotency key: %w", err)
	}
	if count > 0 {
		return false, nil
	}

	if err := s.WriteEventThenRow(ctx, event, applyFn); err != nil {
		return false, err
	}
	return true, nil
}

// ListEvents returns all events for a run, ordered by sequence.
func (s *EventStore) ListEvents(ctx context.Context, runID string) ([]core.Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, sequence, kind, schema_version,
			COALESCE(idempotency_key, ''), COALESCE(causation_id, ''), COALESCE(correlation_id, ''),
			payload, created_at
		FROM events WHERE run_id = ? ORDER BY sequence ASC`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var events []core.Event
	for rows.Next() {
		var e core.Event
		var kindStr, createdAtStr string
		err := rows.Scan(
			&e.ID, &e.RunID, &e.Sequence, &kindStr,
			&e.SchemaVersion, &e.IdempotencyKey, &e.CausationID,
			&e.CorrelationID, &e.Payload, &createdAtStr,
		)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.Kind = core.EventKind(kindStr)
		events = append(events, e)
	}
	return events, rows.Err()
}

// nullableString returns a *string for SQL nullable TEXT columns.
// Empty string is stored as NULL.
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
