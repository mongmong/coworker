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

   CREATE TRIGGER IF NOT EXISTS findings_immutable_before_update
   BEFORE UPDATE ON findings
   FOR EACH ROW
   WHEN
       NEW.path        != OLD.path        OR
       NEW.line        != OLD.line        OR
       NEW.severity    != OLD.severity    OR
       NEW.body        != OLD.body        OR
       NEW.fingerprint != OLD.fingerprint
   BEGIN
       SELECT RAISE(ABORT, 'findings: immutable columns (path, line, severity, body, fingerprint) cannot be updated');
   END;
   ```

   The trigger fires BEFORE UPDATE and raises ABORT when any protected column changes. SQLite ABORT rolls back the statement but not the transaction, matching standard constraint behavior. Only rows where a protected column changes are rejected; updating `resolved_by_job_id` or `resolved_at` alone passes through.

2. Verify the migration runner applies `006_` at startup by checking `store/migrate.go` — no code change expected (the runner walks files in order).

**Test plan (`store/finding_store_test.go`):**

- `TestFindingsImmutableTrigger_PathRejected`: insert a finding via `InsertFinding`, then execute raw `UPDATE findings SET path = 'tampered' WHERE id = ?`, assert the error message contains `"immutable"`.
- `TestFindingsImmutableTrigger_SeverityRejected`: same pattern for `severity`.
- `TestFindingsImmutableTrigger_ResolveAllowed`: insert then call `ResolveFinding` — must succeed (trigger must not block resolution updates).
- `TestFindingsImmutableTrigger_AllImmutableColumns`: iterate all five protected columns (`path`, `line`, `severity`, `body`, `fingerprint`) in a table-driven test; each direct UPDATE must fail.

---

### Phase 2 — I-2: Stage registry includes tester role

**Files to modify:**

- `coding/phaseloop/executor.go` — add `TesterRoles []string` field; use it in `fanOut`
- `coding/workflow/build_from_prd.go` — populate `TesterRoles` from `StageRegistry.RolesForStage("phase-test")`
- `coding/phaseloop/executor_test.go` — update/add tests

**Step-by-step actions:**

1. Add `TesterRoles []string` to `PhaseExecutor`:

   ```go
   // TesterRoles is the list of tester roles dispatched in parallel alongside
   // ReviewerRoles after each developer job. When nil or empty,
   // defaultTesterRoles is used (["tester"]).
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

2. Add a `testerRoles()` helper mirroring `reviewerRoles()`:

   ```go
   func (e *PhaseExecutor) testerRoles() []string {
       if len(e.TesterRoles) > 0 {
           return e.TesterRoles
       }
       return defaultTesterRoles
   }
   ```

3. In `fanOut`, concatenate the two lists before building the errgroup:

   ```go
   roles := append(e.reviewerRoles(), e.testerRoles()...)
   ```

4. In `RunPhasesForPlan` (`coding/workflow/build_from_prd.go`), after populating `ReviewerRoles`, add:

   ```go
   if w.StageRegistry != nil {
       if roles := w.StageRegistry.RolesForStage("phase-review"); roles != nil {
           w.PhaseExecutor.ReviewerRoles = roles
       }
       if roles := w.StageRegistry.RolesForStage("phase-test"); roles != nil {
           w.PhaseExecutor.TesterRoles = roles
       }
   }
   ```

**Test plan:**

- `TestRunPhasesForPlan_DefaultRegistryIncludesTester`: create a `BuildFromPRDWorkflow` with the default `StageRegistry`; run `RunPhasesForPlan`; assert that the stub dispatcher received dispatch calls for `reviewer.arch`, `reviewer.frontend`, and `tester` (not just the two reviewer roles).
- `TestPhaseExecutor_FanOutIncludesTester`: unit test on `fanOut` with a mock dispatcher; assert tester is in the dispatched role list.
- `TestPhaseExecutor_CustomTesterRoles`: set `TesterRoles = ["custom.tester"]` and assert the custom role is dispatched, not the default.

---

### Phase 3 — I-3: Role-level `applies_when`

**Files to modify:**

- `core/role.go` — add `AppliesWhen *RoleAppliesWhen`
- `coding/roles/loader.go` — no code change needed (YAML unmarshalling picks up the new struct field automatically); add validation note if `applies_when` contains unknown predicates
- `coding/phaseloop/executor.go` — evaluate `AppliesWhen` in `fanOut` before dispatching; emit `job.skipped` on skip
- `core/event_kinds.go` (or wherever event kind constants live) — add `EventJobSkipped`
- `coding/phaseloop/executor_test.go` — add applies_when tests
- `coding/roles/loader_test.go` — add YAML parse test

**Step-by-step actions:**

1. In `core/role.go` add:

   ```go
   // RoleAppliesWhen declares the condition under which a role should be
   // dispatched. When nil, the role is always dispatched.
   type RoleAppliesWhen struct {
       // ChangesTouch is a list of glob patterns. The role is dispatched only
       // when at least one file in the diff matches one of the patterns.
       // Uses the same matching semantics as the supervisor changes_touch predicate.
       ChangesTouch []string `yaml:"changes_touch,omitempty"`
   }

   // In Role:
   AppliesWhen *RoleAppliesWhen `yaml:"applies_when,omitempty"`
   ```

2. `loader.go` requires no Go changes — `yaml.Unmarshal` will populate the new field automatically. Add a validation check: if `AppliesWhen` is non-nil and `ChangesTouch` is empty, return an error (`applies_when declared but no predicates specified`).

   Update `validateRole`:

   ```go
   if role.AppliesWhen != nil && len(role.AppliesWhen.ChangesTouch) == 0 {
       return fmt.Errorf("applies_when declared but contains no predicates")
   }
   ```

3. In `coding/phaseloop/executor.go`, add a helper that evaluates `applies_when` against the current working directory diff. Reuse `supervisor.changesTouch` logic — import `coding/supervisor` only if it does not create a cycle (phaseloop already imports `coding`; `coding/supervisor` imports `core` only, so the import is safe). Alternatively, inline the git-glob check to avoid importing supervisor from phaseloop:

   ```go
   // roleShouldDispatch returns false when the role declares applies_when
   // and the condition evaluates to false. workDir is the repo root for
   // git diff-index evaluation. When workDir is empty the check is skipped
   // and the role is always dispatched.
   func roleShouldDispatch(role *core.Role, workDir string) (bool, error) {
       if role.AppliesWhen == nil || len(role.AppliesWhen.ChangesTouch) == 0 {
           return true, nil
       }
       if workDir == "" {
           return true, nil
       }
       return evalChangesTouch(workDir, role.AppliesWhen.ChangesTouch)
   }

   // evalChangesTouch runs `git diff --name-only HEAD` and checks whether
   // any changed file matches one of the glob patterns.
   func evalChangesTouch(workDir string, patterns []string) (bool, error) {
       cmd := exec.CommandContext(context.Background(), "git", "diff", "--name-only", "HEAD")
       cmd.Dir = workDir
       out, err := cmd.Output()
       if err != nil {
           return false, fmt.Errorf("git diff --name-only HEAD: %w", err)
       }
       files := strings.Split(strings.TrimSpace(string(out)), "\n")
       for _, file := range files {
           for _, pattern := range patterns {
               if matched, _ := filepath.Match(pattern, file); matched {
                   return true, nil
               }
           }
       }
       return false, nil
   }
   ```

   Note: `PhaseExecutor` needs a `WorkDir string` field (or inherits it from the dispatcher) for the git command. Add:

   ```go
   // WorkDir is the repository root used for applies_when git predicate
   // evaluation. When empty, applies_when is always true (safe default).
   WorkDir string
   ```

4. In `fanOut`, wrap each role dispatch:

   ```go
   g.Go(func() error {
       role, loadErr := roles.LoadRole(e.RoleDir, roleName)
       if loadErr != nil {
           // Role file missing — log and skip, don't abort the whole fan-out.
           log.Warn("could not load role for applies_when check", "role", roleName, "error", loadErr)
       } else if role.AppliesWhen != nil {
           should, evalErr := roleShouldDispatch(role, e.WorkDir)
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

   `PhaseExecutor` needs `RoleDir string` to load roles for predicate evaluation. Add the field:

   ```go
   // RoleDir is the directory containing role YAML files. Used to load
   // role metadata for applies_when evaluation. When empty, applies_when
   // is always true.
   RoleDir string
   ```

5. Add `EventJobSkipped = EventKind("job.skipped")` to `core/event_kinds.go` (or equivalent constant file). Emit a minimal event in `emitJobSkippedEvent`:

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

- `TestLoadRole_AppliesWhen_Parsed`: write a temp YAML with `applies_when.changes_touch: ["web/**"]`; call `LoadRole`; assert `role.AppliesWhen.ChangesTouch == ["web/**"]`.
- `TestLoadRole_AppliesWhen_EmptyPredicatesRejected`: YAML with `applies_when: {}` (no patterns); assert validation error.
- `TestPhaseExecutor_FanOut_SkipsRoleWhenAppliesWhenFalse`: set `WorkDir` to a temp git repo where the diff touches no web files; assert reviewer.frontend is not dispatched and a `job.skipped` event is emitted.
- `TestPhaseExecutor_FanOut_DispatchesRoleWhenAppliesWhenTrue`: diff touches `web/index.tsx`; assert reviewer.frontend is dispatched.
- `TestPhaseExecutor_FanOut_SkipDoesNotBlockOtherRoles`: reviewer.frontend skipped but reviewer.arch and tester still run.

---

### Phase 4 — I-4: Supervisor errors don't silently pass jobs

**Files to modify:**

- `coding/dispatch.go` — change supervisor error handling in `executeAttempt`
- `coding/dispatch_test.go` — add error-path test

**Step-by-step actions:**

1. In `executeAttempt` (around line 390–396), replace:

   ```go
   verdict, evalErr := d.Supervisor.Evaluate(evalCtx)
   if evalErr != nil {
       logger.Error("supervisor evaluation error", "error", evalErr)
       // Treat evaluation error as a pass — don't block the job
       // for engine bugs.
       verdict = &core.SupervisorVerdict{Pass: true}
   }
   ```

   with:

   ```go
   verdict, evalErr := d.Supervisor.Evaluate(evalCtx)
   if evalErr != nil {
       logger.Error("supervisor evaluation error — failing job", "error", evalErr)
       jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed) //nolint:errcheck
       return nil, fmt.Errorf("supervisor evaluation: %w", evalErr)
   }
   ```

   This propagates the error up through `executeAttempt` → `Orchestrate`, which already marks the run as failed when `Orchestrate` returns an error.

2. Update the comment on `Supervisor` in `Dispatcher` to document the new behavior:

   ```go
   // Supervisor is the optional rule engine. If nil, no contract
   // checks are performed (equivalent to all-pass). If Evaluate returns
   // an error, the job is failed (not silently passed) to avoid
   // violating the no-silent-state-advance invariant.
   Supervisor *supervisor.RuleEngine
   ```

**Test plan:**

- `TestDispatcher_SupervisorError_FailsJob`: configure a `Dispatcher` with a stub supervisor that always returns an error; call `Orchestrate`; assert (a) `Orchestrate` returns an error, (b) the job row in the DB has state `failed`, (c) the run row has state `failed`.
- `TestDispatcher_SupervisorNil_SucceedsWithoutCheck`: `Dispatcher.Supervisor == nil`; `Orchestrate` with a passing agent; assert `DispatchResult` is non-nil and no error.

---

### Phase 5 — I-5: Quality judge errors emit verdict event

**Files to modify:**

- `coding/quality/evaluator.go` — modify the judge-error branch in `EvaluateAtCheckpoint`
- `coding/quality/evaluator_test.go` — add judge-error test

**Step-by-step actions:**

1. In `EvaluateAtCheckpoint`, change the judge-error path (currently around lines 88–93):

   ```go
   verdict, err := e.Judge.Evaluate(ctx, rule, diff, jobContext)
   if err != nil {
       logger.Error("quality judge error", "rule", rule.Name, "error", err)
       // Emit an error verdict event so the failure is durable.
       e.writeVerdictEvent(ctx, cpCtx.RunID, cpCtx.JobID, rule, &Verdict{
           Pass:     false,
           Category: string(rule.Category),
           Findings: []string{fmt.Sprintf("judge error: %v", err)},
       })
       // If the rule is block-capable, create an attention item so the
       // operator knows evaluation failed.
       if IsBlockCapable(rule.Category) {
           itemID, attErr := e.createAttentionItem(ctx, cpCtx, rule, &Verdict{
               Pass:     false,
               Category: string(rule.Category),
               Findings: []string{fmt.Sprintf("judge error: %v", err)},
           })
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
   - Every judge error leaves a `quality.verdict` event (durable record).
   - Block-capable rule errors are promoted to blocking findings and attention items, preventing silent checkpoint advance.
   - Advisory rule errors remain non-blocking (log + event only).

**Test plan:**

- `TestEvaluateAtCheckpoint_JudgeError_EmitsVerdictEvent`: use a judge that always returns an error; call `EvaluateAtCheckpoint`; assert a `quality.verdict` event was written with `"pass": false` and findings containing `"judge error"`.
- `TestEvaluateAtCheckpoint_JudgeError_BlockCapable_CreatesAttention`: rule category is `CategoryCorrectness` (block-capable); judge errors; assert `result.BlockingFindings` non-empty and `result.AttentionItemIDs` non-empty.
- `TestEvaluateAtCheckpoint_JudgeError_Advisory_DoesNotBlock`: rule category is advisory; judge errors; assert `result.Pass == true` and `result.BlockingFindings` empty.

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

- `store/event_store.go` — parse `createdAtStr` in `ListEvents`
- `store/event_store_test.go` — add timestamp round-trip test

**Step-by-step actions:**

1. In `ListEvents` (around lines 126–138), after the `rows.Scan` call:

   ```go
   e.Kind = core.EventKind(kindStr)
   t, err := time.Parse("2006-01-02T15:04:05Z", createdAtStr)
   if err != nil {
       return nil, fmt.Errorf("parse event %q created_at %q: %w", e.ID, createdAtStr, err)
   }
   e.CreatedAt = t
   events = append(events, e)
   ```

   The layout `"2006-01-02T15:04:05Z"` matches the format used in `WriteEventThenRow`:
   ```go
   event.CreatedAt.Format("2006-01-02T15:04:05Z"),
   ```

   If `createdAtStr` ever contains sub-second precision or a timezone offset, the parse will fail. Return the error with the event ID and raw string for debuggability, as shown above.

2. Remove the now-unused `createdAtStr` variable declaration comment, if any.

**Test plan (`store/event_store_test.go`):**

- `TestListEvents_TimestampRoundTrip`: write an event with a known `CreatedAt` (truncated to second precision to match the stored format); call `ListEvents`; assert `events[0].CreatedAt` equals the original time (using `time.Equal`).
- `TestListEvents_ZeroTimestampPrevented`: verify that no event returned by `ListEvents` has a zero `CreatedAt` when at least one event exists.
- `TestListEvents_ParseErrorReturned`: insert a row directly with `created_at = 'not-a-date'`; assert `ListEvents` returns an error containing `"parse event"`.

---

## Self-Review Checklist

- [ ] Migration `006_findings_immutability.sql` runs cleanly on the existing test DB (no existing data conflicts with trigger addition).
- [ ] `TesterRoles` is populated by `RunPhasesForPlan` when a `StageRegistry` is set — verified by integration test.
- [ ] `applies_when` with unknown predicates (not `changes_touch`) returns a validation error from `LoadRole`, not a silent skip.
- [ ] Supervisor error propagation test covers: error returned, job state `failed`, run state `failed`.
- [ ] Judge error verdict event has `"pass": false` and `"findings"` non-empty — confirmed by event store spy in test.
- [ ] `BudgetTimeout` is tested independently of the subprocess call (unit testable with a cancelled parent context).
- [ ] `JobLogWriter.Write` is called synchronously with the stream decode — no goroutine races (confirmed by race detector on `TestCliAgent_Dispatch_WritesJSONLLog`).
- [ ] `ListEvents` parse error test inserts directly via raw SQL, bypassing `WriteEventThenRow`, to simulate a corrupted row.
- [ ] All new packages/files follow existing import ordering convention (stdlib, then internal, then external).
- [ ] `golangci-lint` passes on all modified files after each phase.
- [ ] Full test suite (`go test ./... -race -count=1 -timeout 120s`) passes before creating the PR.

---

## Code Review

_Empty — to be filled in during the review step._

---

## Post-Execution Report

_Empty — to be filled in after implementation._
