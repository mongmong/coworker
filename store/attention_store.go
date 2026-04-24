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

	query := `
		INSERT INTO attention (
			id, run_id, kind, source, job_id, question, options,
			presented_on, answered_on, answered_by, answer, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		item.ID, item.RunID, item.Kind, item.Source, item.JobID,
		item.Question, string(options),
		string(presentedOn), string(answeredOn), item.AnsweredBy, item.Answer,
		item.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert attention: %w", err)
	}
	return nil
}

// GetAttentionByID retrieves an attention item by ID.
func (s *AttentionStore) GetAttentionByID(ctx context.Context, id string) (*core.AttentionItem, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, run_id, kind, source, job_id, question, options,
		presented_on, answered_on, answered_by, answer, created_at, resolved_at
		FROM attention WHERE id = ?`, id)

	item := &core.AttentionItem{}
	var options, presentedOn, answeredOn, resolvedAt sql.NullString

	err := row.Scan(
		&item.ID, &item.RunID, &item.Kind, &item.Source, &item.JobID,
		&item.Question, &options,
		&presentedOn, &answeredOn, &item.AnsweredBy, &item.Answer,
		&item.CreatedAt, &resolvedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get attention: %w", err)
	}

	_ = json.Unmarshal([]byte(options.String), &item.Options)
	_ = json.Unmarshal([]byte(presentedOn.String), &item.PresentedOn)
	_ = json.Unmarshal([]byte(answeredOn.String), &item.AnsweredOn)

	if resolvedAt.Valid {
		t, _ := time.Parse(time.RFC3339, resolvedAt.String)
		item.ResolvedAt = &t
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
		item := &core.AttentionItem{}
		var options, presentedOn, answeredOn, resolvedAt sql.NullString

		if err := rows.Scan(
			&item.ID, &item.RunID, &item.Kind, &item.Source, &item.JobID,
			&item.Question, &options,
			&presentedOn, &answeredOn, &item.AnsweredBy, &item.Answer,
			&item.CreatedAt, &resolvedAt,
		); err != nil {
			return nil, fmt.Errorf("scan attention: %w", err)
		}

		_ = json.Unmarshal([]byte(options.String), &item.Options)
		_ = json.Unmarshal([]byte(presentedOn.String), &item.PresentedOn)
		_ = json.Unmarshal([]byte(answeredOn.String), &item.AnsweredOn)

		if resolvedAt.Valid {
			t, _ := time.Parse(time.RFC3339, resolvedAt.String)
			item.ResolvedAt = &t
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
	query := `
		UPDATE attention
		SET answered_on = json_array(json_extract(answered_on, '$') || ?),
		    answered_by = ?, answer = ?
		WHERE id = ?
	`

	_, err := s.db.ExecContext(ctx, query, answeredBy, answeredBy, answer, id)
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
		item := &core.AttentionItem{}
		var options, presentedOn, answeredOn, resolvedAt sql.NullString

		if err := rows.Scan(
			&item.ID, &item.RunID, &item.Kind, &item.Source, &item.JobID,
			&item.Question, &options,
			&presentedOn, &answeredOn, &item.AnsweredBy, &item.Answer,
			&item.CreatedAt, &resolvedAt,
		); err != nil {
			return nil, fmt.Errorf("scan unanswered: %w", err)
		}

		_ = json.Unmarshal([]byte(options.String), &item.Options)
		_ = json.Unmarshal([]byte(presentedOn.String), &item.PresentedOn)
		_ = json.Unmarshal([]byte(answeredOn.String), &item.AnsweredOn)

		if resolvedAt.Valid {
			t, _ := time.Parse(time.RFC3339, resolvedAt.String)
			item.ResolvedAt = &t
		}

		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return items, nil
}
