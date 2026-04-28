# Codex Independent Second Opinion — 2026-04-28

**Branch:** main (head `75d4cae`, after the re-audit comparison report)
**Reviewer:** Codex (independent fresh-eyes lens — explicitly NOT framed as audit-checklist re-verification)

## Verdict

**Don't ship — confidence: high.**

Three CRITICAL items + 5 HIGH/MEDIUM items found. The two structured audits had converged on "ship-ready"; Codex's unframed second opinion surfaced what the checklists missed.

---

## CRITICAL

### #1 — Architect parser contract mismatch (autopilot blocked)

`agent/cli_handle.go:27`, `coding/prompts/architect.md:68`, `cli/run.go:273`.

The architect prompt emits `{spec_path, manifest_path, notes}` JSON at the end of its run. The CLI stream parser (`streamMessage`) only handles `type=finding|done` (and the cost variants). The architect's bare-envelope JSON is decoded as a struct with no recognized type, the switch falls through, and `result.Artifacts` stays empty. `cli/run.go::extractRunManifestPath` then fails with `"architect did not produce a manifest artifact"`.

**Real V1 `coworker run <prd.md>` fails at the architect step.**

### #2 — Autopilot run fragmentation

`cli/run.go:252`, `coding/dispatch.go:156`, `coding/phaseloop/executor.go:221,392`.

`Dispatcher.Orchestrate` always calls `runStore.CreateRun(...)` with a fresh ID and `mode="interactive"`. Autopilot creates a top-level workflow run, then every dispatch (architect, planner, every phase's developer + reviewers + tester + shipper) creates its own SEPARATE run. The workflow run only carries the spec-approved/plan-approved checkpoint events; the actual job tree lives in N orphan runs that aren't linked anywhere.

**Consequence:** `orch_run_inspect <workflow-run-id>` shows checkpoints with no jobs. Cost reconstruction, replay, and incident debugging are all fragmented.

### #5 — SQLite pool: no busy_timeout, per-connection PRAGMAs

`store/db.go:34,39,45`.

File-backed SQLite uses `database/sql`'s default unbounded pool. Only one initial connection runs `PRAGMA foreign_keys=ON` and `PRAGMA journal_mode=WAL`. WAL mode persists in the file header (so it sticks), but `foreign_keys` is **per-connection** — fresh pooled connections will silently lose FK enforcement. There's no `busy_timeout` set, so contention errors surface as immediate `database is locked` failures.

**Consequence:** Under parallel dispatch + watchdog + cost writes, FKs may not be enforced on some connections, and writes that race the watchdog can fail without retry.

---

## HIGH

### #3 — Persistent worker outputs dropped on the floor

`mcp/handlers_dispatch.go:206,212`, `store/dispatch_store.go:274`.

`orch_job_complete` stores the worker's outputs (findings, artifacts, exit_code) inside the `dispatch.completed` event payload only. The job is marked complete but `findings`, `artifacts`, and `exit_code` columns/rows are not populated. A persistent reviewer that reports blocking findings via the MCP path would have those findings invisible to the rest of the system.

### #4 — Sequential stdout-then-stderr drain in CliAgent (deadlock)

`agent/cli_handle.go:77,109`.

`CliAgent.Wait` loops the stream-json decoder on `h.stdout` until EOF, THEN reads `h.stderr`. If the subprocess writes more than ~64KB to stderr (typical OS pipe buffer) without coworker draining, the subprocess blocks on its stderr write, never closes stdout, and coworker blocks forever on stdout EOF. Wall-clock budget eventually fires; roles without a budget hang indefinitely.

### #6 — Finding persistence failures silently swallowed

`coding/dispatch.go:254`.

The finding-persistence loop logs InsertFinding errors but does not fail the job or run. A transient SQLite lock or malformed row drops a real reviewer finding while the job marches to `complete`. The audit trail looks clean even though it's missing data.

---

## MEDIUM

### #7 — TUI SSE reconnect loop drops events

`tui/events.go:85`, `tui/model.go:168`.

The TUI opens an SSE connection, returns after one event, closes it, then reconnects. The in-memory event bus has no replay buffer, so any events that fire between disconnect and reconnect are lost. During an incident, the dashboard can silently miss state transitions.

### #8 — Lease expiry false-positive event

`store/dispatch_store.go:372,376,437`.

`ExpireLeases` and `RequeueByWorker` emit `dispatch.expired` events even when the guarded UPDATE affects zero rows after a concurrent completion. Replay timelines show dispatches as expired/requeued after they actually completed, creating false duplicate-work evidence in incident postmortems.

---

## Big-picture takeaway

> The scariest part is run reconstruction: the V1 workflow does not keep one coherent run tree, persistent worker outputs are not projected, and the TUI can miss live events during reconnects. In the first real incident, you would not trust a single surface — the DB rows, event log, TUI, and worker output could each tell a different partial story, with no single authoritative view of what actually happened.
