package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chris/coworker/core"
)

// ErrPlanNotFound is returned when a requested plan row does not exist.
var ErrPlanNotFound = errors.New("plan not found")

// PlanStore handles plan projection persistence with event-first writes.
type PlanStore struct {
	db    *DB
	event *EventStore
}

// NewPlanStore creates a PlanStore.
func NewPlanStore(db *DB, event *EventStore) *PlanStore {
	return &PlanStore{db: db, event: event}
}

// CreatePlan writes a plan.created event and inserts the plans row in the same transaction.
func (s *PlanStore) CreatePlan(ctx context.Context, p core.PlanRecord) error {
	blocksOn, err := json.Marshal(p.BlocksOn)
	if err != nil {
		return fmt.Errorf("marshal blocks_on: %w", err)
	}
	state := p.State
	if state == "" {
		state = "pending"
	}
	payload, err := json.Marshal(map[string]any{
		"plan_id":   p.ID,
		"run_id":    p.RunID,
		"number":    p.Number,
		"title":     p.Title,
		"blocks_on": p.BlocksOn,
		"state":     state,
	})
	if err != nil {
		return fmt.Errorf("marshal plan.created payload: %w", err)
	}
	ev := &core.Event{
		ID:            core.NewID(),
		RunID:         p.RunID,
		Kind:          core.EventPlanCreated,
		SchemaVersion: 1,
		CorrelationID: p.ID,
		Payload:       string(payload),
		CreatedAt:     time.Now(),
	}
	return s.event.WriteEventThenRow(ctx, ev, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO plans (id, run_id, number, title, blocks_on, branch, pr_url, state)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.RunID, p.Number, p.Title, string(blocksOn), p.Branch, p.PRURL, state,
		)
		if err != nil {
			return fmt.Errorf("insert plan: %w", err)
		}
		return nil
	})
}

// UpdatePlanState writes a plan.state_changed event and updates the plans row.
func (s *PlanStore) UpdatePlanState(ctx context.Context, planID, state string) error {
	runID, err := s.planRunID(ctx, planID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{
		"plan_id": planID,
		"state":   state,
	})
	if err != nil {
		return fmt.Errorf("marshal plan.state_changed payload: %w", err)
	}
	ev := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventPlanStateChanged,
		SchemaVersion: 1,
		CorrelationID: planID,
		Payload:       string(payload),
		CreatedAt:     time.Now(),
	}
	return s.event.WriteEventThenRow(ctx, ev, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE plans SET state = ? WHERE id = ?`, state, planID)
		if err != nil {
			return fmt.Errorf("update plan state: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("plan state rows affected: %w", err)
		}
		if n == 0 {
			return ErrPlanNotFound
		}
		return nil
	})
}

// UpdatePlanBranchAndPR writes a plan.state_changed event carrying branch/PR metadata.
func (s *PlanStore) UpdatePlanBranchAndPR(ctx context.Context, planID, branch, prURL string) error {
	runID, err := s.planRunID(ctx, planID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{
		"plan_id": planID,
		"branch":  branch,
		"pr_url":  prURL,
	})
	if err != nil {
		return fmt.Errorf("marshal plan branch/pr update: %w", err)
	}
	ev := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventPlanStateChanged,
		SchemaVersion: 1,
		CorrelationID: planID,
		Payload:       string(payload),
		CreatedAt:     time.Now(),
	}
	return s.event.WriteEventThenRow(ctx, ev, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE plans SET branch = ?, pr_url = ? WHERE id = ?`,
			branch, prURL, planID,
		)
		if err != nil {
			return fmt.Errorf("update plan branch/pr: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("plan branch/pr rows affected: %w", err)
		}
		if n == 0 {
			return ErrPlanNotFound
		}
		return nil
	})
}

func (s *PlanStore) planRunID(ctx context.Context, planID string) (string, error) {
	var runID string
	if err := s.db.QueryRowContext(ctx, "SELECT run_id FROM plans WHERE id = ?", planID).Scan(&runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrPlanNotFound
		}
		return "", fmt.Errorf("lookup plan run_id: %w", err)
	}
	return runID, nil
}

// PlanRow is a row from the plans projection.
type PlanRow struct {
	ID       string
	RunID    string
	Number   int
	Title    string
	BlocksOn []int
	Branch   string
	PRURL    string
	State    string
}

// GetPlan retrieves a plan by ID.
func (s *PlanStore) GetPlan(ctx context.Context, id string) (*PlanRow, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, run_id, number, title, blocks_on, branch, pr_url, state
		FROM plans WHERE id = ?`, id)
	return s.scanPlan(row)
}

// ListPlansByRun retrieves all plans for a run ordered by plan number.
func (s *PlanStore) ListPlansByRun(ctx context.Context, runID string) ([]*PlanRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, run_id, number, title, blocks_on, branch, pr_url, state
		FROM plans WHERE run_id = ? ORDER BY number ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("query plans: %w", err)
	}
	defer rows.Close()

	var out []*PlanRow
	for rows.Next() {
		p, err := s.scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("plan rows error: %w", err)
	}
	if out == nil {
		out = []*PlanRow{}
	}
	return out, nil
}

type rowScanner interface {
	Scan(...interface{}) error
}

func (s *PlanStore) scanPlan(r rowScanner) (*PlanRow, error) {
	p := &PlanRow{}
	var blocksOn string
	if err := r.Scan(&p.ID, &p.RunID, &p.Number, &p.Title, &blocksOn, &p.Branch, &p.PRURL, &p.State); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPlanNotFound
		}
		return nil, fmt.Errorf("scan plan: %w", err)
	}
	if blocksOn != "" {
		if err := json.Unmarshal([]byte(blocksOn), &p.BlocksOn); err != nil {
			return nil, fmt.Errorf("unmarshal blocks_on: %w", err)
		}
	}
	return p, nil
}

var _ core.PlanWriter = (*PlanStore)(nil)
