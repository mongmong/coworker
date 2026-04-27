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
