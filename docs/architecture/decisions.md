# Architecture Decisions

This file is the single source of truth for cross-cutting runtime rules.
Updated whenever a plan introduces or revises a cross-cutting decision.

## Decision 1: Event-Log-Before-State Invariant (Plan 100)

**Context:** All state mutations in the runtime must be auditable and recoverable after a crash.

**Decision:** Every state mutation writes an event to the `events` table BEFORE updating the projection tables (`runs`, `jobs`, `findings`, `artifacts`). Both writes happen in the same SQLite transaction. If the transaction fails, neither write persists. If the daemon crashes between transaction commits (a theoretical edge case with SQLite's atomic commits), the event log is the source of truth for replay.

**Enforcement:** `store.EventStore.WriteEventThenRow()` is the only way to write events. All store methods (`RunStore.CreateRun`, `JobStore.CreateJob`, `FindingStore.InsertFinding`, etc.) use it. Direct SQL writes to projection tables are forbidden outside this function.

**Status:** Introduced in Plan 100.

## Decision 2: Findings Immutability (Plan 100)

**Context:** Review findings must form an immutable audit trail. Resolving a finding should link a fix job, not modify or delete the original finding.

**Decision:** The `FindingStore` API exposes only `InsertFinding` and `ResolveFinding`. `ResolveFinding` only updates `resolved_by_job_id` and `resolved_at`, and only if the finding is not already resolved. No API exists to update any other field of a finding after creation.

**Enforcement:** Go API boundary. The store layer does not expose update methods for other finding fields. SQLite-level triggers are not used; the Go API is the enforcement boundary.

**Status:** Introduced in Plan 100.

## Decision 3: Agent Protocol with JobHandle (Plan 100)

**Context:** The runtime needs to support both ephemeral (subprocess) and persistent (MCP-connected) agents.

**Decision:** `core.Agent` is an interface with `Dispatch(ctx, job, prompt) -> (JobHandle, error)`. `JobHandle` provides `Wait(ctx) -> (*JobResult, error)` and `Cancel() -> error`. This async-with-handle pattern supports ephemeral agents (wait for process exit) and persistent agents (wait for MCP `job.complete` callback).

**Enforcement:** All agent implementations must satisfy the `core.Agent` interface.

**Status:** Introduced in Plan 100. One implementation: `agent.CliAgent` (ephemeral subprocess).

## Decision 4: Event Sequence Numbering (Plan 100)

**Context:** Events within a run must be strictly ordered for replay correctness.

**Decision:** Each event gets a monotonically increasing `sequence` number per run, computed as `COALESCE(MAX(sequence), 0) + 1 FROM events WHERE run_id = ?` at write time. This is safe because all event writes for a run are serialized through SQLite transactions.

**Enforcement:** `EventStore.WriteEventThenRow()` auto-assigns the sequence. The `events` table has a UNIQUE constraint on `(run_id, sequence)`.

**Status:** Introduced in Plan 100.

## Decision 5: ID Generation (Plan 100)

**Context:** All entities need unique identifiers.

**Decision:** IDs are 32-character hex strings generated from 16 bytes of `crypto/rand`. This gives 128 bits of randomness, making collisions astronomically unlikely. String-typed IDs are used for readability in logs and database queries.

**Enforcement:** `core.NewID()` is the only ID generation function.

**Status:** Introduced in Plan 100.

## Decision 6: Schema Completion Projections (Plan 119)

**Context:** The V1 runtime data model requires durable plan, checkpoint, supervisor verdict, and cost projections in addition to the authoritative event log.

**Decision:** `plans`, `checkpoints`, `supervisor_events`, and `cost_events` are projection tables of event-log records. All writes to these projection tables go through `EventStore.WriteEventThenRow` so the event-log-before-state invariant is preserved.

**Decision:** `attention` and `checkpoints` remain separate. `attention` is the live human-input UI surface; `checkpoints` is the durable decision record. Every checkpoint-kind attention answer path must pair the attention answer with `CheckpointWriter.ResolveCheckpoint` for the same ID.

**Decision:** `runs.budget_usd` is metadata only in this plan. Budget enforcement is deferred to Plan 121 or later.

**Decision:** Existing checkpoint-kind attention rows are not backfilled into `checkpoints`; coworker had no shipped production runs at the time of this migration.

**Enforcement:** Store APIs for the new projection tables use `WriteEventThenRow`. Coding package consumers depend on `core.*Writer` sink interfaces. Tests cover event-first rollback, checkpoint answer pairing, and supervisor/cost replay shape.

**Status:** Introduced in Plan 119.


## Decision 7: Test Layers (Plan 120)

**Context:** The runtime spec calls for four test layers (unit, integration with mocks, replay, live E2E) to cover correctness, integration, regression against recorded transcripts, and provider compatibility against real CLIs. Plan 120 introduces the missing replay and live scaffolding.

**Decision:** Replay tests live under `tests/replay/<scenario>/` and use a `ReplayAgent` (`agent/replay_agent.go`) that satisfies `core.Agent` by streaming a recorded JSONL transcript through the same `streamMessage` schema as the live `CliAgent`. Replay tests are gated by `COWORKER_REPLAY=1`.

**Decision:** Per-role transcripts are named `<role-with-dots-replaced-by-underscores>.jsonl` (matching the role-file convention: `reviewer.arch` → `reviewer_arch.jsonl`). Each scenario directory also contains `inputs/` (placeholder template inputs) and `expected.json` (per-role assertions: `exit_code`, `findings_count`, `fingerprints`).

**Decision:** Live tests live under `tests/live/` with build tag `live` AND env var `COWORKER_LIVE=1`. Default `go test ./...` does not see them. The smoke tests assert the CLI exits 0 and emits at least one stream-json line on stdout. Cost is documented but **not yet enforced** (Dispatcher has no `core.CostWriter` wiring; deferred to Plan 121).

**Decision:** CI runs replay tests on every push (`make test-replay` step in `ci.yml`); live tests run on a manual `workflow_dispatch` trigger in a separate `live-tests.yml` workflow.

**Enforcement:** `var _ core.Agent = (*ReplayAgent)(nil)` compile-time assertion. Three smoke tests (claude, codex, opencode) exercise each CLI binary independently. `docs/architecture/testing.md` documents the four layers, when to use each, and how to add fixtures.

**Status:** Introduced in Plan 120.


## Decision 8: Cost Capture (Plan 121)

**Context:** V1 needs visibility into per-job cost so live tests can enforce a budget and operators can see cumulative spend.

**Decision:** Cost data is captured per-CLI from stream-json events:
- **Claude Code** emits `{"type":"result","total_cost_usd":...,"usage":{...},"modelUsage":{<model>:{...}}}` once at end-of-run. The parser populates `core.JobResult.Cost` directly with USD, tokens, and the lexicographically-first `modelUsage` key (deterministic across runs because Go map iteration is randomized).
- **Codex** emits `{"type":"turn.completed","usage":{...}}` cumulatively per session — latest event wins. Tokens are captured; USD is left at 0 pending a future per-model price table.
- **OpenCode** does not currently expose token or cost data via the SSE stream we consume; capture is deferred entirely.

**Decision:** Cost is persisted **per attempt**, not just on the final attempt. `executeAttempt` writes a `cost_events` row after each `agent.Wait()` succeeds. This way retries' real API spend is tracked accurately (each retry has a distinct `jobID` but shares a `runID`, so `runs.cost_usd` accumulates correctly via `CostEventStore.RecordCost`'s in-transaction UPDATE).

**Decision:** `Dispatcher.CostWriter` is optional. When nil, cost persistence is skipped. When non-nil, write failures are logged but do not fail dispatch — same posture as `SupervisorWriter`.

**Decision:** Live tests use `verifyCostUnderBudget(t, db, runID, requireRows)` to fail when (a) row count is below the required minimum (catches a silently-broken parser), or (b) `SUM(cost_events.usd) > COWORKER_LIVE_BUDGET_USD` (default 0.50). Codex and OpenCode smoke tests carry `FUTURE` comments explaining why this is not yet active for them.

**Enforcement:** Unit tests in `agent/cost_helpers_test.go` (8 cases), `agent/replay_agent_test.go` (2 new cases), `coding/dispatch_test.go` (4 new cases for cost-writer wiring), and the replay scenario at `tests/replay/developer_then_reviewer/replay_test.go` (cost row + sum assertion).

**Status:** Introduced in Plan 121.
