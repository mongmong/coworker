# Plan 119 — Schema Completion (V1 spec parity)

> **For agentic workers:** This plan is implemented inline by Claude Code. Use the executing-plans pattern: implement phase-by-phase, commit each phase, run the full test suite before merging.

**Goal:** Migrate the SQLite schema to match the V1 spec by adding the missing `plans`, `checkpoints`, `supervisor_events`, and `cost_events` tables, plus the missing `runs` (`prd_path`, `spec_path`, `cost_usd`, `budget_usd`) and `jobs` (`plan_id`, `phase_index`, `cost_usd`) columns. Wire the new tables into event-first writers (`WriteEventThenRow`) so the projection rows are always paired with an authoritative event in the same transaction.

**Architecture:** Additive migrations only — no schema breakage for existing rows. New tables are projections of events emitted by the runtime, so all stores integrate via `EventStore.WriteEventThenRow` to preserve the event-log-before-state invariant from `docs/architecture/decisions.md`. New stores follow the existing repo pattern (`type X struct{ db *DB; event *EventStore }`, constructor `NewX(db *DB, event *EventStore) *X`, methods named after the action — `CreateX`, `GetX`, `ListX`, `UpdateXState`).

**Tech Stack:** modernc.org/sqlite, raw `database/sql` + prepared statements, numbered SQL migrations, Go `time.RFC3339` timestamps (matching existing stores' formatting; see `event_store.go:61`).

**Reference:** `docs/specs/000-coworker-runtime-design.md` §Data Model (lines 752–805); `docs/reviews/2026-04-26-v1-comprehensive-review.md` finding "Schema is missing spec tables" (line 161); `docs/architecture/decisions.md` event-first invariant.

---

## Migration runner contract

Idempotency is **runner-level**, not SQL-level. `store/db.go:60-130` reads files in `migrations/` (sorted by name), parses the leading number as the version, and skips if `schema_migrations` already has that version. SQL bodies therefore do **not** need `IF NOT EXISTS` for `ALTER TABLE` columns; once `007` records its version, the migration body is never replayed. New `CREATE TABLE` statements still use `IF NOT EXISTS` to match the convention in `001_init.sql`.

---

## Scope

In scope:

1. New migration file `007_schema_completion.sql`:
   - `plans(id, run_id, number, title, blocks_on, branch, pr_url, state)`
   - `checkpoints(id, run_id, plan_id, kind, state, decision, decided_by, decided_at, notes)` (separate table from `attention`; see "Attention vs Checkpoints" below)
   - `supervisor_events(id, run_id, job_id, kind, verdict, rule_id, message, created_at)`
   - `cost_events(id, run_id, job_id, provider, model, tokens_in, tokens_out, usd, created_at)`
   - `runs.prd_path`, `runs.spec_path`, `runs.cost_usd`, `runs.budget_usd`
   - `jobs.plan_id`, `jobs.phase_index`, `jobs.cost_usd`
2. Go store types and CRUD helpers using the existing pattern: `PlanStore`, `CheckpointStore`, `SupervisorEventStore`, `CostEventStore`.
3. Event-first writers for new projection rows:
   - `SupervisorEventStore.RecordVerdict(ctx, runID, jobID, ruleResult)` writes `supervisor.verdict` event + projection row in one transaction.
   - `CostEventStore.RecordCost(ctx, runID, jobID, sample)` writes `cost.delta` event + projection row in one transaction.
   - `CheckpointStore.CreateCheckpoint(ctx, ...)` and `ResolveCheckpoint(ctx, ...)` each write paired events.
   - `PlanStore.CreatePlan(ctx, ...)` and `UpdatePlanState(ctx, ...)` each write paired events.
4. Wire the supervisor evaluator (via dispatcher in `coding/dispatch.go`) so each `RuleResult` produces a paired event + row through `SupervisorEventStore.RecordVerdict`. The `RuleEngine.Evaluate` itself stays pure — sink wiring lives where the existing `supervisor.verdict` event is already published.
5. Wire `BuildFromPRDWorkflow.SchedulePlans` and lifecycle transitions through `PlanStore.CreatePlan` / `UpdatePlanState` via a narrow `core` interface (no `coding → store` import).
6. Wire `cli/run.go`, `cli/daemon_http.go`, and `mcp/handlers_checkpoint.go` to call `CheckpointStore.CreateCheckpoint` when an attention item of kind=checkpoint is created, and `ResolveCheckpoint` whenever any code path answers + resolves the same attention item. (All three call-sites must be updated; see V1 review concern #4.)
7. RunStore: extend `CreateRun` and `GetRun` to set/return `prd_path`, `spec_path`, `cost_usd`, `budget_usd`. JobStore: extend `CreateJob` and `GetJob` for `plan_id`, `phase_index`, `cost_usd`.
8. Replay test using the runtime's actual event kinds (`job.created`, `job.completed`, `supervisor.verdict`, `cost.delta`) that round-trips a recorded event log through `WriteEventThenRow` and asserts that the projection tables match.

Out of scope:

- Cost capture from CLI stream-json output. Plan 121 wires actual provider/model/token data; this plan adds the table + writer helper used by that wiring.
- Budget enforcement — `budget_usd` is read-only metadata in this plan.
- TUI / HTTP API surfacing of the new columns (reused as-is by future plans).
- Removing or reshaping any existing tables. The `attention` table is unchanged.

### Attention vs Checkpoints

Two separate tables, intentional, paired:

- `attention` — the **live human-input UI surface**. Created when a checkpoint is opened. Resolved when answered. Already projected via existing `AttentionStore`.
- `checkpoints` — the **durable record of resolved checkpoints** per spec §Data Model. Inserted at the same time as the attention item; resolved when the attention is answered.

Both are written in lockstep. The pairing is enforced by writing both in the same `WriteEventThenRow` transaction. There is **no backfill** for prior `attention.kind='checkpoint'` rows — coworker has no shipped runs in production yet, so historical attention checkpoints will simply not have matching rows in the new table. A `decisions.md` entry documents this.

---

## File Structure

**Create:**
- `store/migrations/007_schema_completion.sql`
- `store/plan_store.go` + `store/plan_store_test.go`
- `store/checkpoint_store.go` + `store/checkpoint_store_test.go`
- `store/supervisor_event_store.go` + `store/supervisor_event_store_test.go`
- `store/cost_event_store.go` + `store/cost_event_store_test.go`
- `store/replay_test.go`
- `testdata/golden_events/run_with_supervisor.jsonl`

**Modify:**
- `core/run.go` (add `PRDPath`, `SpecPath`, `CostUSD`, `BudgetUSD *float64`)
- `core/job.go` (add `PlanID`, `PhaseIndex`, `CostUSD`)
- `core/event.go` (add new event kinds: `EventCheckpointOpened`, `EventCheckpointResolved`, `EventPlanCreated`, `EventPlanStateChanged`)
- `core/sinks.go` (new file — narrow interfaces `PlanWriter`, `CheckpointWriter` for `coding/` consumers; no `store/` import)
- `store/run_store.go` (extend CreateRun/GetRun for new columns)
- `store/job_store.go` (extend CreateJob/GetJob for new columns)
- `coding/dispatch.go` (after each rule result, call SupervisorEventStore via a `core.SupervisorWriter` interface)
- `coding/workflow/build_from_prd.go` (use `core.PlanWriter` from the workflow struct)
- `cli/daemon.go`, `cli/run.go`, `cli/daemon_http.go`, `mcp/handlers_checkpoint.go` (construct stores; resolve checkpoints in answer paths)
- `docs/architecture/decisions.md` (entry: schema completion)

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

-- Checkpoints table — durable record of checkpoint decisions.
-- Paired with attention items: an open checkpoint == open attention item.
-- plan_id may be NULL for run-level checkpoints (spec-approved).
CREATE TABLE IF NOT EXISTS checkpoints (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL REFERENCES runs(id),
    plan_id     TEXT REFERENCES plans(id),
    kind        TEXT NOT NULL,
    state       TEXT NOT NULL DEFAULT 'open',
    decision    TEXT NOT NULL DEFAULT '',
    decided_by  TEXT NOT NULL DEFAULT '',
    decided_at  TEXT,
    notes       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_checkpoints_run_id ON checkpoints(run_id);
CREATE INDEX IF NOT EXISTS idx_checkpoints_plan_id ON checkpoints(plan_id);

-- Supervisor events — projection of supervisor.verdict events.
CREATE TABLE IF NOT EXISTS supervisor_events (
    id         TEXT PRIMARY KEY,
    run_id     TEXT NOT NULL REFERENCES runs(id),
    job_id     TEXT NOT NULL REFERENCES jobs(id),
    kind       TEXT NOT NULL,
    verdict    TEXT NOT NULL,
    rule_id    TEXT NOT NULL DEFAULT '',
    message    TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_supervisor_events_run_id ON supervisor_events(run_id);
CREATE INDEX IF NOT EXISTS idx_supervisor_events_job_id ON supervisor_events(job_id);

-- Cost events — projection of cost.delta events.
CREATE TABLE IF NOT EXISTS cost_events (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL REFERENCES runs(id),
    job_id      TEXT NOT NULL REFERENCES jobs(id),
    provider    TEXT NOT NULL,
    model       TEXT NOT NULL,
    tokens_in   INTEGER NOT NULL DEFAULT 0,
    tokens_out  INTEGER NOT NULL DEFAULT 0,
    usd         REAL NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_cost_events_run_id ON cost_events(run_id);
CREATE INDEX IF NOT EXISTS idx_cost_events_job_id ON cost_events(job_id);

-- Runs: spec-required columns.
ALTER TABLE runs ADD COLUMN prd_path TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN spec_path TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN budget_usd REAL;  -- nullable: NULL means "no budget"

-- Jobs: spec-required columns.
ALTER TABLE jobs ADD COLUMN plan_id TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN phase_index INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0;
```

- [ ] **Step 2 — Run existing tests to confirm migration applies:**

```bash
go test ./store -count=1 -run TestDB
```

Expected: PASS. The migration runner picks up `007_*.sql` automatically (parsed by `db.go:89` `Sscanf("%d_", &version)`). Existing INSERTs into `runs` / `jobs` continue to work because the new columns all have NOT NULL DEFAULT.

- [ ] **Step 3 — Commit:**

```bash
git add store/migrations/007_schema_completion.sql
git commit -m "Plan 119: migration 007 — plans, checkpoints, supervisor_events, cost_events"
```

---

## Phase 2 — Add new event kinds

**Files:**
- Modify: `core/event.go`

- [ ] **Step 1 — Add event kinds:**

```go
const (
    // ... existing kinds ...

    // Plan lifecycle.
    EventPlanCreated      EventKind = "plan.created"
    EventPlanStateChanged EventKind = "plan.state_changed"

    // Checkpoint lifecycle (paired with attention items of kind=checkpoint).
    EventCheckpointOpened   EventKind = "checkpoint.opened"
    EventCheckpointResolved EventKind = "checkpoint.resolved"
)
```

- [ ] **Step 2 — Run unit tests + commit:**

```bash
go test ./core -count=1
git add core/event.go
git commit -m "Plan 119: new event kinds for plan/checkpoint lifecycle"
```

---

## Phase 3 — Run/Job store: new columns

**Files:**
- Modify: `core/run.go`, `core/job.go`, `store/run_store.go`, `store/run_store_test.go`, `store/job_store.go`, `store/job_store_test.go`

- [ ] **Step 1 — Extend `core.Run`:**

```go
// In core/run.go, add fields to Run struct:
PRDPath   string
SpecPath  string
CostUSD   float64
BudgetUSD *float64 // nil = no budget
```

- [ ] **Step 2 — Extend `core.Job`:**

```go
// In core/job.go, add fields:
PlanID     string
PhaseIndex int
CostUSD    float64
```

- [ ] **Step 3 — Update `RunStore.CreateRun` to write new columns inside the same `WriteEventThenRow` apply function:**

```go
return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
    var budget any = nil
    if run.BudgetUSD != nil {
        budget = *run.BudgetUSD
    }
    _, err := tx.ExecContext(ctx,
        `INSERT INTO runs (id, mode, state, started_at,
                           prd_path, spec_path, cost_usd, budget_usd)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
        run.ID, run.Mode, string(run.State),
        run.StartedAt.Format("2006-01-02T15:04:05Z"),
        run.PRDPath, run.SpecPath, run.CostUSD, budget,
    )
    ...
})
```

- [ ] **Step 4 — Update `RunStore.GetRun` (and `ListRuns`) to SELECT and Scan the new columns:**

```go
var budgetUSD sql.NullFloat64
err := s.db.QueryRowContext(ctx,
    `SELECT id, mode, state, started_at, ended_at,
            prd_path, spec_path, cost_usd, budget_usd
       FROM runs WHERE id = ?`, id,
).Scan(&run.ID, &run.Mode, &stateStr, &startedAtStr, &endedAtStr,
       &run.PRDPath, &run.SpecPath, &run.CostUSD, &budgetUSD)
...
if budgetUSD.Valid {
    v := budgetUSD.Float64
    run.BudgetUSD = &v
}
```

- [ ] **Step 5 — Test new columns round-trip:**

In `store/run_store_test.go` (use the existing test-DB helper — likely `mustOpenTestDB(t)`; read the file before writing):

```go
func TestRunStore_NewSpecColumnsRoundTrip(t *testing.T) {
    db, _ := openTestDB(t)
    es := NewEventStore(db)
    rs := NewRunStore(db, es)

    budget := 5.0
    in := &core.Run{
        ID: "run-1", Mode: "autopilot", State: core.RunStateActive,
        StartedAt: time.Now(),
        PRDPath:   "docs/prd.md",
        SpecPath:  "docs/spec.md",
        CostUSD:   1.25,
        BudgetUSD: &budget,
    }
    if err := rs.CreateRun(context.Background(), in); err != nil {
        t.Fatalf("CreateRun: %v", err)
    }
    out, err := rs.GetRun(context.Background(), "run-1")
    if err != nil {
        t.Fatalf("GetRun: %v", err)
    }
    if out.PRDPath != in.PRDPath || out.SpecPath != in.SpecPath ||
        out.CostUSD != in.CostUSD ||
        out.BudgetUSD == nil || *out.BudgetUSD != *in.BudgetUSD {
        t.Errorf("round-trip mismatch: %+v vs %+v", out, in)
    }
}

func TestRunStore_NilBudgetPreserved(t *testing.T) {
    db, _ := openTestDB(t)
    es := NewEventStore(db)
    rs := NewRunStore(db, es)
    in := &core.Run{
        ID: "run-2", Mode: "interactive", State: core.RunStateActive,
        StartedAt: time.Now(),
        BudgetUSD: nil,
    }
    if err := rs.CreateRun(context.Background(), in); err != nil {
        t.Fatal(err)
    }
    out, err := rs.GetRun(context.Background(), "run-2")
    if err != nil || out == nil {
        t.Fatal(err)
    }
    if out.BudgetUSD != nil {
        t.Errorf("BudgetUSD: got %v, want nil", *out.BudgetUSD)
    }
}
```

- [ ] **Step 6 — Update `JobStore.CreateJob` and `GetJob` mirror — three new columns. Test round-trip in `job_store_test.go`.**

- [ ] **Step 7 — Run tests, commit:**

```bash
go test ./store ./core -count=1
git add core/run.go core/job.go store/run_store.go store/run_store_test.go store/job_store.go store/job_store_test.go
git commit -m "Plan 119: extend runs/jobs with spec-required columns"
```

---

## Phase 4 — PlanStore (event-first)

**Files:**
- Create: `store/plan_store.go`, `store/plan_store_test.go`
- Modify: `core/run.go` or new `core/sinks.go` — define `core.PlanWriter` interface for the workflow consumer.

- [ ] **Step 1 — `core/sinks.go` interfaces (so coding/ does not import store/):**

```go
package core

import "context"

// PlanWriter is implemented by stores that persist plan rows.
// Defined in core/ so coding/ can depend on the abstraction without
// importing store/.
type PlanWriter interface {
    CreatePlan(ctx context.Context, p PlanRecord) error
    UpdatePlanState(ctx context.Context, planID, state string) error
    UpdatePlanBranchAndPR(ctx context.Context, planID, branch, prURL string) error
}

// PlanRecord is the inputs needed to record a plan row.
type PlanRecord struct {
    ID       string
    RunID    string
    Number   int
    Title    string
    BlocksOn []int
    Branch   string
    PRURL    string
    State    string // pending | running | done | failed | cancelled
}

// CheckpointWriter is implemented by stores that persist checkpoint rows.
type CheckpointWriter interface {
    CreateCheckpoint(ctx context.Context, c CheckpointRecord) error
    ResolveCheckpoint(ctx context.Context, id, decision, decidedBy, notes string) error
}

type CheckpointRecord struct {
    ID     string
    RunID  string
    PlanID string // empty string == NULL
    Kind   string
    Notes  string
}

// SupervisorWriter records supervisor rule results paired with a supervisor.verdict event.
type SupervisorWriter interface {
    RecordVerdict(ctx context.Context, runID, jobID string, result RuleResult) error
}

// CostWriter records token/cost samples paired with a cost.delta event.
type CostWriter interface {
    RecordCost(ctx context.Context, runID, jobID string, sample CostSample) error
}

type CostSample struct {
    Provider  string
    Model     string
    TokensIn  int
    TokensOut int
    USD       float64
}
```

- [ ] **Step 2 — `store/plan_store.go` (event-first via `WriteEventThenRow`):**

```go
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

var ErrPlanNotFound = errors.New("plan not found")

type PlanStore struct {
    db    *DB
    event *EventStore
}

func NewPlanStore(db *DB, event *EventStore) *PlanStore {
    return &PlanStore{db: db, event: event}
}

// CreatePlan writes a plan.created event and inserts the plans row in the
// same transaction.
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
            p.ID, p.RunID, p.Number, p.Title, string(blocksOn),
            p.Branch, p.PRURL, state,
        )
        if err != nil {
            return fmt.Errorf("insert plan: %w", err)
        }
        return nil
    })
}

func (s *PlanStore) UpdatePlanState(ctx context.Context, planID, state string) error {
    var runID string
    if err := s.db.QueryRowContext(ctx,
        "SELECT run_id FROM plans WHERE id = ?", planID,
    ).Scan(&runID); err != nil {
        if errors.Is(err, sql.ErrNoRows) {
            return ErrPlanNotFound
        }
        return fmt.Errorf("lookup plan run_id: %w", err)
    }
    payload, err := json.Marshal(map[string]string{
        "plan_id": planID, "state": state,
    })
    if err != nil {
        return fmt.Errorf("marshal plan.state_changed: %w", err)
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
            `UPDATE plans SET state = ? WHERE id = ?`, state, planID)
        if err != nil {
            return fmt.Errorf("update plan state: %w", err)
        }
        n, _ := res.RowsAffected()
        if n == 0 {
            return ErrPlanNotFound
        }
        return nil
    })
}

func (s *PlanStore) UpdatePlanBranchAndPR(ctx context.Context, planID, branch, prURL string) error {
    res, err := s.db.ExecContext(ctx,
        `UPDATE plans SET branch = ?, pr_url = ? WHERE id = ?`,
        branch, prURL, planID)
    if err != nil {
        return fmt.Errorf("update plan branch/pr: %w", err)
    }
    n, _ := res.RowsAffected()
    if n == 0 {
        return ErrPlanNotFound
    }
    return nil
}

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

func (s *PlanStore) GetPlan(ctx context.Context, id string) (*PlanRow, error) {
    row := s.db.QueryRowContext(ctx, `
        SELECT id, run_id, number, title, blocks_on, branch, pr_url, state
          FROM plans WHERE id = ?`, id)
    return s.scanPlan(row)
}

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
    return out, rows.Err()
}

type rowScanner interface{ Scan(...interface{}) error }

func (s *PlanStore) scanPlan(r rowScanner) (*PlanRow, error) {
    p := &PlanRow{}
    var blocksOn string
    if err := r.Scan(&p.ID, &p.RunID, &p.Number, &p.Title, &blocksOn,
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

// Compile-time assertion: PlanStore satisfies core.PlanWriter.
var _ core.PlanWriter = (*PlanStore)(nil)
```

- [ ] **Step 3 — Tests `store/plan_store_test.go`:**

Cover at minimum:
1. `CreatePlan` round-trips `GetPlan`, default state="pending", `BlocksOn` JSON survives.
2. `CreatePlan` writes a `plan.created` event (assert via `event.ListEvents(runID)`).
3. `UpdatePlanState` writes a `plan.state_changed` event AND updates the row.
4. `UpdatePlanState` on missing plan returns `ErrPlanNotFound`.
5. `UpdatePlanBranchAndPR` updates fields.
6. `ListPlansByRun` returns rows in `number ASC` order.
7. `GetPlan` on missing returns `ErrPlanNotFound`.
8. Unique `(run_id, number)` enforced by index.
9. `CreatePlan` is event-first: if the apply func fails, the event is also rolled back. Use a fixture that triggers a constraint violation (duplicate plan number) and assert the event count is unchanged.

- [ ] **Step 4 — Run + commit:**

```bash
go test ./store -count=1 -run TestPlanStore
git add core/sinks.go store/plan_store.go store/plan_store_test.go
git commit -m "Plan 119: PlanStore + core.PlanWriter (event-first writes)"
```

---

## Phase 5 — CheckpointStore (event-first, paired with attention)

**Files:**
- Create: `store/checkpoint_store.go`, `store/checkpoint_store_test.go`

- [ ] **Step 1 — Implementation:**

```go
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

var ErrCheckpointNotFound = errors.New("checkpoint not found")

type CheckpointStore struct {
    db    *DB
    event *EventStore
}

func NewCheckpointStore(db *DB, event *EventStore) *CheckpointStore {
    return &CheckpointStore{db: db, event: event}
}

func (s *CheckpointStore) CreateCheckpoint(ctx context.Context, c core.CheckpointRecord) error {
    payload, err := json.Marshal(map[string]string{
        "checkpoint_id": c.ID,
        "run_id":        c.RunID,
        "plan_id":       c.PlanID,
        "kind":          c.Kind,
    })
    if err != nil {
        return fmt.Errorf("marshal checkpoint.opened: %w", err)
    }
    ev := &core.Event{
        ID:            core.NewID(),
        RunID:         c.RunID,
        Kind:          core.EventCheckpointOpened,
        SchemaVersion: 1,
        CorrelationID: c.ID,
        Payload:       string(payload),
        CreatedAt:     time.Now(),
    }
    var planID any
    if c.PlanID != "" {
        planID = c.PlanID
    }
    return s.event.WriteEventThenRow(ctx, ev, func(tx *sql.Tx) error {
        _, err := tx.ExecContext(ctx, `
            INSERT INTO checkpoints (id, run_id, plan_id, kind, state, notes)
            VALUES (?, ?, ?, ?, 'open', ?)`,
            c.ID, c.RunID, planID, c.Kind, c.Notes,
        )
        if err != nil {
            return fmt.Errorf("insert checkpoint: %w", err)
        }
        return nil
    })
}

func (s *CheckpointStore) ResolveCheckpoint(ctx context.Context, id, decision, decidedBy, notes string) error {
    var runID string
    var state string
    if err := s.db.QueryRowContext(ctx,
        "SELECT run_id, state FROM checkpoints WHERE id = ?", id,
    ).Scan(&runID, &state); err != nil {
        if errors.Is(err, sql.ErrNoRows) {
            return ErrCheckpointNotFound
        }
        return fmt.Errorf("lookup checkpoint: %w", err)
    }
    if state == "resolved" {
        return nil // idempotent: already resolved.
    }
    payload, err := json.Marshal(map[string]string{
        "checkpoint_id": id,
        "decision":      decision,
        "decided_by":    decidedBy,
    })
    if err != nil {
        return fmt.Errorf("marshal checkpoint.resolved: %w", err)
    }
    ev := &core.Event{
        ID:            core.NewID(),
        RunID:         runID,
        Kind:          core.EventCheckpointResolved,
        SchemaVersion: 1,
        CorrelationID: id,
        Payload:       string(payload),
        CreatedAt:     time.Now(),
    }
    return s.event.WriteEventThenRow(ctx, ev, func(tx *sql.Tx) error {
        res, err := tx.ExecContext(ctx, `
            UPDATE checkpoints
               SET state = 'resolved',
                   decision = ?,
                   decided_by = ?,
                   decided_at = ?,
                   notes = ?
             WHERE id = ?`,
            decision, decidedBy,
            time.Now().UTC().Format(time.RFC3339Nano),
            notes, id)
        if err != nil {
            return fmt.Errorf("resolve checkpoint: %w", err)
        }
        n, _ := res.RowsAffected()
        if n == 0 {
            return ErrCheckpointNotFound
        }
        return nil
    })
}

type CheckpointRow struct {
    ID         string
    RunID      string
    PlanID     string // empty if NULL
    Kind       string
    State      string
    Decision   string
    DecidedBy  string
    DecidedAt  *time.Time
    Notes      string
}

func (s *CheckpointStore) GetCheckpoint(ctx context.Context, id string) (*CheckpointRow, error) {
    row := s.db.QueryRowContext(ctx, `
        SELECT id, run_id, plan_id, kind, state, decision, decided_by, decided_at, notes
          FROM checkpoints WHERE id = ?`, id)
    return s.scanCheckpoint(row)
}

func (s *CheckpointStore) ListCheckpointsByRun(ctx context.Context, runID string) ([]*CheckpointRow, error) {
    rows, err := s.db.QueryContext(ctx, `
        SELECT id, run_id, plan_id, kind, state, decision, decided_by, decided_at, notes
          FROM checkpoints WHERE run_id = ?
          ORDER BY (state = 'resolved') ASC, decided_at ASC, id ASC`, runID)
    if err != nil {
        return nil, fmt.Errorf("query checkpoints: %w", err)
    }
    defer rows.Close()
    var out []*CheckpointRow
    for rows.Next() {
        c, err := s.scanCheckpoint(rows)
        if err != nil {
            return nil, err
        }
        out = append(out, c)
    }
    return out, rows.Err()
}

func (s *CheckpointStore) scanCheckpoint(r rowScanner) (*CheckpointRow, error) {
    c := &CheckpointRow{}
    var planID, decidedAt sql.NullString
    if err := r.Scan(&c.ID, &c.RunID, &planID, &c.Kind, &c.State,
        &c.Decision, &c.DecidedBy, &decidedAt, &c.Notes); err != nil {
        if errors.Is(err, sql.ErrNoRows) {
            return nil, ErrCheckpointNotFound
        }
        return nil, fmt.Errorf("scan checkpoint: %w", err)
    }
    if planID.Valid {
        c.PlanID = planID.String
    }
    if decidedAt.Valid {
        if t, err := time.Parse(time.RFC3339Nano, decidedAt.String); err == nil {
            c.DecidedAt = &t
        }
    }
    return c, nil
}

var _ core.CheckpointWriter = (*CheckpointStore)(nil)
```

- [ ] **Step 2 — Tests in `store/checkpoint_store_test.go`:**

1. `CreateCheckpoint` writes `checkpoint.opened` event + row (verify both via `EventStore.ListEvents` and `GetCheckpoint`).
2. Round-trip with `PlanID == ""` (NULL) and with non-empty.
3. `ResolveCheckpoint` writes `checkpoint.resolved` event + flips `state`/`decision`/`decided_by`/`decided_at`.
4. `ResolveCheckpoint` is idempotent: second call does nothing, returns nil, does not write a duplicate event.
5. `ResolveCheckpoint` on missing returns `ErrCheckpointNotFound`.
6. `ListCheckpointsByRun` orders open before resolved, then by `decided_at`.
7. Event-first guarantee: simulate apply error (e.g., insert duplicate ID), assert event was rolled back.

- [ ] **Step 3 — Run + commit:**

```bash
go test ./store -count=1 -run TestCheckpointStore
git add store/checkpoint_store.go store/checkpoint_store_test.go
git commit -m "Plan 119: CheckpointStore + core.CheckpointWriter (event-first writes)"
```

---

## Phase 6 — SupervisorEventStore + CostEventStore (event-first writers)

**Files:**
- Create: `store/supervisor_event_store.go`, `store/supervisor_event_store_test.go`
- Create: `store/cost_event_store.go`, `store/cost_event_store_test.go`

- [ ] **Step 1 — `store/supervisor_event_store.go`:**

```go
package store

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "time"

    "github.com/chris/coworker/core"
)

type SupervisorEventStore struct {
    db    *DB
    event *EventStore
}

func NewSupervisorEventStore(db *DB, event *EventStore) *SupervisorEventStore {
    return &SupervisorEventStore{db: db, event: event}
}

// RecordVerdict writes a supervisor.verdict event and the projection row in
// the same transaction.
func (s *SupervisorEventStore) RecordVerdict(ctx context.Context, runID, jobID string, result core.RuleResult) error {
    verdict := "pass"
    switch {
    case result.Skipped:
        verdict = "skipped"
    case !result.Passed:
        verdict = "fail"
    }
    payload, err := json.Marshal(map[string]any{
        "run_id":  runID,
        "job_id":  jobID,
        "verdict": verdict,
        "rule":    result.RuleName,
        "message": result.Message,
    })
    if err != nil {
        return fmt.Errorf("marshal supervisor.verdict: %w", err)
    }
    now := time.Now()
    ev := &core.Event{
        ID:            core.NewID(),
        RunID:         runID,
        Kind:          core.EventSupervisorVerdict,
        SchemaVersion: 1,
        CorrelationID: jobID,
        Payload:       string(payload),
        CreatedAt:     now,
    }
    return s.event.WriteEventThenRow(ctx, ev, func(tx *sql.Tx) error {
        _, err := tx.ExecContext(ctx, `
            INSERT INTO supervisor_events
                (id, run_id, job_id, kind, verdict, rule_id, message, created_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
            core.NewID(), runID, jobID, string(core.EventSupervisorVerdict),
            verdict, result.RuleName, result.Message,
            now.UTC().Format(time.RFC3339Nano),
        )
        if err != nil {
            return fmt.Errorf("insert supervisor_event: %w", err)
        }
        return nil
    })
}

type SupervisorEventRow struct {
    ID        string
    RunID     string
    JobID     string
    Kind      string
    Verdict   string
    RuleID    string
    Message   string
    CreatedAt time.Time
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
    return scanSupervisorRows(rows)
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
    return scanSupervisorRows(rows)
}

func scanSupervisorRows(rows *sql.Rows) ([]*SupervisorEventRow, error) {
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

var _ core.SupervisorWriter = (*SupervisorEventStore)(nil)
```

- [ ] **Step 2 — `store/cost_event_store.go`:**

```go
package store

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "time"

    "github.com/chris/coworker/core"
)

type CostEventStore struct {
    db    *DB
    event *EventStore
}

func NewCostEventStore(db *DB, event *EventStore) *CostEventStore {
    return &CostEventStore{db: db, event: event}
}

func (s *CostEventStore) RecordCost(ctx context.Context, runID, jobID string, sample core.CostSample) error {
    payload, err := json.Marshal(map[string]any{
        "run_id":     runID,
        "job_id":     jobID,
        "provider":   sample.Provider,
        "model":      sample.Model,
        "tokens_in":  sample.TokensIn,
        "tokens_out": sample.TokensOut,
        "usd":        sample.USD,
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
            sample.TokensIn, sample.TokensOut, sample.USD,
            now.UTC().Format(time.RFC3339Nano),
        )
        if err != nil {
            return fmt.Errorf("insert cost_event: %w", err)
        }
        // Also bump the cumulative cost on jobs and runs.
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

var _ core.CostWriter = (*CostEventStore)(nil)
```

- [ ] **Step 3 — Tests:**

`supervisor_event_store_test.go`:
- `RecordVerdict` writes `supervisor.verdict` event AND row, in same tx.
- Verdict values: `pass` (Passed=true), `fail` (Passed=false), `skipped` (Skipped=true).
- `ListByJob` and `ListByRun` order by `created_at ASC, id ASC`.
- Event rollback on apply failure.

`cost_event_store_test.go`:
- `RecordCost` writes `cost.delta` event + row.
- Job and run `cost_usd` are bumped by `sample.USD`.
- `SumByRun` matches the sum of inserted rows.
- `SumByRun` returns 0 for empty.
- Multiple inserts accumulate correctly.

- [ ] **Step 4 — Run + commit:**

```bash
go test ./store -count=1
git add store/supervisor_event_store.go store/supervisor_event_store_test.go store/cost_event_store.go store/cost_event_store_test.go
git commit -m "Plan 119: SupervisorEventStore + CostEventStore (event-first writers)"
```

---

## Phase 7 — Wire dispatcher to SupervisorWriter

**Files:**
- Modify: `coding/dispatch.go` — add `SupervisorWriter core.SupervisorWriter` field to the dispatcher; after each rule result is added to the verdict, call `Recv` on the writer.
- Modify: `cli/daemon.go` and `cli/run.go` — construct `SupervisorEventStore` and inject it into the dispatcher.

- [ ] **Step 1 — Locate the existing supervisor.verdict write site in `coding/dispatch.go`:**

```bash
grep -n "supervisor.verdict\|Evaluate\|SupervisorVerdict" coding/dispatch.go
```

The dispatcher already publishes a `supervisor.verdict` event for the aggregated verdict. After `Evaluate` returns, iterate `verdict.Results` and for each non-empty one call `dispatcher.SupervisorWriter.RecordVerdict(ctx, runID, jobID, result)`. This produces one `supervisor.verdict` event **per rule result** (the aggregated one becomes redundant and should be removed; document this behavior change in the commit message).

- [ ] **Step 2 — Make the writer optional. If `SupervisorWriter == nil`, skip — preserves existing test wiring that does not need persistence.**

- [ ] **Step 3 — Tests in `coding/dispatch_test.go`:**

```go
type captureWriter struct {
    rows []core.RuleResult
}

func (c *captureWriter) RecordVerdict(_ context.Context, _ string, _ string, r core.RuleResult) error {
    c.rows = append(c.rows, r)
    return nil
}

func TestDispatch_PersistsRuleResults(t *testing.T) {
    w := &captureWriter{}
    d := newTestDispatcher(t)
    d.SupervisorWriter = w
    _ = d.Dispatch(...)
    if len(w.rows) == 0 {
        t.Fatalf("expected captured rule results")
    }
}

func TestDispatch_WriterFailureDoesNotFailDispatch(t *testing.T) {
    d := newTestDispatcher(t)
    d.SupervisorWriter = failingWriter{}
    if err := d.Dispatch(...); err != nil {
        t.Fatalf("dispatch must tolerate writer failure: %v", err)
    }
}
```

- [ ] **Step 4 — Wire in `cli/daemon.go`:**

```go
supEventStore := store.NewSupervisorEventStore(db, eventStore)
dispatcher.SupervisorWriter = supEventStore
```

- [ ] **Step 5 — Run + commit:**

```bash
go test ./coding ./cli -count=1
git add coding/dispatch.go coding/dispatch_test.go cli/daemon.go cli/run.go
git commit -m "Plan 119: dispatcher persists supervisor verdicts via event-first store"
```

---

## Phase 8 — Wire BuildFromPRDWorkflow to PlanWriter; resolve Checkpoints in all answer paths

**Files:**
- Modify: `coding/workflow/build_from_prd.go` (add `PlanWriter core.PlanWriter` field; call `CreatePlan` for each scheduled plan; call `UpdatePlanState` at lifecycle transitions)
- Modify: `cli/run.go` (use `CheckpointWriter.CreateCheckpoint` whenever an attention item of `kind=checkpoint` is created; use `ResolveCheckpoint` whenever an answer is written)
- Modify: `cli/daemon_http.go` (`handleAnswerAttention` resolves matching checkpoint after answering attention)
- Modify: `mcp/handlers_checkpoint.go` (`handleCheckpointAdvance` and `handleCheckpointRollback` resolve matching checkpoint after answering attention)

- [ ] **Step 1 — Add fields:**

```go
// coding/workflow/build_from_prd.go
type BuildFromPRDWorkflow struct {
    // ... existing fields ...
    PlanWriter       core.PlanWriter       // optional
    CheckpointWriter core.CheckpointWriter // optional
}
```

- [ ] **Step 2 — In `SchedulePlans`, after each manifest plan is queued, call `CreatePlan`:**

```go
if w.PlanWriter != nil {
    _ = w.PlanWriter.CreatePlan(ctx, core.PlanRecord{
        ID:       fmt.Sprintf("%s-plan-%d", runID, p.ID),
        RunID:    runID,
        Number:   p.ID,
        Title:    p.Title,
        BlocksOn: p.BlocksOn,
        State:    "pending",
    })
}
```

- [ ] **Step 3 — At lifecycle transitions in `RunPhasesForPlan`, call `UpdatePlanState("running"|"done"|"failed")`. Locate transitions by inspecting current code.**

- [ ] **Step 4 — Where attention checkpoints are created (`cli/run.go`, search for `core.AttentionCheckpoint`):** when creating, also call `CheckpointWriter.CreateCheckpoint`. Use the same ID for both rows so resolution can be paired.

- [ ] **Step 5 — In every answer path, resolve matching checkpoint:**

- `cli/daemon_http.go::handleAnswerAttention` — after `AnswerAttention` + `ResolveAttention`, look up `attentionItem.Kind`. If kind == checkpoint, call `CheckpointWriter.ResolveCheckpoint(item.ID, answer, answeredBy, "")`.
- `mcp/handlers_checkpoint.go::handleCheckpointAdvance` — after `AnswerAttention(approve)` + `ResolveAttention`, call `CheckpointWriter.ResolveCheckpoint(in.AttentionID, "approve", answeredBy, in.Notes)`.
- `mcp/handlers_checkpoint.go::handleCheckpointRollback` — similarly with `"reject"`.
- `cli/run.go::resume-after-attention` paths (e.g., `--resume-after-attention`) — search for any call site of `AnswerAttention` and add the `ResolveCheckpoint` call.

The shared invariant: every call to `AttentionStore.AnswerAttention` for a checkpoint-kind item must be followed by `CheckpointWriter.ResolveCheckpoint(sameID, ...)`.

- [ ] **Step 6 — Tests:**

`coding/workflow/build_from_prd_test.go`:
- `TestSchedulePlans_CallsPlanWriter` — stub PlanWriter, assert `CreatePlan` called once per manifest plan with correct fields.

`cli/daemon_http_test.go`:
- `TestAnswerAttention_ResolvesCheckpoint` — POST `/attention/{id}/answer` for a checkpoint-kind item; assert `CheckpointWriter.ResolveCheckpoint` was called with the same ID and the supplied answer.
- `TestAnswerAttention_NonCheckpoint_DoesNotResolve` — POST for a question-kind item; assert no `ResolveCheckpoint` call.

`mcp/handlers_checkpoint_test.go`:
- `TestCheckpointAdvance_ResolvesCheckpoint` — call advance handler; assert `ResolveCheckpoint` called with `decision="approve"`.
- `TestCheckpointRollback_ResolvesCheckpoint` — assert `decision="reject"`.

- [ ] **Step 7 — Run + commit:**

```bash
go test ./coding/workflow ./cli ./mcp -count=1
git add coding/workflow/ cli/ mcp/
git commit -m "Plan 119: persist plan rows + resolve checkpoints in all answer paths"
```

---

## Phase 9 — Replay test (rebuild projection from event log)

**Files:**
- Create: `store/replay_test.go`
- Create: `testdata/golden_events/run_with_supervisor.jsonl`

- [ ] **Step 1 — Generate the fixture from real runtime emission:**

Rather than hand-write JSONL, write a small helper inside the test that emits the canonical event sequence using existing event-store API, then captures and compares the persisted state. This avoids the "fixture goes stale" trap. Pseudo:

```go
// In store/replay_test.go
func TestReplay_RebuildProjectionsFromEventLog(t *testing.T) {
    db, _ := openTestDB(t)
    es := NewEventStore(db)
    rs := NewRunStore(db, es)
    js := NewJobStore(db, es)
    sup := NewSupervisorEventStore(db, es)
    cost := NewCostEventStore(db, es)

    ctx := context.Background()
    if err := rs.CreateRun(ctx, &core.Run{
        ID: "r1", Mode: "interactive", State: core.RunStateActive,
        StartedAt: time.Now(),
    }); err != nil {
        t.Fatal(err)
    }
    if err := js.CreateJob(ctx, &core.Job{
        ID: "j1", RunID: "r1", Role: "developer",
        State: core.JobStatePending,
        DispatchedBy: "scheduler",
        StartedAt: time.Now(),
    }); err != nil {
        t.Fatal(err)
    }
    // Two supervisor verdicts.
    _ = sup.RecordVerdict(ctx, "r1", "j1", core.RuleResult{
        RuleName: "r-A", Passed: true, Message: "ok",
    })
    _ = sup.RecordVerdict(ctx, "r1", "j1", core.RuleResult{
        RuleName: "r-B", Passed: false, Message: "bad",
    })
    // One cost delta.
    _ = cost.RecordCost(ctx, "r1", "j1", core.CostSample{
        Provider: "anthropic", Model: "opus",
        TokensIn: 100, TokensOut: 50, USD: 0.01,
    })

    // Snapshot the event log to the golden file (gated by COWORKER_REGEN=1).
    if os.Getenv("COWORKER_REGEN") == "1" {
        events, _ := es.ListEvents(ctx, "r1")
        writeJSONL(t, "../testdata/golden_events/run_with_supervisor.jsonl", events)
    }

    // Verify projections.
    sups, _ := sup.ListByRun(ctx, "r1")
    if len(sups) != 2 {
        t.Errorf("supervisor_events: got %d, want 2", len(sups))
    }
    if got, _ := cost.SumByRun(ctx, "r1"); got != 0.01 {
        t.Errorf("cost sum: got %v, want 0.01", got)
    }
    runRow, _ := rs.GetRun(ctx, "r1")
    if runRow.CostUSD != 0.01 {
        t.Errorf("run.cost_usd: got %v, want 0.01", runRow.CostUSD)
    }
    jobRow, _ := js.GetJob(ctx, "j1")
    if jobRow.CostUSD != 0.01 {
        t.Errorf("job.cost_usd: got %v, want 0.01", jobRow.CostUSD)
    }
}
```

Then add a second test that reads the golden file, replays only the events into a fresh DB via `WriteEventThenRow` (from raw events), and asserts that the rebuilt projection state equals the original. This is the actual "replay" assertion.

- [ ] **Step 2 — Generate fixture:**

```bash
COWORKER_REGEN=1 go test ./store -count=1 -run TestReplay_RebuildProjectionsFromEventLog
```

- [ ] **Step 3 — Run + commit:**

```bash
go test ./store -count=1 -run TestReplay
git add store/replay_test.go testdata/golden_events/run_with_supervisor.jsonl
git commit -m "Plan 119: replay test — rebuild supervisor/cost projections from event log"
```

---

## Phase 10 — Full verification + decisions.md

- [ ] **Step 1 — Full suite:**

```bash
go build ./...
go test -race ./... -count=1 -timeout 180s
golangci-lint run ./...
```

Expected: build clean, all tests pass with `-race`, 0 lint issues.

- [ ] **Step 2 — Update `docs/architecture/decisions.md`:**

```markdown
### Schema completion (Plan 119)

- Plans, checkpoints, supervisor_events, and cost_events tables now exist as projections of events.
- All projection writes go through `EventStore.WriteEventThenRow` to preserve the event-log-before-state invariant.
- `attention` and `checkpoints` are kept separate by design: attention is the live UI surface, checkpoints is the durable decision record. Every `AttentionStore.AnswerAttention` call for a kind=checkpoint item must be paired with a `CheckpointWriter.ResolveCheckpoint` call (enforced by tests on every answer path: HTTP, MCP, CLI).
- `runs.budget_usd` is recorded but not enforced. Enforcement is deferred to Plan 121+.
- No backfill of pre-Plan-119 attention checkpoints into the new `checkpoints` table — coworker had no shipped runs at the time of this migration.
```

- [ ] **Step 3 — Commit:**

```bash
git add docs/architecture/decisions.md
git commit -m "Plan 119: decisions.md — schema completion + attention/checkpoint pairing"
```

---

## Self-Review Checklist

- [ ] Every new column has NOT NULL DEFAULT (or is intentionally nullable, like `budget_usd` and `decided_at`) so existing inserts continue to work.
- [ ] Every new write path uses `WriteEventThenRow`. No projection row exists without a paired event.
- [ ] No `coding/*` package imports `store/*` directly. All sinks are `core.*Writer` interfaces.
- [ ] Every attention-checkpoint answer path resolves the matching checkpoint row: HTTP `/attention/{id}/answer`, MCP `orch_checkpoint_advance`, MCP `orch_checkpoint_rollback`, any `cli/run.go` resume-after-attention path.
- [ ] Replay test exists, passes, and demonstrates that rebuilding projections from the event log yields equivalent rows.
- [ ] `TestRunStore_*`, `TestJobStore_*`, `TestPlanStore_*`, `TestCheckpointStore_*`, `TestSupervisorEventStore_*`, `TestCostEventStore_*` all pass with `-race`.
- [ ] Migration runner picks up `007_*.sql` automatically (verified by running it).
- [ ] `decisions.md` updated.

---

## Code Review

(To be filled in after implementation by Codex review subagent.)

---

## Post-Execution Report

(To be filled in after implementation.)
