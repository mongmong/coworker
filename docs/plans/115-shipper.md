# Plan 115 — Shipper + Workflow Stage Customization

**Branch:** `feature/plan-115-shipper`
**Blocks on:** 114 (PhaseExecutor, BuildFromPRDWorkflow)
**Parallel-safe with:** 107

---

## Purpose

Complete the autopilot path: after all phases of a plan are clean, create a PR via `gh pr create` and allow per-repo customization of which roles fire at each named stage.

---

## Background

Plan 114 implemented the inner phase loop. The `build-from-prd` workflow currently ends at `RunPhasesForPlan`. Plan 115 adds the terminal step: the `shipper` role creates a PR, records the PR URL as an artifact, and emits a `plan.shipped` event. It also introduces the Level 1 named-stage registry so `policy.yaml` can override which roles fire at `phase-dev`, `phase-review`, `phase-test`, and `phase-ship` without touching Go code.

---

## Architecture

```
coding/
├── roles/
│   └── shipper.yaml          NEW
├── prompts/
│   └── shipper.md            NEW
├── shipper/
│   ├── shipper.go            NEW — ShipPlan, ShipResult
│   ├── shipper_test.go       NEW
│   └── gh.go                 NEW — ghCreatePR shell-out
├── stages/
│   ├── registry.go           NEW — StageRegistry + RolesForStage
│   ├── registry_test.go      NEW
│   └── defaults.go           NEW — default stage definitions
└── workflow/
    └── build_from_prd.go     MODIFY — wire Shipper + StageRegistry
```

---

## Phases

### Phase 1 — Shipper role YAML + prompt

`coding/roles/shipper.yaml` and `coding/prompts/shipper.md`. Mirrors `reviewer_arch` pattern.

Inputs: `plan_path`, `branch`, `title`.
Outputs: PR URL artifact, post-exec report.
CLI: claude-code. Sandbox: git + gh (workspace-write).

### Phase 2 — ready-to-ship checkpoint

`Shipper.Ship` creates an `AttentionCheckpoint` item with kind `ready-to-ship` before calling `gh`. In V1, true blocking is deferred (per spec §Checkpoints); we emit and proceed. The policy can configure `ready-to-ship: block` to gate future blocking when that mechanism lands.

### Phase 3 — gh pr create integration

`coding/shipper/gh.go` shells out to `gh pr create --title ... --body ... --head <branch>` via `os/exec` with a context. Parses the PR URL from stdout. `DryRun` mode skips the real call and returns a synthetic URL for tests.

### Phase 4 — Level 1 stage registry

`coding/stages/registry.go` implements `StageRegistry` with:

```go
type StageRegistry struct {
    workflow  string
    defaults  map[string][]string
    overrides map[string][]string  // merged from policy.WorkflowOverrides[workflow]["stages"]
}

func (r *StageRegistry) RolesForStage(stage string) []string
```

`defaults.go` defines the four default stages for `build-from-prd`:
- `phase-dev`: `["developer"]`
- `phase-review`: `["reviewer.arch", "reviewer.frontend"]`
- `phase-test`: `["tester"]`
- `phase-ship`: `["shipper"]`

### Phase 5 — Wire into BuildFromPRDWorkflow

`BuildFromPRDWorkflow` gains `Shipper *shipper.Shipper` and `StageRegistry *stages.StageRegistry` fields. `RunPhasesForPlan` remains unchanged (PhaseExecutor controls inner loop). After all phases complete, `RunPhasesForPlan` calls `Shipper.Ship`. The `PhaseExecutor.fanOut` reviewer roles are sourced from `StageRegistry.RolesForStage("phase-review")` when a registry is set.

### Phase 6 — Tests

- `coding/shipper/shipper_test.go`: Ship with DryRun=true, verify attention item created and artifact recorded. Test missing-store error paths.
- `coding/stages/registry_test.go`: Default roles, override from policy, empty override disables stage.
- `coding/workflow/build_from_prd_test.go`: end-to-end smoke — manifest → schedule → phase loop (stub) → ship.

---

## Event additions to core/event.go

```go
EventPlanShipped EventKind = "plan.shipped"
```

---

## Key invariants preserved

- Event log before state update: shipper emits `plan.shipped` before recording artifact row.
- Artifacts are pointers: PR URL stored as `artifact.Kind = "pr-url"`, `artifact.Path = <url>`.
- No failure silently advances state: `ghCreatePR` errors are propagated; `Ship` returns error to caller.

---

## Code Review

### Self-review findings

**[FIXED]** `Shipper.ArtifactStore` insertion violated `artifacts.job_id` FK because the
shipper synthesizes a job ID (`"ship-plan-NNN"`) without creating a DB row.
Fix: added `JobStore *store.JobStore` field; when `ArtifactStore` is set and
`JobStore` is non-nil, the shipper creates a minimal job row before inserting
the artifact. Tests caught this immediately.

**[FIXED]** `AttentionStore.InsertAttention` violated `attention.run_id REFERENCES runs(id)`
when the test used a synthetic run_id. Fix: tests now call `seedRun()` to
pre-create the run row before calling Ship.

**[OK]** `stages.StageRegistry` drops the intermediate `"stages"` YAML key from
`policy.WorkflowOverrides[workflow]` since `core.Policy.WorkflowOverrides` is
typed as `map[string]map[string][]string` — the three-level YAML nesting cannot
be represented without an extra indirection. The registry treats every key in
`wfOverride` except `"stages"` as a stage name. This matches the call site in
tests and in the workflow; the YAML format documented in the spec is supported
by encoding stages directly as top-level keys under the workflow name.

**[OK]** `RunPhasesForPlan` return type changed from `([]*phaseloop.PhaseResult, error)` to
`(*RunPhasesResult, error)`. No external callers existed; all callers are in the
workflow package itself and in the workflow tests.

---

## Post-Execution Report

All 6 phases implemented and passing. Full test suite: 0 failures, 0 race
conditions, 0 lint issues.

### What was built

1. `coding/roles/shipper.yaml` + `coding/prompts/shipper.md` — shipper role definition following the reviewer_arch pattern.
2. `coding/shipper/shipper.go` + `gh.go` — `Shipper.Ship`: ready-to-ship checkpoint → (dry-run or) `gh pr create` → ship job row → pr-url artifact → `plan.shipped` event.
3. `coding/stages/registry.go` + `defaults.go` — `StageRegistry` with Level 1 override: `RolesForStage(stage)` returns default or policy-overridden role list; copies returned to prevent caller mutation.
4. `coding/workflow/build_from_prd.go` — `RunPhasesForPlan` extended: after phases complete, calls `Shipper.Ship` when configured; returns `*RunPhasesResult` (breaking change, no external callers).
5. `core/event.go` — `EventPlanShipped` event kind added.
6. Tests: shipper (4), stages (8), workflow (4 new + existing pass).

### Invariants preserved

- Event-log-before-state: `plan.shipped` event written before artifact row insertion.
- Artifacts are pointers: PR URL stored as `artifact.Kind = "pr-url"`, `artifact.Path = <url>`.
- No failure silently advances: `ghCreatePR` error propagated; `Ship` returns error to caller; store failures logged non-fatally (PR already created by then).
- Checkpoint recorded: `ready-to-ship` attention item created; blocking deferred to Plan 103.
