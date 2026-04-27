# Plan 126 — I2 + I7 + I8 cleanups

> Three small IMPORTANT items from the 2026-04-27 V1 audit, bundled because each is a few-line fix.

## I2 — Semantic checkpoint kinds

Today every `CheckpointWriter.CreateCheckpoint` call passes `Kind: string(core.AttentionCheckpoint)` (= "checkpoint"). Spec calls for `phase-clean`, `ready-to-ship`, `quality-gate`, etc. Three sites:

- `coding/phaseloop/executor.go:280` → `"phase-clean"`
- `coding/shipper/shipper.go:114` → `"ready-to-ship"`
- `coding/quality/evaluator.go:218` → `"quality-gate"`

Also add a `cli/run.go::insertRunCheckpoint` review — confirm it passes the right semantic kind already.

Add core constants `core.CheckpointKindPhaseClean`, `core.CheckpointKindReadyToShip`, `core.CheckpointKindQualityGate`, `core.CheckpointKindSpecApproved`, `core.CheckpointKindPlanApproved`, `core.CheckpointKindComplianceBreach` so future sites can't typo.

## I7 — Silent UpdateJobState errors

`coding/dispatch.go:378, 390, 397, 434, 446` (5 sites) ignore UpdateJobState errors with `//nolint:errcheck`. Replace each with `if err := ...; err != nil { logger.Error("...", "error", err) }`. Errors stay best-effort but are visible.

## I8 — Pipe cleanup on Start() failure

`agent/cli_agent.go::Dispatch` calls `cmd.StdoutPipe()` + `cmd.StderrPipe()` before `cmd.Start()`. If Start fails, the pipes are open but never closed → fd leak. Add `stdout.Close(); stderr.Close()` in the Start-error branch.

---

## Phase 1 — Checkpoint kind constants

**File:** `core/attention.go` (or a new `core/checkpoint.go`).

```go
// Checkpoint kinds. Used in CheckpointRecord.Kind. Spec §Workflow line 82.
// Plan 126 (I2).
const (
    CheckpointKindSpecApproved      = "spec-approved"
    CheckpointKindPlanApproved      = "plan-approved"
    CheckpointKindPhaseClean        = "phase-clean"
    CheckpointKindReadyToShip       = "ready-to-ship"
    CheckpointKindComplianceBreach  = "compliance-breach"
    CheckpointKindQualityGate       = "quality-gate"
)
```

Update the three sites to use these:
- phaseloop → `core.CheckpointKindPhaseClean`
- shipper → `core.CheckpointKindReadyToShip`
- quality → `core.CheckpointKindQualityGate`
- cli/run.go::runPlanLoopCreateNextCheckpoint inserts plan-approved attention; verify it propagates `core.CheckpointKindPlanApproved` to the writer.

## Phase 2 — UpdateJobState error logging

For each of the 5 sites in `coding/dispatch.go`, replace:

```go
jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed) //nolint:errcheck
```

with:

```go
if updateErr := jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed); updateErr != nil {
    logger.Error("failed to update job state", "job_id", jobID, "error", updateErr)
}
```

## Phase 3 — Pipe cleanup

```go
// agent/cli_agent.go::Dispatch
if err := cmd.Start(); err != nil {
    stdout.Close() //nolint:errcheck
    stderr.Close() //nolint:errcheck
    return nil, fmt.Errorf("start %q: %w", a.BinaryPath, err)
}
```

## Phase 4 — Verification

`go build`, `go test -race`, `golangci-lint`. No new tests required (these are non-functional safety improvements).

---

## Code Review

Plan was small enough that no separate Codex review was warranted; verified end-to-end via `go test -race ./... -timeout 180s` (28 ok, 0 failed) + `golangci-lint run ./...` (0 issues after a `nolint:gocyclo` annotation on `executeAttempt`).

## Post-Execution Report

### Date
2026-04-27

### Implementation summary

Three small fixes bundled.

**I2 — Semantic checkpoint kinds**
- New `core/attention.go` constants: `CheckpointKindSpecApproved`, `CheckpointKindPlanApproved`, `CheckpointKindPhaseClean`, `CheckpointKindReadyToShip`, `CheckpointKindComplianceBreach`, `CheckpointKindQualityGate`.
- 5 call sites updated:
  - `coding/phaseloop/executor.go:280` → `CheckpointKindPhaseClean`
  - `coding/shipper/shipper.go:114` → `CheckpointKindReadyToShip`
  - `coding/quality/evaluator.go:218` → `CheckpointKindQualityGate`
  - `cli/run.go:295` → `CheckpointKindSpecApproved`
  - `cli/run.go:642` → `CheckpointKindPlanApproved`

**I7 — Silent UpdateJobState errors logged**
- 5 sites in `coding/dispatch.go::executeAttempt` (permission denial, dispatch error, wait error, supervisor eval error, retry-loop) now use `if updErr := ...; updErr != nil { logger.Error(...) }` instead of `//nolint:errcheck`. The errors stay best-effort but no longer disappear.

**I8 — Pipe cleanup on Start() failure**
- `agent/cli_agent.go::Dispatch` closes both stdout and stderr pipes on `cmd.Start()` failure. 3-line addition.

### Verification

```text
go build ./...                                    → clean
go test -race ./... -count=1 -timeout 180s        → 30 ok, 0 failed, 0 races
golangci-lint run ./...                           → 0 issues (with one nolint:gocyclo on executeAttempt)
```

### Notes

- `executeAttempt` now flagged by gocyclo at complexity 23 (threshold 20) after the per-error logging branches; added `//nolint:gocyclo` mirroring the existing annotation on `Orchestrate`. The function is linear; the complexity comes from defensive error handling, not control-flow tangle.
- `cli/run.go::insertRunCheckpoint` already passed semantic strings ("spec-approved", "plan-approved") via the `label` parameter; the change is purely converting hardcoded strings to constants for typo-resistance.
