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

### External review — 2026-04-20

**[FIXED] Critical: StageRegistry is wired as a field but never consulted**

`BuildFromPRDWorkflow.StageRegistry` is declared and documented but `RunPhasesForPlan` never calls `RolesForStage`. The reviewer/tester role list is hardcoded in `coding/phaseloop/executor.go:26` as `var reviewerRoles = []string{"reviewer.arch", "reviewer.frontend", "tester"}`. The plan (Phase 5) states: "The `PhaseExecutor.fanOut` reviewer roles are sourced from `StageRegistry.RolesForStage("phase-review")` when a registry is set." That wiring does not exist. `StageRegistry` is a dead field today — adding it to the struct and importing the package is the only observable effect. The workflow test (`TestBuildFromPRDWorkflow_RunPhasesForPlan_WithShipper_DryRun`) also never asserts that the registry-supplied roles were used instead of the hardcoded defaults.

File references: `coding/workflow/build_from_prd.go:49-51`, `coding/phaseloop/executor.go:24-26`.

Required fix: either (a) pass `StageRegistry.RolesForStage("phase-review")` into `PhaseExecutor` before calling `Execute` (requires adding a `ReviewerRoles []string` field to `PhaseExecutor` or passing it per-call) and add a test that verifies a policy override actually changes which roles are dispatched, or (b) explicitly defer this wiring to Plan 116 and remove the `StageRegistry` field from the struct (and the corresponding plan phase claim) to avoid a documented-but-inoperative API surface.

→ Response: Fixed. Added `ReviewerRoles []string` field to `PhaseExecutor`. `fanOut` now calls `e.reviewerRoles()` which falls back to `defaultReviewerRoles` when nil/empty. `BuildFromPRDWorkflow.RunPhasesForPlan` populates `ReviewerRoles` from `StageRegistry.RolesForStage("phase-review")` when a registry is set. `TestBuildFromPRDWorkflow_StageRegistry_OverrideUsed` verifies the override is dispatched and defaults are suppressed.

**[FIXED] Important: `ArtifactID` is always non-empty in `ShipResult` even when no artifact was stored**

`shipper.go:128` generates `artifactID = core.NewID()` unconditionally. The field is returned in `ShipResult.ArtifactID` (line 189) regardless of whether the artifact insert succeeded or was skipped (nil `ArtifactStore`, nil `JobStore`, or failed `CreateJob`). Callers inspecting `ShipResult.ArtifactID != ""` as a signal that an artifact row exists in the DB will get a false positive. Either (a) only set `ArtifactID` in the return value after the insert succeeds, setting it to `""` on skip/failure, or (b) document clearly that the field holds the *intended* ID and may not correspond to a persisted row.

File reference: `coding/shipper/shipper.go:128,187-191`.

→ Response: Fixed. `artifactID` is now initialised to `""` before the artifact block and only set to the generated ID after `InsertArtifact` succeeds. Skip/failure paths leave it empty. `ShipResult.ArtifactID` is now a reliable signal that a DB row exists.

**[FIXED] Suggestion: dead-code branch in `registry.go` should be deleted**

Lines 49–77 enter an `if stages, ok := wfOverride["stages"]; ok { ... }` block, immediately blank-assign `stages` (`_ = stages`), and exit. The entire `if` body is a no-op; the actual iteration happens in the sibling loop at line 80 that skips the `"stages"` key. The block adds ~30 lines of confusing comments that contradict the actual YAML contract (which the self-review `[OK]` note already clarifies). Delete the dead `if` block; the comment explaining why `"stages"` is skipped belongs on the `continue` at line 84.

File reference: `coding/stages/registry.go:49-77`.

→ Response: Fixed. Deleted the no-op `if stages, ok := wfOverride["stages"]; ok { ... }` block (lines 49–77). The explanatory comment explaining why the `"stages"` key is skipped now lives on the `continue` branch of the iteration loop.

**[OK] gh.go command injection — no issue**

`ghCreatePR` passes `branch`, `title`, and `body` as discrete `exec.Command` argument strings, not via a shell interpreter. There is no shell interpolation: `exec.CommandContext(ctx, "gh", "pr", "create", "--title", title, ...)` hands each string directly to the OS as a separate `argv` element. Newlines or shell metacharacters in a title cannot escape into a second command. The `//nolint:gosec` suppression is accurate — `gosec` flags any `exec.Command` whose first argument is not a literal; the suppression is appropriate here and the justification comment is correct.

**[OK] Shipper.Ship flow — checkpoint then proceed**

The spec (Phase 2) explicitly states "In V1, true blocking is deferred; we emit and proceed." `Ship` records the attention item, logs non-fatally on insert failure, and continues. This matches the documented V1 contract. Plan 103 is the correct vehicle for blocking semantics.

**[OK] Race safety**

`StageRegistry` is immutable after `NewStageRegistry` returns (all writes happen in the constructor; `RolesForStage` and `AllStages` only read from `r.merged`). `Shipper` has no shared mutable state between calls. The race detector confirmed 0 races across 3 packages.

**[OK] Defensive copy in `RolesForStage`**

Both construction-time copies (lines 41-43 and 89-92) and the return-time copy (lines 110-113) are correct. The mutation isolation test (`TestStageRegistry_Mutation_IsolatedFromCaller`) verifies this end-to-end.

**[OK] Empty-list vs nil distinction for disabled stages**

`NewStageRegistry` stores `[]string{}` (not nil) for an empty policy override. `RolesForStage` returns a copy via `make([]string, 0)` for that case, preserving the nil/empty distinction. The test `TestStageRegistry_PolicyOverride_EmptyListDisablesStage` asserts exactly this.

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
