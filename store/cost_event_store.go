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
//
// Concurrency note: the cumulative + budget pre-read happens INSIDE the
// SQLite transaction (Plan 137 fix for re-audit finding). Two concurrent
// RecordCost calls for the same run will each see snapshot-isolated
// cost_usd values, so the cumulative_usd written into each event payload
// matches the post-bump truth in runs.cost_usd. Without the in-transaction
// read, the event payload could carry a stale cumulative under parallel
// dispatch (max_parallel_plans > 1).
//
// Implementation note: we open the transaction directly (not via
// EventStore.WriteEventThenRow) because the payload is computed inside
// the transaction. The event-first invariant is preserved: the event
// INSERT happens before the projection writes within the same tx.
func (s *CostEventStore) RecordCost(ctx context.Context, runID, jobID string, sample core.CostSample) error {
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Read inside the transaction so cumulative reflects the pre-bump
	// value at the same isolation level as the UPDATE below.
	var preCost float64
	var budgetUSD sql.NullFloat64
	if err := tx.QueryRowContext(ctx,
		`SELECT cost_usd, budget_usd FROM runs WHERE id = ?`, runID,
	).Scan(&preCost, &budgetUSD); err != nil {
		return fmt.Errorf("read run cost: %w", err)
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

	// Compute the event sequence number for this run (matches
	// EventStore.WriteEventThenRow's algorithm).
	var seq int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1 FROM events WHERE run_id = ?`,
		runID,
	).Scan(&seq); err != nil {
		return fmt.Errorf("compute event sequence: %w", err)
	}

	// Event-first: INSERT the event row before the projection writes.
	eventID := core.NewID()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO events (id, run_id, sequence, kind, schema_version,
			idempotency_key, causation_id, correlation_id, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, runID, seq, string(core.EventCostDelta), 1,
		nullableString(""), nullableString(""), nullableString(jobID),
		string(payload), now.UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("insert cost.delta event: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO cost_events
			(id, run_id, job_id, provider, model, tokens_in, tokens_out, usd, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		core.NewID(), runID, jobID, sample.Provider, sample.Model,
		sample.TokensIn, sample.TokensOut, sample.USD, now.UTC().Format(time.RFC3339Nano),
	); err != nil {
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

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cost.delta tx: %w", err)
	}

	// Publish to the in-memory bus after commit, mirroring
	// EventStore.WriteEventThenRow. Use the constructed event so SSE
	// subscribers see the same payload + sequence.
	if s.event != nil && s.event.Bus != nil {
		s.event.Bus.Publish(&core.Event{
			ID:            eventID,
			RunID:         runID,
			Sequence:      seq,
			Kind:          core.EventCostDelta,
			SchemaVersion: 1,
			CorrelationID: jobID,
			Payload:       string(payload),
			CreatedAt:     now,
		})
	}

	return nil
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
