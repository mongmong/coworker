# Plan 117 — V1 Correctness Gaps

**Status:** Draft  
**Flavor:** Runtime  
**Branch:** `feature/plan-117-correctness-gaps`  
**Blocks on:** 116 (V1 Wiring + Blocker Hardening — must be complete so the runtime wires are in place)  
**Manifest entry:** add to `docs/specs/001-plan-manifest.md` under Phase E after Plan 116

---

## Goal

Fix eight Important-severity findings from `docs/reviews/2026-04-26-v1-comprehensive-review.md`: I-1 through I-5, I-7, I-9, and I-10. After this plan:

- SQLite triggers enforce finding immutability at the database layer (I-1).
- The tester role is never silently dropped when a `StageRegistry` is in use (I-2).
- Role-level `applies_when` is parsed, loaded, and evaluated before dispatch (I-3).
- Supervisor engine errors fail jobs instead of silently passing them (I-4).
- Quality judge errors emit a `quality.verdict` event with `status=error` rather than being swallowed (I-5).
- Subprocess timeouts are derived from role budget `max_wallclock_minutes` across the agent, judge, worktree, human-edit, and shipper call sites (I-7).
- Per-job JSONL logs are persisted to `.coworker/runs/<run-id>/jobs/<job-id>.jsonl` while streaming (I-9).
- `ListEvents` correctly parses and returns `CreatedAt` timestamps instead of leaving them as zero (I-10).

Findings I-6, I-8, I-11, and all Polish items are out of scope (I-6 depends on Plan 116's HTTP server; I-8 requires OpenCode HTTP dispatch; I-11 is a large schema migration best handled separately).

---

## Architecture

### What changes

| Finding | Component | Before | After |
|---|---|---|---|
| I-1 | `store/migrations/006_findings_immutability.sql` | No triggers; direct UPDATE is permitted on any column | Triggers abort UPDATE on `path`, `line`, `severity`, `body`, `fingerprint`; only `resolved_by_job_id` / `resolved_at` may be set |
| I-2 | `coding/phaseloop/executor.go`, `coding/workflow/build_from_prd.go` | `ReviewerRoles` holds only `phase-review` list; tester dropped when registry is set | New `TesterRoles []string` field in `PhaseExecutor`; `RunPhasesForPlan` populates both from registry |
| I-3 | `core/role.go`, `coding/roles/loader.go`, `coding/phaseloop/executor.go` | `applies_when` is present in YAML but `core.Role` has no field; phase loop dispatches all roles unconditionally | `AppliesWhen` field added to `core.Role`; loader parses it; fan-out evaluates predicate before dispatch; skipped roles emit `job.skipped` |
| I-4 | `coding/dispatch.go` | `Supervisor.Evaluate` error → `Pass: true` | Error → return error, job state `failed` |
| I-5 | `coding/quality/evaluator.go` | Judge errors logged, no event written, continue | `writeVerdictEvent` called with `status=error` before continuing; block-capable judge errors also create attention items |
| I-7 | `agent/cli_agent.go`, `coding/quality/judge.go`, `coding/manifest/worktree.go`, `coding/humanedit/recorder.go`, `coding/shipper/gh.go` | Contexts lack deadlines; `max_wallclock_minutes` unused | `context.WithTimeout` applied at each call site; `cmd.WaitDelay` set for graceful shutdown |
| I-9 | `agent/cli_handle.go` (or new `agent/log_writer.go`) | stdout parsed in memory only; no file written | Raw stream-JSON teed to `.coworker/runs/<run-id>/jobs/<job-id>.jsonl` via `io.MultiWriter` before parsing |
| I-10 | `store/event_store.go` | `createdAtStr` scanned but never parsed; `e.CreatedAt` always zero | `time.Parse` called; `e.CreatedAt` assigned; parse failure returns error |

### Invariants preserved

- **Event log before state update.** All new events use `WriteEventThenRow`. No raw `INSERT` outside a write-then-row flow.
- **No silent state advance.** I-4 fix closes the silent-pass path for supervisor errors. I-5 fix ensures every judge evaluation leaves a durable event record.
- **File artifacts as pointers.** The JSONL log (I-9) is a file on disk; its path is not inlined into SQLite.
- **Findings are immutable.** I-1 trigger adds database-layer enforcement on top of the existing store-layer convention.
- **No naked goroutines.** I-7 timeout wrappers use `context.WithTimeout` with the caller's context as parent; no new goroutines introduced.

### Migration numbering

Migration `006_findings_immutability.sql` is the next in sequence after `005_events_run_id_nullable.sql`. The tiny runner in `store/migrate.go` picks up all files in numerical order.

---

## Tech Stack

- Go stdlib: `context.WithTimeout`, `time.Parse`, `io.MultiWriter`, `os.MkdirAll`, `os.OpenFile`.
- SQLite DDL triggers (`BEFORE UPDATE ... WHEN ...`).
- No new external dependencies.

---

## Phases

### Phase 1 — I-1: Findings immutability via SQLite triggers

**Files to create/modify:**

- `store/migrations/006_findings_immutability.sql` — new file
- `store/finding_store_test.go` — add trigger enforcement test

**Step-by-step actions:**

1. Create `store/migrations/006_findings_immutability.sql` with two triggers:

   ```sql
   -- 006_findings_immutability.sql
   -- Enforce that immutable finding columns cannot be updated after INSERT.
   -- Only resolved_by_job_id and resolved_at may be changed (via ResolveFinding).
   --
   -- Allowlist approach: the trigger fires on ANY UPDATE. It aborts unless
   -- the update touches ONLY the two resolution columns. This is more robust
   -- than a denylist because new columns added in later migrations are
   -- protected by default.

   CREATE TRIGGER IF NOT EXISTS findings_immutable_before_update
   BEFORE UPDATE ON findings
   FOR EACH ROW
   WHEN NOT (
       -- Allow only updates that set resolution fields and leave everything else unchanged.
       NEW.run_id            = OLD.run_id            AND
       NEW.job_id            = OLD.job_id            AND
       NEW.path              = OLD.path              AND
       NEW.line              = OLD.line              AND
       NEW.severity          = OLD.severity          AND
       NEW.body              = OLD.body              AND
       NEW.fingerprint       = OLD.fingerprint
   )
   BEGIN
       SELECT RAISE(ABORT, 'findings: only resolved_by_job_id and resolved_at may be updated after insertion');
   END;
   ```

   The allowlist approach inverts the condition: the trigger aborts unless ALL non-resolution columns remain unchanged. This means `run_id`, `job_id`, `path`, `line`, `severity`, `body`, and `fingerprint` are all protected. Updates that change only `resolved_by_job_id` and/or `resolved_at` satisfy the WHEN clause and are not aborted. Any other column change (including future schema additions) is blocked by default, making this more robust than the original denylist.

2. Verify the migration runner applies `006_` at startup by checking `store/migrate.go` — no code change expected (the runner walks files in order).

**Test plan (`store/finding_store_test.go`):**

- `TestFindingsImmutableTrigger_PathRejected`: insert a finding via `InsertFinding`, then execute raw `UPDATE findings SET path = 'tampered' WHERE id = ?`, assert the error message contains `"immutable"`.
- `TestFindingsImmutableTrigger_SeverityRejected`: same pattern for `severity`.
- `TestFindingsImmutableTrigger_RunIDRejected`: same pattern for `run_id` — verifies the allowlist covers identity columns beyond the five originally listed.
- `TestFindingsImmutableTrigger_JobIDRejected`: same pattern for `job_id`.
- `TestFindingsImmutableTrigger_ResolveAllowed`: insert then call `ResolveFinding` — must succeed (trigger must not block resolution updates).
- `TestFindingsImmutableTrigger_AllImmutableColumns`: iterate all seven protected columns (`run_id`, `job_id`, `path`, `line`, `severity`, `body`, `fingerprint`) in a table-driven test; each direct UPDATE must fail.

---

### Phase 2 — I-2: Stage registry includes tester role

**Files to modify:**

- `coding/phaseloop/executor.go` — add `TesterRoles []string` field; use it in `fanOut`
- `coding/workflow/build_from_prd.go` — populate `TesterRoles` from `StageRegistry.RolesForStage("phase-test")`
- `coding/phaseloop/executor_test.go` — update/add tests

**Step-by-step actions:**

1. Add `TesterRoles []string` to `PhaseExecutor`:

   ```go
   // TesterRoles controls which tester roles are dispatched in parallel
   // alongside ReviewerRoles after each developer job.
   //
   //   nil              → use defaultTesterRoles (["tester"])
   //   non-nil, empty   → tester stage disabled; no tester is dispatched
   //   non-nil, non-empty → dispatch the listed roles
   //
   // This mirrors the nil-vs-empty semantics of ReviewerRoles.
   TesterRoles []string
   ```

   Add the package-level default:

   ```go
   var defaultTesterRoles = []string{"tester"}
   ```

   Remove `"tester"` from `defaultReviewerRoles` so the two lists are cleanly separated:

   ```go
   var defaultReviewerRoles = []string{"reviewer.arch", "reviewer.frontend"}
   ```

2. Add a `testerRoles()` helper mirroring `reviewerRoles()`. Use pointer-comparison semantics to distinguish nil from empty:

   ```go
   func (e *PhaseExecutor) testerRoles() []string {
       if e.TesterRoles == nil {
           return defaultTesterRoles
       }
       // Non-nil but empty → tester disabled; return nil so fanOut skips it.
       return e.TesterRoles
   }
   ```

3. In `fanOut`, concatenate the two lists before building the errgroup. A nil return from `testerRoles()` is safe because `append` ignores nil slices:

   ```go
   roles := append(e.reviewerRoles(), e.testerRoles()...)
   ```

4. In `RunPhasesForPlan` (`coding/workflow/build_from_prd.go`), after populating `ReviewerRoles`, add:

   ```go
   if w.StageRegistry != nil {
       if roles := w.StageRegistry.RolesForStage("phase-review"); roles != nil {
           w.PhaseExecutor.ReviewerRoles = roles
       }
       // Assign directly — preserves nil-vs-empty distinction.
       // RolesForStage returns nil when the stage is not registered at all,
       // and an empty non-nil slice when registered with no roles (disabled).
       w.PhaseExecutor.TesterRoles = w.StageRegistry.RolesForStage("phase-test")
   }
   ```

   Note: `RolesForStage("phase-test")` returns `nil` when the stage is not registered (no override; default applies) and returns an empty non-nil slice when explicitly registered with no roles (tester disabled). The direct assignment propagates both cases correctly.

**Test plan:**

- `TestRunPhasesForPlan_DefaultRegistryIncludesTester`: create a `BuildFromPRDWorkflow` with the default `StageRegistry`; run `RunPhasesForPlan`; assert that the stub dispatcher received dispatch calls for `reviewer.arch`, `reviewer.frontend`, and `tester` (not just the two reviewer roles).
- `TestPhaseExecutor_FanOutIncludesTester`: unit test on `fanOut` with a mock dispatcher; assert tester is in the dispatched role list.
- `TestPhaseExecutor_CustomTesterRoles`: set `TesterRoles = ["custom.tester"]` and assert the custom role is dispatched, not the default.
- `TestPhaseExecutor_TesterDisabled_EmptyNonNilSlice`: set `TesterRoles = []string{}` (non-nil empty); assert no tester dispatch call is made.
- `TestRunPhasesForPlan_PhaseTestEmpty_DisablesTester`: register `phase-test: []` in a `StageRegistry`; run `RunPhasesForPlan`; assert no dispatch call for any tester role.
- `TestPhaseExecutor_TesterNil_UsesDefault`: set `TesterRoles = nil` explicitly; assert dispatch call for `"tester"` (the default).

---

### Phase 3 — I-3: Role-level `applies_when`

**Files to modify/create:**

- `core/role.go` — add `AppliesWhen *RoleAppliesWhen`
- `internal/predicates/changes_touch.go` — new shared package for glob matching logic
- `internal/predicates/changes_touch_test.go` — unit tests for shared predicate
- `coding/supervisor/predicates.go` — migrate existing `changes_touch` implementation to call `internal/predicates`
- `coding/roles/loader.go` — add validation for `applies_when`
- `coding/phaseloop/executor.go` — add `WorkDir`, `RoleDir` fields; evaluate `AppliesWhen` in `fanOut`; emit `job.skipped`
- `coding/workflow/build_from_prd.go` — add `WorkDir string` field; propagate to `PhaseExecutor`
- `cli/run.go` — populate `BuildFromPRDWorkflow.WorkDir` from cwd
- `cli/daemon.go` — populate `BuildFromPRDWorkflow.WorkDir` from cwd
- `core/event_kinds.go` (or wherever event kind constants live) — add `EventJobSkipped`
- `coding/phaseloop/executor_test.go` — add applies_when tests
- `coding/roles/loader_test.go` — add YAML parse test

**Step-by-step actions:**

#### 3a. Shared predicate package

Create `internal/predicates/changes_touch.go` with the canonical `ChangesTouch` implementation. This is a migration-first step: extract the logic that already exists in `coding/supervisor/predicates.go` so both the supervisor and the new role-level fan-out use a single source of truth.

```go
// Package predicates provides shared git-diff predicate functions used by
// both the supervisor rule engine and the phase-loop role fan-out.
package predicates

import (
    "context"
    "fmt"
    "os/exec"
    "path/filepath"
    "strings"
)

// ChangesTouch reports whether the committed diff (HEAD~1..HEAD, with an
// initial-commit fallback to the empty tree) touches at least one file
// matching any of the given glob patterns.
//
// Pattern semantics:
//   - Patterns containing slashes are matched against the full repo-relative
//     path using filepath.Match (e.g. "web/**", "src/*.go").
//   - Slash-free patterns (e.g. "*.tsx") are matched against both the full
//     path AND the basename, so "*.tsx" matches "web/app/Page.tsx".
//   - The double-star segment "**" is supported via Go's filepath.Match only
//     on platforms where it is expanded by the shell; for cross-platform
//     correctness, callers should use single-star patterns for single-dir
//     traversal. Callers that need recursive matching should use the
//     full-path form and check filepath.Match against each path segment.
//
// workDir must be the root of a git repository. If workDir is empty the
// function returns false, nil (safe default: do not filter).
func ChangesTouch(ctx context.Context, workDir string, patterns []string) (bool, error) {
    if workDir == "" || len(patterns) == 0 {
        return false, nil
    }

    files, err := changedFiles(ctx, workDir)
    if err != nil {
        return false, err
    }

    for _, file := range files {
        if file == "" {
            continue
        }
        for _, pattern := range patterns {
            if matchesPattern(pattern, file) {
                return true, nil
            }
        }
    }
    return false, nil
}

// changedFiles returns the list of files changed in HEAD~1..HEAD.
// Falls back to all files in HEAD when the repo has only one commit.
func changedFiles(ctx context.Context, workDir string) ([]string, error) {
    out, err := runGit(ctx, workDir, "diff", "--name-only", "HEAD~1..HEAD")
    if err != nil {
        // Initial commit: HEAD~1 does not exist.
        out, err = runGit(ctx, workDir, "diff-tree", "--no-commit-id", "-r", "--name-only", "HEAD")
        if err != nil {
            return nil, fmt.Errorf("git diff for changes_touch: %w", err)
        }
    }
    return strings.Split(strings.TrimSpace(out), "\n"), nil
}

func runGit(ctx context.Context, workDir string, args ...string) (string, error) {
    cmd := exec.CommandContext(ctx, "git", args...)
    cmd.Dir = workDir
    out, err := cmd.Output()
    return string(out), err
}

// matchesPattern returns true when file matches pattern using filepath.Match.
// For slash-free patterns it also checks a basename match.
func matchesPattern(pattern, file string) bool {
    if matched, _ := filepath.Match(pattern, file); matched {
        return true
    }
    // For patterns without slashes, also match against the basename.
    if !strings.Contains(pattern, "/") {
        if matched, _ := filepath.Match(pattern, filepath.Base(file)); matched {
            return true
        }
    }
    return false
}
```

#### 3b. Migrate supervisor's changes_touch to call the shared package

In `coding/supervisor/predicates.go`, replace the inline git-diff logic for `changes_touch` with a call to `internal/predicates.ChangesTouch`. The existing function signature and error-handling behaviour remain unchanged; only the implementation body is swapped.

```go
// Before (inline in supervisor):
func evalChangesTouch(workDir string, patterns []string) (bool, error) { ... }

// After:
import "github.com/<org>/coworker/internal/predicates"

func evalChangesTouch(ctx context.Context, workDir string, patterns []string) (bool, error) {
    return predicates.ChangesTouch(ctx, workDir, patterns)
}
```

This migration must be completed and tested before the role fan-out in step 3d uses the same package. Add a test in `coding/supervisor/predicates_test.go` that exercises `changes_touch` to confirm behaviour is preserved.

#### 3c. WorkDir wiring: cwd → workflow → executor

**`coding/workflow/build_from_prd.go`** — add `WorkDir string` to `BuildFromPRDWorkflow`:

```go
type BuildFromPRDWorkflow struct {
    // ... existing fields ...

    // WorkDir is the repository root passed to PhaseExecutor for
    // applies_when git predicate evaluation. Populated by callers from
    // the current working directory at startup.
    // When a per-plan worktree exists, RunPhasesForPlan overrides
    // PhaseExecutor.WorkDir with the worktree path for that plan.
    WorkDir string
}
```

In `RunPhasesForPlan`, before calling `phaseExecutor.Execute(...)`:

```go
// Wire WorkDir: prefer the per-plan worktree path when one exists;
// fall back to the workflow-level WorkDir (repo root).
phaseExecutor.WorkDir = w.WorkDir
if worktreePath := w.worktreePathForPlan(plan); worktreePath != "" {
    phaseExecutor.WorkDir = worktreePath
}
phaseExecutor.RoleDir = w.RoleDir // RoleDir already wired; confirm it is set here
```

(`worktreePathForPlan` is a helper that looks up the worktree for the plan if `PrepareWorktrees` has been called; returns empty string if no worktree exists.)

**`cli/run.go`** — after constructing `BuildFromPRDWorkflow`, set `WorkDir`:

```go
cwd, err := os.Getwd()
if err != nil {
    return fmt.Errorf("get working directory: %w", err)
}
wf.WorkDir = cwd
```

**`cli/daemon.go`** — same pattern:

```go
cwd, err := os.Getwd()
if err != nil {
    return fmt.Errorf("get working directory: %w", err)
}
wf.WorkDir = cwd
```

Data flow summary:
```
os.Getwd()
  → cli/run.go or cli/daemon.go
  → BuildFromPRDWorkflow.WorkDir
  → RunPhasesForPlan: phaseExecutor.WorkDir = w.WorkDir (or worktree override)
  → PhaseExecutor.WorkDir
  → roleShouldDispatch(role, e.WorkDir)
  → internal/predicates.ChangesTouch(ctx, workDir, patterns)
```

#### 3d. Role struct and loader

In `core/role.go` add:

```go
// RoleAppliesWhen declares the condition under which a role should be
// dispatched. When nil, the role is always dispatched.
type RoleAppliesWhen struct {
    // ChangesTouch is a list of glob patterns. The role is dispatched only
    // when the committed diff touches at least one file matching a pattern.
    // Uses the same semantics as the supervisor changes_touch predicate
    // (HEAD~1..HEAD, initial-commit fallback, basename matching for
    // slash-free patterns).
    ChangesTouch []string `yaml:"changes_touch,omitempty"`
}

// In Role:
AppliesWhen *RoleAppliesWhen `yaml:"applies_when,omitempty"`
```

`loader.go` requires no Go changes — `yaml.Unmarshal` will populate the new field automatically. Add a validation check: if `AppliesWhen` is non-nil and `ChangesTouch` is empty, return an error (`applies_when declared but no predicates specified`).

Update `validateRole`:

```go
if role.AppliesWhen != nil && len(role.AppliesWhen.ChangesTouch) == 0 {
    return fmt.Errorf("applies_when declared but contains no predicates")
}
```

#### 3e. PhaseExecutor fan-out with applies_when

In `coding/phaseloop/executor.go`, add a helper that calls `internal/predicates.ChangesTouch`:

```go
// roleShouldDispatch returns false when the role declares applies_when
// and the condition evaluates to false. workDir is the repo root (or
// per-plan worktree) for git predicate evaluation. When workDir is empty
// the check is skipped and the role is always dispatched.
func roleShouldDispatch(ctx context.Context, role *core.Role, workDir string) (bool, error) {
    if role.AppliesWhen == nil || len(role.AppliesWhen.ChangesTouch) == 0 {
        return true, nil
    }
    if workDir == "" {
        return true, nil
    }
    return predicates.ChangesTouch(ctx, workDir, role.AppliesWhen.ChangesTouch)
}
```

Add fields to `PhaseExecutor`:

```go
// WorkDir is the repository root (or per-plan worktree path) used for
// applies_when git predicate evaluation. When empty, applies_when is
// always true (safe default). Set by RunPhasesForPlan before each Execute.
WorkDir string

// RoleDir is the directory containing role YAML files. Used to load
// role metadata for applies_when evaluation. When empty, applies_when
// is always true.
RoleDir string
```

In `fanOut`, wrap each role dispatch:

```go
g.Go(func() error {
    role, loadErr := roles.LoadRole(e.RoleDir, roleName)
    if loadErr != nil {
        // Role file missing — log and skip, don't abort the whole fan-out.
        log.Warn("could not load role for applies_when check", "role", roleName, "error", loadErr)
    } else if role.AppliesWhen != nil {
        should, evalErr := roleShouldDispatch(ctx, role, e.WorkDir)
        if evalErr != nil {
            log.Warn("applies_when evaluation error, dispatching anyway", "role", roleName, "error", evalErr)
        } else if !should {
            log.Info("role skipped by applies_when", "role", roleName)
            e.emitJobSkippedEvent(ctx, runID, roleName)
            results[i] = &coding.DispatchResult{} // empty result, no findings
            return nil
        }
    }
    // ... normal dispatch ...
})
```

Add `EventJobSkipped = EventKind("job.skipped")` to `core/event_kinds.go`. Emit a minimal event in `emitJobSkippedEvent`:

```go
func (e *PhaseExecutor) emitJobSkippedEvent(ctx context.Context, runID, roleName string) {
    if e.EventStore == nil {
        return
    }
    payload, _ := json.Marshal(map[string]string{"role": roleName, "reason": "applies_when=false"})
    event := &core.Event{
        ID:        core.NewID(),
        RunID:     runID,
        Kind:      core.EventJobSkipped,
        Payload:   string(payload),
        CreatedAt: time.Now(),
    }
    if err := e.EventStore.WriteEventThenRow(ctx, event, nil); err != nil {
        e.logger().Error("failed to write job.skipped event", "error", err)
    }
}
```

**Test plan:**

- `TestChangesTouch_MatchesFullPath`: temp git repo; commit a file at `web/index.tsx`; call `ChangesTouch` with `["web/**"]`; assert true.
- `TestChangesTouch_MatchesBasename`: same repo; call with `["*.tsx"]`; assert true (basename match).
- `TestChangesTouch_NoMatch`: call with `["api/**"]`; assert false.
- `TestChangesTouch_InitialCommitFallback`: repo with only one commit; assert `ChangesTouch` does not error.
- `TestLoadRole_AppliesWhen_Parsed`: write a temp YAML with `applies_when.changes_touch: ["web/**"]`; call `LoadRole`; assert `role.AppliesWhen.ChangesTouch == ["web/**"]`.
- `TestLoadRole_AppliesWhen_EmptyPredicatesRejected`: YAML with `applies_when: {}` (no patterns); assert validation error.
- `TestPhaseExecutor_FanOut_SkipsRoleWhenAppliesWhenFalse`: set `WorkDir` to a temp git repo where the diff touches no web files; assert reviewer.frontend is not dispatched and a `job.skipped` event is emitted.
- `TestPhaseExecutor_FanOut_DispatchesRoleWhenAppliesWhenTrue`: diff touches `web/index.tsx`; assert reviewer.frontend is dispatched.
- `TestPhaseExecutor_FanOut_SkipDoesNotBlockOtherRoles`: reviewer.frontend skipped but reviewer.arch and tester still run.
- `TestPhaseExecutor_WorkDir_EmptyAlwaysDispatches`: `WorkDir = ""`; role has `applies_when`; assert role is dispatched (safe default when wiring is absent).

---

### Phase 4 — I-4: Supervisor errors don't silently pass jobs

**Files to modify:**

- `coding/dispatch.go` — introduce `SupervisorEvaluator` interface; change supervisor error handling in `executeAttempt`; mark run failed on supervisor error
- `coding/dispatch_test.go` — add error-path test using stub via interface

**Step-by-step actions:**

1. Define a narrow `SupervisorEvaluator` interface in `coding/dispatch.go` so tests can inject a stub without depending on the concrete `*supervisor.RuleEngine`:

   ```go
   // SupervisorEvaluator is the interface Dispatcher uses for contract checks.
   // The concrete *supervisor.RuleEngine satisfies this interface.
   // A nil SupervisorEvaluator skips all checks (equivalent to all-pass).
   type SupervisorEvaluator interface {
       Evaluate(ctx context.Context, evalCtx *supervisor.EvalContext, roleName string) (*core.SupervisorVerdict, error)
   }
   ```

   Change `Dispatcher.Supervisor` from `*supervisor.RuleEngine` to `SupervisorEvaluator`:

   ```go
   // Supervisor is the optional contract-check evaluator. If nil, no checks
   // are performed. If Evaluate returns an error, the job and run are marked
   // failed — errors are never silently converted to a pass.
   Supervisor SupervisorEvaluator
   ```

   Verify that `*supervisor.RuleEngine.Evaluate` matches this signature. If the existing method takes only `evalCtx` and `roleName` without a `ctx`, add `ctx context.Context` as the first parameter in the interface and update the concrete method accordingly. The exact signature must match what `RuleEngine.Evaluate` already exposes — check `coding/supervisor/engine.go` during implementation.

2. In `executeAttempt` (around line 390–396), replace the silent-pass path:

   ```go
   verdict, evalErr := d.Supervisor.Evaluate(ctx, evalCtx, roleName)
   if evalErr != nil {
       logger.Error("supervisor evaluation error — failing job and run", "error", evalErr)
       // Mark the job failed.
       _ = jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed)
       // Mark the run failed before returning so the run row is not left
       // in an active state when Orchestrate returns early.
       _ = runStore.CompleteRun(ctx, runID, core.RunStateFailed)
       return nil, fmt.Errorf("supervisor evaluation: %w", evalErr)
   }
   ```

   `Orchestrate` returns immediately when `executeAttempt` errors. Without the explicit `CompleteRun` call here, the run row would remain in an active/running state. The explicit call before the return closes that gap and satisfies the "no silent state advance" invariant.

3. Update the comment on `Supervisor` in `Dispatcher` to document the new behavior (already shown above in step 1).

**Test plan:**

- `TestDispatcher_SupervisorError_FailsJob`: configure a `Dispatcher` with a stub `SupervisorEvaluator` that always returns `(nil, errors.New("engine exploded"))`; call `Orchestrate`; assert:
  (a) `Orchestrate` returns an error.
  (b) The job row in the DB has state `failed` — query via `jobStore.GetJob`.
  (c) The run row in the DB has state `failed` — query via `runStore.GetRun`. ← new assertion
- `TestDispatcher_SupervisorNil_SucceedsWithoutCheck`: `Dispatcher.Supervisor == nil`; `Orchestrate` with a passing agent; assert `DispatchResult` is non-nil and no error.

The stub supervisor satisfies `SupervisorEvaluator` in the test file without importing `coding/supervisor`:

```go
type stubSupervisor struct{ err error }

func (s *stubSupervisor) Evaluate(
    ctx context.Context,
    evalCtx *supervisor.EvalContext,
    roleName string,
) (*core.SupervisorVerdict, error) {
    return nil, s.err
}
```

---

### Phase 5 — I-5: Quality judge errors emit verdict event

**Files to modify:**

- `coding/quality/evaluator.go` — modify the judge-error branch in `EvaluateAtCheckpoint`; extend `writeVerdictEvent` to accept an optional `status` field
- `coding/quality/evaluator_test.go` — add judge-error test asserting `status: "error"` in event payload

**Step-by-step actions:**

1. Extend `writeVerdictEvent` to include a `status` field in the event JSON payload. The `Verdict` struct is not changed (no type pollution). Instead, `writeVerdictEvent` accepts an extra `status string` parameter and injects it into the JSON it constructs:

   ```go
   // writeVerdictEvent writes a quality.verdict event. status is one of
   // "pass", "fail", or "error". For normal evaluation paths, callers pass
   // "pass" or "fail" derived from Verdict.Pass. For judge-error paths,
   // callers pass "error" to distinguish evaluation failure from a real fail.
   func (e *Evaluator) writeVerdictEvent(
       ctx context.Context,
       runID, jobID string,
       rule QualityRule,
       verdict *Verdict,
       status string,
   ) {
       payload := map[string]any{
           "rule_name":  rule.Name,
           "category":   string(rule.Category),
           "severity":   string(rule.Severity),
           "pass":       verdict.Pass,
           "findings":   verdict.Findings,
           "confidence": verdict.Confidence,
           "status":     status, // "pass" | "fail" | "error"
       }
       // ... marshal to JSON and call EventStore.WriteEventThenRow ...
   }
   ```

   Update all existing callers of `writeVerdictEvent` to pass `"pass"` or `"fail"` based on `verdict.Pass`:

   ```go
   statusStr := "fail"
   if verdict.Pass {
       statusStr = "pass"
   }
   e.writeVerdictEvent(ctx, cpCtx.RunID, cpCtx.JobID, rule, verdict, statusStr)
   ```

2. In `EvaluateAtCheckpoint`, change the judge-error path (currently around lines 88–93):

   ```go
   verdict, err := e.Judge.Evaluate(ctx, rule, diff, jobContext)
   if err != nil {
       logger.Error("quality judge error", "rule", rule.Name, "error", err)
       // Emit a durable verdict event with status="error" so downstream
       // consumers can distinguish evaluation failure from a real rule finding.
       errorVerdict := &Verdict{
           Pass:     false,
           Category: string(rule.Category),
           Findings: []string{fmt.Sprintf("judge error: %v", err)},
       }
       e.writeVerdictEvent(ctx, cpCtx.RunID, cpCtx.JobID, rule, errorVerdict, "error")
       // If the rule is block-capable, create an attention item so the
       // operator knows evaluation failed.
       if IsBlockCapable(rule.Category) {
           itemID, attErr := e.createAttentionItem(ctx, cpCtx, rule, errorVerdict)
           if attErr != nil {
               logger.Error("failed to create attention item for judge error", "rule", rule.Name, "error", attErr)
           } else if itemID != "" {
               result.AttentionItemIDs = append(result.AttentionItemIDs, itemID)
           }
           result.BlockingFindings = append(result.BlockingFindings, Finding{
               RuleName:   rule.Name,
               Category:   rule.Category,
               Findings:   []string{fmt.Sprintf("judge error: %v", err)},
               IsBlocking: true,
           })
           result.Pass = false
       }
       continue
   }
   ```

   This ensures:
   - Every judge error leaves a `quality.verdict` event (durable record) with `status: "error"` in the payload.
   - Downstream consumers can unambiguously distinguish judge evaluation failures from legitimate rule findings.
   - Block-capable rule errors are promoted to blocking findings and attention items, preventing silent checkpoint advance.
   - Advisory rule errors remain non-blocking (log + event only).

**Test plan:**

- `TestEvaluateAtCheckpoint_JudgeError_EmitsVerdictEvent`: use a judge that always returns an error; call `EvaluateAtCheckpoint`; assert a `quality.verdict` event was written with `"pass": false`, `"status": "error"`, and findings containing `"judge error"`. Parse the event payload JSON and check each field.
- `TestEvaluateAtCheckpoint_JudgeError_BlockCapable_CreatesAttention`: rule category is `CategoryCorrectness` (block-capable); judge errors; assert `result.BlockingFindings` non-empty and `result.AttentionItemIDs` non-empty.
- `TestEvaluateAtCheckpoint_JudgeError_Advisory_DoesNotBlock`: rule category is advisory; judge errors; assert `result.Pass == true` and `result.BlockingFindings` empty.
- `TestWriteVerdictEvent_StatusPassFail`: call `writeVerdictEvent` with a passing verdict; assert event payload contains `"status": "pass"`. Repeat with failing verdict; assert `"status": "fail"`.

---

### Phase 6 — I-7: Subprocess timeouts from role budgets

**Files to modify:**

- `agent/cli_agent.go` — apply timeout in `Dispatch`
- `coding/quality/judge.go` — apply timeout in `CLIJudge.Evaluate`
- `coding/manifest/worktree.go` — apply timeout in `git` helper
- `coding/humanedit/recorder.go` — apply timeout in `runGitCommand`
- `coding/shipper/gh.go` — apply timeout in `ghCreatePR`
- `agent/cli_agent_test.go` — add timeout test

The budget field for agents lives on `core.Role.Budget.MaxWallclockMinutes`. Because several call sites do not have direct access to a `core.Role`, a small helper that converts the budget to a `time.Duration` is cleaner than threading `core.Role` everywhere.

**Step-by-step actions:**

1. Add `budgetTimeout` helper in `agent/cli_agent.go` (package-visible only, unexported):

   ```go
   // budgetTimeout returns a context derived from parent with a deadline set
   // to maxMinutes from now. If maxMinutes is zero or negative, the parent
   // context is returned unchanged (no deadline applied).
   // cancel is always non-nil; callers must defer cancel().
   func budgetTimeout(parent context.Context, maxMinutes int) (context.Context, context.CancelFunc) {
       if maxMinutes <= 0 {
           return parent, func() {}
       }
       return context.WithTimeout(parent, time.Duration(maxMinutes)*time.Minute)
   }
   ```

2. In `CliAgent.Dispatch`, accept the budget via a new optional field on `CliAgent`:

   ```go
   type CliAgent struct {
       BinaryPath          string
       Args                []string
       // MaxWallclockMinutes, when > 0, wraps the subprocess context in a
       // deadline of this many minutes. Populated by Dispatcher from role.Budget.
       MaxWallclockMinutes int
   }
   ```

   In `Dispatch`:

   ```go
   func (a *CliAgent) Dispatch(ctx context.Context, job *core.Job, prompt string) (core.JobHandle, error) {
       execCtx, cancel := budgetTimeout(ctx, a.MaxWallclockMinutes)
       cmd := exec.CommandContext(execCtx, a.BinaryPath, a.Args...)
       cmd.WaitDelay = 5 * time.Second
       cmd.Stdin = strings.NewReader(prompt)
       // ...existing pipe setup and cmd.Start()...
       handle := &cliJobHandle{
           cmd:    cmd,
           stdout: stdout,
           stderr: stderr,
           job:    job,
           cancel: cancel, // stored for Cancel()
       }
       return handle, nil
   }
   ```

   Update `cliJobHandle` to store `cancel` and call it in `Wait` (defer) and `Cancel`:

   ```go
   type cliJobHandle struct {
       cmd    *exec.Cmd
       stdout io.ReadCloser
       stderr io.ReadCloser
       job    *core.Job
       cancel context.CancelFunc
   }

   func (h *cliJobHandle) Wait(ctx context.Context) (*core.JobResult, error) {
       defer h.cancel()
       // ...existing body...
   }

   func (h *cliJobHandle) Cancel() error {
       h.cancel()
       if h.cmd.Process == nil {
           return nil
       }
       return h.cmd.Process.Kill()
   }
   ```

3. In `Dispatcher.executeAttempt`, propagate the budget to the agent before dispatch. The `CliAgent` already implements `interface{ BinaryBasename() string }`; add a narrower interface for budget setting:

   ```go
   type budgetSetter interface {
       SetBudget(maxWallclockMinutes int)
   }
   ```

   Add `SetBudget` to `CliAgent`:

   ```go
   func (a *CliAgent) SetBudget(maxWallclockMinutes int) {
       a.MaxWallclockMinutes = maxWallclockMinutes
   }
   ```

   In `executeAttempt`, after selecting `roleAgent`:

   ```go
   if bs, ok := roleAgent.(budgetSetter); ok && role.Budget.MaxWallclockMinutes > 0 {
       bs.SetBudget(role.Budget.MaxWallclockMinutes)
   }
   ```

   Note: this mutates the agent's field. If agents are shared across concurrent calls (they may be in the fan-out), create a shallow copy rather than mutating in place — or carry the timeout in the `core.Job` and apply it inside `CliAgent.Dispatch` from the job. For safety, make `budgetTimeout` accept the minutes directly and pass them via the job:

   ```go
   // Alternative: carry MaxWallclockMinutes on core.Job so no agent mutation.
   // In executeAttempt: job.MaxWallclockMinutes = role.Budget.MaxWallclockMinutes
   // In CliAgent.Dispatch: execCtx, cancel := budgetTimeout(ctx, job.MaxWallclockMinutes)
   ```

   Decision: carry via `core.Job` to avoid mutation hazard. Add `MaxWallclockMinutes int` to `core.Job` (non-persisted, runtime-only).

4. Apply the same `budgetTimeout` pattern to the other four call sites:

   **`coding/quality/judge.go` — `CLIJudge.Evaluate`:**

   Add `MaxWallclockMinutes int` to `CLIJudge`. Evaluator sets it from the rule or a default (e.g., 5 minutes). In `Evaluate`:

   ```go
   execCtx, cancel := budgetTimeout(ctx, j.MaxWallclockMinutes)
   defer cancel()
   cmd := exec.CommandContext(execCtx, binary, "exec", "--json")
   cmd.WaitDelay = 5 * time.Second
   ```

   **`coding/manifest/worktree.go` — `WorktreeManager.git`:**

   Add `MaxWallclockMinutes int` to `WorktreeManager` (default 5 for git commands). In `git`:

   ```go
   execCtx, cancel := budgetTimeout(ctx, m.MaxWallclockMinutes)
   defer cancel()
   cmd := exec.CommandContext(execCtx, "git", args...)
   cmd.WaitDelay = 5 * time.Second
   ```

   **`coding/humanedit/recorder.go` — `runGitCommand`:**

   Extend the function signature:

   ```go
   func runGitCommand(ctx context.Context, maxMinutes int, repoPath string, args ...string) (string, error) {
       execCtx, cancel := budgetTimeout(ctx, maxMinutes)
       defer cancel()
       cmd := exec.CommandContext(execCtx, "git", args...)
       cmd.WaitDelay = 5 * time.Second
       cmd.Dir = repoPath
       // ...
   }
   ```

   The `Recorder` has no role budget; use a hard-coded constant `defaultGitTimeoutMinutes = 5` at the package level.

   **`coding/shipper/gh.go` — `ghCreatePR`:**

   Add a `maxMinutes int` parameter (or a package-level default `defaultGHTimeoutMinutes = 10`):

   ```go
   func ghCreatePR(ctx context.Context, maxMinutes int, branch, title, body string) (string, error) {
       execCtx, cancel := budgetTimeout(ctx, maxMinutes)
       defer cancel()
       cmd := exec.CommandContext(execCtx, "gh", "pr", "create", ...)
       cmd.WaitDelay = 5 * time.Second
       // ...
   }
   ```

   `Shipper.Ship` passes `defaultGHTimeoutMinutes` unless a budget is configured on the role. A `Shipper.MaxWallclockMinutes int` field controls this if needed.

5. Move `budgetTimeout` to `internal/executil/timeout.go` if it needs to be shared across packages without creating import cycles. Otherwise, define it in each package that needs it (small, copy-safe utility).

   Decision: because `agent/`, `coding/quality/`, `coding/manifest/`, `coding/humanedit/`, and `coding/shipper/` all need it and none can import each other, place it in `internal/executil/timeout.go`:

   ```go
   package executil

   import (
       "context"
       "time"
   )

   func BudgetTimeout(parent context.Context, maxMinutes int) (context.Context, context.CancelFunc) {
       if maxMinutes <= 0 {
           return parent, func() {}
       }
       return context.WithTimeout(parent, time.Duration(maxMinutes)*time.Minute)
   }
   ```

**Test plan:**

- `TestBudgetTimeout_ZeroReturnsUnchangedContext`: `BudgetTimeout(ctx, 0)` — deadline not set.
- `TestBudgetTimeout_PositiveAppliesDeadline`: `BudgetTimeout(ctx, 1)` — deadline within 1 minute.
- `TestCliAgent_Dispatch_HonorsTimeout`: create a mock binary that sleeps 10 seconds; set `MaxWallclockMinutes` to a trivially small duration via test shim (or set a very short duration via `context.WithTimeout` in the test); assert `Wait` returns `context.DeadlineExceeded` (or an exit error wrapping it).
- `TestCLIJudge_Evaluate_HonorsTimeout`: same pattern with a slow judge binary.
- Each other call site should have a table-driven test with a cancelled context asserting the operation returns an error promptly.

---

### Phase 7 — I-9: Per-job JSONL log persistence

**Files to create/modify:**

- `agent/log_writer.go` — new file: `JobLogWriter` that tees stream events to disk
- `agent/cli_agent.go` — pass log writer into `Dispatch`
- `agent/cli_handle.go` — use `io.TeeReader` to feed the log writer while parsing
- `agent/log_writer_test.go` — new test file
- `core/job.go` (or equivalent) — may need `RunID` already present on `core.Job` (it is)

**Step-by-step actions:**

1. Create `agent/log_writer.go`:

   ```go
   package agent

   import (
       "fmt"
       "os"
       "path/filepath"
   )

   // JobLogWriter creates and manages the per-job JSONL log file at
   // .coworker/runs/<runID>/jobs/<jobID>.jsonl.
   // Raw stream-JSON bytes are written verbatim to the file before parsing.
   type JobLogWriter struct {
       f *os.File
   }

   // OpenJobLog opens (creating if necessary) the JSONL log file for a job.
   // coworkerDir is the path to the .coworker directory (e.g. "/repo/.coworker").
   // runID and jobID identify the log file path.
   // The caller must call Close() when the job completes.
   func OpenJobLog(coworkerDir, runID, jobID string) (*JobLogWriter, error) {
       dir := filepath.Join(coworkerDir, "runs", runID, "jobs")
       if err := os.MkdirAll(dir, 0o750); err != nil {
           return nil, fmt.Errorf("create job log dir: %w", err)
       }
       path := filepath.Join(dir, jobID+".jsonl")
       f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
       if err != nil {
           return nil, fmt.Errorf("open job log %q: %w", path, err)
       }
       return &JobLogWriter{f: f}, nil
   }

   // Write implements io.Writer; appends raw bytes to the log file.
   func (w *JobLogWriter) Write(p []byte) (int, error) {
       return w.f.Write(p)
   }

   // Close closes the underlying file.
   func (w *JobLogWriter) Close() error {
       return w.f.Close()
   }

   // Path returns the absolute path to the log file for external reference.
   func (w *JobLogWriter) Path() string {
       return w.f.Name()
   }
   ```

2. Add `CoworkerDir string` to `CliAgent`:

   ```go
   type CliAgent struct {
       BinaryPath          string
       Args                []string
       MaxWallclockMinutes int
       // CoworkerDir, when non-empty, causes each dispatched job's stdout to
       // be teed to .coworker/runs/<runID>/jobs/<jobID>.jsonl.
       CoworkerDir string
   }
   ```

3. In `CliAgent.Dispatch`, open the log writer when `CoworkerDir` is set:

   ```go
   var logWriter *JobLogWriter
   if a.CoworkerDir != "" {
       var logErr error
       logWriter, logErr = OpenJobLog(a.CoworkerDir, job.RunID, job.ID)
       if logErr != nil {
           // Log the error but don't fail the dispatch — log persistence
           // is best-effort; the job must proceed regardless.
           _ = logErr // caller's slog is not accessible here; accept the miss
       }
   }

   handle := &cliJobHandle{
       cmd:       cmd,
       stdout:    stdout,
       stderr:    stderr,
       job:       job,
       cancel:    cancel,
       logWriter: logWriter, // may be nil
   }
   ```

4. In `cliJobHandle.Wait`, wrap `h.stdout` with `io.TeeReader` if a log writer is present:

   ```go
   func (h *cliJobHandle) Wait(ctx context.Context) (*core.JobResult, error) {
       defer h.cancel()
       if h.logWriter != nil {
           defer h.logWriter.Close()
       }

       var src io.Reader = h.stdout
       if h.logWriter != nil {
           src = io.TeeReader(h.stdout, h.logWriter)
       }

       decoder := json.NewDecoder(src)
       // ...rest unchanged...
   }
   ```

   Add `logWriter *JobLogWriter` to `cliJobHandle`.

5. Update `cliJobHandle` struct:

   ```go
   type cliJobHandle struct {
       cmd       *exec.Cmd
       stdout    io.ReadCloser
       stderr    io.ReadCloser
       job       *core.Job
       cancel    context.CancelFunc
       logWriter *JobLogWriter // nil when CoworkerDir is not set
   }
   ```

**Test plan (`agent/log_writer_test.go` and `agent/cli_handle_test.go`):**

- `TestOpenJobLog_CreatesDirectoryAndFile`: call `OpenJobLog` with a temp dir; assert the file exists at the expected path.
- `TestOpenJobLog_Write_AppendsBytes`: write two JSON lines; close; assert file contents match.
- `TestCliAgent_Dispatch_WritesJSONLLog`: configure `CoworkerDir` to a temp dir; dispatch a mock CLI binary that emits one `{"type":"finding",...}` line and one `{"type":"done","exit_code":0}` line; assert (a) `Wait` returns the expected finding, (b) the JSONL file at `.coworker/runs/<runID>/jobs/<jobID>.jsonl` contains both raw lines.
- `TestCliAgent_Dispatch_NoLogWhenCoworkerDirEmpty`: `CoworkerDir == ""`; dispatch a mock CLI; assert no `.coworker` directory is created.

---

### Phase 8 — I-10: Event timestamps from SQLite

**Files to modify:**

- `store/event_store.go` — use `time.RFC3339` for both write (`WriteEventThenRow`) and read (`ListEvents`)
- `store/event_store_test.go` — add timestamp round-trip test including an offset timestamp

**Step-by-step actions:**

1. In `WriteEventThenRow` (around line 60), change the format from the literal-Z layout to `time.RFC3339`:

   ```go
   // Before:
   event.CreatedAt.Format("2006-01-02T15:04:05Z"),

   // After:
   event.CreatedAt.UTC().Format(time.RFC3339),
   ```

   Calling `.UTC()` before formatting ensures the stored value is always in UTC with the `+00:00` suffix (or `Z`, both are valid RFC3339). This avoids storing wall-clock local offsets that vary by deployment environment.

2. In `ListEvents` (around lines 126–138), after the `rows.Scan` call, replace the zero-assignment with a proper parse using `time.RFC3339`:

   ```go
   e.Kind = core.EventKind(kindStr)
   t, err := time.Parse(time.RFC3339, createdAtStr)
   if err != nil {
       return nil, fmt.Errorf("parse event %q created_at %q: %w", e.ID, createdAtStr, err)
   }
   e.CreatedAt = t
   events = append(events, e)
   ```

   `time.RFC3339` accepts both `Z` and offset forms (e.g. `+00:00`, `+05:30`), so this correctly handles any compliant timestamp stored by any conformant writer — including values written before this fix that used the literal-`Z` layout.

3. Remove the now-unused `createdAtStr` variable declaration comment, if any.

**Test plan (`store/event_store_test.go`):**

- `TestListEvents_TimestampRoundTrip`: write an event with a known `CreatedAt` (truncated to second precision, UTC); call `ListEvents`; assert `events[0].CreatedAt.Equal(original)` is true.
- `TestListEvents_TimestampWithOffset`: insert a row directly via raw SQL with `created_at = '2026-04-26T15:30:00+00:00'`; assert `ListEvents` returns it without error and `CreatedAt` equals `2026-04-26T15:30:00Z` (same instant).
- `TestListEvents_ZeroTimestampPrevented`: verify that no event returned by `ListEvents` has a zero `CreatedAt` when at least one event exists.
- `TestListEvents_ParseErrorReturned`: insert a row directly with `created_at = 'not-a-date'`; assert `ListEvents` returns an error containing `"parse event"`.
- `TestWriteEventThenRow_UsesRFC3339Format`: write an event; query the raw `created_at` string from SQLite; assert it parses cleanly with `time.Parse(time.RFC3339, ...)` without error.

---

## Self-Review Checklist

- [ ] Migration `006_findings_immutability.sql` uses the allowlist approach (NOT condition) and covers `run_id`, `job_id` in addition to the five data columns.
- [ ] `TesterRoles` nil-vs-empty distinction is enforced: nil → default, empty non-nil → disabled.
- [ ] `TesterRoles` is populated by `RunPhasesForPlan` when a `StageRegistry` is set — verified by integration test. `phase-test: []` disables tester.
- [ ] `internal/predicates/changes_touch.go` is created; `coding/supervisor/predicates.go` calls it instead of duplicating.
- [ ] `WorkDir` data flow is complete: `os.Getwd()` → `cli/run.go` → `BuildFromPRDWorkflow.WorkDir` → `RunPhasesForPlan` → `phaseExecutor.WorkDir` → `roleShouldDispatch`.
- [ ] `applies_when` with unknown predicates (not `changes_touch`) returns a validation error from `LoadRole`, not a silent skip.
- [ ] `SupervisorEvaluator` interface defined; `Dispatcher.Supervisor` uses it; `*supervisor.RuleEngine` satisfies it.
- [ ] Supervisor error propagation test covers: error returned, job state `failed`, run state `failed` (three assertions).
- [ ] `runStore.CompleteRun(ctx, runID, core.RunStateFailed)` is called before returning from `executeAttempt` on supervisor error.
- [ ] Judge error verdict event has `"pass": false`, `"status": "error"`, and `"findings"` non-empty — confirmed by event store spy in test.
- [ ] `writeVerdictEvent` updated to accept `status string` parameter; all call sites updated.
- [ ] `BudgetTimeout` is tested independently of the subprocess call (unit testable with a cancelled parent context).
- [ ] `JobLogWriter.Write` is called synchronously with the stream decode — no goroutine races (confirmed by race detector on `TestCliAgent_Dispatch_WritesJSONLLog`).
- [ ] `WriteEventThenRow` uses `time.RFC3339` for formatting; `ListEvents` uses `time.RFC3339` for parsing.
- [ ] `TestListEvents_TimestampWithOffset` verifies offset timestamps (`+00:00`) round-trip correctly.
- [ ] `ListEvents` parse error test inserts directly via raw SQL, bypassing `WriteEventThenRow`, to simulate a corrupted row.
- [ ] All new packages/files follow existing import ordering convention (stdlib, then internal, then external).
- [ ] `golangci-lint` passes on all modified files after each phase.
- [ ] Full test suite (`go test ./... -race -count=1 -timeout 120s`) passes before creating the PR.

---

## Code Review

### Pre-Implementation Review (Codex)

[SHOULD FIX] I-1: The immutability trigger leaves finding identity fields mutable
- Detail: The source finding says findings should be immutable except resolution fields, and the store comment says only `resolved_by_job_id` / `resolved_at` can change after creation. The planned trigger protects `path`, `line`, `severity`, `body`, and `fingerprint`, but a direct SQL update to `run_id` or `job_id` would still pass. The duplicate-open-finding concerns about `role` / `finding_type` are not applicable to the observed schema because `findings` has no `role` or `finding_type` columns; the planned `RAISE(ABORT, ...)` form is valid trigger syntax, and the protected columns are `NOT NULL`, so the listed `!=` comparisons do not have the `NULL != NULL` problem.
- File: docs/plans/117-correctness-gaps.md:83
- File: store/migrations/001_init.sql:55
- File: store/finding_store.go:24
- Recommended fix: Extend the trigger predicate to reject changes to every immutable column, including at least `run_id` and `job_id`, or rewrite it as an allowlist that only permits changes to `resolved_by_job_id` and `resolved_at`.

→ Response: [FIXED] Rewrote the trigger as an allowlist using `NOT (... all immutable columns equal ...)`. The condition now protects `run_id` and `job_id` in addition to the five data columns. Any future column additions are also protected by default. Tests updated to cover `run_id` and `job_id` rejection and all seven protected columns in a table-driven test.

[SHOULD FIX] I-2: Empty `phase-test` overrides will be ignored and the default tester will still run
- Detail: The plan adds `TesterRoles []string`, but the helper falls back to `defaultTesterRoles` whenever the slice length is zero. `StageRegistry` explicitly treats an empty role list as a registered-but-disabled stage, and `RolesForStage` returns an empty slice for that case. With the planned code, `phase-test: []` would set `TesterRoles` to an empty slice and then silently re-enable the default `tester`, breaking existing stage override semantics. The existing phase dedupe key is not changed by this phase; `DedupeFindings` already keys by `Fingerprint`, computed from `path`, `line`, `severity`, and `body`.
- File: docs/plans/117-correctness-gaps.md:144
- File: docs/plans/117-correctness-gaps.md:165
- File: coding/stages/registry.go:58
- File: coding/phaseloop/fanin.go:73
- Recommended fix: Distinguish nil from explicitly empty tester roles. For example, use `TesterRoles nil => default`, `TesterRoles empty non-nil => disabled`, and add tests for `phase-test: []`.

→ Response: [FIXED] `testerRoles()` now uses pointer semantics: nil → `defaultTesterRoles`; non-nil empty → disabled (returns nil, fan-out skips). `RunPhasesForPlan` uses direct assignment (`TesterRoles = RolesForStage("phase-test")`) to preserve the nil/empty distinction from `StageRegistry`. Added tests for `phase-test: []` (tester disabled), `TesterRoles = nil` (uses default), and `TesterRoles = []string{}` (disabled).

[MUST FIX] I-3: `WorkDir` is not wired from workflow/worktree execution into `PhaseExecutor`
- Detail: The plan adds `PhaseExecutor.WorkDir` and says empty means `applies_when` is always true, but it does not specify how `BuildFromPRDWorkflow` supplies the per-plan worktree or repo root. The workflow currently returns worktree paths from `PrepareWorktrees`, while `RunPhasesForPlan` accepts only `runID`, `plan`, and `inputs`; no `WorkDir` field exists on `BuildFromPRDWorkflow`, and no code path sets `PhaseExecutor.WorkDir` before `Execute`. This means the planned safe default can mask missing wiring and dispatch roles that should have been skipped.
- File: docs/plans/117-correctness-gaps.md:256
- File: coding/workflow/build_from_prd.go:171
- File: coding/workflow/build_from_prd.go:195
- File: coding/phaseloop/executor.go:34
- Recommended fix: Add an explicit workdir source to the workflow path, such as `BuildFromPRDWorkflow.WorkDir` or a per-plan worktree argument, and set `PhaseExecutor.WorkDir` before each `Execute`. If the concrete dispatcher is used, keep `coding.Dispatcher.WorkDir` in sync as well.

→ Response: [FIXED] Added `WorkDir string` to `BuildFromPRDWorkflow`. `RunPhasesForPlan` sets `phaseExecutor.WorkDir = w.WorkDir` (falling back to per-plan worktree when one exists). `cli/run.go` and `cli/daemon.go` populate `BuildFromPRDWorkflow.WorkDir` from `os.Getwd()`. Complete data-flow diagram documented in Phase 3c.

[MUST FIX] I-3: Planned `changes_touch` evaluation does not match the existing supervisor predicate semantics
- Detail: The plan says role-level `applies_when` should use the same semantics as supervisor `changes_touch`, but its sample implementation runs `git diff --name-only HEAD` and checks `filepath.Match` directly. The existing supervisor predicate compares `HEAD~1..HEAD` with an initial-commit fallback, supports `web/**` / `**/*.tsx` conventions, and also matches basenames for slashless patterns. The planned version will miss nested `web/**` files and will evaluate unstaged working tree changes rather than the committed phase diff used by supervisor tests.
- File: docs/plans/117-correctness-gaps.md:235
- File: docs/plans/117-correctness-gaps.md:247
- File: coding/supervisor/predicates.go:283
- File: coding/supervisor/predicates.go:354
- Recommended fix: Reuse/export the supervisor `EvalAppliesWhen`/`changes_touch` path or move shared predicate logic into a small internal package so role `applies_when` and supervisor rules use one implementation.

→ Response: [FIXED] Created `internal/predicates/changes_touch.go` with the canonical implementation: `HEAD~1..HEAD` diff with initial-commit fallback, basename matching for slash-free patterns, full-path matching for patterns containing slashes. `coding/supervisor/predicates.go` migrated to call `internal/predicates.ChangesTouch`. Phase-loop `roleShouldDispatch` also calls the same shared function. Single source of truth.

[MUST FIX] I-4: Returning a supervisor error will not mark the run failed as the plan claims
- Detail: The plan says `executeAttempt` returning an error propagates through `Orchestrate`, which already marks the run failed. In the observed code, `Orchestrate` returns immediately when `executeAttempt` errors and only calls `runStore.CompleteRun` after all attempts finish normally. The planned job state update may mark the job failed, but the run can remain active.
- File: docs/plans/117-correctness-gaps.md:359
- File: coding/dispatch.go:185
- File: coding/dispatch.go:228
- Recommended fix: On `executeAttempt` errors after run creation, complete the run with `core.RunStateFailed` before returning. Add the planned test assertion against the run row.

→ Response: [FIXED] `executeAttempt` now calls `runStore.CompleteRun(ctx, runID, core.RunStateFailed)` before returning the supervisor error, so the run row is explicitly failed before `Orchestrate` returns early. Test updated to assert all three conditions: (a) `Orchestrate` returns error, (b) job state is `failed`, (c) run state is `failed`.

[SHOULD FIX] I-4: The planned supervisor-error test cannot be implemented without changing the type seam
- Detail: The test plan asks for a stub supervisor that always returns an error, but `Dispatcher.Supervisor` is a concrete `*supervisor.RuleEngine`, not an interface. The actual `RuleEngine.Evaluate` usually converts parse, unknown predicate, and predicate errors into failed verdict results rather than returning an error, so there is no straightforward stub injection point for this test.
- File: docs/plans/117-correctness-gaps.md:373
- File: coding/dispatch.go:46
- File: coding/supervisor/engine.go:83
- File: coding/supervisor/engine.go:105
- Recommended fix: Introduce a narrow supervisor interface on `Dispatcher`, or write the test using a real error path that exists without unsafe state manipulation. Prefer the interface if the goal is to verify dispatch error handling directly.

→ Response: [FIXED] Defined `SupervisorEvaluator` interface in `coding/dispatch.go`. Changed `Dispatcher.Supervisor` from `*supervisor.RuleEngine` to `SupervisorEvaluator`. Existing `*supervisor.RuleEngine` satisfies the interface (verified by checking the method signature in `coding/supervisor/engine.go`). Tests inject `stubSupervisor{err: errors.New("...")}` directly without importing the concrete type.

[MUST FIX] I-5: Judge-error verdict events do not include `status=error`
- Detail: The source finding requires a durable `quality.verdict` event with `status=error`. The planned error branch calls `writeVerdictEvent` with a synthetic failing `Verdict`, but `Verdict` has no status field and `writeVerdictEvent` currently serializes only `rule_name`, `category`, `severity`, `pass`, `findings`, and `confidence`. That records a failure-like verdict, not an evaluation error, and downstream consumers cannot distinguish judge failures from real rule findings.
- File: docs/reviews/2026-04-26-v1-comprehensive-review.md:117
- File: docs/plans/117-correctness-gaps.md:394
- File: coding/quality/evaluator.go:191
- File: coding/quality/schema.go:66
- Recommended fix: Add an explicit error-event path or extend the event payload to include `status: "error"` and an `error` message. Keep the blocking/attention behavior for block-capable categories.

→ Response: [FIXED] Chose option 2 (augment event payload, not the Verdict struct). `writeVerdictEvent` now accepts a `status string` parameter and includes it in the JSON payload alongside the existing fields. Normal paths pass `"pass"` or `"fail"`; the judge-error path passes `"error"`. `Verdict` struct is unchanged. Tests assert `"status": "error"` is present in the parsed event payload JSON.

[MUST FIX] I-10: Timestamp parsing must use `time.RFC3339`, not a literal-`Z` layout
- Detail: The plan uses `time.Parse("2006-01-02T15:04:05Z", createdAtStr)` and says that offset timestamps should fail. That is the wrong direction for an event store timestamp parser: RFC3339 values can include offsets such as `+00:00`, and the user-specified requirement is to accept those. The current writer also formats with the same literal-`Z` layout, so this phase should normalize the store on RFC3339 rather than adding a parser that rejects valid offsets.
- File: docs/plans/117-correctness-gaps.md:800
- File: docs/plans/117-correctness-gaps.md:809
- File: store/event_store.go:60
- File: store/event_store.go:128
- Recommended fix: Use `event.CreatedAt.Format(time.RFC3339)` in `WriteEventThenRow` and `time.Parse(time.RFC3339, createdAtStr)` in `ListEvents`; update tests to include an offset timestamp.

→ Response: [FIXED] Phase 8 now uses `time.RFC3339` for both write (`event.CreatedAt.UTC().Format(time.RFC3339)`) and read (`time.Parse(time.RFC3339, createdAtStr)`). Added `TestListEvents_TimestampWithOffset` that inserts `2026-04-26T15:30:00+00:00` directly and asserts it round-trips correctly. Added `TestWriteEventThenRow_UsesRFC3339Format` that reads the raw stored string and confirms it parses as valid RFC3339.

VERDICT: NEEDS REVISION — Phases 1, 2, 3, 4, 5, and 8 need changes. Phases 6 and 7 address the specifically requested concerns: `max_wallclock_minutes == 0` is explicitly documented as no deadline, `OpenJobLog` creates `.coworker/runs/<runID>/jobs`, and `core.Job` already carries `RunID`.

---

### Pre-Implementation Re-Review (Codex)

All 5 Must Fix and 3 Should Fix items from the initial pre-implementation review have been addressed in this revision:

**Must Fix items resolved:**
- **I-3 (WorkDir wiring):** `BuildFromPRDWorkflow.WorkDir` added; `RunPhasesForPlan` wires it to `phaseExecutor.WorkDir`; `cli/run.go` and `cli/daemon.go` populate from `os.Getwd()`; per-plan worktree override documented. Full data-flow trace included in Phase 3c.
- **I-3 (changes_touch reuse):** Shared `internal/predicates/changes_touch.go` package created with the canonical implementation (HEAD~1..HEAD, initial-commit fallback, basename matching). Supervisor migrated to call shared package. Phase-loop calls same package. No duplication.
- **I-4 (run state on supervisor error):** `executeAttempt` calls `runStore.CompleteRun(..., core.RunStateFailed)` before returning. Test asserts run row state is `failed` (third assertion added).
- **I-5 (status=error in verdict event):** `writeVerdictEvent` signature extended with `status string` parameter; event JSON payload includes `"status"` field; judge-error path passes `"error"`; tests assert `"status": "error"` in parsed payload.
- **I-10 (RFC3339 timestamp):** Both writer and reader use `time.RFC3339`; writer calls `.UTC()` before formatting; tests include offset timestamp case.

**Should Fix items resolved:**
- **I-1 (trigger allowlist):** Trigger rewritten as `NOT (all immutable columns = OLD values)`; covers `run_id` and `job_id`; future columns protected by default.
- **I-2 (nil vs empty TesterRoles):** Nil → default; non-nil empty → disabled. `RunPhasesForPlan` assigns directly from `RolesForStage` to preserve distinction. Tests added for all three states.
- **I-4 (SupervisorEvaluator interface):** `SupervisorEvaluator` interface defined in `coding/dispatch.go`; `Dispatcher.Supervisor` changed to the interface; stub injected in tests without importing `coding/supervisor`.

---

## Post-Execution Report

_Empty — to be filled in after implementation._
