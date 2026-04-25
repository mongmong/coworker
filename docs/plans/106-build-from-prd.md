# Plan 106 — `build-from-prd` Manifest and DAG Scheduler

**Flavor:** Runtime
**Blocks on:** 101, 102
**Parallel-safe with:** 103, 104, 105, 107, 114
**Branch:** `feature/plan-106-build-from-prd`
**Manifest entry:** `docs/specs/001-plan-manifest.md` §106

---

## Goal

First autopilot milestone: plan manifest schema, DAG scheduling, and git worktree management. Full phase execution (developer → reviewer → tester loop) is deferred to Plan 114. This plan delivers the scheduling and workspace infrastructure that Plan 114 and Plan 115 will build on.

---

## Architecture

```
coding/
├── manifest/
│   ├── schema.go          # PlanManifest, PlanEntry structs + slug helper
│   ├── loader.go          # LoadManifest — reads + validates a YAML file
│   ├── loader_test.go
│   ├── scheduler.go       # DAGScheduler — pure logic, no store imports
│   ├── scheduler_test.go
│   ├── worktree.go        # WorktreeManager — shells out to git worktree
│   └── worktree_test.go
├── roles/
│   ├── architect.yaml     # NEW
│   └── planner.yaml       # NEW
├── prompts/
│   ├── architect.md       # NEW
│   └── planner.md         # NEW
└── workflow/
    ├── build_from_prd.go      # BuildFromPRDWorkflow skeleton
    └── build_from_prd_test.go
```

### Key design decisions

- **DAGScheduler is pure logic.** No imports from `store/`. Takes a `*PlanManifest` and a `map[int]bool` of completed plan IDs; returns the slice of `PlanEntry` values that are unblocked and not yet started or completed. This makes it trivially unit-testable with no database.
- **WorktreeManager shells out.** Uses `os/exec` + `exec.CommandContext` to call `git worktree add` and `git worktree remove`. Returns typed errors for common failure modes (branch already exists, worktree already exists). Respects the spec's lifecycle: create at plan-start, keep after ship, remove via `coworker cleanup`.
- **BuildFromPRDWorkflow is a scaffold.** Implements `core.Workflow`. Loads the manifest, asks the scheduler which plans are ready, and creates worktrees for parallel plans. The architect/planner dispatch loop is a stub that will be fleshed out by Plan 114.
- **Branch naming:** `feature/plan-{id}-{slug}` where slug = title lowercased, spaces → hyphens, non-alphanumeric stripped, max 40 chars.
- **Worktree path:** `.coworker/worktrees/plan-{id}-{slug}/`

---

## Phases

### Phase 1 — Plan manifest schema + loader + validator

Files: `coding/manifest/schema.go`, `coding/manifest/loader.go`, `coding/manifest/loader_test.go`

Structs:

```go
type PlanManifest struct {
    SpecPath string      `yaml:"spec_path"`
    Plans    []PlanEntry `yaml:"plans"`
}

type PlanEntry struct {
    ID       int      `yaml:"id"`
    Title    string   `yaml:"title"`
    Phases   []string `yaml:"phases"`
    BlocksOn []int    `yaml:"blocks_on"`
}
```

Validation rules:
- `spec_path` is required (non-empty)
- `plans` must be non-empty
- each plan must have `id > 0` and non-empty `title`
- `blocks_on` IDs must reference IDs present in the manifest
- plan IDs must be unique

Slug function: `PlanSlug(title string) string` — lowercased, spaces to hyphens, non-`[a-z0-9-]` stripped, collapsed repeated hyphens, max 40 chars, no leading/trailing hyphens.

Branch function: `BranchName(id int, title string) string` — `"feature/plan-{id}-{slug}"`.

### Phase 2 — Architect role YAML + prompt

Files: `coding/roles/architect.yaml`, `coding/prompts/architect.md`

Role inputs: `prd_path`, `repo_context` (optional). Outputs: spec path + plan manifest path. CLI: codex (deep-think). Sandbox: read-only + write `docs/specs/` and `docs/plans/`. Concurrency: single.

### Phase 3 — Planner role YAML + prompt

Files: `coding/roles/planner.yaml`, `coding/prompts/planner.md`

Role inputs: `spec_path`, `plan_skeleton` (the plan entry from the manifest). Outputs: detailed plan file at `docs/plans/{NNN}-{slug}.md`. CLI: claude-code. Sandbox: read-only + write `docs/plans/`. Concurrency: single.

### Phase 4 — DAG scheduler

File: `coding/manifest/scheduler.go`, `coding/manifest/scheduler_test.go`

```go
type DAGScheduler struct {
    Manifest *PlanManifest
    Policy   *core.Policy
}

// ReadyPlans returns plans whose block_on deps are all in completed,
// and which are not themselves in completed or active.
func (s *DAGScheduler) ReadyPlans(completed map[int]bool, active map[int]bool) []PlanEntry

// MaxParallelPlans returns the effective concurrency cap from policy.
func (s *DAGScheduler) MaxParallelPlans() int
```

`ReadyPlans` logic:
1. For each plan in `manifest.Plans`, skip if `completed[plan.ID]` or `active[plan.ID]`.
2. Check all `plan.BlocksOn` IDs are in `completed`. If any is not, skip.
3. Append to ready list.
4. Return up to `MaxParallelPlans() - len(active)` entries (so we don't schedule more than the cap allows in total).

### Phase 5 — Worktree creation

File: `coding/manifest/worktree.go`, `coding/manifest/worktree_test.go`

```go
type WorktreeManager struct {
    RepoRoot string // absolute path to the git repo root
    BaseDir  string // absolute path to .coworker/worktrees/
}

// Create creates the feature branch and git worktree for a plan.
// Returns the absolute worktree path.
// If the worktree already exists, returns its path without error.
func (m *WorktreeManager) Create(ctx context.Context, planID int, title, baseBranch string) (string, error)

// Remove removes the worktree and deletes the feature branch.
// Safe to call even if the worktree no longer exists.
func (m *WorktreeManager) Remove(ctx context.Context, planID int, title string) error

// WorktreePath returns the expected worktree path for a plan (does not create it).
func (m *WorktreeManager) WorktreePath(planID int, title string) string
```

Implementation: shells out to `git -C <repoRoot> worktree add <path> -b <branch> <base>`. On `git worktree remove`, passes `--force` so stale index locks don't block cleanup.

### Phase 6 — Per-plan branch management

Covered by `WorktreeManager.Create`: the branch `feature/plan-{id}-{slug}` is created atomically with the worktree. Non-blocking rebase policy is documented in the worktree README (not enforced programmatically in this plan — that is Plan 114/115 scope).

### Phase 7 — BuildFromPRDWorkflow skeleton

Files: `coding/workflow/build_from_prd.go`, `coding/workflow/build_from_prd_test.go`

Implements `core.Workflow`. Fields: manifest path, policy, worktree manager. On `Run`, loads manifest, constructs scheduler, creates worktrees for the first ready batch. Full phase loop is a TODO stub.

### Phase 8 — Tests

- `loader_test.go`: valid YAML round-trip; missing `spec_path`; empty plans; duplicate IDs; invalid `blocks_on` reference; unknown extra fields ignored.
- `scheduler_test.go`: empty completed set returns all root plans; blocking deps respected; cap from policy applied; all-completed returns empty; single-dep chain; diamond dependency.
- `worktree_test.go`: Create + path existence; Remove removes path; idempotent Create; WorktreePath returns correct value without creating anything. (Uses `t.TempDir()` + `git init` to create a throwaway repo.)

---

## Testing checklist

- [ ] `go build ./...` passes
- [ ] `go test ./coding/manifest/... -count=1 -timeout 60s` passes
- [ ] `go test ./coding/workflow/... -count=1 -timeout 60s` passes
- [ ] `go test ./... -count=1 -timeout 60s` passes (no regressions)
- [ ] `golangci-lint run ./...` passes

---

## Code Review

Reviewed by author after implementation.

**`coding/manifest/schema.go`**
- `PlanSlug` uses compiled regexps as package-level vars — correct approach for avoiding per-call compilation. [PASS]
- `Validate` is pure and has no side effects. Validation errors include field indices and IDs for fast debugging. [PASS]
- `BranchName` and `WorktreeDirName` are trivial derivations — clear, testable. [PASS]

**`coding/manifest/loader.go`**
- `LoadManifest` and `ParseManifest` are cleanly separated so embedded-YAML callers (tests, future spec replay) don't need a file. [PASS]

**`coding/manifest/scheduler.go`**
- `ReadyPlans` is pure: no I/O, no store imports. Fully exercised by table-driven unit tests. [PASS]
- Slot calculation (`cap - len(active)`) is correct: respects MaxParallelPlans across running + newly-ready. [PASS]
- `allCompleted` iterates slice, not map — appropriate since `blocks_on` is typically ≤ 5 entries. [PASS]

**`coding/manifest/worktree.go`**
- `Create` is idempotent: re-stat before shelling out, return early if path already exists. [PASS]
- Branch-already-exists fallback uses `git worktree add <path> <branch>` (no -b) to attach to the existing branch. [PASS]
- `Remove` calls `git worktree prune` (best-effort) after removal to keep the git registry clean. [PASS]
- `isBranchNotFoundError` uses string matching on stderr; this is acceptable for a local git subprocess. [PASS]

**`coding/workflow/build_from_prd.go`**
- `PrepareWorktrees` skips worktree creation for single-plan runs and when no manager is set — matches spec §Workspace model. [PASS]
- `Run` returns a `BuildFromPRDResult` rather than inline maps — clean for Plan 114 to extend. [PASS]
- Full phase loop (developer → reviewer → shipper) is explicitly stubbed as TODO(Plan 114). [PASS]

**Role YAMLs / prompts**
- `architect.yaml` and `planner.yaml` follow the same structure as `reviewer_arch.yaml`. [PASS]
- Prompts use `{{.PrdPath}}` / `{{.SpecPath}}` template fields consistent with how the dispatcher renders prompts. [PASS]

No open items.

---

## Post-Execution Report

**Shipped:** 2026-04-20 on `feature/plan-106-build-from-prd`.

**Phases completed:**
1. Plan manifest schema (`PlanManifest`, `PlanEntry`, `PlanSlug`, `BranchName`, `WorktreeDirName`, `Validate`) — `coding/manifest/schema.go`
2. Manifest loader (`LoadManifest`, `ParseManifest`) — `coding/manifest/loader.go`
3. DAG scheduler (`DAGScheduler`, `ReadyPlans`, `MaxParallelPlans`) — `coding/manifest/scheduler.go`
4. Worktree manager (`WorktreeManager`, `Create`, `Remove`, `WorktreePath`) — `coding/manifest/worktree.go`
5. `BuildFromPRDWorkflow` scaffold (`LoadManifest`, `Schedule`, `PrepareWorktrees`, `Run`) — `coding/workflow/build_from_prd.go`
6. Architect role YAML + prompt — `coding/roles/architect.yaml`, `coding/prompts/architect.md`
7. Planner role YAML + prompt — `coding/roles/planner.yaml`, `coding/prompts/planner.md`

**Tests:** 27 tests in `coding/manifest` + 7 in `coding/workflow` (build-from-prd). Full suite: 0 failures, 0 regressions.

**Linting:** `golangci-lint run ./...` — 0 issues.

**Deferred to Plan 114:** architect/planner dispatch loop, developer → reviewer → tester phase loop, `fix_cycles`, `phase-clean` checkpoint.

**Key decisions:**
- `DAGScheduler` has zero store imports — pure logic, no database required for scheduling tests.
- `WorktreeManager.Create` is idempotent: stat-before-shell-out prevents duplicate `git worktree add` calls on retry.
- `ParseManifest` is exported separately from `LoadManifest` so embedded YAML (tests, replay) works without filesystem access.
- `PlanSlug` max-40-char truncation trims trailing hyphens to avoid branch names like `feature/plan-100-very-long-`.

**No architectural decisions require updating `docs/architecture/decisions.md`** — this plan introduces no new cross-cutting invariants; it builds on existing patterns (os/exec subprocess, pure-logic modules, yaml loader pattern).
