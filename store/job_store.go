package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chris/coworker/core"
)

// JobStore handles job persistence with event-log-before-state writes.
type JobStore struct {
	db    *DB
	event *EventStore
}

// NewJobStore creates a JobStore.
func NewJobStore(db *DB, event *EventStore) *JobStore {
	return &JobStore{db: db, event: event}
}

// CreateJob creates a new job and writes a job.created event.
func (s *JobStore) CreateJob(ctx context.Context, job *core.Job) error {
	payload, err := json.Marshal(map[string]string{
		"job_id": job.ID,
		"run_id": job.RunID,
		"role":   job.Role,
		"cli":    job.CLI,
	})
	if err != nil {
		return fmt.Errorf("marshal job.created payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         job.RunID,
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		CorrelationID: job.ID,
		Payload:       string(payload),
		CreatedAt:     job.StartedAt,
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO jobs (id, run_id, role, state, dispatched_by, cli, started_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			job.ID, job.RunID, job.Role, string(job.State),
			job.DispatchedBy, job.CLI,
			job.StartedAt.Format("2006-01-02T15:04:05Z"),
		)
		return err
	})
}

// UpdateJobState updates the state of a job and writes the appropriate event.
func (s *JobStore) UpdateJobState(ctx context.Context, jobID string, newState core.JobState) error {
	now := time.Now()

	var eventKind core.EventKind
	switch newState {
	case core.JobStateComplete:
		eventKind = core.EventJobCompleted
	case core.JobStateFailed:
		eventKind = core.EventJobFailed
	case core.JobStateDispatched:
		eventKind = core.EventJobLeased
	default:
		eventKind = core.EventKind("job.state_changed")
	}

	payload, err := json.Marshal(map[string]string{
		"job_id": jobID,
		"state":  string(newState),
	})
	if err != nil {
		return fmt.Errorf("marshal job state payload: %w", err)
	}

	// Look up run_id for the event.
	var runID string
	err = s.db.QueryRowContext(ctx, "SELECT run_id FROM jobs WHERE id = ?", jobID).Scan(&runID)
	if err != nil {
		return fmt.Errorf("get run_id for job %q: %w", jobID, err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          eventKind,
		SchemaVersion: 1,
		CorrelationID: jobID,
		Payload:       string(payload),
		CreatedAt:     now,
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		var setEndedAt string
		if newState == core.JobStateComplete || newState == core.JobStateFailed || newState == core.JobStateCancelled {
			setEndedAt = ", ended_at = '" + now.Format("2006-01-02T15:04:05Z") + "'"
		}
		_, err := tx.ExecContext(ctx,
			"UPDATE jobs SET state = ?"+setEndedAt+" WHERE id = ?",
			string(newState), jobID,
		)
		return err
	})
}

// GetJob retrieves a job by ID.
func (s *JobStore) GetJob(ctx context.Context, id string) (*core.Job, error) {
	var job core.Job
	var stateStr, startedAtStr string
	var endedAtStr sql.NullString

	err := s.db.QueryRowContext(ctx,
		"SELECT id, run_id, role, state, dispatched_by, cli, started_at, ended_at FROM jobs WHERE id = ?", id,
	).Scan(&job.ID, &job.RunID, &job.Role, &stateStr,
		&job.DispatchedBy, &job.CLI, &startedAtStr, &endedAtStr)
	if err != nil {
		return nil, fmt.Errorf("get job %q: %w", id, err)
	}

	job.State = core.JobState(stateStr)
	job.StartedAt, _ = time.Parse("2006-01-02T15:04:05Z", startedAtStr)
	if endedAtStr.Valid {
		t, _ := time.Parse("2006-01-02T15:04:05Z", endedAtStr.String)
		job.EndedAt = &t
	}

	return &job, nil
}
