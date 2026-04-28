# Plan 132 — B7: replay scenarios

> Closes the last audit BLOCKER. Adds three single-role replay scenarios; phase-loop scenarios deferred (they need PhaseExecutor + StageRegistry test wiring beyond the existing single-Dispatcher template).

## Scenarios shipped

### claude_cost_capture
Developer transcript ends with a Claude `result` event carrying `total_cost_usd`. Asserts the cost row was written with the right provider/USD/tokens/model.

### codex_tokens_no_usd
Reviewer transcript ends with a Codex `turn.completed` event carrying token counts but no USD. Asserts tokens are captured + USD is 0 (per Decision 8). Two findings round-trip through dedup.

### mixed_severity_findings
Reviewer emits four findings of all severity levels (critical / important / minor / nit). Asserts the histogram of stored severities + that `reviewer_handle` is set on all four.

## Deferred to follow-ups

**Multi-phase plan**, **supervisor retry-then-pass**, **phase-clean checkpoint**, **quality-gate escalation**, and **worker registration/heartbeat** scenarios all require the PhaseExecutor or supervisor or worker registry to be replay-driven, not just a single Dispatcher.Orchestrate call. Adding test infrastructure to swap ReplayAgent into PhaseExecutor's role-fanout is a bigger plan; tracked for follow-up.

## Verification

```
go build ./...                                              → clean
go test -race ./... -count=1 -timeout 180s                  → 33 ok, 0 failed, 0 races
golangci-lint run ./...                                     → 0 issues
COWORKER_REPLAY=1 go test ./tests/replay/... -count=1       → 4 PASS
```
