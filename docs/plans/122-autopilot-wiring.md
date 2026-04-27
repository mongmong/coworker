# Plan 122 — B1: Autopilot Phase Execution Wiring

> Implemented inline. Phase-by-phase commits, full suite green before merge.

**Goal:** Make `coworker run <prd.md>` actually execute plan phases end-to-end. Today the post-spec-approved code path (`cli/run.go:503-511`) constructs `BuildFromPRDWorkflow` without `PhaseExecutor`, `Shipper`, or `StageRegistry`, so `RunPhasesForPlan` errors immediately at `coding/workflow/build_from_prd.go:223`. This is the V1 ship-readiness audit's only `[BLOCKER]` flagged by both audit lanes.

**Architecture:**
- Add a single helper `buildPhaseRunner(ctx, runID, db, policy, attentionStore, checkpointWriter, eventStore, logger)` in `cli/run.go` that returns a fully-wired `*workflow.BuildFromPRDWorkflow`.
- The helper constructs: a phase-execution Dispatcher (separate from the planner Dispatcher to avoid reusing one Dispatcher across roles with different role dirs), a `PhaseExecutor` wired to that Dispatcher + EventStore + AttentionStore + CheckpointWriter, a `Shipper` wired to AttentionStore + EventStore + ArtifactStore + JobStore + CheckpointWriter, and a default `StageRegistry`.
- Replace the partially-wired struct literal at line 503-511 with a call to the new helper.
- Two integration tests:
  1. `TestRun_PostApproved_DispatchesPhases` — given an approved spec + plan, the runner reaches `RunPhasesForPlan` without the "PhaseExecutor is required" error.
  2. `TestRun_PostApproved_RunsToShip` — full happy path with stub agents; phases complete clean; shipper runs in dry-run; PR URL recorded.

**Tech Stack:** No new dependencies. Reuses existing types from `coding/`, `coding/phaseloop`, `coding/shipper`, `coding/stages`, `store/`.

**Reference:** `docs/reviews/2026-04-27-comprehensive-audit.md` §B1; `coding/workflow/build_from_prd.go:223` for the failing assertion; `coding/workflow/build_from_prd_test.go:71-79` for the test-side wiring pattern that production should mirror.

---

## Required-API audit (verify before writing code)

| Surface | Reality (verified) |
| --- | --- |
| `phaseloop.PhaseExecutor` fields | `Dispatcher Orchestrator`, `EventStore *store.EventStore`, `AttentionStore *store.AttentionStore`, `CheckpointWriter core.CheckpointWriter`, `Policy *core.Policy`, `ReviewerRoles []string`, `TesterRoles []string`, `WorkDir`, `RoleDir`, `Logger` (`coding/phaseloop/executor.go:39-87`). |
| `phaseloop.Orchestrator` interface | `Orchestrate(ctx context.Context, input *coding.DispatchInput) (*coding.DispatchResult, error)` — satisfied by `*coding.Dispatcher`. |
| `shipper.Shipper` fields | `AttentionStore`, `CheckpointWriter`, `EventStore`, `ArtifactStore`, `JobStore`, `Logger`, `DryRun` (`coding/shipper/shipper.go:24-47`). |
| `stages.StageRegistry` constructor | Check `coding/stages/registry.go` for the production constructor; defaults are pulled from `coding/stages/defaults.go`. |
| `manifest.WorktreeManager` | Optional; required only when `max_parallel_plans > 1`. V1 stays sequential, so nil is fine for now. |
| `buildRunDispatcher(db, policy, logger)` returns `*coding.Dispatcher` | Already wires Agent + CLIAgents + DB + Logger + Policy + SupervisorWriter + CostWriter (`cli/run.go:760-783`). |

---

## Scope

In scope:

1. New helper `buildPhaseRunner` in `cli/run.go` that returns a fully-wired `*workflow.BuildFromPRDWorkflow`.
2. Construct a phase-execution `*coding.Dispatcher` inside the helper (or accept the existing one and re-wire RoleDir for phase work — TBD during impl based on Dispatcher reuse cost).
3. Construct `phaseloop.PhaseExecutor` with the production Dispatcher + EventStore + AttentionStore + CheckpointWriter + Policy + Logger; propagate WorkDir and RoleDir.
4. Construct `shipper.Shipper` with AttentionStore + CheckpointWriter + EventStore + ArtifactStore + JobStore + Logger; default `DryRun=false` (production uses real `gh pr create`).
5. Construct a default `stages.StageRegistry` (from policy if it provides workflow_overrides, else defaults).
6. Replace the partially-wired struct literal at `cli/run.go:503-511` with a single call to the helper.
7. Two integration tests in `cli/run_test.go`:
   - `TestRun_PostApproved_DispatchesPhases` — uses an existing test scaffold; asserts `RunPhasesForPlan` is reached and returns without the dispatch-disabled error.
   - `TestRun_PostApproved_RunsToShip` — full happy path with `Shipper.DryRun=true` and a stub `Orchestrator`.
8. Update `decisions.md` (Decision 9 or extend existing): production wiring of `BuildFromPRDWorkflow` lives in `cli/run.go::buildPhaseRunner`; tests construct PhaseExecutor directly via test helpers.

Out of scope:

- WorktreeManager wiring for parallel plans (`max_parallel_plans > 1`) — defer until parallel runs are actively used. Keep nil-safe behavior.
- Real `gh pr create` integration testing (covered separately by the live test scaffolding from Plan 120 + the Shipper PR-creation tests flagged as IMPORTANT-I6 in the audit).
- Refactoring `runPlanLoopWithDeps` — minimal-change in this plan.
- Wiring fixes for `phase-clean` / `ready-to-ship` checkpoint kind names (audit IMPORTANT-I2) — separate plan.

---

## File Structure

**Modify:**
- `cli/run.go` — add `buildPhaseRunner`; call it at line 503-511.
- `cli/run_test.go` — add 2 integration tests.
- `docs/architecture/decisions.md` — extend Decision 9 entry.

**Create:** none.

---

## Phase 1 — Add `buildPhaseRunner` helper

**Files:**
- Modify: `cli/run.go`

- [ ] **Step 1 — Read the imports of `cli/run.go` to confirm the packages already imported (phaseloop, shipper, stages).** Add missing imports.

- [ ] **Step 2 — Add the helper at the end of `cli/run.go` (after `buildRunDispatcher`):**

```go
// buildPhaseRunner constructs a fully-wired BuildFromPRDWorkflow ready to
// execute plan phases. The runner has its own phase-execution Dispatcher
// (not the planner Dispatcher) so role/prompt directories, agent maps,
// and supervisor wiring stay independent.
//
// Returns the runner and any error from sub-component construction. Callers
// must close any owned subprocess resources via run termination.
func buildPhaseRunner(
	ctx context.Context,
	runID string,
	db *store.DB,
	policy *core.Policy,
	attentionStore *store.AttentionStore,
	checkpointWriter core.CheckpointWriter,
	eventStore *store.EventStore,
	logger *slog.Logger,
) (*workflow.BuildFromPRDWorkflow, error) {
	cwd, _ := os.Getwd()
	roleDir := runRoleDir
	if roleDir == "" {
		roleDir = filepath.Join(".coworker", "roles")
		if _, statErr := os.Stat(roleDir); os.IsNotExist(statErr) {
			roleDir = filepath.Join("coding", "roles")
		}
	}

	// Phase-execution dispatcher — separate from the planner dispatcher so
	// the two can have independent role configs (planner runs alone; phase
	// dispatcher routes developer/reviewer/tester roles via CLIAgents map).
	phaseDispatcher, err := buildRunDispatcher(db, policy, logger)
	if err != nil {
		return nil, fmt.Errorf("build phase dispatcher: %w", err)
	}

	phaseExec := &phaseloop.PhaseExecutor{
		Dispatcher:       phaseDispatcher,
		EventStore:       eventStore,
		AttentionStore:   attentionStore,
		CheckpointWriter: checkpointWriter,
		Policy:           policy,
		WorkDir:          cwd,
		RoleDir:          roleDir,
		Logger:           logger,
	}

	jobStore := store.NewJobStore(db, eventStore)
	artifactStore := store.NewArtifactStore(db, eventStore)

	ship := &shipper.Shipper{
		AttentionStore:   attentionStore,
		CheckpointWriter: checkpointWriter,
		EventStore:       eventStore,
		ArtifactStore:    artifactStore,
		JobStore:         jobStore,
		Logger:           logger,
		DryRun:           runShipperDryRun, // see flag wiring below
	}

	registry := stages.NewStageRegistry()
	if policy != nil {
		registry.ApplyOverrides(policy)
	}

	manifestPath := runManifestPath
	runner := &workflow.BuildFromPRDWorkflow{
		ManifestPath:     manifestPath,
		Policy:           policy,
		Logger:           logger,
		WorkDir:          cwd,
		RoleDir:          roleDir,
		PhaseExecutor:    phaseExec,
		Shipper:          ship,
		StageRegistry:    registry,
		PlanWriter:       store.NewPlanStore(db, eventStore),
		CheckpointWriter: checkpointWriter,
	}
	return runner, nil
}
```

- [ ] **Step 3 — Resolve any unknown identifiers** (`stages.NewStageRegistry`, `registry.ApplyOverrides`, `runShipperDryRun`, `runManifestPath`):
  - **`stages.NewStageRegistry`** — read `coding/stages/registry.go` to confirm exact constructor name. If named differently, adapt.
  - **`registry.ApplyOverrides`** — verify this method exists; if it has a different name, adapt or use the exact API.
  - **`runShipperDryRun`** — add a top-level `var runShipperDryRun bool` and a flag binding `runCmd.Flags().BoolVar(&runShipperDryRun, "shipper-dry-run", false, "skip real gh pr create; record a dry-run URL")`. Default false (production uses real gh).
  - **`runManifestPath`** — already exists or available as a function-scoped variable; keep the parameter shape close to the existing wiring at line 503-511.

- [ ] **Step 4 — Replace the partially-wired struct at `cli/run.go:503-511`:**

```go
runner, err := buildPhaseRunner(
    ctx, runID, db, policy, attentionStore, checkpointWriter, eventStore, logger,
)
if err != nil {
    return fmt.Errorf("build phase runner: %w", err)
}
```

- [ ] **Step 5 — Verify build:**

```bash
go build ./...
```

Expected: clean. If `stages.NewStageRegistry` or `registry.ApplyOverrides` don't exist with those names, fix the snippet to match reality.

- [ ] **Step 6 — Run existing tests:**

```bash
go test -race ./cli ./coding/workflow ./coding/phaseloop -count=1 -timeout 60s
```

Expected: PASS — the existing tests use stubs and don't touch the production wiring.

- [ ] **Step 7 — Commit:**

```bash
git add cli/run.go
git commit -m "Plan 122 Phase 1: buildPhaseRunner — fully-wired BuildFromPRDWorkflow for autopilot"
```

---

## Phase 2 — Integration tests

**Files:**
- Modify: `cli/run_test.go`

- [ ] **Step 1 — Read existing `cli/run_test.go`** to find similar tests and reuse helpers (e.g., test DB, policy, dispatcher stubs).

- [ ] **Step 2 — Add `TestRun_PostApproved_DispatchesPhases`:**

This test verifies that the post-spec-approved attention path constructs a runner that can call `RunPhasesForPlan` without the "PhaseExecutor is required" error. Since the audit's BLOCKER claim is about this specific assertion, the simplest test is:

```go
func TestRun_PostApproved_BuildsRunnerWithPhaseExecutor(t *testing.T) {
    // ... wire up an in-memory DB, EventStore, AttentionStore, CheckpointWriter ...
    runner, err := buildPhaseRunner(
        context.Background(),
        "test-run-1",
        db,
        nil, // policy — nil OK for default behavior
        attentionStore,
        checkpointStore,
        eventStore,
        slog.Default(),
    )
    if err != nil {
        t.Fatalf("buildPhaseRunner: %v", err)
    }
    if runner.PhaseExecutor == nil {
        t.Fatal("PhaseExecutor is nil; runner not fully wired")
    }
    if runner.Shipper == nil {
        t.Fatal("Shipper is nil; runner not fully wired")
    }
    if runner.StageRegistry == nil {
        t.Fatal("StageRegistry is nil; runner not fully wired")
    }
    // RunPhasesForPlan still requires real role files / Agent, so we don't
    // call it here — the structural wiring is what this test guards.
}
```

- [ ] **Step 3 — Add `TestRun_PostApproved_RunsToShip`** (full happy-path stubbed):

```go
func TestRun_PostApproved_RunsToShip(t *testing.T) {
    // 1. Create temp manifest with a single plan + single phase.
    // 2. Wire the runner via deps.Runner = customRunner where customRunner
    //    is a BuildFromPRDWorkflow with PhaseExecutor that uses a stub
    //    Orchestrator returning JobResult{ExitCode: 0} — this is the
    //    "always clean" stub from build_from_prd_test.go::newTestPhaseExecutor.
    // 3. Call runPlanLoopWithDeps with a plan-approved attention item.
    // 4. Assert no error, assert ShipResult.PRURL non-empty (dry-run).
    // ... (full test code; mirror existing post-resume test patterns) ...
}
```

If reusing the test-helper pattern from `coding/workflow/build_from_prd_test.go::newTestPhaseExecutor` is awkward (cross-package test helper), inline the `stubOrchestrator` definition in the cli/run_test.go test.

- [ ] **Step 4 — Run + commit:**

```bash
go test -race ./cli -count=1 -timeout 60s -run TestRun_PostApproved
git add cli/run_test.go
git commit -m "Plan 122 Phase 2: integration tests for runner wiring + happy-path-to-ship"
```

---

## Phase 3 — decisions.md update + final verification

**Files:**
- Modify: `docs/architecture/decisions.md`

- [ ] **Step 1 — Append a brief addendum to Decision 7 (Test Layers) or add Decision 9:**

```markdown
## Decision 9: Production Workflow Wiring (Plan 122)

**Context:** `coding/workflow/build_from_prd.go::BuildFromPRDWorkflow` requires several optional collaborators (PhaseExecutor, Shipper, StageRegistry) to actually run plan phases end-to-end. Without them, `RunPhasesForPlan` returns the "PhaseExecutor is required" error, leaving autopilot non-functional. Tests construct these collaborators ad-hoc; production previously did not.

**Decision:** A single helper `cli/run.go::buildPhaseRunner` constructs and wires the full production runner. It builds a phase-execution Dispatcher (separate from the planner Dispatcher), a PhaseExecutor with EventStore + AttentionStore + CheckpointWriter, a Shipper with all five stores it needs, and a default StageRegistry that applies any policy.workflow_overrides. The runner is then injected into `runPlanLoopWithDeps`.

**Decision:** Tests continue to construct `*phaseloop.PhaseExecutor` and `*shipper.Shipper` directly via existing test helpers (`newTestPhaseExecutor`, `newDirtyPhaseExecutor`). The production helper is exercised end-to-end by `TestRun_PostApproved_RunsToShip`.

**Enforcement:** `TestRun_PostApproved_BuildsRunnerWithPhaseExecutor` verifies that buildPhaseRunner returns a fully-wired runner; `TestRun_PostApproved_RunsToShip` verifies the wired runner reaches Shipper without erroring.

**Status:** Introduced in Plan 122.
```

- [ ] **Step 2 — Full verification:**

```bash
go build ./...
go test -race ./... -count=1 -timeout 180s
golangci-lint run ./...
```

Expected: build clean, all tests PASS, 0 lint issues.

- [ ] **Step 3 — Commit + merge.**

---

## Self-Review Checklist

- [ ] `buildPhaseRunner` returns a runner with non-nil PhaseExecutor, Shipper, StageRegistry, PlanWriter, CheckpointWriter.
- [ ] The new helper does not introduce any `coding → store` import (uses interfaces from `core/sinks.go` for Plan/Checkpoint writers).
- [ ] `RunPhasesForPlan` no longer errors with "PhaseExecutor is required" when invoked from the production code path.
- [ ] `Shipper.DryRun` defaults to false (production uses real gh) but is overridable via `--shipper-dry-run`.
- [ ] StageRegistry honors `policy.workflow_overrides` when policy is non-nil.
- [ ] No regressions in existing tests.

---

## Code Review

(To be filled in after implementation by Codex review subagent.)

---

## Post-Execution Report

(To be filled in after implementation.)
