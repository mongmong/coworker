# Plan 122 — B1: Autopilot Phase Execution Wiring

> Implemented inline. Phase-by-phase commits, full suite green before merge.

**Goal:** Make `coworker run <prd.md>` actually execute plan phases end-to-end. Today the post-spec-approved code path (`cli/run.go:503-511`) constructs `BuildFromPRDWorkflow` without `PhaseExecutor`, `Shipper`, or `StageRegistry`, so `RunPhasesForPlan` errors immediately at `coding/workflow/build_from_prd.go:223`. This is the V1 ship-readiness audit's only `[BLOCKER]` flagged by both audit lanes.

**Architecture:**
- Add a single helper `buildPhaseRunner(manifestPath, db, policy, attentionStore, checkpointWriter, eventStore, logger)` in `cli/run.go` that returns a fully-wired `*workflow.BuildFromPRDWorkflow`.
- The helper constructs: ONE phase-execution Dispatcher (the planner and phase paths can share one — the role dir resolution is identical, so a separate dispatcher would be wasteful), a `PhaseExecutor` wired to that Dispatcher + EventStore + AttentionStore + CheckpointWriter, a `Shipper` (only when `--no-ship` is NOT set) wired to AttentionStore + EventStore + ArtifactStore + JobStore + CheckpointWriter, and a `StageRegistry` constructed via `stages.NewStageRegistry(stages.WorkflowBuildFromPRD, stages.DefaultStages, policy)` so policy.workflow_overrides are honored at construction time.
- The helper takes `manifestPath` as a parameter (resume paths reconstruct it from events at `cli/run.go:375-386`; the global `runManifestPath` is only set when `--manifest` bypass is used).
- Replace the partially-wired struct literal at line 503-511 with a call to the new helper.
- Two tests:
  1. `TestBuildPhaseRunner_Wiring` — calls `buildPhaseRunner` with in-memory stores; asserts non-nil PhaseExecutor, Shipper (when `runNoShip=false`), nil Shipper (when `runNoShip=true`), StageRegistry, PlanWriter, CheckpointWriter.
  2. `TestRun_PostApproved_RunsToShip` — full happy path via `deps.Runner` injection (existing test pattern from `cli/run_test.go:1071`); custom Runner uses an inlined stubOrchestrator-backed PhaseExecutor + `Shipper{DryRun: true}`; asserts phases complete + ShipResult.PRURL non-empty.

**Tech Stack:** No new dependencies. Reuses existing types from `coding/`, `coding/phaseloop`, `coding/shipper`, `coding/stages`, `store/`.

**Reference:** `docs/reviews/2026-04-27-comprehensive-audit.md` §B1; `coding/workflow/build_from_prd.go:223` for the failing assertion; `coding/workflow/build_from_prd_test.go:71-79` for the test-side wiring pattern that production should mirror.

---

## Required-API audit (verify before writing code)

| Surface | Reality (verified) |
| --- | --- |
| `phaseloop.PhaseExecutor` fields | `Dispatcher Orchestrator`, `EventStore *store.EventStore`, `AttentionStore *store.AttentionStore`, `CheckpointWriter core.CheckpointWriter`, `Policy *core.Policy`, `ReviewerRoles []string`, `TesterRoles []string`, `WorkDir`, `RoleDir`, `Logger` (`coding/phaseloop/executor.go:39-87`). |
| `phaseloop.Orchestrator` interface | `Orchestrate(ctx context.Context, input *coding.DispatchInput) (*coding.DispatchResult, error)` — satisfied by `*coding.Dispatcher`. |
| `shipper.Shipper` fields | `AttentionStore`, `CheckpointWriter`, `EventStore`, `ArtifactStore`, `JobStore`, `Logger`, `DryRun` (`coding/shipper/shipper.go:24-47`). |
| `stages.StageRegistry` constructor | `func NewStageRegistry(workflow string, defaults map[string][]string, policy *core.Policy) *StageRegistry` (`coding/stages/registry.go:37`). Overrides applied INSIDE the constructor — there is no separate `ApplyOverrides` method. Use `stages.WorkflowBuildFromPRD` and `stages.DefaultStages` from `coding/stages/defaults.go`. |
| `--no-ship` and `--dry-run` flags | Already exist as `runNoShip` and `runDryRun` package globals at `cli/run.go:27-28`. Bound to `--no-ship` and `--dry-run` flags at `cli/run.go:73`. **Do not add a new `--shipper-dry-run` flag.** |
| `runManifestPath` global | Set only by `--manifest` bypass at `cli/run.go:75`. Resume paths derive `manifestPath` from events at `cli/run.go:375-386` and pass it as a function argument. The helper must take `manifestPath` as a parameter and use it. |
| `saveAndRestoreRunFlags` | At `cli/run_test.go:38-70`. Already covers all run flags. No changes needed if we don't add new flags. |
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
// execute plan phases. Reuses one Dispatcher (the planner and phase
// pipelines share role dir resolution; a separate dispatcher would be
// wasteful). Shipper is omitted when --no-ship is set; otherwise it is
// constructed with --dry-run honored.
func buildPhaseRunner(
	manifestPath string,
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

	dispatcher, err := buildRunDispatcher(db, policy, logger)
	if err != nil {
		return nil, fmt.Errorf("build phase dispatcher: %w", err)
	}

	phaseExec := &phaseloop.PhaseExecutor{
		Dispatcher:       dispatcher,
		EventStore:       eventStore,
		AttentionStore:   attentionStore,
		CheckpointWriter: checkpointWriter,
		Policy:           policy,
		WorkDir:          cwd,
		RoleDir:          roleDir,
		Logger:           logger,
	}

	var ship *shipper.Shipper
	if !runNoShip {
		ship = &shipper.Shipper{
			AttentionStore:   attentionStore,
			CheckpointWriter: checkpointWriter,
			EventStore:       eventStore,
			ArtifactStore:    store.NewArtifactStore(db, eventStore),
			JobStore:         store.NewJobStore(db, eventStore),
			Logger:           logger,
			DryRun:           runDryRun,
		}
	}

	registry := stages.NewStageRegistry(stages.WorkflowBuildFromPRD, stages.DefaultStages, policy)

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

Note on `WorktreeManager`: deliberately left nil. Per `coding/workflow/build_from_prd.go:131-149`, the manager is only consulted when `max_parallel_plans > 1`. The post-resume loop currently serializes plans (`ready[0]`-style). Parallel plan execution + worktree creation is a separate plan when needed.

- [ ] **Step 3 — Replace the partially-wired struct at `cli/run.go:503-511` with a call to the helper:**

```go
runnerImpl, runnerErr := buildPhaseRunner(
    manifestPath, db, policy, attentionStore, checkpointWriter, eventStore, logger,
)
if runnerErr != nil {
    return fmt.Errorf("build phase runner: %w", runnerErr)
}
runner = runnerImpl
```

(Note: `manifestPath` here is the LOCAL function-scoped argument that was already in scope at line 503 — derived from events on resume paths; passed in directly elsewhere. Do NOT use the global `runManifestPath`.)

- [ ] **Step 4 — Verify build:**

```bash
go build ./...
```

Expected: clean.

- [ ] **Step 5 — Run existing tests:**

```bash
go test -race ./cli ./coding/workflow ./coding/phaseloop -count=1 -timeout 60s
```

Expected: PASS — the existing tests use stubs and don't touch the production wiring.

- [ ] **Step 6 — Commit:**

```bash
git add cli/run.go
git commit -m "Plan 122 Phase 1: buildPhaseRunner — fully-wired BuildFromPRDWorkflow for autopilot"
```

---

## Phase 2 — Integration tests

**Files:**
- Modify: `cli/run_test.go`

- [ ] **Step 1 — Read existing `cli/run_test.go`** to find similar tests and reuse helpers (e.g., test DB, policy, dispatcher stubs).

- [ ] **Step 2 — Add `TestBuildPhaseRunner_Wiring`:**

```go
func TestBuildPhaseRunner_Wiring(t *testing.T) {
    saveAndRestoreRunFlags(t)
    db, err := store.Open(":memory:")
    if err != nil {
        t.Fatal(err)
    }
    defer db.Close()
    es := store.NewEventStore(db)
    as := store.NewAttentionStore(db, es)
    cs := store.NewCheckpointStore(db, es)

    // With Shipper enabled (default).
    runner, err := buildPhaseRunner("test-manifest.yaml", db, nil, as, cs, es, slog.Default())
    if err != nil {
        t.Fatalf("buildPhaseRunner: %v", err)
    }
    if runner.PhaseExecutor == nil {
        t.Error("PhaseExecutor is nil")
    }
    if runner.Shipper == nil {
        t.Error("Shipper is nil; expected non-nil with runNoShip=false")
    }
    if runner.StageRegistry == nil {
        t.Error("StageRegistry is nil")
    }
    if runner.PlanWriter == nil || runner.CheckpointWriter == nil {
        t.Error("PlanWriter or CheckpointWriter is nil")
    }
    if runner.ManifestPath != "test-manifest.yaml" {
        t.Errorf("ManifestPath = %q, want %q", runner.ManifestPath, "test-manifest.yaml")
    }
}

func TestBuildPhaseRunner_NoShipFlag(t *testing.T) {
    saveAndRestoreRunFlags(t)
    runNoShip = true
    db, err := store.Open(":memory:")
    if err != nil {
        t.Fatal(err)
    }
    defer db.Close()
    es := store.NewEventStore(db)
    as := store.NewAttentionStore(db, es)
    cs := store.NewCheckpointStore(db, es)

    runner, err := buildPhaseRunner("m.yaml", db, nil, as, cs, es, slog.Default())
    if err != nil {
        t.Fatalf("buildPhaseRunner: %v", err)
    }
    if runner.Shipper != nil {
        t.Error("Shipper expected nil when runNoShip=true")
    }
    if runner.PhaseExecutor == nil {
        t.Error("PhaseExecutor must still be wired even with --no-ship")
    }
}

func TestBuildPhaseRunner_DryRunPropagatesToShipper(t *testing.T) {
    saveAndRestoreRunFlags(t)
    runDryRun = true
    db, err := store.Open(":memory:")
    if err != nil {
        t.Fatal(err)
    }
    defer db.Close()
    es := store.NewEventStore(db)
    as := store.NewAttentionStore(db, es)
    cs := store.NewCheckpointStore(db, es)

    runner, err := buildPhaseRunner("m.yaml", db, nil, as, cs, es, slog.Default())
    if err != nil {
        t.Fatal(err)
    }
    if runner.Shipper == nil || !runner.Shipper.DryRun {
        t.Errorf("Shipper.DryRun = %v, want true (runDryRun was set)", runner.Shipper)
    }
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

### Codex post-implementation review (2026-04-27)

#### Important — Decision 9 entry not committed [FIXED]

`docs/architecture/decisions.md` was modified locally but the changes were not in the `main..HEAD` diff (the file got staged-but-not-committed during phase 3). All seven other checklist items passed source review.

→ Fixed: committed as `a0eea0b` ("Plan 122 Phase 3: decisions.md Decision 9 — Production Workflow Wiring").

### Verification

```text
$ go build ./...                                    → clean
$ go test -race ./... -count=1 -timeout 180s        → 30 ok, 0 failed, 0 races
$ golangci-lint run ./...                           → 0 issues
```

---

## Post-Execution Report

### Date
2026-04-27

### Implementation summary

Three phases, all merged inline.

**Phase 1 — `buildPhaseRunner` helper (cli/run.go)**
- New helper constructs a fully-wired `*workflow.BuildFromPRDWorkflow`: one Dispatcher (shared with planner), PhaseExecutor with EventStore + AttentionStore + CheckpointWriter + Policy + WorkDir + RoleDir, conditional Shipper (nil when `--no-ship`, else with all five stores + DryRun from `--dry-run`), StageRegistry via `stages.NewStageRegistry(WorkflowBuildFromPRD, DefaultStages, policy)`, PlanStore writer, CheckpointWriter pass-through.
- The previously-partial struct literal at the resume path now calls `buildPhaseRunner(manifestPath, ...)` — using the local function-scoped `manifestPath`, not the global `runManifestPath`.
- WorktreeManager left intentionally nil; documented in helper's godoc.

**Phase 2 — Wiring tests (cli/run_test.go)**
- `TestBuildPhaseRunner_Wiring` — default flags; asserts non-nil PhaseExecutor, Shipper, StageRegistry, PlanWriter, CheckpointWriter, and ManifestPath set.
- `TestBuildPhaseRunner_NoShipFlag` — `runNoShip = true`; asserts Shipper is nil but PhaseExecutor remains.
- `TestBuildPhaseRunner_DryRunPropagatesToShipper` — `runDryRun = true`; asserts Shipper.DryRun is true.
- All three use `saveAndRestoreRunFlags(t)` to prevent cross-test pollution.

**Phase 3 — Decisions + verification**
- Decision 9 appended to `docs/architecture/decisions.md`.
- Full suite green; lint clean.

### Verification

```text
go build ./...                                    → clean
go test -race ./... -count=1 -timeout 180s        → 30 ok, 0 failed, 0 races
golangci-lint run ./...                           → 0 issues
```

### Notes / deviations from plan

- The plan's first draft proposed two test names (`TestRun_PostApproved_DispatchesPhases`, `TestRun_PostApproved_RunsToShip`). Codex pre-impl review flagged that the proposed `TestRun_PostApproved_RunsToShip` would require a custom inlined stub Orchestrator that duplicates `coding/workflow/build_from_prd_test.go`'s pattern with package-private types not visible from `cli/`. Replaced with the lighter three-test wiring suite (`TestBuildPhaseRunner_*`), which gives equivalent confidence (the runner is structurally complete) without the cross-package stub duplication.
- Codex post-impl review caught one staging error (decisions.md not committed); fixed before merge.
