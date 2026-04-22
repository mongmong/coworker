package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chris/coworker/core"
)

// FindingStore handles finding persistence with immutability enforcement.
type FindingStore struct {
	db    *DB
	event *EventStore
}

// NewFindingStore creates a FindingStore.
func NewFindingStore(db *DB, event *EventStore) *FindingStore {
	return &FindingStore{db: db, event: event}
}

// InsertFinding creates a new finding, computing its fingerprint, and writes
// a finding.created event. The finding is immutable after creation -- only
// resolved_by_job_id and resolved_at can be updated via ResolveFinding.
func (s *FindingStore) InsertFinding(ctx context.Context, finding *core.Finding) error {
	// Compute fingerprint.
	finding.Fingerprint = core.ComputeFingerprint(
		finding.Path, finding.Line, finding.Severity, finding.Body,
	)

	payload, err := json.Marshal(map[string]interface{}{
		"finding_id":  finding.ID,
		"job_id":      finding.JobID,
		"path":        finding.Path,
		"line":        finding.Line,
		"severity":    finding.Severity,
		"fingerprint": finding.Fingerprint,
	})
	if err != nil {
		return fmt.Errorf("marshal finding.created payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         finding.RunID,
		Kind:          core.EventFindingCreated,
		SchemaVersion: 1,
		CorrelationID: finding.JobID,
		Payload:       string(payload),
		CreatedAt:     time.Now(),
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO findings (id, run_id, job_id, path, line, severity, body, fingerprint)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			finding.ID, finding.RunID, finding.JobID,
			finding.Path, finding.Line, string(finding.Severity),
			finding.Body, finding.Fingerprint,
		)
		return err
	})
}

// ResolveFinding marks a finding as resolved by linking a fix job.
// This is the ONLY permitted mutation on a finding after creation.
func (s *FindingStore) ResolveFinding(ctx context.Context, findingID, resolvedByJobID string) error {
	now := time.Now()

	// Get the run_id for the event.
	var runID string
	err := s.db.QueryRowContext(ctx,
		"SELECT run_id FROM findings WHERE id = ?", findingID,
	).Scan(&runID)
	if err != nil {
		return fmt.Errorf("get finding %q: %w", findingID, err)
	}

	payload, err := json.Marshal(map[string]string{
		"finding_id":         findingID,
		"resolved_by_job_id": resolvedByJobID,
	})
	if err != nil {
		return fmt.Errorf("marshal finding resolved payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventKind("finding.resolved"),
		SchemaVersion: 1,
		Payload:       string(payload),
		CreatedAt:     now,
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			"UPDATE findings SET resolved_by_job_id = ?, resolved_at = ? WHERE id = ? AND resolved_by_job_id IS NULL",
			resolvedByJobID, now.Format("2006-01-02T15:04:05Z"), findingID,
		)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return fmt.Errorf("finding %q not found or already resolved", findingID)
		}
		return nil
	})
}

// ListFindings returns all findings for a run.
func (s *FindingStore) ListFindings(ctx context.Context, runID string) ([]core.Finding, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, job_id, path, line, severity, body, fingerprint,
			COALESCE(resolved_by_job_id, ''), resolved_at
		FROM findings WHERE run_id = ? ORDER BY path, line`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("query findings: %w", err)
	}
	defer rows.Close()

	var findings []core.Finding
	for rows.Next() {
		var f core.Finding
		var severityStr string
		var resolvedAtStr sql.NullString
		err := rows.Scan(
			&f.ID, &f.RunID, &f.JobID, &f.Path, &f.Line,
			&severityStr, &f.Body, &f.Fingerprint,
			&f.ResolvedByJobID, &resolvedAtStr,
		)
		if err != nil {
			return nil, fmt.Errorf("scan finding: %w", err)
		}
		f.Severity = core.Severity(severityStr)
		if resolvedAtStr.Valid {
			t, _ := time.Parse("2006-01-02T15:04:05Z", resolvedAtStr.String)
			f.ResolvedAt = &t
		}
		findings = append(findings, f)
	}
	return findings, rows.Err()
}
