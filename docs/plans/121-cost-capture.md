# Plan 121 — Cost Capture (Claude) + Live Budget Enforcement

> **For agentic workers:** Implemented inline phase-by-phase, commit each phase, run the full suite before merging.

**Goal:** Wire token/cost capture into the dispatch pipeline so a `cost_events` row is written for **every attempt** of a job that produces cost-bearing output (retries are recorded separately so total run cost is accurate). Use the captured rows to enforce per-test budgets in live smoke tests. Scope is intentionally narrow: Claude Code's stream-json `result` event provides `total_cost_usd` directly; Codex emits tokens-only (`turn.completed.usage`, cumulative-per-session — last-event-wins) and OpenCode HTTP currently exposes no cost — both have explicit `[FUTURE]` markers and `tests/live/{codex,opencode}_smoke_test.go` documents that budget enforcement is not active for them.

**Architecture:**
- Extend `core.JobResult` with an optional `Cost *core.CostSample`. `CliAgent.Wait` parses the additional event kinds (`result` for Claude — fired exactly once at the end; `turn.completed` for Codex — cumulative session usage, take the last one). `ReplayAgent.Wait` decodes the same event shapes from transcripts via a shared `populateCost(streamMessage, *core.JobResult)` helper.
- `Dispatcher` gains an optional `CostWriter core.CostWriter` field. **Persistence happens per-attempt** — at the end of `executeAttempt` (alongside the existing supervisor-result recording), if `attemptResult.result.Cost != nil`, the dispatcher calls `CostWriter.RecordCost(ctx, runID, jobID, *cost)`. This produces one `cost_events` row per retry, so total run spend reflects the real API cost across retries (matching the cost ledger semantics in the spec). Failure to persist is logged but does not fail the attempt.
- Live tests for Claude run a small dispatch through `coding.Dispatcher` and assert at least one `cost_events` row is written and that `SUM(usd) ≤ budgetUSD()`. The existing `exec.CommandContext`-based smoke tests stay (they verify the binary works at all) — the new `TestLive_Claude_BudgetGuard` test exercises the dispatcher path. Codex and OpenCode smoke tests retain the existing exec-based shape; their files gain a comment noting that budget enforcement is not active for them.
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

1. Extend `agent/cli_handle.go::streamMessage` with optional cost fields shared across Claude `result` and Codex `turn.completed`:
   - `TotalCostUSD float64 \`json:"total_cost_usd,omitempty"\``
   - `Usage *streamUsage` (tokens — `input_tokens`, `output_tokens`, `cache_read_input_tokens`, `cached_input_tokens`)
   - `ModelUsage map[string]modelUsageRow \`json:"modelUsage,omitempty"\`` (Claude only)
2. Extract a shared helper `populateCost(msg streamMessage, result *core.JobResult)` (in `agent/` package, exported for `replay_agent.go` reuse) so `CliAgent` and `ReplayAgent` use identical semantics. The helper handles `result` and `turn.completed`. **For `turn.completed` (Codex), it overwrites — Codex's `usage` field is cumulative per session, so the latest event has the final number.** For `result` (Claude), it sets the cost (only one `result` per run). Both events from one transcript: `result` wins (Claude is unambiguous about USD).
3. Model selection: there is **no `streamMessage.Model` field**. Claude's model name comes from the `modelUsage` map keys (`{"claude-opus-4-7[1m]": {...}}`). To keep `CostSample.Model` deterministic across runs (Go map iteration is randomized), `populateCost` sorts the `modelUsage` keys lexicographically and takes the first one. Codex `turn.completed` carries no model name, so `CostSample.Model` is left empty for Codex — documented in the helper.
4. Extend `core.JobResult` with `Cost *core.CostSample`. Nil when neither cost-bearing event was seen.
5. `Dispatcher.CostWriter core.CostWriter` optional field. **Persistence happens per attempt** inside `executeAttempt` (after `agent.Wait()` returns and the supervisor result is recorded). Each retry produces its own `cost_events` row tied to the retry's `jobID`. Failure logs and continues — does not fail dispatch.
6. Wire `CostEventStore` into the production dispatcher in `cli/daemon.go` and `cli/run.go`.
7. Add `tests/live/helpers.go::verifyCostUnderBudget(t, db, runID, requireRows int)` that:
   - queries `cost_events` rows for the run.
   - if `requireRows > 0` and the row count is below it, fails the test (catches a broken parser silently passing).
   - if `SUM(usd) > budgetUSD()`, fails the test.
8. New live test `tests/live/claude_smoke_test.go::TestLive_Claude_BudgetGuard` that constructs a fresh `coding.Dispatcher` with `Agent: agent.NewCliAgent("claude", "-p", "<trivial-prompt>", "--output-format", "stream-json", "--verbose")`, dispatches a minimal `smoke` role (see Step 9 below), and asserts cost. The existing exec-based `TestLive_Claude_Smoke` is kept unchanged.
9. **Minimal smoke role:** add a `tests/live/testdata/roles/smoke.yaml` (or reuse a small role from `coding/roles/`) that takes no required inputs and uses a 1-line prompt template — so the smoke test does not invoke the heavyweight `developer` role.
10. Unit tests: `CliAgent` parser populates `Cost` from a Claude-shaped `result` event; from a Codex-shaped `turn.completed` event (USD=0, tokens populated); skips when neither appears. Multiple `turn.completed` events: last-one-wins. Tests live in `agent/cli_handle_test.go` (create — does not yet exist).
11. Replay test scenario fixture extended with a Claude-shaped `result` line on developer's transcript; replay test asserts both `SumByRun` matches the expected USD AND `ListByJob` returns at least one row.
12. `docs/architecture/decisions.md` Decision 8: cost capture per-CLI; only Claude provides USD directly; Codex tokens captured at USD=0; OpenCode deferred. Documents the cumulative vs per-turn semantics and why per-attempt persistence matters for retry accuracy.

Out of scope (with explicit reasons; documented in code where relevant):

- **Codex USD computation from tokens** — requires a per-model price table; tracked as a future plan. Until then, `cost_events.usd` is 0 for Codex jobs (a comment in `populateCost` says so).
- **OpenCode cost** — no data available in the SSE stream we consume; `tests/live/opencode_smoke_test.go` has a `// FUTURE: budget enforcement` comment pointing at this plan.
- **Runtime budget enforcement** — `runs.budget_usd` is still recorded but not enforced during a run. A future plan will compare cumulative cost to budget on every cost write and surface a `cost.budget_exceeded` checkpoint. This plan only enforces per-live-test caps.
- **TUI / HTTP / MCP cost projection updates** — `tui/model.go:69` expects `input_tok`/`output_tok`/`cost_usd`/`cumulative_usd` while the events emit `tokens_in`/`tokens_out`/`usd`. A separate plan will reconcile event payload shapes across consumers. This plan does not modify TUI, HTTP, or MCP.
- **Transcript recording machinery beyond what already exists** — `.coworker/runs/<runID>/jobs/<jobID>.jsonl` is already written by Plan 117. Documenting the manual extraction recipe lives in `docs/architecture/testing.md`; no new code in this plan.

---

## File Structure

**Create:**
- `agent/cli_handle_test.go` (does not yet exist — covers the new parser cases)
- `agent/cost_helpers.go` (the shared `populateCost(msg, *core.JobResult)` helper, used by both `CliAgent.Wait` and `ReplayAgent.Wait`)
- `tests/live/testdata/roles/smoke.yaml` (a minimal role for the budget-guard live test)
- `tests/live/testdata/prompts/smoke.md` (the matching trivial prompt template)

**Modify:**
- `agent/cli_handle.go` (extend `streamMessage`; call `populateCost`)
- `agent/replay_agent.go` (call `populateCost` in the same switch)
- `agent/replay_agent_test.go` (new test: cost in replay)
- `core/job.go` (add `Cost *core.CostSample` to `JobResult`)
- `coding/dispatch.go` (add `CostWriter` field; persist cost **per attempt** inside `executeAttempt`)
- `coding/dispatch_test.go` (new tests; see Phase 3)
- `cli/daemon.go`, `cli/run.go` (construct `CostEventStore`; wire to dispatcher)
- `tests/live/helpers.go` (add `verifyCostUnderBudget(t, db, runID, requireRows int)`)
- `tests/live/claude_smoke_test.go` (add `TestLive_Claude_BudgetGuard` alongside the existing exec-based smoke)
- `tests/live/codex_smoke_test.go`, `tests/live/opencode_smoke_test.go` (FUTURE comment)
- `tests/replay/developer_then_reviewer/transcripts/developer.jsonl` (add a Claude-shaped `result` line)
- `tests/replay/developer_then_reviewer/expected.json` (`expect_cost_usd` field)
- `tests/replay/developer_then_reviewer/replay_test.go` (assert `cost_events` row written + sum)
- `docs/architecture/decisions.md` (Decision 8)

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

Extract a shared helper into `agent/cost_helpers.go`. Both `CliAgent.Wait` and `ReplayAgent.Wait` call it for every decoded `streamMessage`:

```go
// agent/cost_helpers.go
package agent

import (
    "sort"

    "github.com/chris/coworker/core"
)

// populateCost inspects msg and updates result.Cost when the message is a
// recognized cost-bearing event. Idempotent: later calls overwrite Cost
// only when the new event is a recognized type.
//
// Claude: "result" event fires once at end-of-run; we set Cost from
// total_cost_usd, usage, and the lexicographically-first modelUsage key.
//
// Codex: "turn.completed" emits the usage struct cumulatively per session;
// we overwrite Cost on every turn.completed so the LAST one wins (which is
// the final cumulative usage). USD stays at 0 — Codex provides no USD
// figure; per-model price-table conversion is deferred to a future plan.
func populateCost(msg streamMessage, result *core.JobResult) {
    switch msg.Type {
    case "result":
        if msg.TotalCostUSD <= 0 && msg.Usage == nil && msg.ModelUsage == nil {
            return
        }
        cs := &core.CostSample{
            Provider: "anthropic",
            USD:      msg.TotalCostUSD,
        }
        if msg.Usage != nil {
            cs.TokensIn = msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens
            cs.TokensOut = msg.Usage.OutputTokens
        }
        // Sort modelUsage keys to make selection deterministic across runs.
        if len(msg.ModelUsage) > 0 {
            keys := make([]string, 0, len(msg.ModelUsage))
            for k := range msg.ModelUsage {
                keys = append(keys, k)
            }
            sort.Strings(keys)
            cs.Model = keys[0]
        }
        result.Cost = cs
    case "turn.completed":
        if msg.Usage == nil {
            return
        }
        result.Cost = &core.CostSample{
            Provider:  "openai",
            TokensIn:  msg.Usage.InputTokens + msg.Usage.CachedInputTokens,
            TokensOut: msg.Usage.OutputTokens,
            // USD: 0 — see Plan 121 §Out of scope (price table is future work).
        }
    }
}
```

In `cli_handle.go::Wait`, after the existing finding/done switch, call `populateCost(msg, result)` for every decoded message (not in a switch — `populateCost` has its own no-op for non-cost types).

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

- [ ] **Step 2 — Persist per-attempt** inside `executeAttempt` (NOT in `Orchestrate` after the loop). Findings persist from the LAST attempt only because earlier attempts' findings are subsumed by the retry; cost is the OPPOSITE — every retry consumes real API tokens, so each attempt produces its own row.

Read `coding/dispatch.go::executeAttempt` first; locate where `agent.Wait()` returns and the `dispatchAttemptResult` is assembled. Add this block right after the wait succeeds, alongside any existing supervisor-result write:

```go
// In executeAttempt, after agent.Wait() returns result:
if d.CostWriter != nil && result.Cost != nil {
    if err := d.CostWriter.RecordCost(ctx, runID, jobID, *result.Cost); err != nil {
        logger.Error("failed to persist cost sample",
            "run_id", runID, "job_id", jobID, "attempt", attempt, "error", err)
    }
}
```

Each retry has a distinct `jobID` (verified by the existing dispatcher tests at `coding/dispatch.go:342-348`) — so this produces N rows for N attempts, all keyed off the same `runID` but different `jobID`s. `runs.cost_usd` accumulates correctly because `CostEventStore.RecordCost` bumps it inside the same transaction (Plan 119 wiring).

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

- [ ] **Step 3 — In `replay_test.go`, set `CostWriter` on the dispatcher and assert both row count and sum after the developer dispatch:**

```go
es := store.NewEventStore(devDB)
ce := store.NewCostEventStore(devDB, es)

// Wire the dispatcher with CostWriter
devDisp.CostWriter = ce

// ... after Orchestrate returns devOut ...
rows, err := ce.ListByJob(context.Background(), devOut.JobID)
if err != nil {
    t.Fatalf("cost ListByJob: %v", err)
}
if len(rows) != 1 {
    t.Fatalf("developer cost rows = %d, want 1", len(rows))
}
sum, err := ce.SumByRun(context.Background(), devOut.RunID)
if err != nil {
    t.Fatalf("cost SumByRun: %v", err)
}
if sum != expected["developer"].ExpectCostUSD {
    t.Errorf("developer cost sum = %v, want %v", sum, expected["developer"].ExpectCostUSD)
}
```

Add `ExpectCostUSD float64 \`json:"expect_cost_usd,omitempty"\`` to the `roleExpected` struct.

- [ ] **Step 4 — Run + commit:**

```bash
COWORKER_REPLAY=1 go test ./tests/replay/... -count=1
git add tests/replay/developer_then_reviewer/
git commit -m "Plan 121 Phase 4: replay scenario verifies cost persistence"
```

---

## Phase 5 — Live test budget enforcement

**Files:** `tests/live/helpers.go`, `tests/live/claude_smoke_test.go`

- [ ] **Step 1 — Add the smoke-role fixture under `tests/live/testdata/`:**

`tests/live/testdata/roles/smoke.yaml`:
```yaml
name: smoke
concurrency: single
cli: claude-code
prompt_template: prompts/smoke.md
inputs:
  required: []
outputs:
  contract: {}
  emits: {}
sandbox: read-only
permissions:
  allowed_tools: ["read"]
  never: []
  requires_human: []
budget:
  max_tokens_per_job: 1000
  max_wallclock_minutes: 1
  max_cost_usd: 0.10
retry_policy:
  on_contract_fail: skip
  on_job_error: skip
```

`tests/live/testdata/prompts/smoke.md`:
```
Print a single stream-json line: {"type":"done","exit_code":0}
```

- [ ] **Step 2 — Add `verifyCostUnderBudget` helper:**

```go
//go:build live

package live

import (
    "context"
    "testing"

    "github.com/chris/coworker/store"
)

// verifyCostUnderBudget queries cost_events for the run and fails the
// test if (a) row count < requireRows, or (b) SUM(usd) > budgetUSD().
// requireRows=1 catches a broken parser silently writing zero rows;
// requireRows=0 tolerates zero rows (Codex/OpenCode have no USD wired).
func verifyCostUnderBudget(t *testing.T, db *store.DB, runID string, requireRows int) {
    t.Helper()
    es := store.NewEventStore(db)
    ce := store.NewCostEventStore(db, es)

    rows, err := ce.ListByRun(context.Background(), runID)
    if err != nil {
        t.Fatalf("cost ListByRun: %v", err)
    }
    if len(rows) < requireRows {
        t.Fatalf("cost rows = %d, want >= %d (parser may have skipped events)",
            len(rows), requireRows)
    }
    sum, err := ce.SumByRun(context.Background(), runID)
    if err != nil {
        t.Fatalf("cost SumByRun: %v", err)
    }
    budget := budgetUSD()
    if sum > budget {
        t.Fatalf("test cost = $%.4f exceeded budget $%.2f (rows=%d)",
            sum, budget, len(rows))
    }
    t.Logf("test cost = $%.4f (rows=%d, budget $%.2f)", sum, len(rows), budget)
}
```

(If `CostEventStore.ListByRun` does not yet exist, mirror `ListByJob` from `store/cost_event_store.go` and add it; minor extension that does not affect schema. Verify before adding — Plan 119 may have already added it.)

- [ ] **Step 3 — Add a SECOND test `TestLive_Claude_BudgetGuard` in `tests/live/claude_smoke_test.go`** alongside the existing exec-based smoke. The new test:

```go
//go:build live

package live

import (
    "context"
    "path/filepath"
    "testing"

    "github.com/chris/coworker/agent"
    "github.com/chris/coworker/coding"
    "github.com/chris/coworker/store"
)

// TestLive_Claude_BudgetGuard exercises the full dispatcher path: a tiny
// CliAgent invocation against the real claude binary, with cost capture
// and budget enforcement via cost_events.
func TestLive_Claude_BudgetGuard(t *testing.T) {
    requireLiveEnv(t)
    bin := requireBinary(t, "claude")

    db, err := store.Open(":memory:")
    if err != nil {
        t.Fatal(err)
    }
    defer db.Close()
    es := store.NewEventStore(db)

    a := agent.NewCliAgent(bin, "-p",
        "--output-format", "stream-json", "--verbose")
    // Note: -p prompt comes via stdin from CliAgent (CliAgent always sends
    // prompt on stdin); the role's prompt template at testdata/prompts/smoke.md
    // becomes the rendered prompt.

    repoRoot := repoRootFromTest(t)
    smokeDir := filepath.Join(repoRoot, "tests", "live", "testdata")

    d := &coding.Dispatcher{
        Agent:      a,
        DB:         db,
        RoleDir:    filepath.Join(smokeDir, "roles"),
        PromptDir:  smokeDir,
        CostWriter: store.NewCostEventStore(db, es),
    }
    ctx, cancel := withTimeout(t, 60*time.Second)
    defer cancel()
    res, err := d.Orchestrate(ctx, &coding.DispatchInput{
        RoleName: "smoke",
        Inputs:   map[string]string{},
    })
    if err != nil {
        t.Fatalf("smoke dispatch: %v", err)
    }
    verifyCostUnderBudget(t, db, res.RunID, 1)
}

func repoRootFromTest(t *testing.T) string {
    t.Helper()
    // tests/live → ../../
    abs, err := filepath.Abs(filepath.Join("..", ".."))
    if err != nil {
        t.Fatal(err)
    }
    return abs
}
```

If the `-p` argv vs stdin question matters at runtime: verify locally that `claude -p < stdin` reads the prompt from stdin (it does in headless `--output-format stream-json` mode per Anthropic's docs; if it doesn't, fall back to passing the prompt via additional argv or use a different invocation pattern). Document the chosen invocation in the commit.

- [ ] **Step 4 — Add a comment in `codex_smoke_test.go` and `opencode_smoke_test.go`** noting that budget enforcement is not active for these CLIs and pointing at this plan's Out of Scope section. Example:

```go
// FUTURE: budget enforcement via cost_events is not active for codex —
// turn.completed.usage emits tokens but no USD figure. See Plan 121
// §Out of Scope. Tracked for follow-up via a per-model price table.
```

- [ ] **Step 5 — Run live test (real API call):**

```bash
COWORKER_LIVE=1 make test-live
```

Verify: the Claude budget-guard test logs `rows=1` (or more if retries fire) and stays under budget. The exec-based smoke continues to pass.

- [ ] **Step 6 — Commit:**

```bash
git add tests/live/
git commit -m "Plan 121 Phase 5: live test enforces cost budget against cost_events (smoke role + dispatcher path)"
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

### Codex post-implementation review (2026-04-27)

#### Important — Claude `result` could be overwritten by a later `turn.completed` [FIXED]

`agent/cost_helpers.go:32` — original `populateCost` overwrote `result.Cost` on every recognized event type. If a transcript happened to contain both a Claude `result` AND a Codex `turn.completed` (in any order with the latter last), the USD-bearing Anthropic sample would be replaced by a tokens-only OpenAI sample with USD=0. In practice no single CLI emits both, but the parser should be robust.

→ Fixed: the `turn.completed` branch now checks `result.Cost.Provider == "anthropic"` and short-circuits if true. New test `TestPopulateCost_ClaudeResultWinsOverLaterTurnCompleted` codifies the rule.

#### Sandbox-only blockers (build/test/lint) [N/A]

Codex flagged blockers because its sandbox `/tmp` is read-only and golangci-lint isn't installed. Local verification on the actual repo:

```text
$ go build ./...                                                  → clean
$ go test -race ./... -count=1 -timeout 180s                      → 30 ok, 0 failed, 0 races
$ golangci-lint run ./...                                         → 0 issues
$ COWORKER_REPLAY=1 make test-replay                              → PASS
$ COWORKER_LIVE=1 go test -tags live ./tests/live/... -run TestLive_Claude_BudgetGuard
                                                                  → PASS, cost $0.1215 under $0.50 budget
```

---

## Post-Execution Report

### Date
2026-04-27

### Implementation summary

Six phases, all merged inline:

**Phase 1+2 — Parser + JobResult**
- `core.JobResult.Cost *core.CostSample` field added.
- `agent/cli_handle.go::streamMessage` extended with `TotalCostUSD`, `Usage`, `ModelUsage` fields.
- Shared `agent/cost_helpers.go::populateCost` extracts cost from `result` (Claude) and `turn.completed` (Codex). Sorted modelUsage key wins for deterministic Claude model selection. Codex `turn.completed` is cumulative-per-session (latest event wins).
- `CliAgent.Wait` and `ReplayAgent.Wait` both call `populateCost` for every decoded message.
- 9 unit tests: Claude result, deterministic model, empty no-op, Codex turn.completed (single + cumulative last-wins), unknown event no-op, done no-op, claude-result-wins-over-later-turn-completed, replay agent cost from result, replay agent cost from turn.completed.

**Phase 3 — Dispatcher cost persistence**
- `Dispatcher.CostWriter core.CostWriter` optional field.
- `executeAttempt` records cost after `agent.Wait()` succeeds, before returning attempt result. Each retry produces its own cost_events row tied to the retry's distinct `jobID`.
- Best-effort: write failure logged; dispatch continues.
- Wired `CostEventStore` into both `cli/daemon.go` and `cli/run.go` production paths.
- 4 dispatcher tests added.

**Phase 4 — Replay scenario carries cost**
- `tests/replay/developer_then_reviewer/transcripts/developer.jsonl` extended with a Claude `result` line.
- `expected.json` adds `expect_cost_usd: 0.0123`.
- Test wires `CostEventStore` into the developer dispatcher and asserts `ListByJob` returns 1 row + `SumByRun` matches the expected USD.

**Phase 5 — Live test budget enforcement**
- `store.CostEventStore.ListByRun` added (mirrors `ListByJob`).
- `tests/live/helpers.go::verifyCostUnderBudget(t, db, runID, requireRows)` enforces row count + sum < `budgetUSD()`.
- New minimal smoke role at `tests/live/testdata/roles/smoke.yaml` (one input `prompt_text`, allows `bash:claude`).
- New live test `TestLive_Claude_BudgetGuard` runs CliAgent through Dispatcher; verified locally with `COWORKER_LIVE=1` (real API call, cost $0.12 under $0.50 budget, 3.77s wall).
- `TestLive_{Codex,OpenCode}_Smoke` get `FUTURE` comments explaining inactive enforcement.

**Phase 6 — Documentation + verification**
- `docs/architecture/decisions.md` Decision 8 added.

### Verification

```text
go build ./...                                          → clean
go test -race ./... -count=1 -timeout 180s              → 30 ok, 0 failed, 0 races
golangci-lint run ./...                                 → 0 issues
COWORKER_REPLAY=1 make test-replay                      → PASS
COWORKER_LIVE=1 go test -tags live ... TestLive_Claude_BudgetGuard
                                                        → PASS, $0.1215 under $0.50 budget
```

### Notes / deviations from plan

- The smoke role initially had `inputs.required: []`, which trips the role validator (`coding/roles/loader.go:97` — "inputs.required must have at least one entry"). Resolved by adding a single `prompt_text` input that's substituted into a trivial prompt template.
- Smoke role needed `"bash:claude"` in `allowed_tools` to satisfy default-deny permission enforcement. Documented in the role file.
- Codex post-impl review found one mixed-transcript edge case (turn.completed could overwrite an earlier result); fixed in a follow-up commit before merge.
