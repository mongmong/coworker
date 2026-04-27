package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chris/coworker/core"
)

// RunStore handles run persistence with event-log-before-state writes.
type RunStore struct {
	db    *DB
	event *EventStore
}

// NewRunStore creates a RunStore.
func NewRunStore(db *DB, event *EventStore) *RunStore {
	return &RunStore{db: db, event: event}
}

// CreateRun creates a new run and writes a run.created event.
func (s *RunStore) CreateRun(ctx context.Context, run *core.Run) error {
	payload, err := json.Marshal(map[string]string{
		"run_id": run.ID,
		"mode":   run.Mode,
	})
	if err != nil {
		return fmt.Errorf("marshal run.created payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         run.ID,
		Kind:          core.EventRunCreated,
		SchemaVersion: 1,
		Payload:       string(payload),
		CreatedAt:     run.StartedAt,
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO runs (id, mode, state, started_at) VALUES (?, ?, ?, ?)`,
			run.ID, run.Mode, string(run.State),
			run.StartedAt.Format("2006-01-02T15:04:05Z"),
		)
		if err != nil {
			return fmt.Errorf("insert run: %w", err)
		}
		return nil
	})
}

// GetRun retrieves a run by ID.
func (s *RunStore) GetRun(ctx context.Context, id string) (*core.Run, error) {
	var run core.Run
	var stateStr, startedAtStr string
	var endedAtStr sql.NullString

	err := s.db.QueryRowContext(ctx,
		"SELECT id, mode, state, started_at, ended_at FROM runs WHERE id = ?", id,
	).Scan(&run.ID, &run.Mode, &stateStr, &startedAtStr, &endedAtStr)
	if err != nil {
		return nil, fmt.Errorf("get run %q: %w", id, err)
	}

	run.State = core.RunState(stateStr)
	run.StartedAt, _ = time.Parse("2006-01-02T15:04:05Z", startedAtStr)
	if endedAtStr.Valid {
		t, _ := time.Parse("2006-01-02T15:04:05Z", endedAtStr.String)
		run.EndedAt = &t
	}

	return &run, nil
}

// ListRuns retrieves all runs ordered by started_at descending.
func (s *RunStore) ListRuns(ctx context.Context) ([]*core.Run, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, mode, state, started_at, ended_at FROM runs ORDER BY started_at DESC")
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var runs []*core.Run
	for rows.Next() {
		var run core.Run
		var stateStr, startedAtStr string
		var endedAtStr sql.NullString
		if err := rows.Scan(&run.ID, &run.Mode, &stateStr, &startedAtStr, &endedAtStr); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		run.State = core.RunState(stateStr)
		run.StartedAt, _ = time.Parse("2006-01-02T15:04:05Z", startedAtStr)
		if endedAtStr.Valid {
			t, _ := time.Parse("2006-01-02T15:04:05Z", endedAtStr.String)
			run.EndedAt = &t
		}
		runs = append(runs, &run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	if runs == nil {
		runs = []*core.Run{}
	}
	return runs, nil
}

// CompleteRun marks a run as completed and writes a run.completed event.
func (s *RunStore) CompleteRun(ctx context.Context, runID string, state core.RunState) error {
	now := time.Now()
	payload, err := json.Marshal(map[string]string{
		"run_id": runID,
		"state":  string(state),
	})
	if err != nil {
		return fmt.Errorf("marshal run.completed payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventRunCompleted,
		SchemaVersion: 1,
		Payload:       string(payload),
		CreatedAt:     now,
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE runs SET state = ?, ended_at = ? WHERE id = ?",
			string(state), now.Format("2006-01-02T15:04:05Z"), runID,
		)
		return err
	})
}
