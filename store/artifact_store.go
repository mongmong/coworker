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
