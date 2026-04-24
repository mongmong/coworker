package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chris/coworker/core"
)

// ArtifactStore handles artifact persistence.
type ArtifactStore struct {
	db    *DB
	event *EventStore
}

// NewArtifactStore creates an ArtifactStore.
func NewArtifactStore(db *DB, event *EventStore) *ArtifactStore {
	return &ArtifactStore{db: db, event: event}
}

// ListArtifacts returns all artifacts associated with a job.
func (s *ArtifactStore) ListArtifacts(ctx context.Context, jobID string) ([]core.Artifact, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, job_id, kind, path FROM artifacts WHERE job_id = ? ORDER BY id",
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("query artifacts: %w", err)
	}
	defer rows.Close()

	var artifacts []core.Artifact
	for rows.Next() {
		var a core.Artifact
		if err := rows.Scan(&a.ID, &a.JobID, &a.Kind, &a.Path); err != nil {
			return nil, fmt.Errorf("scan artifact: %w", err)
		}
		artifacts = append(artifacts, a)
	}
	return artifacts, rows.Err()
}

// InsertArtifact creates a new artifact and writes an artifact.created event.
// Artifacts are pointers to files on disk; nothing is inlined.
func (s *ArtifactStore) InsertArtifact(ctx context.Context, artifact *core.Artifact, runID string) error {
	payload, err := json.Marshal(map[string]string{
		"artifact_id": artifact.ID,
		"job_id":      artifact.JobID,
		"kind":        artifact.Kind,
		"path":        artifact.Path,
	})
	if err != nil {
		return fmt.Errorf("marshal artifact.created payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventArtifactCreated,
		SchemaVersion: 1,
		CorrelationID: artifact.JobID,
		Payload:       string(payload),
		CreatedAt:     time.Now(),
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO artifacts (id, job_id, kind, path) VALUES (?, ?, ?, ?)",
			artifact.ID, artifact.JobID, artifact.Kind, artifact.Path,
		)
		return err
	})
}
