package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chris/coworker/core"
)

// ErrCheckpointNotFound is returned when a requested checkpoint row does not exist.
var ErrCheckpointNotFound = errors.New("checkpoint not found")

// CheckpointStore handles checkpoint projection persistence with event-first writes.
type CheckpointStore struct {
	db    *DB
	event *EventStore
}

// NewCheckpointStore creates a CheckpointStore.
func NewCheckpointStore(db *DB, event *EventStore) *CheckpointStore {
	return &CheckpointStore{db: db, event: event}
}

// CreateCheckpoint writes a checkpoint.opened event and inserts the checkpoint row.
func (s *CheckpointStore) CreateCheckpoint(ctx context.Context, c core.CheckpointRecord) error {
	payload, err := json.Marshal(map[string]string{
		"checkpoint_id": c.ID,
		"run_id":        c.RunID,
		"plan_id":       c.PlanID,
		"kind":          c.Kind,
	})
	if err != nil {
		return fmt.Errorf("marshal checkpoint.opened: %w", err)
	}
	ev := &core.Event{
		ID:            core.NewID(),
		RunID:         c.RunID,
		Kind:          core.EventCheckpointOpened,
		SchemaVersion: 1,
		CorrelationID: c.ID,
		Payload:       string(payload),
		CreatedAt:     time.Now(),
	}
	var planID any
	if c.PlanID != "" {
		planID = c.PlanID
	}
	return s.event.WriteEventThenRow(ctx, ev, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO checkpoints (id, run_id, plan_id, kind, state, notes)
			VALUES (?, ?, ?, ?, 'open', ?)`,
			c.ID, c.RunID, planID, c.Kind, c.Notes,
		)
		if err != nil {
			return fmt.Errorf("insert checkpoint: %w", err)
		}
		return nil
	})
}

// ResolveCheckpoint writes a checkpoint.resolved event and marks the checkpoint resolved.
func (s *CheckpointStore) ResolveCheckpoint(ctx context.Context, id, decision, decidedBy, notes string) error {
	var runID string
	var state string
	if err := s.db.QueryRowContext(ctx,
		"SELECT run_id, state FROM checkpoints WHERE id = ?", id,
	).Scan(&runID, &state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCheckpointNotFound
		}
		return fmt.Errorf("lookup checkpoint: %w", err)
	}
	if state == "resolved" {
		return nil
	}

	payload, err := json.Marshal(map[string]string{
		"checkpoint_id": id,
		"decision":      decision,
		"decided_by":    decidedBy,
	})
	if err != nil {
		return fmt.Errorf("marshal checkpoint.resolved: %w", err)
	}
	now := time.Now()
	ev := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventCheckpointResolved,
		SchemaVersion: 1,
		CorrelationID: id,
		Payload:       string(payload),
		CreatedAt:     now,
	}
	return s.event.WriteEventThenRow(ctx, ev, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE checkpoints
			SET state = 'resolved',
				decision = ?,
				decided_by = ?,
				decided_at = ?,
				notes = ?
			WHERE id = ?`,
			decision, decidedBy, now.UTC().Format(time.RFC3339Nano), notes, id,
		)
		if err != nil {
			return fmt.Errorf("resolve checkpoint: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("resolve checkpoint rows affected: %w", err)
		}
		if n == 0 {
			return ErrCheckpointNotFound
		}
		return nil
	})
}

// CheckpointRow is a row from the checkpoints projection.
type CheckpointRow struct {
	ID        string
	RunID     string
	PlanID    string
	Kind      string
	State     string
	Decision  string
	DecidedBy string
	DecidedAt *time.Time
	Notes     string
}

// GetCheckpoint retrieves a checkpoint by ID.
func (s *CheckpointStore) GetCheckpoint(ctx context.Context, id string) (*CheckpointRow, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, run_id, plan_id, kind, state, decision, decided_by, decided_at, notes
		FROM checkpoints WHERE id = ?`, id)
	return s.scanCheckpoint(row)
}

// ListCheckpointsByRun retrieves checkpoints for a run with open rows first.
func (s *CheckpointStore) ListCheckpointsByRun(ctx context.Context, runID string) ([]*CheckpointRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, run_id, plan_id, kind, state, decision, decided_by, decided_at, notes
		FROM checkpoints WHERE run_id = ?
		ORDER BY (state = 'resolved') ASC, decided_at ASC, id ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("query checkpoints: %w", err)
	}
	defer rows.Close()

	var out []*CheckpointRow
	for rows.Next() {
		c, err := s.scanCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("checkpoint rows error: %w", err)
	}
	if out == nil {
		out = []*CheckpointRow{}
	}
	return out, nil
}

func (s *CheckpointStore) scanCheckpoint(r rowScanner) (*CheckpointRow, error) {
	c := &CheckpointRow{}
	var planID, decidedAt sql.NullString
	if err := r.Scan(&c.ID, &c.RunID, &planID, &c.Kind, &c.State,
		&c.Decision, &c.DecidedBy, &decidedAt, &c.Notes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrCheckpointNotFound
		}
		return nil, fmt.Errorf("scan checkpoint: %w", err)
	}
	if planID.Valid {
		c.PlanID = planID.String
	}
	if decidedAt.Valid {
		t, err := time.Parse(time.RFC3339Nano, decidedAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse checkpoint.decided_at: %w", err)
		}
		c.DecidedAt = &t
	}
	return c, nil
}

var _ core.CheckpointWriter = (*CheckpointStore)(nil)
