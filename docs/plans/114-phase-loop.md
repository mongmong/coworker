# Plan 114 — Phase Loop + Fan-In

**Branch:** `feature/plan-114-phase-loop`
**Blocks on:** 106 (WorktreeManager, BuildFromPRDWorkflow scaffold)
**Parallel-safe with:** 107, 109, 110

---

## Purpose

Implement the inner loop of the `build-from-prd` workflow: for each phase in a plan, dispatch a developer role, fan-out to parallel reviewers + tester, deduplicate findings by fingerprint, check for clean state, and retry with feedback up to `max_fix_cycles_per_phase` times. If the phase never converges, emit a `phase-clean` checkpoint event and continue.

---

## Background

`coding/workflow/build_from_prd.go` contains a `TODO(Plan 114)` stub in its `Run` method. Plans 106 set up the manifest scheduling and worktree creation; Plan 115 will ship PRs. This plan wires the middle: per-phase execution.

The design spec (§Workflow State Machine) defines:

```
phase loop:
    [developer] → [rev.arch ∥ rev.frontend ∥ tester] → dedupe
       → fix-loop (≤ max_fix_cycles_per_phase)
    ◆ phase-clean (on-failure by default)
```

Fan-In Aggregation rules (§Fan-In Aggregation):
- Findings: merged by fingerprint, all source job IDs preserved.
- Test results: any fail = phase fail.
- Artifacts: no path conflicts.
- Notes: chronological.
- Costs: summed.

---

## Architecture

```
coding/
└── phaseloop/
    ├── executor.go       # PhaseExecutor: orchestrates one phase
    ├── executor_test.go
    ├── fanin.go          # DedupeFindings, AggregateResults
    ├── fanin_test.go
    ├── fixloop.go        # FixLoop: retry dev with finding feedback
    └── fixloop_test.go

coding/workflow/
└── build_from_prd.go    # MODIFY: wire PhaseExecutor into TODO stub

core/event.go            # ADD: EventPhaseStarted, EventPhaseCompleted, EventPhaseFailed
```

---

## Key Types

### PhaseExecutor (`coding/phaseloop/executor.go`)

```go
type PhaseExecutor struct {
    Dispatcher   *coding.Dispatcher
    EventStore   *store.EventStore
    Policy       *core.Policy
    Logger       *slog.Logger
}

type PhaseResult struct {
    Findings    []core.Finding
    Artifacts   []core.Artifact
    TestsPassed bool
    FixCycles   int
    Clean       bool // true if no findings after all cycles
}

func (e *PhaseExecutor) Execute(ctx context.Context, runID string, planID int, phaseIndex int, phaseName string, inputs map[string]string) (*PhaseResult, error)
```

Execute flow:
1. Emit `phase.started` event.
2. Dispatch `developer` role.
3. Fan-out: dispatch `reviewer.arch` + `reviewer.frontend` + `tester` in parallel via errgroup.
4. Aggregate results; dedupe findings by fingerprint.
5. If findings remain and `fixCycles < maxFixCyclesPerPhase`: build feedback, re-dispatch developer, increment fixCycles, go to step 3.
6. If findings remain after max cycles: emit `phase-clean` checkpoint event, `Clean=false`.
7. Emit `phase.completed` or `phase.failed` event.
8. Return `PhaseResult`.

### DedupeFindings (`coding/phaseloop/fanin.go`)

```go
func DedupeFindings(findings []core.Finding) []core.Finding
```

Groups by fingerprint. First occurrence kept; all source job IDs are attached to the `SourceJobIDs` field. (Field added to `core.Finding` or tracked via a parallel map in the result struct.)

Implementation note: since `core.Finding.JobID` is a single string, we track multiple source job IDs in a local map and annotate the kept finding's Body with source info, or we add a `SourceJobIDs []string` field to `core.Finding` for this purpose.

Decision: add `SourceJobIDs []string` to `core.Finding`. This is not persisted to SQLite (the individual findings already carry their `job_id`); it is only used in-memory for the phase result.

### AggregateResults (`coding/phaseloop/fanin.go`)

```go
type AggregatedResults struct {
    Findings    []core.Finding
    Artifacts   []core.Artifact
    TestsPassed bool
    TotalCost   float64
}

func AggregateResults(results []*coding.DispatchResult) *AggregatedResults
```

- Concatenates all findings from results.
- TestsPassed = true iff all results have ExitCode == 0.
- Artifacts: concatenated (conflicts logged, not fatal at this layer).
- TotalCost: zero for now (cost tracking is Plan 113+).

### FixLoop (`coding/phaseloop/fixloop.go`)

```go
type FixLoop struct {
    Executor *PhaseExecutor
}

func BuildFindingFeedback(findings []core.Finding) string
```

Builds a supervisor-style feedback string from findings, prepended to the next developer dispatch prompt.

---

## Phases

### Phase 1 — Event types in `core/event.go`

Add:
```go
EventPhaseStarted   EventKind = "phase.started"
EventPhaseCompleted EventKind = "phase.completed"
EventPhaseFailed    EventKind = "phase.failed"
EventPhaseClean     EventKind = "phase.clean"    // checkpoint event
```

Also add `SourceJobIDs []string` to `core.Finding` (in-memory only).

### Phase 2 — `coding/phaseloop/fanin.go`

Implement `DedupeFindings` and `AggregateResults`. Unit tests cover: empty input, single result, multiple results with same fingerprint (dedupe), different fingerprints (all kept), exit code aggregation.

### Phase 3 — `coding/phaseloop/fixloop.go`

Implement `BuildFindingFeedback`. Unit tests cover: empty findings, single finding, multiple findings.

### Phase 4 — `coding/phaseloop/executor.go`

Implement `PhaseExecutor.Execute`. Integration-style tests use in-memory stub dispatchers.

### Phase 5 — Wire into `build_from_prd.go`

Replace `TODO(Plan 114)` stub. The `PhaseExecutor` is added as a field on `BuildFromPRDWorkflow`.

---

## Policy Defaults

`MaxFixCyclesPerPhase` defaults to 5 when zero. Defined in `coding/phaseloop/executor.go`.

---

## Dependencies

- `golang.org/x/sync` for errgroup — already in `go.sum` as indirect dep; promote to direct with `go get`.
- No new SQLite schema changes.

---

## Testing

- Unit: `fanin_test.go` — dedupe, aggregate.
- Unit: `fixloop_test.go` — feedback string builder.
- Integration (in-process stubs): `executor_test.go` — full Execute flow with mock Dispatcher.

---

## Code Review

### Findings

**[FIXED] `DispatchResult` lacked `Artifacts` field**
`coding/dispatch.go:DispatchResult` had no `Artifacts` field, so `AggregateResults` in `fanin.go` could not collect artifacts from dispatcher outputs. Added `Artifacts []core.Artifact` to `DispatchResult` and wired it from `lastResult.Artifacts` in `Orchestrate`.

**[FIXED] Dead variables in `runLoop`**
Initial version declared `lastDeduped`, `lastArtifacts`, `lastTestsPassed` outside the loop but never read them — they were shadowed by inner-loop locals. Removed the outer declarations; all return sites use the loop-local `agg`/`deduped` directly.

**[FIXED] `capturingOrchestratorForFeedback.state` unused field**
The test struct had an unused `state interface{}` field left from an earlier draft. Removed.

**[FIXED] gofmt alignment issues in `fanin.go` and `fanin_test.go`**
Two struct literal fields were not tab-aligned per gofmt. Fixed via `gofmt -w`.

**[FIXED] `TestPhaseExecutor_DedupeAcrossReviewers` used zero JobIDs**
The stub findings in the test had no `JobID` set, so `DedupeFindings` never added them to `SourceJobIDs`. Fixed by giving each reviewer's finding a distinct `JobID` in the stub.

**[OK] `Orchestrator` interface placement**
The `Orchestrator` interface is defined in `coding/phaseloop/executor.go` (not in `coding/`). This is intentional — it is an adapter boundary for testability, not a domain-level abstraction. The concrete `*coding.Dispatcher` satisfies it without modification.

**[OK] errgroup with pre-allocated results slice**
`fanOut` uses `results[i] = result` with a pre-allocated slice where each goroutine writes to a different index. No mutex is needed because slice element writes at distinct indices are safe under the Go memory model when errgroup.Wait() synchronizes.

**[OK] `maxFixCycles` policy default**
Zero or negative `MaxFixCyclesPerPhase` falls back to `DefaultMaxFixCycles = 5`. Nil policy also falls back. This matches the spec note "default 5".

### Review 2

**C1 [FIXED] Data race in `stubOrchestrator`**
`coding/phaseloop/executor_test.go`: `stubOrchestrator` mutated `callCount` and `roleCounts` without synchronization while being called from concurrent goroutines in `fanOut`. Added `sync.Mutex` field; `Orchestrate` now locks before mutating and releases before invoking `fn` (to avoid holding the lock during potentially slow callbacks). Verified with `go test -race ./coding/phaseloop/... -count=1`.

**S1 [FIXED] Closure-level `reviewerCallCount` in `TestPhaseExecutor_FixCycleThenClean`**
The `int` variable captured by the stub closure was read/written from concurrent goroutines. Replaced with `atomic.Int32` (using `atomic.AddInt32` / `sync/atomic`) so the increment-and-read is race-free.

**I1 [FIXED] `phase-clean` checkpoint doesn't create an attention item**
`coding/phaseloop/executor.go`: After emitting the `phase.clean` event, the code now creates a `core.AttentionItem` (kind=`checkpoint`, source=`phase-loop`) via `AttentionStore.InsertAttention` when `e.AttentionStore` is non-nil. Added `AttentionStore *store.AttentionStore` field to `PhaseExecutor`; nil means no item (true blocking deferred to Plan 103). Insert errors are logged but do not fail the phase.

**I2 [FIXED] `SourceJobIDs` aliasing in `DedupeFindings`**
`coding/phaseloop/fanin.go`: The first-occurrence finding's `SourceJobIDs` and the group's `sourceJobs` shared the same backing array. Subsequent `append` in the duplicate path could silently overwrite the canonical finding's slice. Fixed by copying both at first-occurrence time and again after each duplicate accumulation.

**I3 [WONTFIX] `capturingOrchestratorForFeedback` not mutex-protected**
This stub is only used in `TestPhaseExecutor_FeedbackPassedToDevOnFixCycle`, which dispatches reviewers concurrently. However, the struct only captures inputs for the `developer` role, which is dispatched sequentially (never in the fan-out group). No concurrent writes occur, so no mutex is needed. Confirmed by reading the fan-out code: `developer` is dispatched before `g.Go` calls begin.

**I4 [WONTFIX] `TestPhaseExecutor_ExhaustFixCycles` doesn't verify attention item**
The test has no `AttentionStore` wired, so the attention path is not exercised. Adding a full `AttentionStore`-wired test for the exhaustion path is deferred; the existing test verifies the returned `PhaseResult` (Clean=false, FixCycles=max, Findings non-empty), which is the primary contract.

**S2–S5 [WONTFIX]** Minor style notes (log field ordering, comment phrasing, redundant blank lines, consistent use of `t.Fatalf` vs `t.Errorf`). Not worth the churn; existing style is consistent within the file.

---

## Post-Execution Report

### What was built

Plan 114 implemented in 5 phases over one session on branch `feature/plan-114-phase-loop`.

**New files:**
- `coding/phaseloop/executor.go` — `PhaseExecutor` with `Execute`, `runLoop`, `fanOut`, event emission
- `coding/phaseloop/executor_test.go` — 8 integration-style tests using stub `Orchestrator`
- `coding/phaseloop/fanin.go` — `DedupeFindings`, `AggregateResults`
- `coding/phaseloop/fanin_test.go` — 11 unit tests
- `coding/phaseloop/fixloop.go` — `BuildFindingFeedback`, `maxFixCycles`
- `coding/phaseloop/fixloop_test.go` — 9 unit tests
- `docs/plans/114-phase-loop.md` (this file)

**Modified files:**
- `core/event.go` — added `EventPhaseStarted`, `EventPhaseCompleted`, `EventPhaseFailed`, `EventPhaseClean`
- `core/finding.go` — added `SourceJobIDs []string` (in-memory, not persisted)
- `coding/dispatch.go` — added `Artifacts []core.Artifact` to `DispatchResult`
- `coding/workflow/build_from_prd.go` — added `PhaseExecutor` field, `RunPhasesForPlan` method, updated `Run` docstring
- `go.mod` — promoted `golang.org/x/sync` to direct dependency

### Test results

All 21 packages pass with `-race`. Zero lint issues (golangci-lint).

**Review 2 fixes applied (post-initial-ship):** critical data race in `stubOrchestrator` fixed via `sync.Mutex`; `reviewerCallCount` closure variable made race-free via `atomic.AddInt32`; `phase-clean` checkpoint now creates an `AttentionItem` when `AttentionStore` is wired; `SourceJobIDs` aliasing in `DedupeFindings` eliminated by defensive copying.

### Design decisions

1. **`Orchestrator` interface in phaseloop** — Instead of directly referencing `*coding.Dispatcher`, the executor uses a local `Orchestrator` interface. This keeps tests fast (no DB + mock binary) and decouples the packages.

2. **Fan-out writes to pre-allocated slice by index** — Avoids mutex overhead; safe because each goroutine owns its slot.

3. **`SourceJobIDs` in-memory only** — The field is on `core.Finding` but tagged `json:"-" yaml:"-"` and not written to SQLite. The persistence layer stores individual per-job findings with their own `job_id`; the in-memory dedup view is only needed by the phase result.

4. **`BuildFromPRDWorkflow.RunPhasesForPlan`** — Added as a separate method rather than embedding in `Run`. This preserves the current caller contract (Run returns scheduling info) and lets Plan 115 build on top cleanly.

### Known limitations (deferred)

- `TotalCost` in `AggregatedResults` is always 0.0. Cost ledger tracking is Plan 113+.
- The `developer` role YAML does not yet exist; tests use a stub Orchestrator. Real integration requires the role file (Plan 115 or a separate bootstrap step).
- `RunPhasesForPlan` runs phases sequentially. Parallelism within a plan (if future specs add it) would require changes here.
