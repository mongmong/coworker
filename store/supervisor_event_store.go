package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chris/coworker/core"
)

// SupervisorEventStore persists supervisor.verdict projection rows.
type SupervisorEventStore struct {
	db    *DB
	event *EventStore
}

// NewSupervisorEventStore creates a SupervisorEventStore.
func NewSupervisorEventStore(db *DB, event *EventStore) *SupervisorEventStore {
	return &SupervisorEventStore{db: db, event: event}
}

// RecordVerdict writes a supervisor.verdict event and the projection row in the same transaction.
func (s *SupervisorEventStore) RecordVerdict(ctx context.Context, runID, jobID string, result core.RuleResult) error {
	verdict := "pass"
	switch {
	case result.Skipped:
		verdict = "skipped"
	case !result.Passed:
		verdict = "fail"
	}
	payload, err := json.Marshal(map[string]any{
		"run_id":  runID,
		"job_id":  jobID,
		"verdict": verdict,
		"rule":    result.RuleName,
		"message": result.Message,
	})
	if err != nil {
		return fmt.Errorf("marshal supervisor.verdict: %w", err)
	}
	now := time.Now()
	ev := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventSupervisorVerdict,
		SchemaVersion: 1,
		CorrelationID: jobID,
		Payload:       string(payload),
		CreatedAt:     now,
	}
	return s.event.WriteEventThenRow(ctx, ev, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO supervisor_events
				(id, run_id, job_id, kind, verdict, rule_id, message, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			core.NewID(), runID, jobID, string(core.EventSupervisorVerdict),
			verdict, result.RuleName, result.Message, now.UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("insert supervisor_event: %w", err)
		}
		return nil
	})
}

// SupervisorEventRow is a row from the supervisor_events projection.
type SupervisorEventRow struct {
	ID        string
	RunID     string
	JobID     string
	Kind      string
	Verdict   string
	RuleID    string
	Message   string
	CreatedAt time.Time
}

// ListByJob lists supervisor events for a job.
func (s *SupervisorEventStore) ListByJob(ctx context.Context, jobID string) ([]*SupervisorEventRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, run_id, job_id, kind, verdict, rule_id, message, created_at
		FROM supervisor_events WHERE job_id = ?
		ORDER BY created_at ASC, id ASC`, jobID)
	if err != nil {
		return nil, fmt.Errorf("query supervisor_events: %w", err)
	}
	defer rows.Close()
	return scanSupervisorRows(rows)
}

// ListByRun lists supervisor events for a run.
func (s *SupervisorEventStore) ListByRun(ctx context.Context, runID string) ([]*SupervisorEventRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, run_id, job_id, kind, verdict, rule_id, message, created_at
		FROM supervisor_events WHERE run_id = ?
		ORDER BY created_at ASC, id ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("query supervisor_events by run: %w", err)
	}
	defer rows.Close()
	return scanSupervisorRows(rows)
}

func scanSupervisorRows(rows *sql.Rows) ([]*SupervisorEventRow, error) {
	var out []*SupervisorEventRow
	for rows.Next() {
		e := &SupervisorEventRow{}
		var createdAt string
		if err := rows.Scan(&e.ID, &e.RunID, &e.JobID, &e.Kind, &e.Verdict,
			&e.RuleID, &e.Message, &createdAt); err != nil {
			return nil, fmt.Errorf("scan supervisor_event: %w", err)
		}
		t, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse supervisor_event.created_at: %w", err)
		}
		e.CreatedAt = t
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("supervisor_event rows error: %w", err)
	}
	if out == nil {
		out = []*SupervisorEventRow{}
	}
	return out, nil
}

var _ core.SupervisorWriter = (*SupervisorEventStore)(nil)
