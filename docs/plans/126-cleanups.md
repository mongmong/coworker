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

(Filled after.)

## Post-Execution Report

(Filled after.)
