package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chris/coworker/core"
)

// CostEventStore persists cost.delta projection rows.
type CostEventStore struct {
	db    *DB
	event *EventStore
}

// NewCostEventStore creates a CostEventStore.
func NewCostEventStore(db *DB, event *EventStore) *CostEventStore {
	return &CostEventStore{db: db, event: event}
}

// RecordCost writes a cost.delta event and the projection row in the same transaction.
//
// The event payload includes the per-sample fields (provider, model, tokens,
// USD) plus `cumulative_usd` (the run's total cost AFTER this sample is
// applied) and `budget_usd` (the run's configured budget, 0 when unset).
// TUI and other live consumers read those derived fields directly from the
// event payload without re-querying the runs row. Plan 130 (I11).
func (s *CostEventStore) RecordCost(ctx context.Context, runID, jobID string, sample core.CostSample) error {
	// Pre-read the run's current cost + budget so the event payload can
	// surface cumulative + budget to live consumers (e.g., TUI). The
	// transaction below bumps runs.cost_usd; cumulative = pre + sample.USD.
	var preCost float64
	var budgetUSD sql.NullFloat64
	if err := s.db.QueryRowContext(ctx,
		`SELECT cost_usd, budget_usd FROM runs WHERE id = ?`, runID,
	).Scan(&preCost, &budgetUSD); err != nil {
		// Non-fatal: missing run row is a real error, but we still want
		// the cost write itself to surface it via the FK below. Treat
		// the read as best-effort.
		preCost = 0
	}
	cumulative := preCost + sample.USD
	budget := 0.0
	if budgetUSD.Valid {
		budget = budgetUSD.Float64
	}

	payload, err := json.Marshal(map[string]any{
		"run_id":         runID,
		"job_id":         jobID,
		"provider":       sample.Provider,
		"model":          sample.Model,
		"tokens_in":      sample.TokensIn,
		"tokens_out":     sample.TokensOut,
		"usd":            sample.USD,
		"cumulative_usd": cumulative,
		"budget_usd":     budget,
	})
	if err != nil {
		return fmt.Errorf("marshal cost.delta: %w", err)
	}
	now := time.Now()
	ev := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventCostDelta,
		SchemaVersion: 1,
		CorrelationID: jobID,
		Payload:       string(payload),
		CreatedAt:     now,
	}
	return s.event.WriteEventThenRow(ctx, ev, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO cost_events
				(id, run_id, job_id, provider, model, tokens_in, tokens_out, usd, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			core.NewID(), runID, jobID, sample.Provider, sample.Model,
			sample.TokensIn, sample.TokensOut, sample.USD, now.UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("insert cost_event: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE jobs SET cost_usd = cost_usd + ? WHERE id = ?`,
			sample.USD, jobID); err != nil {
			return fmt.Errorf("bump job cost: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE runs SET cost_usd = cost_usd + ? WHERE id = ?`,
			sample.USD, runID); err != nil {
			return fmt.Errorf("bump run cost: %w", err)
		}
		return nil
	})
}

// CostEventRow is a row from the cost_events projection.
type CostEventRow struct {
	ID        string
	RunID     string
	JobID     string
	Provider  string
	Model     string
	TokensIn  int
	TokensOut int
	USD       float64
	CreatedAt time.Time
}

// SumByRun returns total recorded cost for a run.
func (s *CostEventStore) SumByRun(ctx context.Context, runID string) (float64, error) {
	var total sql.NullFloat64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(usd), 0) FROM cost_events WHERE run_id = ?`, runID,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum cost_events: %w", err)
	}
	return total.Float64, nil
}

// SumByJob returns total recorded cost for a job.
func (s *CostEventStore) SumByJob(ctx context.Context, jobID string) (float64, error) {
	var total sql.NullFloat64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(usd), 0) FROM cost_events WHERE job_id = ?`, jobID,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum cost_events by job: %w", err)
	}
	return total.Float64, nil
}

// ListByJob lists cost events for a job.
func (s *CostEventStore) ListByJob(ctx context.Context, jobID string) ([]*CostEventRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, run_id, job_id, provider, model, tokens_in, tokens_out, usd, created_at
		FROM cost_events WHERE job_id = ?
		ORDER BY created_at ASC, id ASC`, jobID)
	if err != nil {
		return nil, fmt.Errorf("query cost_events: %w", err)
	}
	defer rows.Close()

	var out []*CostEventRow
	for rows.Next() {
		e := &CostEventRow{}
		var createdAt string
		if err := rows.Scan(&e.ID, &e.RunID, &e.JobID, &e.Provider, &e.Model,
			&e.TokensIn, &e.TokensOut, &e.USD, &createdAt); err != nil {
			return nil, fmt.Errorf("scan cost_event: %w", err)
		}
		t, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse cost_event.created_at: %w", err)
		}
		e.CreatedAt = t
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cost_event rows error: %w", err)
	}
	if out == nil {
		out = []*CostEventRow{}
	}
	return out, nil
}

// ListByRun lists cost events for a run, ordered chronologically.
func (s *CostEventStore) ListByRun(ctx context.Context, runID string) ([]*CostEventRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, run_id, job_id, provider, model, tokens_in, tokens_out, usd, created_at
		FROM cost_events WHERE run_id = ?
		ORDER BY created_at ASC, id ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("query cost_events by run: %w", err)
	}
	defer rows.Close()

	var out []*CostEventRow
	for rows.Next() {
		e := &CostEventRow{}
		var createdAt string
		if err := rows.Scan(&e.ID, &e.RunID, &e.JobID, &e.Provider, &e.Model,
			&e.TokensIn, &e.TokensOut, &e.USD, &createdAt); err != nil {
			return nil, fmt.Errorf("scan cost_event: %w", err)
		}
		t, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse cost_event.created_at: %w", err)
		}
		e.CreatedAt = t
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cost_event rows error: %w", err)
	}
	if out == nil {
		out = []*CostEventRow{}
	}
	return out, nil
}

var _ core.CostWriter = (*CostEventStore)(nil)
