package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chris/coworker/core"
)

// DispatchStore manages the dispatch queue for the pull-model worker protocol.
// Workers poll ClaimNextDispatch to receive work and call CompleteDispatch when done.
type DispatchStore struct {
	db    *DB
	event *EventStore
}

// NewDispatchStore creates a DispatchStore.
func NewDispatchStore(db *DB, event *EventStore) *DispatchStore {
	return &DispatchStore{db: db, event: event}
}

// EnqueueDispatch inserts a pending dispatch and emits a dispatch.queued event.
func (s *DispatchStore) EnqueueDispatch(ctx context.Context, d *core.Dispatch) error {
	if d.ID == "" {
		d.ID = core.NewID()
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now()
	}
	d.State = core.DispatchStatePending

	inputsJSON, err := json.Marshal(d.Inputs)
	if err != nil {
		return fmt.Errorf("marshal dispatch inputs: %w", err)
	}

	payload, err := json.Marshal(map[string]string{
		"dispatch_id": d.ID,
		"run_id":      d.RunID,
		"role":        d.Role,
		"job_id":      d.JobID,
	})
	if err != nil {
		return fmt.Errorf("marshal dispatch.queued payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         d.RunID,
		Kind:          core.EventDispatchQueued,
		SchemaVersion: 1,
		CorrelationID: d.ID,
		Payload:       string(payload),
		CreatedAt:     d.CreatedAt,
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO dispatches
				(id, run_id, role, job_id, prompt, inputs, state, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			d.ID, d.RunID, d.Role,
			nullableString(d.JobID),
			nullableString(d.Prompt),
			string(inputsJSON),
			string(d.State),
			d.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		)
		return err
	})
}

// ClaimNextDispatch finds the oldest pending dispatch for the given role, sets
// its state to leased, records leased_at, emits a dispatch.leased event, and
// returns the claimed dispatch. Returns nil if no pending dispatch exists.
func (s *DispatchStore) ClaimNextDispatch(ctx context.Context, role string) (*core.Dispatch, error) {
	now := time.Now()

	// Find the oldest pending dispatch for the role.
	var d core.Dispatch
	var inputsJSON, createdAtStr string
	var jobID, prompt, workerHandle sql.NullString

	err := s.db.QueryRowContext(ctx,
		`SELECT id, run_id, role, job_id, prompt, inputs, state, worker_handle, created_at
		FROM dispatches
		WHERE state = 'pending' AND role = ?
		ORDER BY created_at ASC
		LIMIT 1`,
		role,
	).Scan(
		&d.ID, &d.RunID, &d.Role, &jobID, &prompt,
		&inputsJSON, &d.State, &workerHandle,
		&createdAtStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find pending dispatch: %w", err)
	}

	if jobID.Valid {
		d.JobID = jobID.String
	}
	if prompt.Valid {
		d.Prompt = prompt.String
	}
	if workerHandle.Valid {
		d.WorkerHandle = workerHandle.String
	}
	d.CreatedAt, _ = time.Parse("2006-01-02T15:04:05Z", createdAtStr)

	if err := json.Unmarshal([]byte(inputsJSON), &d.Inputs); err != nil {
		return nil, fmt.Errorf("unmarshal dispatch inputs: %w", err)
	}

	// Emit event then update state to leased.
	payload, err := json.Marshal(map[string]string{
		"dispatch_id": d.ID,
		"run_id":      d.RunID,
		"role":        d.Role,
		"job_id":      d.JobID,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal dispatch.leased payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         d.RunID,
		Kind:          core.EventDispatchLeased,
		SchemaVersion: 1,
		CorrelationID: d.ID,
		Payload:       string(payload),
		CreatedAt:     now,
	}

	if err := s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE dispatches SET state = 'leased', leased_at = ? WHERE id = ?`,
			now.UTC().Format("2006-01-02T15:04:05Z"), d.ID,
		)
		return err
	}); err != nil {
		return nil, fmt.Errorf("lease dispatch: %w", err)
	}

	d.State = core.DispatchStateLeased
	d.LeasedAt = &now
	return &d, nil
}

// CompleteDispatch marks a dispatch as completed and emits a dispatch.completed event.
func (s *DispatchStore) CompleteDispatch(ctx context.Context, dispatchID string, outputs map[string]interface{}) error {
	now := time.Now()

	// Look up run_id for the event.
	var runID string
	err := s.db.QueryRowContext(ctx,
		"SELECT run_id FROM dispatches WHERE id = ?", dispatchID,
	).Scan(&runID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("dispatch %q not found", dispatchID)
	}
	if err != nil {
		return fmt.Errorf("get run_id for dispatch %q: %w", dispatchID, err)
	}

	outputsJSON, err := json.Marshal(outputs)
	if err != nil {
		return fmt.Errorf("marshal outputs: %w", err)
	}

	payload, err := json.Marshal(map[string]string{
		"dispatch_id": dispatchID,
		"run_id":      runID,
		"outputs":     string(outputsJSON),
	})
	if err != nil {
		return fmt.Errorf("marshal dispatch.completed payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventDispatchCompleted,
		SchemaVersion: 1,
		CorrelationID: dispatchID,
		Payload:       string(payload),
		CreatedAt:     now,
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE dispatches SET state = 'completed', completed_at = ? WHERE id = ?`,
			now.UTC().Format("2006-01-02T15:04:05Z"), dispatchID,
		)
		return err
	})
}

// ExpireLeases finds all leased dispatches older than the given timeout, resets
// their state to pending, and emits dispatch.expired events for each.
func (s *DispatchStore) ExpireLeases(ctx context.Context, timeout time.Duration) error {
	cutoff := time.Now().Add(-timeout).UTC().Format("2006-01-02T15:04:05Z")

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id FROM dispatches WHERE state = 'leased' AND leased_at <= ?`,
		cutoff,
	)
	if err != nil {
		return fmt.Errorf("query expired leases: %w", err)
	}
	defer rows.Close()

	type expiredRow struct {
		id    string
		runID string
	}
	var expired []expiredRow
	for rows.Next() {
		var r expiredRow
		if err := rows.Scan(&r.id, &r.runID); err != nil {
			return fmt.Errorf("scan expired dispatch: %w", err)
		}
		expired = append(expired, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows error: %w", err)
	}

	now := time.Now()
	for _, r := range expired {
		payload, err := json.Marshal(map[string]string{
			"dispatch_id": r.id,
			"run_id":      r.runID,
		})
		if err != nil {
			return fmt.Errorf("marshal dispatch.expired payload: %w", err)
		}

		event := &core.Event{
			ID:            core.NewID(),
			RunID:         r.runID,
			Kind:          core.EventDispatchExpired,
			SchemaVersion: 1,
			CorrelationID: r.id,
			Payload:       string(payload),
			CreatedAt:     now,
		}

		if err := s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				`UPDATE dispatches SET state = 'pending', leased_at = NULL WHERE id = ?`,
				r.id,
			)
			return err
		}); err != nil {
			return fmt.Errorf("expire dispatch %q: %w", r.id, err)
		}
	}

	return nil
}

// GetDispatch retrieves a dispatch by ID.
func (s *DispatchStore) GetDispatch(ctx context.Context, id string) (*core.Dispatch, error) {
	var d core.Dispatch
	var inputsJSON, createdAtStr string
	var jobID, prompt, workerHandle, leasedAt, completedAt sql.NullString

	err := s.db.QueryRowContext(ctx,
		`SELECT id, run_id, role, job_id, prompt, inputs, state, worker_handle,
			leased_at, completed_at, created_at
		FROM dispatches WHERE id = ?`,
		id,
	).Scan(
		&d.ID, &d.RunID, &d.Role, &jobID, &prompt,
		&inputsJSON, &d.State, &workerHandle,
		&leasedAt, &completedAt, &createdAtStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get dispatch %q: %w", id, err)
	}

	if jobID.Valid {
		d.JobID = jobID.String
	}
	if prompt.Valid {
		d.Prompt = prompt.String
	}
	if workerHandle.Valid {
		d.WorkerHandle = workerHandle.String
	}
	d.CreatedAt, _ = time.Parse("2006-01-02T15:04:05Z", createdAtStr)
	if leasedAt.Valid {
		t, _ := time.Parse("2006-01-02T15:04:05Z", leasedAt.String)
		d.LeasedAt = &t
	}
	if completedAt.Valid {
		t, _ := time.Parse("2006-01-02T15:04:05Z", completedAt.String)
		d.CompletedAt = &t
	}

	if err := json.Unmarshal([]byte(inputsJSON), &d.Inputs); err != nil {
		return nil, fmt.Errorf("unmarshal dispatch inputs: %w", err)
	}

	return &d, nil
}
