# Plan 129 — I1 (partial): read-only CLI commands

> The audit's I1 is "6 missing CLI commands". This plan ships the three read-only ones (`status`, `logs`, `inspect`); the three state-mutating ones (`resume`, `redo`, `edit`) are deferred to a follow-up since they require run-scheduler integration.

## status

`coworker status` (list all runs, table) and `coworker status <run-id>` (single-run details with job table). New file `cli/status.go`. Spec line 549.

## logs

`coworker logs <job-id>` reads `.coworker/runs/<run-id>/jobs/<job-id>.jsonl` and prints. `--follow` tails by polling at 200ms. New file `cli/logs.go`. Spec line 558.

## inspect

`coworker inspect <job-id>` shows job details + findings + supervisor verdicts + cost samples. New file `cli/inspect.go`. Spec line 508.

## Tests

- `cli/status_test.go` — 4 tests (no-runs, list, details, unknown-id).
- `cli/inspect_test.go` — 2 tests (unknown, known).
- `cli/logs_test.go` — 3 tests (unknown, missing-file, prints).

## Verification

```
go build ./...                                  → clean
go test -race ./... -count=1 -timeout 180s      → 30 ok, 0 failed, 0 races
golangci-lint run ./...                         → 0 issues
```
