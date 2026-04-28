# Plan 139 — Codex second-opinion CRITICAL fixes

> Codex's independent second-opinion review (`docs/reviews/2026-04-28-codex-second-opinion.md`) found 3 CRITICAL issues that the structured audits missed. All three fixed here.

## CRITICAL #1 — Architect parser path

**Was:** Architect prompt emitted `{spec_path, manifest_path, notes}` JSON. The CLI parser only handled `type=finding|done`. Architect dispatch returned with empty `Artifacts`; `cli/run.go::extractRunManifestPath` failed → autopilot blocked.

**Fix:**
- Add `Kind string` field to `streamMessage`.
- New `case "artifact"` in `cliJobHandle.Wait` populates `JobResult.Artifacts`.
- Architect prompt updated to emit one JSONL line per artifact + done.
- Two new parser tests: happy path + incomplete-entry defense.

## CRITICAL #2 — Run fragmentation

**Was:** `Dispatcher.Orchestrate` always called `runStore.CreateRun(...)` with a fresh ID. Autopilot created N orphan runs (one per role dispatch); the workflow run only had checkpoint events. `orch_run_inspect` on the workflow run showed nothing about the actual work.

**Fix:**
- New optional `DispatchInput.RunID` field. When set, attach to the existing run instead of creating a new one. When empty, legacy behavior (new interactive run for `coworker invoke <role>` and tests).
- `Orchestrate` skips `runStore.CreateRun` AND `runStore.CompleteRun` when attached — the caller owns lifecycle.
- `cli/run.go` (architect + planner dispatches) and `coding/phaseloop/executor.go` (developer + reviewer/tester dispatches) now pass `RunID: workflowRunID`.
- New test `TestOrchestrate_AttachesToExistingRun` asserts: result.RunID matches the workflow run, the workflow run is still active after dispatch, no orphan run was created.

## CRITICAL #5 — SQLite pool: per-connection PRAGMAs + busy_timeout

**Was:** `store/db.go::Open` ran `PRAGMA foreign_keys=ON` and `PRAGMA journal_mode=WAL` via `sqlDB.Exec` on the initial connection. The pool then handed out fresh connections without these PRAGMAs — `foreign_keys` is per-connection in SQLite, so FK enforcement was silently disabled on later connections. No `busy_timeout` either: contention surfaced as immediate `database is locked` failures.

**Fix:**
- New `buildSQLiteDSN(path)` builds a `file:` URI DSN with `_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)`. modernc.org/sqlite applies these PRAGMAs to **every** connection the pool opens.
- In-memory uses `file::memory:?_pragma=...` (without `cache=shared`, which would break test isolation) and keeps the existing `MaxOpenConns(1)` cap.
- `sqlDB.Ping()` after open so PRAGMA failures surface immediately.
- New test `TestOpen_PerConnectionPragmas` opens two connections from the pool and asserts each has `foreign_keys=1` AND `busy_timeout=5000`.

## Verification

```
go build ./...                                  → clean
go test -race ./... -count=1 -timeout 180s      → 27 packages PASS, 0 failed, 0 races
golangci-lint run ./...                         → 0 issues
```

## Remaining from second opinion

- HIGH #3, #4, #6 → Plan 140.
- MEDIUM #7, #8 → Plan 141.
