# Plan 121 — Cost Capture (Claude) + Live Budget Enforcement

> **For agentic workers:** Implemented inline phase-by-phase, commit each phase, run the full suite before merging.

**Goal:** Wire token/cost capture into the dispatch pipeline so a `cost_events` row is written for every job that produces cost-bearing output. Use the captured rows to enforce per-test budgets in live smoke tests. Scope is intentionally narrow: Claude Code's stream-json `result` event provides `total_cost_usd` directly; Codex emits tokens-only (`turn.completed.usage`) and OpenCode HTTP currently exposes no cost — both are deferred to a follow-up plan with explicit `[FUTURE]` markers in the code.

**Architecture:**
- Extend `core.JobResult` with an optional `Cost *core.CostSample`. `CliAgent.Wait` parses the additional event kinds (`result` for Claude; `turn.completed` for Codex) and populates the field when cost data is present. `ReplayAgent.Wait` decodes the same event shapes from transcripts.
- `Dispatcher.Orchestrate` gains an optional `CostWriter core.CostWriter` field. After the last attempt completes, if `lastResult.Cost != nil`, the dispatcher calls `CostWriter.RecordCost(ctx, runID, jobID, *lastResult.Cost)` — preserving the event-first invariant via the existing `CostEventStore.RecordCost` that wraps `WriteEventThenRow`.
- Live tests query `cost_events` after each smoke run. If `SUM(usd) > budgetUSD()`, the test fails with a clear message. The budget guard becomes real.
- No new tables; no schema changes. Reuses Plan 119's `cost_events`.

**Tech Stack:** Go, `core.JobResult`, `core.CostSample`, `core.CostWriter`, existing `CostEventStore`, build-tag-gated live tests.

**Reference:** `docs/specs/000-coworker-runtime-design.md` §Cost ledger; `docs/plans/119-schema-completion.md` (CostEventStore scaffolding); `docs/plans/120-test-infrastructure.md` (live tests where the budget guard becomes real).

---

## Required-API audit (verify before writing code)

| Surface | Reality (verified) |
| --- | --- |
| `agent/cli_handle.go::streamMessage` | Currently has `Type`, `Path`, `Line`, `Severity`, `Body`, `ExitCode`. Drops unknown event types. |
| Claude stream-json final event | `{"type":"result", "total_cost_usd": 0.46..., "usage": {...}, "modelUsage": {"<model>": {"costUSD":...}}}` (verified via `spike/001/stream-output.jsonl:19`). |
| Codex stream-json final event | `{"type":"turn.completed", "usage": {"input_tokens": ..., "cached_input_tokens": ..., "output_tokens": ...}}` (verified via `spike/002/exec-jsonl.txt`). No USD figure. |
| OpenCode HTTP/SSE | `agent/opencode_http_agent.go` — does not surface token or cost data on the SSE stream we currently consume. |
| `core.CostSample` (Plan 119) | `Provider`, `Model`, `TokensIn`, `TokensOut`, `USD`. |
| `core.CostWriter` (Plan 119) | `RecordCost(ctx, runID, jobID, sample) error`. |
| `store.CostEventStore.RecordCost` (Plan 119) | Writes `cost.delta` event + `cost_events` row + bumps `runs.cost_usd` and `jobs.cost_usd` in a single transaction. |
| `Dispatcher.Orchestrate` finding-persistence loop | `coding/dispatch.go:222-232`. The right place to call `CostWriter.RecordCost` is right next to the finding persistence. |

---

## Scope

In scope:

1. Extend `agent/cli_handle.go::streamMessage` with optional cost fields:
   - `TotalCostUSD float64 \`json:"total_cost_usd,omitempty"\`` (Claude `result` event)
   - `Usage *streamUsage` for tokens (`input_tokens`, `output_tokens`, `cached_input_tokens`/`cache_read_input_tokens`)
   - `Model string \`json:"model,omitempty"\`` (Claude `result` event has it under `modelUsage` keys; Codex via prior `assistant`'s `model` field — extracted as best-effort)
2. Extend `core.JobResult` with `Cost *core.CostSample`. Populated by `CliAgent.Wait` and `ReplayAgent.Wait` when a recognized cost-bearing event appears.
3. `Dispatcher.CostWriter core.CostWriter` field. After `lastResult` is finalized, if `Cost != nil`, persist via `CostWriter.RecordCost`. Failure to persist is logged but does not fail dispatch (best-effort, same pattern as `SupervisorWriter`).
4. Wire `CostEventStore` into the production dispatcher in `cli/daemon.go` and `cli/run.go`.
5. Update `tests/live/helpers.go` with a `verifyCostUnderBudget(t, db, runID)` helper that queries `cost_events` and fails if `SUM(usd) > budgetUSD()`.
6. Update one live test (`tests/live/claude_smoke_test.go`) to use this helper. Codex and OpenCode tests stay budget-unenforced for now (with a comment pointing at this plan).
7. Unit tests: `CliAgent` parser populates `Cost` from a Claude-shaped `result` event; from a Codex-shaped `turn.completed` event (USD=0, tokens populated); skips when neither appears. Unchanged behavior for transcripts without those events.
8. Replay test scenario fixture extended with a Claude-shaped `result` line on developer's transcript; replay test asserts `cost_events` is populated.
9. `docs/architecture/decisions.md` Decision 8: cost capture is per-CLI; only Claude provides USD directly; Codex/OpenCode deferred to a future plan with the explicit reason (need a price table for Codex; OpenCode SSE doesn't expose tokens).

Out of scope:

- Codex USD computation from tokens (needs a price table).
- OpenCode cost (no data available).
- Cumulative `runs.cost_usd` enforcement / budget cutoff during a run (this plan only enforces per-live-test cost; runtime budget enforcement is a future plan).
- Per-attempt cost rows (only the final attempt's cost is recorded, matching how findings are persisted).
- Transcript recording machinery beyond what already exists (`.coworker/runs/<runID>/jobs/<jobID>.jsonl` is already written by Plan 117).

---

## File Structure

**Modify:**
- `agent/cli_handle.go` (extend `streamMessage` parser)
- `agent/cli_handle_test.go` (new tests — create if absent)
- `agent/replay_agent.go` (parse same cost events from transcripts)
- `agent/replay_agent_test.go` (new test for cost in replay)
- `core/job.go` (add `Cost *core.CostSample` to `JobResult`)
- `coding/dispatch.go` (add `CostWriter` field; persist cost after lastResult)
- `coding/dispatch_test.go` (new tests: writer called when Cost != nil; nil writer is no-op; writer error is non-fatal)
- `cli/daemon.go`, `cli/run.go` (construct `CostEventStore`; wire to dispatcher)
- `tests/live/helpers.go` (add `verifyCostUnderBudget` helper)
- `tests/live/claude_smoke_test.go` (use the helper)
- `tests/replay/developer_then_reviewer/transcripts/developer.jsonl` (add a Claude-shaped result line)
- `tests/replay/developer_then_reviewer/expected.json` (assert cost present)
- `tests/replay/developer_then_reviewer/replay_test.go` (assert `cost_events` row written)
- `docs/architecture/decisions.md` (Decision 8)

**Create:** none.

---

## Phase 1 — Parser + JobResult

**Files:** `agent/cli_handle.go`, `core/job.go`

- [ ] **Step 1 — extend `core.JobResult`:**

```go
// core/job.go
type JobResult struct {
    Findings  []Finding
    Artifacts []Artifact
    ExitCode  int
    Stdout    string
    Stderr    string

    // Cost is populated when the agent's stream-json output contained a
    // recognized cost-bearing event (Claude `result`, Codex `turn.completed`).
    // Nil otherwise. The dispatcher persists this via core.CostWriter when
    // configured.
    Cost *CostSample
}
```

- [ ] **Step 2 — extend `streamMessage` and parser in `agent/cli_handle.go`:**

```go
type streamMessage struct {
    Type     string `json:"type"`
    Path     string `json:"path,omitempty"`
    Line     int    `json:"line,omitempty"`
    Severity string `json:"severity,omitempty"`
    Body     string `json:"body,omitempty"`
    ExitCode int    `json:"exit_code,omitempty"`

    // Cost-bearing fields (Claude result / Codex turn.completed).
    TotalCostUSD float64                  `json:"total_cost_usd,omitempty"`
    Usage        *streamUsage             `json:"usage,omitempty"`
    ModelUsage   map[string]modelUsageRow `json:"modelUsage,omitempty"`
}

type streamUsage struct {
    // Common across CLIs (some omit specific fields).
    InputTokens          int `json:"input_tokens,omitempty"`
    OutputTokens         int `json:"output_tokens,omitempty"`
    CacheReadInputTokens int `json:"cache_read_input_tokens,omitempty"` // Claude
    CachedInputTokens    int `json:"cached_input_tokens,omitempty"`     // Codex
}

type modelUsageRow struct {
    InputTokens  int     `json:"inputTokens"`
    OutputTokens int     `json:"outputTokens"`
    CostUSD      float64 `json:"costUSD"`
}
```

In the decode loop, add a case for `"result"` and `"turn.completed"` that calls a helper to populate `result.Cost`:

```go
case "result":
    // Claude headless emits {"type":"result", "total_cost_usd":..., "modelUsage":{<model>:{...}}}
    if msg.TotalCostUSD > 0 || msg.Usage != nil {
        cs := &core.CostSample{
            Provider: "anthropic",
            USD:      msg.TotalCostUSD,
        }
        if msg.Usage != nil {
            cs.TokensIn = msg.Usage.InputTokens
            cs.TokensOut = msg.Usage.OutputTokens
        }
        // Pick the first model key as the model identifier.
        for model := range msg.ModelUsage {
            cs.Model = model
            break
        }
        result.Cost = cs
    }
case "turn.completed":
    // Codex emits {"type":"turn.completed", "usage":{...}}. No USD figure.
    if msg.Usage != nil {
        result.Cost = &core.CostSample{
            Provider:  "openai",
            TokensIn:  msg.Usage.InputTokens + msg.Usage.CachedInputTokens,
            TokensOut: msg.Usage.OutputTokens,
            // USD: 0 — see Plan 121 §Out of scope (price table is future work).
        }
    }
```

- [ ] **Step 3 — Tests:**

If `agent/cli_handle_test.go` does not exist, create it. Otherwise add to it. Use a minimal harness that constructs a `cliJobHandle` with stub `io.ReadCloser` for stdout/stderr (look for an existing helper in `cli_agent_test.go`).

Tests:
1. `Wait` parses Claude `result` line → `result.Cost.{Provider="anthropic", USD>0, TokensIn>0, Model!="""}`.
2. `Wait` parses Codex `turn.completed` line → `result.Cost.{Provider="openai", USD=0, TokensIn>0}`.
3. `Wait` with neither event → `result.Cost == nil`.
4. `Wait` with both events (e.g. nested mocks) → last-event-wins or first-event-wins (pick one explicitly; document).

- [ ] **Step 4 — Run + commit:**

```bash
go test ./agent ./core -count=1
git add agent/cli_handle.go core/job.go agent/cli_handle_test.go
git commit -m "Plan 121 Phase 1: capture cost from Claude result + Codex turn.completed events"
```

---

## Phase 2 — ReplayAgent parity

**Files:** `agent/replay_agent.go`, `agent/replay_agent_test.go`

- [ ] **Step 1 — Mirror the same cases in the replay agent's parser switch.** ReplayAgent uses the same `streamMessage` struct, so this is a parallel switch addition. Refactor by extracting a shared helper `populateCost(msg streamMessage, result *core.JobResult)` in `cli_handle.go` so the replay agent can reuse it (export the helper or inline-share).

- [ ] **Step 2 — Test:** add `TestReplayAgent_CostFromResult` and `TestReplayAgent_CostFromTurnCompleted` mirroring CliAgent's tests.

- [ ] **Step 3 — Commit:**

```bash
go test ./agent -count=1 -run TestReplayAgent
git add agent/replay_agent.go agent/replay_agent_test.go
git commit -m "Plan 121 Phase 2: ReplayAgent parses cost events from transcripts"
```

---

## Phase 3 — Dispatcher cost persistence

**Files:** `coding/dispatch.go`, `coding/dispatch_test.go`, `cli/daemon.go`, `cli/run.go`

- [ ] **Step 1 — Add `CostWriter` field:**

```go
// coding/dispatch.go
type Dispatcher struct {
    // ... existing fields ...

    // CostWriter records cost samples after each completed job. Optional;
    // when nil, cost persistence is skipped. Failure to persist is logged
    // but does not fail dispatch.
    CostWriter core.CostWriter
}
```

- [ ] **Step 2 — Persist in `Orchestrate` right after the finding loop:**

```go
// After: for i := range lastResult.Findings { ... }
if d.CostWriter != nil && lastResult.Cost != nil {
    if err := d.CostWriter.RecordCost(ctx, runID, lastJobID, *lastResult.Cost); err != nil {
        logger.Error("failed to persist cost sample",
            "run_id", runID, "job_id", lastJobID, "error", err)
    }
}
```

- [ ] **Step 3 — Wire in `cli/daemon.go` and `cli/run.go`:**

In each construction site, after building the dispatcher add:

```go
costStore := store.NewCostEventStore(db, eventStore)
dispatcher.CostWriter = costStore
```

(Pattern matches Plan 119's SupervisorWriter wiring.)

- [ ] **Step 4 — Tests in `coding/dispatch_test.go`:**

1. `TestDispatch_PersistsCostWhenPresent` — stub agent returns `JobResult.Cost != nil`; capture writer asserts `RecordCost` called with same `runID`/`jobID`/`sample`.
2. `TestDispatch_NoCostWhenAgentResultMissing` — stub agent returns `Cost == nil`; capture writer is never called.
3. `TestDispatch_CostWriterErrorIsNonFatal` — writer returns error; dispatch still succeeds.
4. `TestDispatch_NilCostWriterIsNoOp` — `CostWriter == nil`; agent returns Cost; dispatch succeeds without panicking.

- [ ] **Step 5 — Commit:**

```bash
go test ./coding ./cli -count=1
git add coding/dispatch.go coding/dispatch_test.go cli/daemon.go cli/run.go
git commit -m "Plan 121 Phase 3: dispatcher persists cost samples via CostWriter"
```

---

## Phase 4 — Replay scenario carries cost

**Files:** `tests/replay/developer_then_reviewer/transcripts/developer.jsonl`, `tests/replay/developer_then_reviewer/expected.json`, `tests/replay/developer_then_reviewer/replay_test.go`

- [ ] **Step 1 — Append a Claude-shaped `result` event to the developer transcript:**

```jsonl
{"type":"done","exit_code":0}
{"type":"result","total_cost_usd":0.0123,"usage":{"input_tokens":100,"output_tokens":50},"modelUsage":{"claude-opus-4-7":{"inputTokens":100,"outputTokens":50,"costUSD":0.0123}}}
```

- [ ] **Step 2 — Update `expected.json`:**

```json
{
    "developer": {
        "exit_code": 0,
        "findings_count": 0,
        "expect_cost_usd": 0.0123
    },
    "reviewer.arch": {
        "exit_code": 0,
        "findings_count": 2,
        "fingerprints": ["main.go:42:important", "store.go:17:minor"]
    }
}
```

- [ ] **Step 3 — In `replay_test.go`, after the developer dispatch, query `cost_events` and assert one row exists with `usd == expect_cost_usd`:**

```go
ce := store.NewCostEventStore(devDB, store.NewEventStore(devDB))
sum, err := ce.SumByRun(context.Background(), devOut.RunID)
if err != nil {
    t.Fatalf("cost SumByRun: %v", err)
}
if sum != expected["developer"].ExpectCostUSD {
    t.Errorf("developer cost = %v, want %v", sum, expected["developer"].ExpectCostUSD)
}
```

(Add `ExpectCostUSD float64 \`json:"expect_cost_usd,omitempty"\`` to the `roleExpected` struct. Set the dispatcher's `CostWriter` in the test setup.)

- [ ] **Step 4 — Run + commit:**

```bash
COWORKER_REPLAY=1 go test ./tests/replay/... -count=1
git add tests/replay/developer_then_reviewer/
git commit -m "Plan 121 Phase 4: replay scenario verifies cost persistence"
```

---

## Phase 5 — Live test budget enforcement

**Files:** `tests/live/helpers.go`, `tests/live/claude_smoke_test.go`

- [ ] **Step 1 — Add `verifyCostUnderBudget` helper:**

```go
//go:build live

package live

import (
    "context"
    "testing"

    "github.com/chris/coworker/store"
)

// verifyCostUnderBudget queries cost_events for the run and fails the
// test if SUM(usd) > budgetUSD(). Tolerates zero rows (Codex/OpenCode
// today emit no USD; only Claude has cost wired).
func verifyCostUnderBudget(t *testing.T, db *store.DB, runID string) {
    t.Helper()
    es := store.NewEventStore(db)
    ce := store.NewCostEventStore(db, es)
    sum, err := ce.SumByRun(context.Background(), runID)
    if err != nil {
        t.Fatalf("cost SumByRun: %v", err)
    }
    budget := budgetUSD()
    if sum > budget {
        t.Fatalf("test cost = $%.4f exceeded budget $%.2f", sum, budget)
    }
    t.Logf("test cost = $%.4f (budget $%.2f)", sum, budget)
}
```

- [ ] **Step 2 — Update `claude_smoke_test.go`** so it dispatches via `coding.Dispatcher` (not `exec.CommandContext` directly) so `cost_events` is populated. The smoke test now:
  1. Opens a fresh in-memory DB.
  2. Constructs a Dispatcher with `Agent: agent.NewCliAgent("claude", ...)`, `CostWriter: store.NewCostEventStore(db, es)`.
  3. Calls `Orchestrate` with role=developer (or a minimal role test_smoke if needed).
  4. Calls `verifyCostUnderBudget(t, db, result.RunID)`.

(If swapping the test out is more disruptive than expected, keep the exec.CommandContext path AND add a second separate test `TestLive_Claude_BudgetGuard` that runs a tiny dispatch through Dispatcher with the cost helper. Pick one approach during implementation; document the choice in the commit message.)

- [ ] **Step 3 — Add a comment in `codex_smoke_test.go` and `opencode_smoke_test.go`** noting that budget enforcement is not active for these CLIs and pointing at this plan's Out of Scope section.

- [ ] **Step 4 — Run live test (real API call):**

```bash
COWORKER_LIVE=1 make test-live
```

Verify: the Claude smoke logs the cost and stays under budget. (If it exceeds, lower the prompt size; the prompt should produce <100 tokens.)

- [ ] **Step 5 — Commit:**

```bash
git add tests/live/
git commit -m "Plan 121 Phase 5: live test enforces cost budget against cost_events"
```

---

## Phase 6 — Documentation + verification

**Files:** `docs/architecture/decisions.md`, full suite

- [ ] **Step 1 — Append Decision 8:**

```markdown
## Decision 8: Cost Capture (Plan 121)

**Context:** V1 needs visibility into per-job cost so live tests can enforce a budget and operators can see cumulative spend.

**Decision:** Cost data is captured per-CLI from stream-json events:
- **Claude Code** emits `{"type":"result","total_cost_usd":...,"usage":{...},"modelUsage":{<model>:{...}}}` at the end of every run. The parser populates `core.JobResult.Cost` directly.
- **Codex** emits `{"type":"turn.completed","usage":{<token-counts>}}` with no USD figure. Tokens are captured; USD is left at 0 pending a future per-model price table.
- **OpenCode** does not currently expose token or cost data via the SSE stream we consume; capture is deferred.

**Decision:** `Dispatcher` persists cost via the new optional `CostWriter` field, which adapts `*store.CostEventStore`. Failure to persist is logged but does not fail the dispatch — same posture as `SupervisorWriter`.

**Decision:** Live tests use `verifyCostUnderBudget(t, db, runID)` to fail when `SUM(cost_events.usd) > COWORKER_LIVE_BUDGET_USD` (default 0.50). Codex and OpenCode tests document this is not yet enforced for their CLIs.

**Enforcement:** Unit tests in `agent/`, `coding/`, and `tests/replay/` cover the parser, dispatcher persistence, and replay-fixture cost paths.

**Status:** Introduced in Plan 121.
```

- [ ] **Step 2 — Full verification:**

```bash
go build ./...
go test -race ./... -count=1 -timeout 180s
golangci-lint run ./...
COWORKER_REPLAY=1 make test-replay
```

Expected: build clean, all tests pass with `-race`, 0 lint issues, replay scenario asserts cost.

- [ ] **Step 3 — Commit + merge.**

---

## Self-Review Checklist

- [ ] `streamMessage` parser handles `result` and `turn.completed` event types without breaking existing `finding`/`done` cases.
- [ ] `core.JobResult.Cost` is `nil` when neither cost-bearing event is present.
- [ ] `Dispatcher.CostWriter == nil` is a safe no-op (same defensive pattern as `SupervisorWriter`).
- [ ] `CostWriter.RecordCost` failure is logged, not returned; dispatch still succeeds.
- [ ] Replay scenario asserts cost is persisted (round-trips through CostEventStore).
- [ ] Live test for Claude reads `cost_events` and enforces budget.
- [ ] Codex and OpenCode tests document deferred enforcement (with reason).
- [ ] No new tables; no schema migrations.
- [ ] No `coding/*` package imports `store/*` for the cost wiring (uses `core.CostWriter`).
- [ ] `decisions.md` Decision 8 added.

---

## Code Review

(To be filled in after implementation by Codex review subagent.)

---

## Post-Execution Report

(To be filled in after implementation.)
