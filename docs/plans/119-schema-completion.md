# Plan 119 — Schema Completion (V1 spec parity)

> **For agentic workers:** This plan is implemented inline by Claude Code. Use the executing-plans pattern: implement phase-by-phase, commit each phase, run the full test suite before merging.

**Goal:** Migrate the SQLite schema to match the V1 spec by adding the missing `plans`, `checkpoints`, `supervisor_events`, and `cost_events` tables, plus the missing `runs` (`prd_path`, `spec_path`, `cost_usd`, `budget_usd`) and `jobs` (`plan_id`, `phase_index`, `cost_usd`) columns. Wire the new tables into the corresponding event-write helpers so they remain synchronized with the event log.

**Architecture:** Additive migrations only — no schema breakage for existing rows. New tables are projections of existing events (`supervisor.verdict`, `cost.delta`, checkpoint attention items) so a replay test is included to demonstrate that the projections can be reconstructed. The two pre-existing schema-evolution patterns are preserved: numbered migration files in `store/migrations/` driven by `db.go`'s migration runner, and event-first writes via `WriteEventThenRow`.

**Tech Stack:** modernc.org/sqlite, raw `database/sql` + prepared statements, numbered SQL migrations, Go `time.RFC3339Nano` timestamps.

**Reference:** `docs/specs/000-coworker-runtime-design.md` §Data Model (lines 752–805); `docs/reviews/2026-04-26-v1-comprehensive-review.md` finding "Schema is missing spec tables" (line 161).

---

## Scope

In scope:

1. New migration file `007_schema_completion.sql` adding:
   - `plans(id, run_id, number, title, blocks_on, branch, pr_url, state)` (state defaults to `pending`)
   - `checkpoints(id, run_id, plan_id, kind, state, decision, decided_by, decided_at, notes)` (separate from `attention`; the attention table remains the live human-input UI surface — checkpoints durably record the resolved decision)
   - `supervisor_events(id, run_id, job_id, kind, verdict, rule_id, message, created_at)`
   - `cost_events(id, run_id, job_id, provider, model, tokens_in, tokens_out, usd, created_at)`
   - `runs.prd_path TEXT`, `runs.spec_path TEXT`, `runs.cost_usd REAL DEFAULT 0`, `runs.budget_usd REAL`
   - `jobs.plan_id TEXT`, `jobs.phase_index INTEGER`, `jobs.cost_usd REAL DEFAULT 0`
2. Go store types (`PlanRow`, `CheckpointRow`, `SupervisorEventRow`, `CostEventRow`) and CRUD helpers (`Insert`, `GetByID`, `ListByRun`).
3. Wire the supervisor evaluator (`coding/supervisor/engine.go`) so each `RuleResult` produces a row in `supervisor_events`.
4. Wire `cli/run.go` and the `BuildFromPRDWorkflow.SchedulePlans` path so each scheduled plan creates a `plans` row and each `phase-clean`/`plan-approved`/`ready-to-ship` attention item creates a `checkpoints` row at decision time.
5. RunStore: extend `Insert` and `Get` to set/return `prd_path`, `spec_path`, `cost_usd`, `budget_usd`. JobStore: extend `Insert` and `Get` for `plan_id`, `phase_index`, `cost_usd`.
6. Replay test demonstrating that `supervisor_events` and `cost_events` projections can be rebuilt from the `events` log.

Out of scope:

- Cost ledger updates from cli stream-json output (the helper exists; its caller wiring is Plan 121+).
- TUI / HTTP API surfacing of the new columns (next plan).
- Budget enforcement (a `budget_usd` column does not yet enforce anything; the scheduler will read it in Plan 121+).
- Removing or reshaping any existing tables.

---

## File Structure

**Create:**
- `store/migrations/007_schema_completion.sql`
- `store/plan_store.go` + `store/plan_store_test.go`
- `store/checkpoint_store.go` + `store/checkpoint_store_test.go`
- `store/supervisor_event_store.go` + `store/supervisor_event_store_test.go`
- `store/cost_event_store.go` + `store/cost_event_store_test.go`
- `store/replay_test.go` (rebuild projection from events log)

**Modify:**
- `store/run_store.go` (`prd_path`, `spec_path`, `cost_usd`, `budget_usd` fields and Insert/Get)
- `store/job_store.go` (`plan_id`, `phase_index`, `cost_usd` fields and Insert/Get)
- `core/run.go` and `core/job.go` (mirror new fields on the in-memory structs)
- `coding/supervisor/engine.go` (after evaluation, write rows via SupervisorEventStore)
- `coding/workflow/build_from_prd.go` (after scheduling, write rows via PlanStore)
- `cli/daemon.go` and `cli/run.go` (construct the new stores and inject)

**Test fixtures:**
- `testdata/golden_events/run_with_supervisor.jsonl` — minimal recorded event stream used by the replay test.

---

## Phase 1 — Migration file

**Files:**
- Create: `store/migrations/007_schema_completion.sql`

- [ ] **Step 1 — Write the migration:**

```sql
-- Plan 119: schema completion to match V1 spec data model.
-- Adds plans, checkpoints, supervisor_events, cost_events tables and the
-- spec-required columns on runs and jobs. All additive; existing rows remain valid.

-- Plans table — DAG nodes per run.
CREATE TABLE IF NOT EXISTS plans (
    id        TEXT PRIMARY KEY,
    run_id    TEXT NOT NULL REFERENCES runs(id),
    number    INTEGER NOT NULL,
    title     TEXT NOT NULL,
    blocks_on TEXT NOT NULL DEFAULT '[]',  -- JSON array of plan numbers
    branch    TEXT NOT NULL DEFAULT '',
    pr_url    TEXT NOT NULL DEFAULT '',
    state     TEXT NOT NULL DEFAULT 'pending'
);

CREATE INDEX IF NOT EXISTS idx_plans_run_id ON plans(run_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_plans_run_number
    ON plans(run_id, number);

-- Checkpoints table — durable record of resolved checkpoint decisions.
-- The attention table holds the live "needs answer" item; once resolved,
-- a checkpoints row preserves the decision separately so attention can be
-- pruned/archived independently.
CREATE TABLE IF NOT EXISTS checkpoints (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL REFERENCES runs(id),
    plan_id     TEXT,
    kind        TEXT NOT NULL,            -- spec-approved | plan-approved | phase-clean | ready-to-ship | compliance-breach | quality-gate
    state       TEXT NOT NULL DEFAULT 'open',  -- open | resolved
    decision    TEXT,                     -- approve | reject | (empty when open)
    decided_by  TEXT,
    decided_at  TEXT,
    notes       TEXT
);

CREATE INDEX IF NOT EXISTS idx_checkpoints_run_id ON checkpoints(run_id);
CREATE INDEX IF NOT EXISTS idx_checkpoints_plan_id ON checkpoints(plan_id);

-- Supervisor events — projection of supervisor.verdict events.
CREATE TABLE IF NOT EXISTS supervisor_events (
    id         TEXT PRIMARY KEY,
    run_id     TEXT NOT NULL REFERENCES runs(id),
    job_id     TEXT NOT NULL,
    kind       TEXT NOT NULL,             -- "supervisor.verdict" | "supervisor.retry" | "compliance-breach"
    verdict    TEXT NOT NULL,             -- "pass" | "fail" | "skipped"
    rule_id    TEXT,
    message    TEXT,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_supervisor_events_run_id ON supervisor_events(run_id);
CREATE INDEX IF NOT EXISTS idx_supervisor_events_job_id ON supervisor_events(job_id);

-- Cost events — projection of cost.delta events.
CREATE TABLE IF NOT EXISTS cost_events (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL REFERENCES runs(id),
    job_id      TEXT NOT NULL,
    provider    TEXT NOT NULL,
    model       TEXT NOT NULL,
    tokens_in   INTEGER NOT NULL DEFAULT 0,
    tokens_out  INTEGER NOT NULL DEFAULT 0,
    usd         REAL NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_cost_events_run_id ON cost_events(run_id);
CREATE INDEX IF NOT EXISTS idx_cost_events_job_id ON cost_events(job_id);

-- Runs: spec-required columns (additive).
ALTER TABLE runs ADD COLUMN prd_path TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN spec_path TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN budget_usd REAL;

-- Jobs: spec-required columns (additive).
ALTER TABLE jobs ADD COLUMN plan_id TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN phase_index INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0;
```

- [ ] **Step 2 — Run the migration runner unit test:**

```bash
go test ./store -run TestRunMigrations -count=1
```

Expected: PASS — the migration runner discovers `007_*.sql` and applies it idempotently. (If the runner currently hard-codes a max version, fix it to glob the migrations directory.)

- [ ] **Step 3 — Commit:**

```bash
git add store/migrations/007_schema_completion.sql
git commit -m "Plan 119: migration 007 — plans, checkpoints, supervisor_events, cost_events tables"
```

---

## Phase 2 — Run/Job store updates

**Files:**
- Modify: `core/run.go`, `core/job.go`, `store/run_store.go`, `store/run_store_test.go`, `store/job_store.go`, `store/job_store_test.go`

- [ ] **Step 1 — Extend `core.Run`:**

In `core/run.go`, add fields:

```go
type Run struct {
    // ... existing fields ...
    PRDPath   string
    SpecPath  string
    CostUSD   float64
    BudgetUSD *float64 // pointer so NULL is preserved as "no budget"
}
```

- [ ] **Step 2 — Extend `core.Job`:**

In `core/job.go`, add fields:

```go
type Job struct {
    // ... existing fields ...
    PlanID     string
    PhaseIndex int
    CostUSD    float64
}
```

- [ ] **Step 3 — Update `RunStore.Insert`:**

In `store/run_store.go`, change the INSERT statement to include the new columns. Read existing column writes from the file before editing — preserve all existing fields.

```sql
INSERT INTO runs (id, mode, state, started_at, ended_at,
                  prd_path, spec_path, cost_usd, budget_usd)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
```

When `BudgetUSD == nil`, bind `sql.NullFloat64{Valid: false}`. When non-nil, bind the value.

- [ ] **Step 4 — Update `RunStore.Get`:**

Add the four new columns to the SELECT statement and Scan into the new struct fields. Use `sql.NullFloat64` for `budget_usd` and convert to `*float64` on the Run struct.

- [ ] **Step 5 — Test new fields round-trip:**

Add to `store/run_store_test.go`:

```go
func TestRunStore_NewFieldsRoundTrip(t *testing.T) {
    s := newTestRunStore(t)
    budget := 5.0
    in := &core.Run{
        ID:        "run-1",
        Mode:      "interactive",
        State:     "active",
        StartedAt: time.Now(),
        PRDPath:   "docs/prd.md",
        SpecPath:  "docs/spec.md",
        CostUSD:   1.25,
        BudgetUSD: &budget,
    }
    if err := s.Insert(context.Background(), in); err != nil {
        t.Fatalf("Insert: %v", err)
    }
    out, err := s.Get(context.Background(), "run-1")
    if err != nil || out == nil {
        t.Fatalf("Get: %v %v", out, err)
    }
    if out.PRDPath != in.PRDPath || out.SpecPath != in.SpecPath ||
        out.CostUSD != in.CostUSD ||
        out.BudgetUSD == nil || *out.BudgetUSD != *in.BudgetUSD {
        t.Errorf("round-trip mismatch: got %+v, want %+v", out, in)
    }
}

func TestRunStore_NilBudget(t *testing.T) {
    s := newTestRunStore(t)
    in := &core.Run{
        ID: "run-2", Mode: "interactive", State: "active",
        StartedAt: time.Now(),
        BudgetUSD: nil,
    }
    if err := s.Insert(context.Background(), in); err != nil {
        t.Fatal(err)
    }
    out, err := s.Get(context.Background(), "run-2")
    if err != nil || out == nil {
        t.Fatal(err)
    }
    if out.BudgetUSD != nil {
        t.Errorf("BudgetUSD: got %v, want nil", *out.BudgetUSD)
    }
}
```

- [ ] **Step 6 — Update `JobStore.Insert` / `Get`:**

In `store/job_store.go`, mirror the same pattern: add `plan_id`, `phase_index`, `cost_usd` to INSERT and SELECT.

- [ ] **Step 7 — Test the new job fields:**

Add to `store/job_store_test.go`:

```go
func TestJobStore_NewFieldsRoundTrip(t *testing.T) {
    s, _ := newTestJobStore(t)  // adapt to the existing test helper
    j := &core.Job{
        ID:         "job-1",
        RunID:      "run-1",
        Role:       "developer",
        State:      "pending",
        StartedAt:  time.Now(),
        PlanID:     "plan-100",
        PhaseIndex: 3,
        CostUSD:    0.42,
    }
    if err := s.Insert(context.Background(), j); err != nil {
        t.Fatal(err)
    }
    out, err := s.Get(context.Background(), "job-1")
    if err != nil || out == nil {
        t.Fatal(err)
    }
    if out.PlanID != j.PlanID || out.PhaseIndex != j.PhaseIndex || out.CostUSD != j.CostUSD {
        t.Errorf("mismatch: %+v", out)
    }
}
```

- [ ] **Step 8 — Run tests:**

```bash
go test ./store -count=1
go test ./core -count=1
```

Expected: PASS for both, including new tests.

- [ ] **Step 9 — Commit:**

```bash
git add core/run.go core/job.go store/run_store.go store/run_store_test.go store/job_store.go store/job_store_test.go
git commit -m "Plan 119: extend runs/jobs with spec-required columns"
```

---

## Phase 3 — PlanStore

**Files:**
- Create: `store/plan_store.go`, `store/plan_store_test.go`

- [ ] **Step 1 — `store/plan_store.go`:**

```go
package store

import (
    "context"
    "database/sql"
    "encoding/json"
    "errors"
    "fmt"
    "time"
)

// PlanRow represents a row in the plans table.
type PlanRow struct {
    ID       string
    RunID    string
    Number   int
    Title    string
    BlocksOn []int
    Branch   string
    PRURL    string
    State    string // pending | running | done | failed | cancelled
}

// ErrPlanNotFound is returned when a Get does not match any row.
var ErrPlanNotFound = errors.New("plan not found")

type PlanStore struct {
    db *sql.DB
}

func NewPlanStore(db *sql.DB) *PlanStore {
    return &PlanStore{db: db}
}

func (s *PlanStore) Insert(ctx context.Context, p *PlanRow) error {
    blocksOn, err := json.Marshal(p.BlocksOn)
    if err != nil {
        return fmt.Errorf("marshal blocks_on: %w", err)
    }
    state := p.State
    if state == "" {
        state = "pending"
    }
    _, err = s.db.ExecContext(ctx, `
        INSERT INTO plans (id, run_id, number, title, blocks_on, branch, pr_url, state)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
        p.ID, p.RunID, p.Number, p.Title, string(blocksOn),
        p.Branch, p.PRURL, state,
    )
    if err != nil {
        return fmt.Errorf("insert plan: %w", err)
    }
    return nil
}

func (s *PlanStore) Get(ctx context.Context, id string) (*PlanRow, error) {
    row := s.db.QueryRowContext(ctx, `
        SELECT id, run_id, number, title, blocks_on, branch, pr_url, state
          FROM plans WHERE id = ?`, id)
    return s.scanRow(row)
}

func (s *PlanStore) ListByRun(ctx context.Context, runID string) ([]*PlanRow, error) {
    rows, err := s.db.QueryContext(ctx, `
        SELECT id, run_id, number, title, blocks_on, branch, pr_url, state
          FROM plans WHERE run_id = ? ORDER BY number ASC`, runID)
    if err != nil {
        return nil, fmt.Errorf("query plans: %w", err)
    }
    defer rows.Close()

    var out []*PlanRow
    for rows.Next() {
        p, err := s.scanRow(rows)
        if err != nil {
            return nil, err
        }
        out = append(out, p)
    }
    return out, rows.Err()
}

func (s *PlanStore) UpdateState(ctx context.Context, id, state string) error {
    res, err := s.db.ExecContext(ctx,
        `UPDATE plans SET state = ? WHERE id = ?`, state, id)
    if err != nil {
        return fmt.Errorf("update plan state: %w", err)
    }
    n, _ := res.RowsAffected()
    if n == 0 {
        return ErrPlanNotFound
    }
    return nil
}

func (s *PlanStore) UpdateBranchAndPR(ctx context.Context, id, branch, prURL string) error {
    res, err := s.db.ExecContext(ctx,
        `UPDATE plans SET branch = ?, pr_url = ? WHERE id = ?`, branch, prURL, id)
    if err != nil {
        return fmt.Errorf("update plan branch/pr: %w", err)
    }
    n, _ := res.RowsAffected()
    if n == 0 {
        return ErrPlanNotFound
    }
    return nil
}

type rowScanner interface {
    Scan(dest ...interface{}) error
}

func (s *PlanStore) scanRow(row rowScanner) (*PlanRow, error) {
    p := &PlanRow{}
    var blocksOn string
    if err := row.Scan(&p.ID, &p.RunID, &p.Number, &p.Title, &blocksOn,
        &p.Branch, &p.PRURL, &p.State); err != nil {
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

// ensure time import is used by callers that need RFC3339 helpers
var _ = time.RFC3339Nano
```

- [ ] **Step 2 — `store/plan_store_test.go`:**

```go
package store

import (
    "context"
    "errors"
    "testing"
    "time"

    "github.com/chris/coworker/core"
)

func newTestPlanStore(t *testing.T) (*PlanStore, *RunStore) {
    t.Helper()
    db := openTestDB(t)  // existing helper that runs migrations
    rs := NewRunStore(db)
    if err := rs.Insert(context.Background(), &core.Run{
        ID: "run-1", Mode: "autopilot", State: "active",
        StartedAt: time.Now(),
    }); err != nil {
        t.Fatal(err)
    }
    return NewPlanStore(db), rs
}

func TestPlanStore_InsertAndGet(t *testing.T) {
    s, _ := newTestPlanStore(t)
    p := &PlanRow{ID: "plan-1", RunID: "run-1", Number: 100, Title: "test plan",
        BlocksOn: []int{99}, Branch: "feature/plan-100"}
    if err := s.Insert(context.Background(), p); err != nil {
        t.Fatal(err)
    }
    got, err := s.Get(context.Background(), "plan-1")
    if err != nil {
        t.Fatal(err)
    }
    if got.Number != 100 || got.Title != "test plan" ||
        len(got.BlocksOn) != 1 || got.BlocksOn[0] != 99 ||
        got.State != "pending" {
        t.Errorf("got %+v", got)
    }
}

func TestPlanStore_ListByRunOrderedByNumber(t *testing.T) {
    s, _ := newTestPlanStore(t)
    for _, n := range []int{102, 100, 101} {
        if err := s.Insert(context.Background(), &PlanRow{
            ID:    "p-" + string(rune('0'+n)),
            RunID: "run-1", Number: n, Title: "x",
        }); err != nil {
            t.Fatal(err)
        }
    }
    got, err := s.ListByRun(context.Background(), "run-1")
    if err != nil {
        t.Fatal(err)
    }
    if len(got) != 3 || got[0].Number != 100 || got[2].Number != 102 {
        t.Errorf("ordering wrong: %+v", got)
    }
}

func TestPlanStore_UpdateState(t *testing.T) {
    s, _ := newTestPlanStore(t)
    if err := s.Insert(context.Background(), &PlanRow{
        ID: "p", RunID: "run-1", Number: 100, Title: "x",
    }); err != nil {
        t.Fatal(err)
    }
    if err := s.UpdateState(context.Background(), "p", "running"); err != nil {
        t.Fatal(err)
    }
    got, _ := s.Get(context.Background(), "p")
    if got.State != "running" {
        t.Errorf("state = %q, want running", got.State)
    }
}

func TestPlanStore_NotFound(t *testing.T) {
    s, _ := newTestPlanStore(t)
    _, err := s.Get(context.Background(), "missing")
    if !errors.Is(err, ErrPlanNotFound) {
        t.Errorf("got %v, want ErrPlanNotFound", err)
    }
    err = s.UpdateState(context.Background(), "missing", "running")
    if !errors.Is(err, ErrPlanNotFound) {
        t.Errorf("got %v, want ErrPlanNotFound", err)
    }
}

func TestPlanStore_UniqueRunNumber(t *testing.T) {
    s, _ := newTestPlanStore(t)
    a := &PlanRow{ID: "a", RunID: "run-1", Number: 100, Title: "a"}
    b := &PlanRow{ID: "b", RunID: "run-1", Number: 100, Title: "b"}
    if err := s.Insert(context.Background(), a); err != nil {
        t.Fatal(err)
    }
    if err := s.Insert(context.Background(), b); err == nil {
        t.Errorf("expected unique constraint violation")
    }
}

func TestPlanStore_BlocksOnEmpty(t *testing.T) {
    s, _ := newTestPlanStore(t)
    p := &PlanRow{ID: "p", RunID: "run-1", Number: 100, Title: "x"}
    if err := s.Insert(context.Background(), p); err != nil {
        t.Fatal(err)
    }
    got, _ := s.Get(context.Background(), "p")
    if got.BlocksOn != nil && len(got.BlocksOn) != 0 {
        t.Errorf("BlocksOn = %v, want empty", got.BlocksOn)
    }
}
```

- [ ] **Step 3 — Run tests, then commit:**

```bash
go test ./store -count=1 -run TestPlanStore
git add store/plan_store.go store/plan_store_test.go
git commit -m "Plan 119: PlanStore with CRUD + state/branch updates"
```

---

## Phase 4 — CheckpointStore

**Files:**
- Create: `store/checkpoint_store.go`, `store/checkpoint_store_test.go`

- [ ] **Step 1 — `store/checkpoint_store.go`:**

```go
package store

import (
    "context"
    "database/sql"
    "errors"
    "fmt"
    "time"
)

type CheckpointRow struct {
    ID         string
    RunID      string
    PlanID     *string // nullable: spec-approved & top-level kinds have no plan
    Kind       string
    State      string  // open | resolved
    Decision   string  // approve | reject | "" when open
    DecidedBy  string
    DecidedAt  *time.Time
    Notes      string
}

var ErrCheckpointNotFound = errors.New("checkpoint not found")

type CheckpointStore struct {
    db *sql.DB
}

func NewCheckpointStore(db *sql.DB) *CheckpointStore {
    return &CheckpointStore{db: db}
}

func (s *CheckpointStore) Insert(ctx context.Context, c *CheckpointRow) error {
    state := c.State
    if state == "" {
        state = "open"
    }
    var planID sql.NullString
    if c.PlanID != nil {
        planID = sql.NullString{String: *c.PlanID, Valid: true}
    }
    var decidedAt sql.NullString
    if c.DecidedAt != nil {
        decidedAt = sql.NullString{String: c.DecidedAt.UTC().Format(time.RFC3339Nano), Valid: true}
    }
    _, err := s.db.ExecContext(ctx, `
        INSERT INTO checkpoints (id, run_id, plan_id, kind, state, decision, decided_by, decided_at, notes)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        c.ID, c.RunID, planID, c.Kind, state, c.Decision, c.DecidedBy, decidedAt, c.Notes,
    )
    if err != nil {
        return fmt.Errorf("insert checkpoint: %w", err)
    }
    return nil
}

func (s *CheckpointStore) Resolve(ctx context.Context, id, decision, decidedBy, notes string) error {
    res, err := s.db.ExecContext(ctx, `
        UPDATE checkpoints
           SET state = 'resolved',
               decision = ?,
               decided_by = ?,
               decided_at = ?,
               notes = ?
         WHERE id = ?`,
        decision, decidedBy, time.Now().UTC().Format(time.RFC3339Nano), notes, id)
    if err != nil {
        return fmt.Errorf("resolve checkpoint: %w", err)
    }
    n, _ := res.RowsAffected()
    if n == 0 {
        return ErrCheckpointNotFound
    }
    return nil
}

func (s *CheckpointStore) Get(ctx context.Context, id string) (*CheckpointRow, error) {
    row := s.db.QueryRowContext(ctx, `
        SELECT id, run_id, plan_id, kind, state, decision, decided_by, decided_at, notes
          FROM checkpoints WHERE id = ?`, id)
    return s.scanRow(row)
}

func (s *CheckpointStore) ListByRun(ctx context.Context, runID string) ([]*CheckpointRow, error) {
    rows, err := s.db.QueryContext(ctx, `
        SELECT id, run_id, plan_id, kind, state, decision, decided_by, decided_at, notes
          FROM checkpoints WHERE run_id = ?
          ORDER BY decided_at IS NULL DESC, decided_at ASC, id ASC`, runID)
    if err != nil {
        return nil, fmt.Errorf("query checkpoints: %w", err)
    }
    defer rows.Close()
    var out []*CheckpointRow
    for rows.Next() {
        c, err := s.scanRow(rows)
        if err != nil {
            return nil, err
        }
        out = append(out, c)
    }
    return out, rows.Err()
}

func (s *CheckpointStore) scanRow(row rowScanner) (*CheckpointRow, error) {
    c := &CheckpointRow{}
    var planID, decision, decidedBy, decidedAt, notes sql.NullString
    if err := row.Scan(&c.ID, &c.RunID, &planID, &c.Kind, &c.State,
        &decision, &decidedBy, &decidedAt, &notes); err != nil {
        if errors.Is(err, sql.ErrNoRows) {
            return nil, ErrCheckpointNotFound
        }
        return nil, fmt.Errorf("scan checkpoint: %w", err)
    }
    if planID.Valid {
        s := planID.String
        c.PlanID = &s
    }
    c.Decision = decision.String
    c.DecidedBy = decidedBy.String
    c.Notes = notes.String
    if decidedAt.Valid {
        if t, err := time.Parse(time.RFC3339Nano, decidedAt.String); err == nil {
            c.DecidedAt = &t
        }
    }
    return c, nil
}
```

- [ ] **Step 2 — `store/checkpoint_store_test.go`:**

Tests to include:
- `Insert + Get` round-trips a checkpoint with `PlanID == nil` (spec-approved)
- `Insert + Get` round-trips a checkpoint with `PlanID != nil` (plan-approved)
- `Resolve` updates state to `resolved`, sets `decision`, `decided_by`, `decided_at`, `notes`
- `Resolve` on a missing ID returns `ErrCheckpointNotFound`
- `ListByRun` returns checkpoints ordered (open first, then resolved by `decided_at` asc)
- `Get` on missing returns `ErrCheckpointNotFound`

Use the same `openTestDB(t)` helper.

- [ ] **Step 3 — Test, then commit:**

```bash
go test ./store -count=1 -run TestCheckpointStore
git add store/checkpoint_store.go store/checkpoint_store_test.go
git commit -m "Plan 119: CheckpointStore for resolved checkpoint records"
```

---

## Phase 5 — SupervisorEventStore + CostEventStore

**Files:**
- Create: `store/supervisor_event_store.go`, `store/supervisor_event_store_test.go`
- Create: `store/cost_event_store.go`, `store/cost_event_store_test.go`

- [ ] **Step 1 — SupervisorEventStore:**

```go
package store

import (
    "context"
    "database/sql"
    "errors"
    "fmt"
    "time"
)

type SupervisorEventRow struct {
    ID        string
    RunID     string
    JobID     string
    Kind      string  // supervisor.verdict | supervisor.retry | compliance-breach
    Verdict   string  // pass | fail | skipped
    RuleID    string
    Message   string
    CreatedAt time.Time
}

type SupervisorEventStore struct {
    db *sql.DB
}

func NewSupervisorEventStore(db *sql.DB) *SupervisorEventStore {
    return &SupervisorEventStore{db: db}
}

func (s *SupervisorEventStore) Insert(ctx context.Context, e *SupervisorEventRow) error {
    if e.CreatedAt.IsZero() {
        e.CreatedAt = time.Now().UTC()
    }
    _, err := s.db.ExecContext(ctx, `
        INSERT INTO supervisor_events
            (id, run_id, job_id, kind, verdict, rule_id, message, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
        e.ID, e.RunID, e.JobID, e.Kind, e.Verdict, e.RuleID, e.Message,
        e.CreatedAt.UTC().Format(time.RFC3339Nano),
    )
    if err != nil {
        return fmt.Errorf("insert supervisor_event: %w", err)
    }
    return nil
}

func (s *SupervisorEventStore) ListByJob(ctx context.Context, jobID string) ([]*SupervisorEventRow, error) {
    rows, err := s.db.QueryContext(ctx, `
        SELECT id, run_id, job_id, kind, verdict, rule_id, message, created_at
          FROM supervisor_events WHERE job_id = ?
          ORDER BY created_at ASC, id ASC`, jobID)
    if err != nil {
        return nil, fmt.Errorf("query supervisor_events: %w", err)
    }
    defer rows.Close()
    var out []*SupervisorEventRow
    for rows.Next() {
        e := &SupervisorEventRow{}
        var createdAt string
        if err := rows.Scan(&e.ID, &e.RunID, &e.JobID, &e.Kind, &e.Verdict,
            &e.RuleID, &e.Message, &createdAt); err != nil {
            return nil, fmt.Errorf("scan supervisor_event: %w", err)
        }
        if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
            e.CreatedAt = t
        } else {
            return nil, fmt.Errorf("parse supervisor_event.created_at: %w", err)
        }
        out = append(out, e)
    }
    return out, rows.Err()
}

func (s *SupervisorEventStore) ListByRun(ctx context.Context, runID string) ([]*SupervisorEventRow, error) {
    rows, err := s.db.QueryContext(ctx, `
        SELECT id, run_id, job_id, kind, verdict, rule_id, message, created_at
          FROM supervisor_events WHERE run_id = ?
          ORDER BY created_at ASC, id ASC`, runID)
    if err != nil {
        return nil, fmt.Errorf("query supervisor_events by run: %w", err)
    }
    defer rows.Close()
    var out []*SupervisorEventRow
    for rows.Next() {
        e := &SupervisorEventRow{}
        var createdAt string
        if err := rows.Scan(&e.ID, &e.RunID, &e.JobID, &e.Kind, &e.Verdict,
            &e.RuleID, &e.Message, &createdAt); err != nil {
            return nil, fmt.Errorf("scan: %w", err)
        }
        if t, perr := time.Parse(time.RFC3339Nano, createdAt); perr == nil {
            e.CreatedAt = t
        } else {
            return nil, fmt.Errorf("parse: %w", perr)
        }
        out = append(out, e)
    }
    return out, rows.Err()
}

// Ensure errors import is used (kept for parity with sibling stores).
var _ = errors.Is
```

- [ ] **Step 2 — Tests:**

`store/supervisor_event_store_test.go` covers:
- Round-trip Insert + ListByJob (single row)
- Multi-row ordering by `created_at ASC, id ASC`
- ListByRun
- Default `CreatedAt` set to now when zero on Insert
- Verdict values "pass", "fail", "skipped" all accepted
- Empty optional fields (RuleID, Message) accepted

- [ ] **Step 3 — CostEventStore:**

```go
package store

import (
    "context"
    "database/sql"
    "fmt"
    "time"
)

type CostEventRow struct {
    ID         string
    RunID      string
    JobID      string
    Provider   string
    Model      string
    TokensIn   int
    TokensOut  int
    USD        float64
    CreatedAt  time.Time
}

type CostEventStore struct {
    db *sql.DB
}

func NewCostEventStore(db *sql.DB) *CostEventStore {
    return &CostEventStore{db: db}
}

func (s *CostEventStore) Insert(ctx context.Context, e *CostEventRow) error {
    if e.CreatedAt.IsZero() {
        e.CreatedAt = time.Now().UTC()
    }
    _, err := s.db.ExecContext(ctx, `
        INSERT INTO cost_events
            (id, run_id, job_id, provider, model, tokens_in, tokens_out, usd, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        e.ID, e.RunID, e.JobID, e.Provider, e.Model, e.TokensIn, e.TokensOut,
        e.USD, e.CreatedAt.UTC().Format(time.RFC3339Nano),
    )
    if err != nil {
        return fmt.Errorf("insert cost_event: %w", err)
    }
    return nil
}

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
        if t, perr := time.Parse(time.RFC3339Nano, createdAt); perr == nil {
            e.CreatedAt = t
        } else {
            return nil, fmt.Errorf("parse cost_event.created_at: %w", perr)
        }
        out = append(out, e)
    }
    return out, rows.Err()
}
```

- [ ] **Step 4 — Tests:**

`store/cost_event_store_test.go` covers:
- Insert + ListByJob round-trip
- SumByRun with multiple rows
- SumByRun returns 0 for empty
- SumByJob with multiple rows
- Default CreatedAt set when zero

- [ ] **Step 5 — Run all store tests, then commit:**

```bash
go test ./store -count=1
git add store/supervisor_event_store.go store/supervisor_event_store_test.go store/cost_event_store.go store/cost_event_store_test.go
git commit -m "Plan 119: SupervisorEventStore + CostEventStore"
```

---

## Phase 6 — Wire supervisor evaluator to SupervisorEventStore

**Files:**
- Modify: `coding/supervisor/engine.go` — add an optional `EventSink` interface so callers can wire `SupervisorEventStore` without `coding/supervisor` importing `store/`. Default behavior (no sink) is identical.
- Modify: `coding/dispatch.go` — when constructing `SupervisorEvaluator`, pass the sink if non-nil.

- [ ] **Step 1 — Define the sink interface in `coding/supervisor`:**

```go
// EventSink is implemented by callers that want each rule result persisted as
// a row separate from the events log. Optional. The engine evaluates the same
// way regardless of whether a sink is provided; if Recv fails, the verdict is
// still returned (best-effort persistence).
type EventSink interface {
    Recv(ctx context.Context, runID, jobID string, result core.RuleResult) error
}
```

Add an `EvalContext.RunID` field if it does not already carry one — re-use the existing field if present (engine already takes RunID; check `coding/supervisor/engine.go` before duplicating).

- [ ] **Step 2 — In `Engine.Evaluate`, after each rule decision, call `sink.Recv` if a sink is set:**

```go
if e.sink != nil {
    if err := e.sink.Recv(ctx.Ctx, ctx.RunID, ctx.JobID, result); err != nil {
        // Best-effort: log via slog and continue.
        slog.Warn("supervisor event sink failed", "err", err)
    }
}
```

- [ ] **Step 3 — Adapter in `cli/`:**

In `cli/daemon.go` (and `cli/run.go` if it constructs its own engine), wrap `SupervisorEventStore`:

```go
type supervisorEventAdapter struct{ store *store.SupervisorEventStore }

func (a *supervisorEventAdapter) Recv(ctx context.Context, runID, jobID string, r core.RuleResult) error {
    verdict := "pass"
    switch {
    case r.Skipped:
        verdict = "skipped"
    case !r.Passed:
        verdict = "fail"
    }
    return a.store.Insert(ctx, &store.SupervisorEventRow{
        ID:      uuid.NewString(),
        RunID:   runID,
        JobID:   jobID,
        Kind:    string(core.EventSupervisorVerdict),
        Verdict: verdict,
        RuleID:  r.RuleName,
        Message: r.Message,
    })
}
```

(Use the existing UUID/ULID helper this codebase already uses — check `core/id.go` or similar.)

- [ ] **Step 4 — Tests:**

`coding/supervisor/engine_test.go`:

```go
type captureSink struct{ rows []core.RuleResult }
func (c *captureSink) Recv(ctx context.Context, runID, jobID string, r core.RuleResult) error {
    c.rows = append(c.rows, r); return nil
}

func TestEngine_EmitsToSink(t *testing.T) {
    eng := NewRuleEngine(rules)  // adapt to existing constructor
    sink := &captureSink{}
    eng.SetSink(sink)
    _, _ = eng.Evaluate(buildEvalContext())
    if len(sink.rows) == 0 {
        t.Fatalf("sink got 0 results")
    }
}

func TestEngine_SinkErrorDoesNotFailEvaluate(t *testing.T) {
    eng := NewRuleEngine(rules)
    eng.SetSink(failingSink{})
    if _, err := eng.Evaluate(buildEvalContext()); err != nil {
        t.Fatalf("Evaluate must succeed even when sink fails: %v", err)
    }
}
```

- [ ] **Step 5 — Run, then commit:**

```bash
go test ./coding/supervisor -count=1
go test ./cli -count=1
git add coding/supervisor/ coding/dispatch.go cli/daemon.go cli/run.go
git commit -m "Plan 119: persist supervisor verdicts via SupervisorEventStore"
```

---

## Phase 7 — Wire BuildFromPRDWorkflow to PlanStore + CheckpointStore

**Files:**
- Modify: `coding/workflow/build_from_prd.go`, `cli/run.go`, `cli/daemon.go`

- [ ] **Step 1 — Add an optional `PlanStore` and `CheckpointStore` field on `BuildFromPRDWorkflow`:**

```go
type BuildFromPRDWorkflow struct {
    // ... existing fields ...
    PlanStore       PlanStoreSink
    CheckpointStore CheckpointStoreSink
}

type PlanStoreSink interface {
    Insert(ctx context.Context, p *store.PlanRow) error
    UpdateState(ctx context.Context, id, state string) error
}

type CheckpointStoreSink interface {
    Insert(ctx context.Context, c *store.CheckpointRow) error
    Resolve(ctx context.Context, id, decision, decidedBy, notes string) error
}
```

(If keeping `coding/` clean of `store/` imports is desired, define narrower interfaces with concrete types in `core/` or have `cli/` adapt — review the project's existing import discipline before choosing.)

- [ ] **Step 2 — In `SchedulePlans`, after each manifest plan is added, call `PlanStore.Insert`:**

```go
for _, p := range w.allPlans {
    if w.PlanStore != nil {
        _ = w.PlanStore.Insert(ctx, &store.PlanRow{
            ID:       fmt.Sprintf("%s-plan-%d", runID, p.ID),
            RunID:    runID,
            Number:   p.ID,
            Title:    p.Title,
            BlocksOn: p.BlocksOn,
            State:    "pending",
        })
    }
}
```

- [ ] **Step 3 — When a plan transitions to running/done/failed, call `UpdateState`.**

Identify state-transition sites in `RunPhasesForPlan` and after `Shipper.Ship`. Add `UpdateState` calls; behavior is unchanged when `PlanStore` is nil.

- [ ] **Step 4 — Mirror for checkpoints in `cli/run.go` (where attention items are created):**

When a `phase-clean` / `plan-approved` / `ready-to-ship` attention item is written, also `CheckpointStore.Insert` an open row. When the attention is answered, `CheckpointStore.Resolve` with the decision.

- [ ] **Step 5 — Tests:**

`coding/workflow/build_from_prd_test.go`:
- New test: `TestBuildFromPRD_SchedulePlansWritesPlanRows` injects a stub PlanStoreSink, asserts one `Insert` per scheduled plan.
- New test: `TestBuildFromPRD_PlanStateTransitionsRecorded` — full happy path; expect `pending → running → done` UpdateState sequence.

`cli/run_test.go`:
- `TestRun_CheckpointInsertedOnAttention` — given a faked attention item, the CheckpointStore is told to Insert.
- `TestRun_CheckpointResolvedOnAnswer` — when attention is answered approve, CheckpointStore.Resolve gets `decision="approve"`.

- [ ] **Step 6 — Run tests, then commit:**

```bash
go test ./coding/workflow ./cli -count=1
git add coding/workflow/build_from_prd.go coding/workflow/build_from_prd_test.go cli/run.go cli/daemon.go cli/run_test.go
git commit -m "Plan 119: persist plans + checkpoints during run lifecycle"
```

---

## Phase 8 — Replay test (rebuild projection from events)

**Files:**
- Create: `store/replay_test.go`, `testdata/golden_events/run_with_supervisor.jsonl`

- [ ] **Step 1 — Write the golden event log fixture:**

`testdata/golden_events/run_with_supervisor.jsonl` (one line per event):

```json
{"id":"e1","run_id":"r1","sequence":1,"kind":"run.started","payload":{"mode":"interactive"},"created_at":"2026-04-26T10:00:00Z"}
{"id":"e2","run_id":"r1","sequence":2,"kind":"job.started","payload":{"job_id":"j1","role":"developer"},"created_at":"2026-04-26T10:00:01Z"}
{"id":"e3","run_id":"r1","sequence":3,"kind":"supervisor.verdict","payload":{"job_id":"j1","verdict":"pass","rule":"r-A","message":"ok"},"created_at":"2026-04-26T10:00:02Z"}
{"id":"e4","run_id":"r1","sequence":4,"kind":"supervisor.verdict","payload":{"job_id":"j1","verdict":"fail","rule":"r-B","message":"bad"},"created_at":"2026-04-26T10:00:03Z"}
{"id":"e5","run_id":"r1","sequence":5,"kind":"cost.delta","payload":{"job_id":"j1","provider":"anthropic","model":"opus","tokens_in":100,"tokens_out":50,"usd":0.01},"created_at":"2026-04-26T10:00:04Z"}
{"id":"e6","run_id":"r1","sequence":6,"kind":"job.complete","payload":{"job_id":"j1"},"created_at":"2026-04-26T10:00:05Z"}
```

- [ ] **Step 2 — Replay test:**

```go
func TestReplay_ReconstructsSupervisorAndCostProjections(t *testing.T) {
    db := openTestDB(t)
    runStore := NewRunStore(db)
    if err := runStore.Insert(context.Background(), &core.Run{
        ID: "r1", Mode: "interactive", State: "active", StartedAt: time.Now(),
    }); err != nil {
        t.Fatal(err)
    }

    // Read fixture and emit each event via the existing event-write helpers.
    events := readJSONL(t, "../testdata/golden_events/run_with_supervisor.jsonl")
    eventStore := NewEventStore(db)
    supStore := NewSupervisorEventStore(db)
    costStore := NewCostEventStore(db)

    for _, e := range events {
        if err := eventStore.Append(context.Background(), &core.Event{...}); err != nil {
            t.Fatal(err)
        }
        switch e.Kind {
        case "supervisor.verdict":
            _ = supStore.Insert(context.Background(), &SupervisorEventRow{...})
        case "cost.delta":
            _ = costStore.Insert(context.Background(), &CostEventRow{...})
        }
    }

    sups, _ := supStore.ListByRun(context.Background(), "r1")
    if len(sups) != 2 {
        t.Errorf("supervisor_events: got %d, want 2", len(sups))
    }
    sum, _ := costStore.SumByRun(context.Background(), "r1")
    if sum != 0.01 {
        t.Errorf("cost sum: got %v, want 0.01", sum)
    }
}
```

- [ ] **Step 3 — Run the test, then commit:**

```bash
go test ./store -count=1 -run TestReplay
git add store/replay_test.go testdata/golden_events/run_with_supervisor.jsonl
git commit -m "Plan 119: replay test for supervisor/cost event projections"
```

---

## Phase 9 — Full verification + decisions update

- [ ] **Step 1 — Full suite:**

```bash
go build ./...
go test -race ./... -count=1 -timeout 120s
golangci-lint run ./...
```

Expected: build clean, all tests pass with `-race`, 0 lint issues.

- [ ] **Step 2 — Update `docs/architecture/decisions.md`:**

Add an entry summarizing:
- Schema completion preserves the projection model: events first, projections second.
- `attention` and `checkpoints` are intentionally separate (attention = open queue; checkpoints = durable decisions). Keep the two writes paired in the same transaction whenever possible.
- `BudgetUSD` is a recorded but not-yet-enforced field.

- [ ] **Step 3 — Commit:**

```bash
git add docs/architecture/decisions.md
git commit -m "Plan 119: decisions.md — schema completion, attention vs checkpoints"
```

- [ ] **Step 4 — Open PR-ready branch state, mark plan ready for code review.**

---

## Self-Review Checklist

- [ ] Every new column has a sensible NOT NULL default so existing inserts/migrations don't break.
- [ ] Every new store has Insert + Get/List + (where it makes sense) Update or Resolve.
- [ ] Every new store has a unit test that covers happy path, missing-row error, and at least one ordering / list assertion.
- [ ] No `coding/*` package imports `store/*` directly — sinks are interfaces.
- [ ] `BudgetUSD` correctly distinguishes nil (unset) from 0.
- [ ] Replay test exists and passes.
- [ ] Migration runner picks up `007_*.sql` automatically (verified by running it).
- [ ] `decisions.md` updated.

---

## Code Review

(To be filled in after implementation by Codex review subagent.)

---

## Post-Execution Report

(To be filled in after implementation.)
