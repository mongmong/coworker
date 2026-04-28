# Plan 136 — N6 strengthening + V1.1 deferral catalog

> Closes the last actionable nice-to-have and consolidates the deferred items into a single decision entry so future plans have a clear catalog.

## N6 — Strengthen `TestOrchestrate_CostWriterErrorIsNonFatal`

The test verified the dispatcher returned without error when the cost writer failed, but didn't assert that the dispatch itself succeeded (ExitCode/JobState/RunState). Strengthened to:

- `res.ExitCode == 0` — agent's exit propagates.
- `JobStore.GetJob(JobID).State == JobStateComplete` — final state is correct.
- `RunStore.GetRun(RunID).State == RunStateCompleted` — run flows to completion.

This guards against a regression where a swallowed cost-writer error accidentally leaves the job/run in a wrong state.

## Decision 15 — V1.1+ deferral catalog

Consolidated all the "documented as out-of-scope for V1" items from Plans 122-135 + the audit's open items into one Decision entry:

- Codex USD pricing (audit N8) — needs per-model price table.
- Runtime budget enforcement — `runs.budget_usd` recorded but not enforced.
- TUI attention auto-refresh (audit I10) — needs event-bus side-effect publish OR HTTP polling.
- `coworker redo` — needs `DispatchInput.Inputs` persisted on jobs row.
- `coworker resume` — already covered by `--resume-after-attention`.
- Filesystem watch on `coworker edit` — needs fs-event → human-edit job wiring.
- Phase-loop replay scenarios (multi-phase, supervisor retry, etc.) — needs PhaseExecutor/supervisor/worker test wiring.
- `cmd/coworker/main.go` test — 3-line shim, coverage implicit.
- Time-parse silent drops in store reads — trusted-source data, debug-log if needed.

Each entry documents the ask, the workaround (where one exists), and the rough shape of the future fix.

## Verification

```
go build ./...                                  → clean
go test -race ./... -count=1 -timeout 180s      → 33 ok, 0 failed, 0 races
golangci-lint run ./...                         → 0 issues
```
