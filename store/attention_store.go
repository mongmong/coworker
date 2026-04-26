package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chris/coworker/core"
)

// AttentionStore provides access to the attention table.
type AttentionStore struct {
	db *DB
}

// NewAttentionStore creates a new AttentionStore.
func NewAttentionStore(db *DB) *AttentionStore {
	return &AttentionStore{db: db}
}

// InsertAttention creates a new attention item.
func (s *AttentionStore) InsertAttention(ctx context.Context, item *core.AttentionItem) error {
	if item.ID == "" {
		item.ID = core.NewID()
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}

	presentedOn, _ := json.Marshal(item.PresentedOn)
	answeredOn, _ := json.Marshal(item.AnsweredOn)
	options, _ := json.Marshal(item.Options)

	// Use nil for empty answer so ListUnansweredByRun (answer IS NULL) works correctly.
	var answer, answeredBy interface{}
	if item.Answer != "" {
		answer = item.Answer
		answeredBy = item.AnsweredBy
	}

	query := `
		INSERT INTO attention (
			id, run_id, kind, source, job_id, question, options,
			presented_on, answered_on, answered_by, answer, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		item.ID, item.RunID, string(item.Kind), item.Source, item.JobID,
		item.Question, string(options),
		string(presentedOn), string(answeredOn), answeredBy, answer,
		item.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert attention: %w", err)
	}
	return nil
}

// scanAttentionItem scans a row into an AttentionItem.
func scanAttentionItem(scan func(dest ...interface{}) error) (*core.AttentionItem, error) {
	item := &core.AttentionItem{}
	var kindStr, createdAtStr string
	var options, presentedOn, answeredOn, resolvedAt, answeredBy, answer, jobID sql.NullString

	err := scan(
		&item.ID, &item.RunID, &kindStr, &item.Source, &jobID,
		&item.Question, &options,
		&presentedOn, &answeredOn, &answeredBy, &answer,
		&createdAtStr, &resolvedAt,
	)
	if err != nil {
		return nil, err
	}

	item.Kind = core.AttentionKind(kindStr)
	item.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
	if jobID.Valid {
		item.JobID = jobID.String
	}
	if answeredBy.Valid {
		item.AnsweredBy = answeredBy.String
	}
	if answer.Valid {
		item.Answer = answer.String
	}
	if resolvedAt.Valid {
		t, _ := time.Parse(time.RFC3339, resolvedAt.String)
		item.ResolvedAt = &t
	}

	_ = json.Unmarshal([]byte(options.String), &item.Options)
	_ = json.Unmarshal([]byte(presentedOn.String), &item.PresentedOn)
	_ = json.Unmarshal([]byte(answeredOn.String), &item.AnsweredOn)

	return item, nil
}

// GetAttentionByID retrieves an attention item by ID.
func (s *AttentionStore) GetAttentionByID(ctx context.Context, id string) (*core.AttentionItem, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, run_id, kind, source, job_id, question, options,
		presented_on, answered_on, answered_by, answer, created_at, resolved_at
		FROM attention WHERE id = ?`, id)

	item, err := scanAttentionItem(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get attention: %w", err)
	}
	return item, nil
}

// ListAttentionByRun retrieves all attention items for a run, optionally filtered by kind.
func (s *AttentionStore) ListAttentionByRun(ctx context.Context, runID string, kind *core.AttentionKind) ([]*core.AttentionItem, error) {
	query := `SELECT id, run_id, kind, source, job_id, question, options,
	presented_on, answered_on, answered_by, answer, created_at, resolved_at
	FROM attention WHERE run_id = ?`
	args := []interface{}{runID}

	if kind != nil {
		query += ` AND kind = ?`
		args = append(args, string(*kind))
	}

	query += ` ORDER BY created_at ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list attention: %w", err)
	}
	defer rows.Close()

	var items []*core.AttentionItem
	for rows.Next() {
		item, err := scanAttentionItem(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan attention: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return items, nil
}

// AnswerAttention marks an attention item as answered.
func (s *AttentionStore) AnswerAttention(ctx context.Context, id, answer, answeredBy string) error {
	answeredOnJSON, err := json.Marshal([]string{answeredBy})
	if err != nil {
		return fmt.Errorf("marshal answered_on: %w", err)
	}
	query := `
		UPDATE attention
		SET answered_by = ?, answer = ?, answered_on = ?
		WHERE id = ?
	`

	_, err = s.db.ExecContext(ctx, query, answeredBy, answer, string(answeredOnJSON), id)
	if err != nil {
		return fmt.Errorf("answer attention: %w", err)
	}
	return nil
}

// ResolveAttention marks an attention item as resolved.
func (s *AttentionStore) ResolveAttention(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	query := `UPDATE attention SET resolved_at = ? WHERE id = ?`
	_, err := s.db.ExecContext(ctx, query, now, id)
	if err != nil {
		return fmt.Errorf("resolve attention: %w", err)
	}
	return nil
}

// ListUnansweredByRun returns all unanswered attention items for a run.
func (s *AttentionStore) ListUnansweredByRun(ctx context.Context, runID string) ([]*core.AttentionItem, error) {
	query := `SELECT id, run_id, kind, source, job_id, question, options,
	presented_on, answered_on, answered_by, answer, created_at, resolved_at
	FROM attention WHERE run_id = ? AND answer IS NULL
	ORDER BY created_at ASC`

	rows, err := s.db.QueryContext(ctx, query, runID)
	if err != nil {
		return nil, fmt.Errorf("list unanswered: %w", err)
	}
	defer rows.Close()

	var items []*core.AttentionItem
	for rows.Next() {
		item, err := scanAttentionItem(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan unanswered: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return items, nil
}

// GetUnansweredCheckpointForRun returns the most-recently-created unanswered
// attention item with kind="checkpoint" and the given source for the run.
// Returns nil (not an error) when no matching item exists.
func (s *AttentionStore) GetUnansweredCheckpointForRun(ctx context.Context, runID, source string) (*core.AttentionItem, error) {
	query := `SELECT id, run_id, kind, source, job_id, question, options,
	presented_on, answered_on, answered_by, answer, created_at, resolved_at
	FROM attention
	WHERE run_id = ? AND kind = 'checkpoint' AND source = ? AND answer IS NULL
	ORDER BY created_at DESC LIMIT 1`

	row := s.db.QueryRowContext(ctx, query, runID, source)
	item, err := scanAttentionItem(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get unanswered checkpoint: %w", err)
	}
	return item, nil
}

// ListAllPending returns all unanswered attention items across all runs.
func (s *AttentionStore) ListAllPending(ctx context.Context) ([]*core.AttentionItem, error) {
	query := `SELECT id, run_id, kind, source, job_id, question, options,
	presented_on, answered_on, answered_by, answer, created_at, resolved_at
	FROM attention WHERE answer IS NULL
	ORDER BY created_at ASC`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list all pending: %w", err)
	}
	defer rows.Close()

	var items []*core.AttentionItem
	for rows.Next() {
		item, err := scanAttentionItem(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan pending: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return items, nil
}
