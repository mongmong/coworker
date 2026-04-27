# Plan 125 — B3 + B4: Schema Drift (findings columns + dispatches.mode)

> Implemented inline. Additive migration; no breaking changes.

**Goal:** Close two BLOCKERs from the 2026-04-27 audit:
1. **B3** — `findings` table missing spec-required columns `plan_id`, `phase_index`, `reviewer_handle`. Spec line 778-779 declares them; implementation has only the basic core. Operators can't query findings by plan or phase, can't attribute findings to a specific reviewer role.
2. **B4** — `dispatches` table missing the spec's `mode` column (`persistent | ephemeral | in-process`). Implementation tracks dispatch lifecycle via state (`pending|leased|completed`) but provides no auditable record of HOW each dispatch executed.

**Architecture:**
- One additive migration (`009_findings_dispatches_drift.sql`) adds the four missing columns. All have `NOT NULL DEFAULT ''` (or `0` for the integer) so existing rows continue to satisfy the schema.
- Update `core.Finding` and `core.Dispatch` to expose the new fields.
- Update finding-write paths (`coding/dispatch.go::Orchestrate` finding loop, `coding/phaseloop/fanin.go`) and the finding-resolution path to populate `plan_id`, `phase_index`, `reviewer_handle` from the dispatch context.
- Update `coding/dispatch.go::EnqueueDispatch` (or wherever Dispatcher persists dispatches) to write `mode = "ephemeral"` for synchronous spawns. The existing pull-model `EnqueueDispatch` (in `store/dispatch_store.go`) writes `mode = "persistent"` since it's the queue for persistent workers. The MCP `orch_next_dispatch` claim path doesn't change `mode` — the value reflects intent at enqueue time.
- `FindingStore.ListFindings` returns the new fields; new test asserts round-trip.
- `DispatchStore.ClaimNextDispatch` and friends preserve `mode` through state transitions.

**Tech Stack:** Additive SQL migration, Go struct field additions, no new dependencies.

**Reference:** `docs/specs/000-coworker-runtime-design.md` lines 758-805 (Data Model); `docs/reviews/2026-04-27-comprehensive-audit.md` §B3 + §B4.

---

## Required-API audit

| Surface | Reality |
| --- | --- |
| `store/migrations/008_*.sql` | Doesn't exist yet (migration 007 = Plan 119). New file is `009_findings_dispatches_drift.sql` (or `008_*.sql` — verify the next available number first). |
| `core.Finding` fields | `ID, RunID, JobID, Path, Line, Severity, Body, Fingerprint, ResolvedByJobID, ResolvedAt, SourceJobIDs` — needs `PlanID, PhaseIndex, ReviewerHandle` added. |
| `core.Dispatch` fields | `ID, RunID, Role, JobID, Prompt, Inputs, State, WorkerHandle, LeasedAt, CompletedAt, CreatedAt` — needs `Mode` added. |
| `FindingStore.InsertFinding` | At `store/finding_store.go:27`. INSERT statement needs the three new columns. |
| `FindingStore.ListFindings` | At `store/finding_store.go:118`. SELECT needs the three new columns + Scan into struct. |
| `DispatchStore.EnqueueDispatch` | At `store/dispatch_store.go:26`. INSERT needs `mode` column. |
| Existing finding-write call sites | `coding/dispatch.go::Orchestrate` at finding-persistence loop; `coding/phaseloop/fanin.go` for fan-in dedup; tests. Each must pass through PlanID/PhaseIndex/ReviewerHandle when known. |
| Spec line 770-772 says `mode: persistent | ephemeral | in-process`. | Implementation: `EnqueueDispatch` is for the pull queue (persistent). Direct `agent.Dispatch` invocations (ephemeral) don't go through the dispatches table today. So writing `mode` only in `EnqueueDispatch` covers the persistent path; ephemeral dispatch happens entirely in-process and is recorded only via the `jobs` table + events, not `dispatches`. **Confirm this matches the spec's intent.** |

---

## Scope

In scope:

1. New migration file (next available number) adds:
   - `findings.plan_id TEXT NOT NULL DEFAULT ''`
   - `findings.phase_index INTEGER NOT NULL DEFAULT 0`
   - `findings.reviewer_handle TEXT NOT NULL DEFAULT ''`
   - `dispatches.mode TEXT NOT NULL DEFAULT 'persistent'` (the pull queue is always persistent)
   - Indexes: `idx_findings_plan_id`, `idx_findings_run_plan` (run_id, plan_id), `idx_dispatches_mode`.
2. `core/finding.go` — three new fields with sensible defaults; existing code that constructs `Finding{...}` without setting them continues to compile because Go zero-values them.
3. `core/dispatch.go` — `Mode` field; constants `DispatchModePersistent`, `DispatchModeEphemeral`, `DispatchModeInProcess`.
4. `store/finding_store.go` — INSERT + SELECT cover the new columns. Scan into the new struct fields.
5. `store/dispatch_store.go` — INSERT writes `mode` (default `"persistent"` if empty). Scan covers it.
6. `coding/dispatch.go::Orchestrate` finding-persistence loop — pass `f.PlanID = ...` if plan context is available; otherwise leave empty (so existing tests still pass). The dispatcher receives plan context via `DispatchInput.Inputs["plan_id"]` / `["phase_index"]` for some roles; thread it through.
7. `coding/phaseloop/fanin.go` — preserve PlanID/PhaseIndex during dedup.
8. New test: round-trip insert + list of finding with all new fields populated; round-trip insert + list of dispatch with explicit mode.
9. Update findings-immutability trigger (migration 006) — ensure the new columns are NOT in the immutable set (they're set at insert time and shouldn't change, but no need to TRIGGER-protect them since the resolution UPDATE doesn't touch them).
10. `decisions.md` Decision 13 — schema drift reconciliation.

Out of scope:

- TUI / HTTP / MCP surface updates that consume the new fields (separate plan).
- Recording dispatch mode for ephemeral / in-process dispatches in the dispatches table (currently those don't go through dispatches at all). This is documented in Decision 13 as a forward-looking note; `mode` for ephemeral would require a separate event-write path that's out of scope here.
- Backfilling old findings rows with derived plan_id/phase_index — pre-existing rows keep `''`/`0`.

---

## File Structure

**Create:**
- `store/migrations/008_findings_dispatches_drift.sql` (or 009 if 008 already exists — verify)

**Modify:**
- `core/finding.go` — `PlanID`, `PhaseIndex`, `ReviewerHandle` fields.
- `core/dispatch.go` — `Mode` field + constants.
- `store/finding_store.go` — INSERT, SELECT, Scan.
- `store/finding_store_test.go` — round-trip test.
- `store/dispatch_store.go` — INSERT, SELECT, Scan.
- `store/dispatch_store_test.go` — round-trip test for mode.
- `coding/dispatch.go::Orchestrate` — populate finding fields from input map.
- `coding/phaseloop/fanin.go` — preserve fields through dedup.
- `docs/architecture/decisions.md` — Decision 13.

---

## Phase 1 — Migration

**Files:** `store/migrations/008_findings_dispatches_drift.sql`

- [ ] **Step 1 — Verify the next migration number:**

```bash
ls store/migrations/
```

Take the next free number (probably 008 if 007 was Plan 119 and there's no 008 yet).

- [ ] **Step 2 — Write the migration:**

```sql
-- Plan 125: schema drift fixes (BLOCKERs B3 + B4 from 2026-04-27 audit).
-- Adds spec-required columns to findings and dispatches.

-- B3: findings — plan_id, phase_index, reviewer_handle.
ALTER TABLE findings ADD COLUMN plan_id TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN phase_index INTEGER NOT NULL DEFAULT 0;
ALTER TABLE findings ADD COLUMN reviewer_handle TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_findings_plan_id ON findings(plan_id);
CREATE INDEX IF NOT EXISTS idx_findings_run_plan ON findings(run_id, plan_id);

-- B4: dispatches — mode (persistent | ephemeral | in-process).
-- Pre-existing rows are all from the pull queue, so default to 'persistent'.
ALTER TABLE dispatches ADD COLUMN mode TEXT NOT NULL DEFAULT 'persistent';

CREATE INDEX IF NOT EXISTS idx_dispatches_mode ON dispatches(mode);
```

- [ ] **Step 3 — Run migration test:**

```bash
go test ./store -count=1 -run TestDB
```

- [ ] **Step 4 — Commit:**

```bash
git add store/migrations/008_findings_dispatches_drift.sql
git commit -m "Plan 125 Phase 1: migration 008 — findings spec columns + dispatches.mode"
```

---

## Phase 2 — Core struct fields

**Files:** `core/finding.go`, `core/dispatch.go`

- [ ] **Step 1 — Extend `core.Finding`:**

```go
type Finding struct {
    // ... existing fields ...

    // PlanID is the plan number this finding is associated with (e.g., "100").
    // Empty when the finding originated outside a plan execution path
    // (interactive ad-hoc dispatch). Plan 125.
    PlanID string

    // PhaseIndex is the 0-based phase index within the plan when the finding
    // was created. Plan 125.
    PhaseIndex int

    // ReviewerHandle identifies which reviewer role produced the finding
    // (e.g., "reviewer.arch", "reviewer.frontend"). Empty when the finding
    // was synthesized in-process (e.g., a contract violation surfaced as a
    // finding by the supervisor). Plan 125.
    ReviewerHandle string
}
```

- [ ] **Step 2 — Extend `core.Dispatch`:**

```go
type Dispatch struct {
    // ... existing fields ...

    // Mode declares how this dispatch is to be executed.
    //   "persistent" — pulled by a long-lived CLI worker via MCP.
    //   "ephemeral"  — spawned synchronously as a subprocess.
    //   "in-process" — handled by an in-process agent (rare; e.g., supervisor).
    // Default "persistent" for the pull queue. Plan 125.
    Mode string
}

const (
    DispatchModePersistent string = "persistent"
    DispatchModeEphemeral  string = "ephemeral"
    DispatchModeInProcess  string = "in-process"
)
```

- [ ] **Step 3 — Run unit tests + commit:**

```bash
go test ./core -count=1
git add core/finding.go core/dispatch.go
git commit -m "Plan 125 Phase 2: core.Finding + core.Dispatch new fields"
```

---

## Phase 3 — Store INSERT/SELECT updates

**Files:** `store/finding_store.go`, `store/dispatch_store.go`, plus tests.

- [ ] **Step 1 — Extend `FindingStore.InsertFinding`** (`store/finding_store.go:55-65`):

```go
return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
    _, err := tx.ExecContext(ctx,
        `INSERT INTO findings
            (id, run_id, job_id, path, line, severity, body, fingerprint,
             plan_id, phase_index, reviewer_handle)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        finding.ID, finding.RunID, finding.JobID,
        finding.Path, finding.Line, string(finding.Severity),
        finding.Body, finding.Fingerprint,
        finding.PlanID, finding.PhaseIndex, finding.ReviewerHandle,
    )
    return err
})
```

- [ ] **Step 2 — Extend `FindingStore.ListFindings`** to SELECT and Scan the new columns.

- [ ] **Step 3 — Extend `DispatchStore.EnqueueDispatch`:**

```go
mode := d.Mode
if mode == "" {
    mode = core.DispatchModePersistent
}
// INSERT statement now has 10 columns; bind mode at the end.
```

Update SELECT statements (`ClaimNextDispatch`, `ListPendingDispatches`, etc.) to include `mode` and Scan it into `*Dispatch.Mode`.

- [ ] **Step 4 — Tests:**

`store/finding_store_test.go`:
- New test: `TestFindingStore_NewFieldsRoundTrip` — insert with PlanID="100", PhaseIndex=2, ReviewerHandle="reviewer.arch"; ListFindings returns the values.
- New test: `TestFindingStore_DefaultsForUnpopulatedFields` — insert with empty values; round-trip yields empty strings / zero.

`store/dispatch_store_test.go`:
- New test: `TestDispatchStore_ModeDefaultPersistent` — Enqueue without setting Mode; round-trip yields `"persistent"`.
- New test: `TestDispatchStore_ModeRoundTrip` — Enqueue with Mode="ephemeral"; round-trip preserves.

- [ ] **Step 5 — Commit:**

```bash
go test -race ./store ./core -count=1
git add store/finding_store.go store/finding_store_test.go store/dispatch_store.go store/dispatch_store_test.go
git commit -m "Plan 125 Phase 3: store INSERT/SELECT for new finding + dispatch fields"
```

---

## Phase 4 — Wire fields through dispatcher + phase loop

**Files:** `coding/dispatch.go`, `coding/phaseloop/fanin.go`, related tests.

- [ ] **Step 1 — In `coding/dispatch.go::Orchestrate` finding-persistence loop, populate the new fields when known:**

The `DispatchInput.Inputs` map may contain `"plan_id"` and `"phase_index"` strings (the workflow passes them in for plan-driven dispatches). The dispatcher's role name is `role.Name` — that maps to `ReviewerHandle` for reviewer roles.

```go
for i := range lastResult.Findings {
    f := &lastResult.Findings[i]
    f.RunID = runID
    f.JobID = lastJobID
    if f.ID == "" {
        f.ID = core.NewID()
    }
    // Populate new spec fields from dispatch context. Best-effort —
    // missing inputs leave the fields empty (acceptable per migration
    // defaults). Plan 125.
    if v, ok := input.Inputs["plan_id"]; ok {
        f.PlanID = v
    }
    if v, ok := input.Inputs["phase_index"]; ok {
        if n, err := strconv.Atoi(v); err == nil {
            f.PhaseIndex = n
        }
    }
    if strings.HasPrefix(role.Name, "reviewer.") {
        f.ReviewerHandle = role.Name
    }
    if err := findingStore.InsertFinding(ctx, f); err != nil { /* ... */ }
}
```

- [ ] **Step 2 — In `coding/phaseloop/fanin.go`, ensure deduplication preserves the new fields.** Re-read fanin.go before editing to verify the existing dedup loop preserves all `Finding` fields (likely it copies the whole struct, so no change needed). If it constructs a new struct from scratch, propagate.

- [ ] **Step 3 — Existing tests should still pass.** New test in `coding/dispatch_test.go`:

```go
func TestOrchestrate_PopulatesPlanFieldsOnFindings(t *testing.T) {
    // Reuse the makeMockDispatcher pattern. Pass plan_id/phase_index/spec_path
    // in DispatchInput.Inputs. After Orchestrate, query the findings store
    // and assert the new fields are populated.
}
```

- [ ] **Step 4 — Commit:**

```bash
go test -race ./coding -count=1
git add coding/dispatch.go coding/dispatch_test.go coding/phaseloop/
git commit -m "Plan 125 Phase 4: populate finding plan_id/phase_index/reviewer_handle"
```

---

## Phase 5 — Decision 13 + verification

**Files:** `docs/architecture/decisions.md`

- [ ] **Step 1 — Append Decision 13:**

```markdown
## Decision 13: Schema Drift Reconciliation (Plan 125)

**Context:** The 2026-04-27 V1 audit (BLOCKERs B3 + B4) flagged that `findings` was missing spec-required columns (`plan_id`, `phase_index`, `reviewer_handle`) and `dispatches` was missing the spec's `mode` column.

**Decision:** Plan 125 adds an additive migration (008) for all four columns with sensible defaults (empty string / 0 / "persistent"). Existing rows continue to satisfy the schema; new code populates the fields where context is available.

**Decision:** Reviewer attribution: `reviewer.arch` and `reviewer.frontend` populate `findings.reviewer_handle = role.Name`. Findings synthesized in-process (e.g., supervisor contract violations) leave the field empty. This matches the spec's intent: the column attributes external review findings, not internal contract checks.

**Decision:** `dispatches.mode` is populated **only at enqueue time**. The pull-model `EnqueueDispatch` always writes `"persistent"` (the queue is for persistent workers). Ephemeral and in-process dispatches don't go through the `dispatches` table today; for those, `mode` is conceptual rather than recorded. A future plan may unify all dispatch modes through the same audit table; for V1 the column provides the intended audit field for the persistent path.

**Status:** Introduced in Plan 125.
```

- [ ] **Step 2 — Full verification:**

```bash
go build ./...
go test -race ./... -count=1 -timeout 180s
golangci-lint run ./...
```

- [ ] **Step 3 — Commit + merge.**

---

## Self-Review Checklist

- [ ] Migration uses `ALTER TABLE ... ADD COLUMN ... NOT NULL DEFAULT` so existing rows continue to satisfy the schema.
- [ ] Indexes added for foreseeable queries: `findings(plan_id)`, `findings(run_id, plan_id)`, `dispatches(mode)`.
- [ ] `Finding` struct fields are exported (caps); JSON/DB names use snake_case via SQL column names.
- [ ] `Dispatch.Mode` defaults to `"persistent"` when empty (back-compat for callers that don't set it).
- [ ] Reviewer attribution: `reviewer_handle` is set ONLY for `reviewer.*` roles. Other roles leave it empty.
- [ ] No regressions in existing tests (the new fields are optional / zero-valued).
- [ ] Decision 13 documents the trade-offs.

---

## Code Review

(To be filled in.)

---

## Post-Execution Report

(To be filled in.)
