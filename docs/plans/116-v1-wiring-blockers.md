# Plan 116 — V1 Wiring + Blocker Hardening

**Status:** Draft  
**Flavor:** Runtime  
**Branch:** `feature/plan-116-v1-wiring-blockers`  
**Blocks on:** 115 (Shipper and workflow customization — must be complete for RunPhasesForPlan to exist)  
**Manifest entry:** add to `docs/specs/001-plan-manifest.md` under Phase E after Plan 115

---

## Goal

Address all six production blockers identified in `docs/reviews/2026-04-26-v1-comprehensive-review.md` (B-1 through B-6). After this plan the binary is end-to-end runnable: `coworker run <prd.md>` drives the full autopilot workflow, the daemon serves HTTP/SSE, MCP role invocation dispatches real jobs, dirty phases block advancement, role permissions are enforced at boundaries, and agents can call `orch_checkpoint_*` tools to advance workflow checkpoints.

No Important or Polish items from the review are addressed here — they are candidates for Plan 117 or later.

---

## Architecture

### What changes

| Component | Before | After |
|---|---|---|
| `cli/run.go` | Missing | New cobra command wiring the full autopilot loop |
| `cli/daemon.go` | MCP stdio only, no HTTP | HTTP + SSE + REST on `--http-port` (default 7700), concurrent with MCP stdio |
| `mcp/server.go` + `cli/daemon.go` | `Dispatcher` always nil in daemon | Real `*coding.Dispatcher` built and passed at daemon startup |
| `coding/workflow/build_from_prd.go` | `Clean==false` logs and continues | `Clean==false` stops iteration, returns `StoppedAtPhaseClean: true` with attention ID |
| `core/role.go` + new `core/permission.go` | `RolePermissions` parsed, never evaluated | Permission evaluator; `never` hard-fails, undeclared logs warning (true blocking deferred) |
| `mcp/server.go` + new `mcp/handlers_checkpoint.go` | No `orch_checkpoint_*` tools | Three checkpoint tools registered, backed by AttentionStore |

### Invariants preserved

- Event log before state update: all new store writes go through `WriteEventThenRow` or use existing store methods that already do so.
- No silent state advance: `Clean==false` → workflow stops, not silent.
- Pull model: HTTP REST endpoints are for state reads and attention answers; MCP stdio remains the dispatch channel.
- File artifacts as pointers: no changes to the artifacts table or schema.

### New HTTP surface (daemon)

```
GET  /events                  — SSE stream (existing SSEHandler, newly mounted)
GET  /runs                    — list runs (JSON)
GET  /runs/{id}               — run details (JSON)
GET  /runs/{id}/jobs          — list jobs for a run (JSON)
GET  /attention               — list pending attention items (JSON)
POST /attention/{id}/answer   — answer attention item (JSON body: {answer, answered_by})
```

All endpoints share the same `*store.DB` and `*eventbus.InMemoryBus` instances that the MCP server already holds.

---

## Tech Stack

Same as the rest of the project: Go stdlib `net/http` for the HTTP server (no external router — the surface is small enough for `http.NewServeMux`), `net/http/httptest` for tests, existing store layer.

---

## Phases

### Phase 1 — B-1: `coworker run <prd.md>` autopilot entry point

**Files to create / modify:**

- `cli/run.go` — new file (approximately 200 lines)
- `cli/run_test.go` — new file

**Step-by-step actions:**

1. Declare package-level flag variables:
   - `runDBPath string`
   - `runPolicyPath string`
   - `runMaxParallelPlans int`
   - `runNoShip bool`
   - `runDryRun bool`
   - `runResumeAfterAttention string` (attention ID to resume from after a phase-clean block)

2. Register `runCmd` cobra command with `Use: "run <manifest.yaml>"`, `Args: cobra.ExactArgs(1)`.  
   Bind all flags in `init()`. Add to `rootCmd`.

3. Implement `runAutopilot(cmd *cobra.Command, manifestPath string) error`:
   a. Resolve and validate `manifestPath` — return descriptive error if file does not exist.
   b. Open DB via `store.Open(dbPath)`.
   c. Load policy via `coding/policy.LoadPolicy(policyPath)` (or `core.DefaultPolicy()` if no file).
   d. Build logger from `cmd.ErrOrStderr()`.
   e. Instantiate `coding.Dispatcher` with `RoleDir`, `PromptDir`, `Agent` (from role's CLI field), `DB`, `Logger`, `Supervisor` (from rule engine loaded from `.coworker/rules/`).
   f. Instantiate `phaseloop.PhaseExecutor` with the dispatcher, event store, attention store, and policy.
   g. Instantiate `coding/manifest.WorktreeManager` (only when `--max-parallel-plans > 1`).
   h. Instantiate `workflow.BuildFromPRDWorkflow{ManifestPath, Policy, WorktreeManager, PhaseExecutor, Shipper (nil when --no-ship), StageRegistry}`.
   i. Run the scheduler loop:
      ```
      completed := map[int]bool{}
      active    := map[int]bool{}
      for {
          result, err := wf.Run(ctx, completed, active)
          // handle err
          if len(result.ReadyPlans) == 0 { break }
          for each plan in result.ReadyPlans:
              active[plan.ID] = true
              inputs := buildInputs(result.Worktrees, plan)
              runID  := core.NewID()
              phasesResult, err := wf.RunPhasesForPlan(ctx, runID, plan, inputs)
              // handle StoppedAtPhaseClean — print message, exit non-zero
              delete(active, plan.ID)
              completed[plan.ID] = true
      }
      ```
   j. If `--dry-run`, validate manifest + print schedule, exit 0 without dispatching.
   k. If `--resume-after-attention <id>`, check `AttentionStore.GetAttentionByID` — if answered, re-enter the loop from the blocked plan; if not answered, print status and exit non-zero.

4. `buildInputs(worktrees map[int]string, plan manifest.PlanEntry) map[string]string`:  
   Returns `{"branch": "feature/plan-NNN-slug", "plan_id": "NNN", "worktree_path": "..."}` — standard inputs expected by phase executor.

**Test plan (`cli/run_test.go`):**

- Dry-run with a valid manifest returns exit 0 and prints the schedule.
- Dry-run with missing manifest returns descriptive error.
- Stub dispatcher (implement `phaseloop.Orchestrator` in test); run one plan with one phase; assert `completed[planID] == true` after loop.
- `StoppedAtPhaseClean == true` → command exits non-zero and prints attention item ID.
- `--resume-after-attention <id>` when item is unanswered → prints "waiting for human answer" and exits non-zero.
- `--resume-after-attention <id>` when item is answered → continues (mock the rest of the loop to return immediately).

---

### Phase 2 — B-2: Daemon HTTP/SSE server

**Files to modify:**

- `cli/daemon.go` — add HTTP server construction and concurrent run
- `cli/daemon_http_test.go` — new file (HTTP endpoint tests)

**Step-by-step actions:**

1. Add flag `daemonHTTPPort int` (default 7700) to `daemonCmd.Flags()`.

2. In `runDaemon`, after creating the event bus and before running the MCP server, build the HTTP mux:

   ```
   func buildHTTPMux(bus *eventbus.InMemoryBus, stores httpStores) *http.ServeMux
   ```

   Register handlers on `mux`:
   - `GET /events` → `eventbus.SSEHandler(bus)`
   - `GET /runs` → `handleListRuns(stores.run)`
   - `GET /runs/{id}` → `handleGetRun(stores.run)` (parse id from path)
   - `GET /runs/{id}/jobs` → `handleListJobs(stores.job)`
   - `GET /attention` → `handleListAttention(stores.attention)`
   - `POST /attention/{id}/answer` → `handleAnswerAttention(stores.attention)`

   Path parsing uses Go 1.22+ `http.ServeMux` pattern matching (`GET /runs/{id}`) — the project already requires Go 1.22 for this syntax.

3. `httpStores` is a small local struct (not exported):
   ```go
   type httpStores struct {
       run       *store.RunStore
       job       *store.JobStore
       attention *store.AttentionStore
   }
   ```

4. Implement handler functions in a new file `cli/daemon_http.go`:
   - `handleListRuns(rs *store.RunStore) http.HandlerFunc` — calls `rs.ListRuns(ctx)`, encodes JSON.
   - `handleGetRun(rs *store.RunStore) http.HandlerFunc` — parses `{id}` from `r.PathValue("id")`, calls `rs.GetRun(ctx, id)`, 404 on nil.
   - `handleListJobs(js *store.JobStore) http.HandlerFunc` — parses `{id}`, calls `js.ListJobsByRun(ctx, id)`, encodes JSON.
   - `handleListAttention(as *store.AttentionStore) http.HandlerFunc` — calls `as.ListAllPending(ctx)`, encodes JSON.
   - `handleAnswerAttention(as *store.AttentionStore) http.HandlerFunc` — decodes `{answer, answered_by}` from request body, calls `as.AnswerAttention` then `as.ResolveAttention`.

5. Start the HTTP server concurrently with the MCP stdio server using `errgroup`:
   ```go
   g, gCtx := errgroup.WithContext(ctx)
   g.Go(func() error { return srv.Run(gCtx) })
   g.Go(func() error {
       httpSrv := &http.Server{Addr: fmt.Sprintf(":%d", daemonHTTPPort), Handler: mux}
       go func() { <-gCtx.Done(); httpSrv.Shutdown(context.Background()) }()
       if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
           return err
       }
       return nil
   })
   return g.Wait()
   ```

6. Ensure graceful shutdown: `httpSrv.Shutdown` is called on context cancel.

**Test plan (`cli/daemon_http_test.go`):**

- Use `httptest.NewServer(buildHTTPMux(...))` with an in-memory DB.
- `GET /events` — connect, write one event to bus, read SSE line, verify JSON matches.
- `GET /runs` — create a run in DB, GET, assert it appears in JSON.
- `GET /runs/{id}` — 200 for known ID, 404 for unknown.
- `GET /runs/{id}/jobs` — create run + job, verify list.
- `GET /attention` — insert unanswered item, verify it appears.
- `POST /attention/{id}/answer` — answer an item, re-fetch via `GET /attention`, verify it is gone from pending list.
- Malformed JSON body on `POST /attention/{id}/answer` → 400.
- `GET /runs/{id}` with non-existent store (nil run store) → graceful 503 or 404.

---

### Phase 3 — B-3: Wire production Dispatcher into MCP daemon

**Files to modify:**

- `cli/daemon.go` — build real `*coding.Dispatcher` and pass into `ServerConfig`
- `cli/daemon_integration_test.go` — new file or extend existing test

**Step-by-step actions:**

1. Add flags to `daemonCmd`:
   - `daemonRoleDir string` (default `.coworker/roles`)
   - `daemonPromptDir string` (default `.coworker`)

2. In `runDaemon`, after opening the DB and creating the event bus, build the dispatcher:

   ```go
   func buildDispatcher(db *store.DB, roleDir, promptDir string, logger *slog.Logger) (*coding.Dispatcher, error)
   ```

   Inside `buildDispatcher`:
   a. Resolve `roleDir` — if `.coworker/roles` does not exist, fall back to `coding/roles` (same logic as `cli/invoke.go`).
   b. Resolve `promptDir` similarly.
   c. Build `supervisor.RuleEngine` by loading rules from `<roleDir>/../rules/` (or skip if no rules directory exists yet — return nil supervisor, log a warning).
   d. Determine agent binary from config or env (default `codex` for now, same as invoke).
   e. Return `&coding.Dispatcher{RoleDir, PromptDir, Agent: agent.NewCliAgent(binary), DB, Logger, Supervisor}`.

3. Pass the built dispatcher into `mcpserver.NewServer(mcpserver.ServerConfig{..., Dispatcher: dispatcher})`.

4. Remove the comment "Dispatcher is wired in a later plan phase" from `cli/daemon.go`.

5. Add an integration test that:
   a. Starts a real `mcpserver.Server` (not stdio — test the handler directly via `CallRoleInvoke`).
   b. Uses a stub agent (`testdata/mocks/codex` script that emits one finding).
   c. Calls `mcp.CallRoleInvoke(ctx, dispatcher, "reviewer.arch", inputs)`.
   d. Asserts a run and job row were created in the DB.
   e. Asserts the finding was persisted.

**Test plan:**

- `buildDispatcher` with non-existent role dir falls back gracefully.
- `buildDispatcher` with existing rules dir loads the rule engine.
- Integration test: `orch_role_invoke` via `CallRoleInvoke` → run + job created → finding persisted (uses mock codex binary).
- `ServerConfig.Dispatcher != nil` → `Tools()` still returns the same 14 tool names (no regression).

---

### Phase 4 — B-4: Block dirty phases at phase-clean

**Files to modify:**

- `coding/workflow/build_from_prd.go` — `RunPhasesForPlan` and `RunPhasesResult`
- `coding/workflow/build_from_prd_test.go` — extend existing tests

**Step-by-step actions:**

1. Add fields to `RunPhasesResult`:
   ```go
   StoppedAtPhaseClean bool   // true when a phase returned Clean==false
   DirtyPhaseIndex     int    // zero-based index of the dirty phase
   DirtyPhaseName      string // human-readable name
   AttentionItemID     string // ID of the phase-clean attention item created by PhaseExecutor
   ```

2. In `RunPhasesForPlan`, after `phaseResults = append(phaseResults, result)`:
   ```go
   if !result.Clean {
       out := &RunPhasesResult{
           PhaseResults:        phaseResults,
           StoppedAtPhaseClean: true,
           DirtyPhaseIndex:     i,
           DirtyPhaseName:      phaseName,
           // AttentionItemID resolved below
       }
       // Retrieve the most recently created attention item for this run as the
       // checkpoint ID. PhaseExecutor already inserted it; we surface the ID here
       // so the CLI can print it.
       if w.PhaseExecutor.AttentionStore != nil {
           items, listErr := w.PhaseExecutor.AttentionStore.ListUnansweredByRun(ctx, runID)
           if listErr == nil && len(items) > 0 {
               out.AttentionItemID = items[len(items)-1].ID
           }
       }
       log.Warn("phase-clean block: stopping workflow",
           "plan_id", plan.ID,
           "phase_index", i,
           "phase_name", phaseName,
           "attention_id", out.AttentionItemID,
       )
       return out, nil
   }
   ```

3. Remove the existing log line that merely logs `result.Clean` and continues (it is now unreachable for `Clean==false` paths).

4. In `cli/run.go` (Phase 1), the caller checks `phasesResult.StoppedAtPhaseClean` and:
   - Prints: `"Phase %d (%s) of plan %d did not converge. Attention item: %s. Re-run with --resume-after-attention %s to continue after resolving."`
   - Returns `fmt.Errorf(...)` which causes cobra to exit non-zero.

5. Ensure `Shipper.Ship` is NOT called when `StoppedAtPhaseClean == true` (the early return before the ship block already ensures this).

**Test plan:**

- Stub `PhaseExecutor` that returns `Clean==false` on phase 0: `RunPhasesForPlan` returns `StoppedAtPhaseClean==true`, `DirtyPhaseIndex==0`, no `ShipResult`.
- Stub returns `Clean==false` on phase 1 of 3: stops at phase 1, phases 2 and 3 never execute.
- After `StoppedAtPhaseClean`, the shipper is not called (assert mock shipper call count == 0).
- Clean phases: `StoppedAtPhaseClean==false`, shipper called once.
- `AttentionItemID` is populated when `AttentionStore` is set and an item exists.
- `AttentionItemID` is empty string (not panic) when `AttentionStore` is nil.

---

### Phase 5 — B-5: Role permission enforcement at runtime

**Files to create / modify:**

- `core/permission.go` — new file: `Permission` type, parser, matcher
- `core/permission_test.go` — new file
- `coding/dispatch.go` — add permission check before agent dispatch
- `mcp/handlers_dispatch.go` — add permission check in `handleRoleInvoke`

**Step-by-step actions:**

#### 5a — Permission type and matcher (`core/permission.go`)

1. Define the `Permission` struct and kind constants:
   ```go
   type PermissionKind string
   const (
       PermKindRead    PermissionKind = "read"
       PermKindWrite   PermissionKind = "write"
       PermKindEdit    PermissionKind = "edit"
       PermKindNetwork PermissionKind = "network"
       PermKindBash    PermissionKind = "bash"   // bash:<command>
       PermKindMCP     PermissionKind = "mcp"    // mcp:<tool_name>
   )
   
   type Permission struct {
       Kind    PermissionKind
       Subject string // the part after the colon, e.g. "git" for "bash:git"
       Raw     string // original string for display
   }
   ```

2. `ParsePermission(s string) (Permission, error)`:
   - Splits on first `:` to get kind + subject.
   - Simple strings like `"read"`, `"write"`, `"network"` have no subject.
   - Returns error for unknown kinds or malformed input.

3. `ParsePermissions(ss []string) ([]Permission, error)`:
   - Calls `ParsePermission` on each string; returns all errors joined.

4. `MatchPermission(action Permission, allowed []Permission) bool`:
   - Exact match on `Kind` + `Subject`.
   - Glob match: subject `"*"` matches any subject for the same kind.
   - Case-insensitive for subject comparison.

5. `EvaluateAction(action Permission, perms RolePermissions) PermissionDecision`:
   ```go
   type PermissionDecision int
   const (
       PermDecisionAllow      PermissionDecision = iota
       PermDecisionHardDeny                      // matched never
       PermDecisionRequiresHuman                 // matched requires_human
       PermDecisionUndeclared                    // not in any list
   )
   ```
   Logic:
   - Parse `perms.Never` → if `action` matches any → return `PermDecisionHardDeny`.
   - Parse `perms.RequiresHuman` → if matches → return `PermDecisionRequiresHuman`.
   - Parse `perms.AllowedTools` → if matches → return `PermDecisionAllow`.
   - Otherwise → return `PermDecisionUndeclared`.

#### 5b — Enforcement at ephemeral CLI dispatch (`coding/dispatch.go`)

1. Add a new method `checkPermissions(role *core.Role, actions []core.Permission) error` on `Dispatcher`.

2. In `executeAttempt`, before calling `d.Agent.Dispatch`:
   - Build the set of actions implied by the role's CLI binary. For V1 this is a single `bash:<cli-binary>` permission (e.g., `bash:codex`).
   - Call `core.EvaluateAction` for each action against `role.Permissions`.
   - If `PermDecisionHardDeny`: return `fmt.Errorf("permission denied (never): action %s is hard-denied for role %s", action.Raw, role.Name)` — this fails the job immediately.
   - If `PermDecisionRequiresHuman` or `PermDecisionUndeclared`: log a structured warning at `slog.Warn` level with fields `role`, `action`, `decision`. Do NOT block (true attention-item blocking requires the attention queue to be wired into the wait path, which is deferred). Add a TODO comment referencing Plan 117.

3. Note: this does not yet intercept tool calls *within* the agent session; that requires hook integration (Claude Code's PreToolUse hook). V1 only checks the top-level CLI binary invocation.

#### 5c — Enforcement at MCP `orch_role_invoke` (`mcp/handlers_dispatch.go`)

1. In `handleRoleInvoke`, after loading the dispatcher result and before returning, add a pre-invocation check:
   - The action being checked is `bash:<role.CLI>` (same as above).
   - Load the role via a new lightweight `roles.LoadRole(d.RoleDir, in.Role)` call at the top of the handler.
   - `EvaluateAction` → if `HardDeny` → return `fmt.Errorf(...)` before dispatching.
   - If `RequiresHuman` or `Undeclared` → log warning, proceed.

   This requires `handleRoleInvoke` to have access to `RoleDir`. Update `handleRoleInvoke` signature to accept an additional `roleDir string` argument (or embed it in a closure over the `Dispatcher` since `Dispatcher.RoleDir` is already a field).

**Test plan (`core/permission_test.go`):**

- `ParsePermission("read")` → `{Kind: PermKindRead, Subject: "", Raw: "read"}`.
- `ParsePermission("bash:git")` → `{Kind: PermKindBash, Subject: "git"}`.
- `ParsePermission("bash:*")` → subject `"*"`.
- `ParsePermission("unknown:foo")` → error.
- `MatchPermission` exact match: `bash:git` matches `bash:git`.
- `MatchPermission` glob: `bash:*` matches `bash:git`.
- `MatchPermission` no match: `bash:curl` does not match `bash:git`.
- `EvaluateAction` when action in `never` → `HardDeny`.
- `EvaluateAction` when action in `requires_human` → `RequiresHuman`.
- `EvaluateAction` when action in `allowed_tools` → `Allow`.
- `EvaluateAction` when not in any list → `Undeclared`.
- `EvaluateAction` precedence: `never` wins over `allowed_tools` if both match.

**Test plan (`coding/dispatch_test.go` additions):**

- Dispatcher with a role whose `never` includes `bash:codex`: `Orchestrate` returns permission-denied error before any agent subprocess is started.
- Dispatcher with `allowed_tools: [bash:codex]`: proceeds normally (mock agent).
- Dispatcher with undeclared action: logs warning, proceeds (mock agent returns success).

---

### Phase 6 — B-6: Add `orch_checkpoint_*` MCP tools

**Files to create / modify:**

- `mcp/handlers_checkpoint.go` — new file (three handlers)
- `mcp/handlers_checkpoint_test.go` — new file
- `mcp/server.go` — register three new tools in `registerTools` and update `Tools()`

**Step-by-step actions:**

#### 6a — Handler implementations (`mcp/handlers_checkpoint.go`)

All three tools operate on `*store.AttentionStore` filtering for `kind = "checkpoint"`.

1. **`orch_checkpoint_list`**

   Input:
   ```go
   type checkpointListInput struct {
       RunID string `json:"run_id,omitempty"`
   }
   ```
   Output:
   ```go
   type checkpointListOutput struct {
       Items []attentionItemOutput `json:"items"`
   }
   ```
   Implementation (`handleCheckpointList`):
   - If `RunID` is provided: call `as.ListAttentionByRun(ctx, runID, &core.AttentionCheckpoint)`.
   - Otherwise: call `as.ListAllPending(ctx)`, filter client-side to `kind=="checkpoint"`.
   - Convert to `attentionItemOutput` slice via `convertAttentionItems` (already exported from `handlers_attention.go` or make it a package-level unexported helper).
   - Return.

2. **`orch_checkpoint_advance`**

   Input:
   ```go
   type checkpointAdvanceInput struct {
       AttentionID string `json:"attention_id"`
       AnsweredBy  string `json:"answered_by,omitempty"`
       Notes       string `json:"notes,omitempty"`
   }
   ```
   Output: `checkpointActionOutput{Status string, AttentionID string}`

   Implementation (`handleCheckpointAdvance`):
   - Validate `AttentionID` non-empty.
   - Call `as.GetAttentionByID(ctx, id)` — if nil return error "checkpoint not found".
   - Verify `item.Kind == core.AttentionCheckpoint`; if not, return error "not a checkpoint".
   - `answeredBy` defaults to `"user"` if empty.
   - Call `as.AnswerAttention(ctx, id, "approve", answeredBy)`.
   - Call `as.ResolveAttention(ctx, id)`.
   - Return `{Status: "approved", AttentionID: id}`.

3. **`orch_checkpoint_rollback`**

   Same structure as advance but calls `AnswerAttention(ctx, id, "reject", answeredBy)`.
   Returns `{Status: "rejected", AttentionID: id}`.

#### 6b — Register tools (`mcp/server.go`)

1. In `registerTools`, after the attention tools block, add a checkpoint tools block:
   ```go
   if s.stores.attention != nil {
       mcp.AddTool(s.inner, &mcp.Tool{Name: "orch_checkpoint_list", ...}, handleCheckpointList(s.stores.attention))
       mcp.AddTool(s.inner, &mcp.Tool{Name: "orch_checkpoint_advance", ...}, handleCheckpointAdvance(s.stores.attention))
       mcp.AddTool(s.inner, &mcp.Tool{Name: "orch_checkpoint_rollback", ...}, handleCheckpointRollback(s.stores.attention))
   } else {
       // stubs for the three tools
   }
   ```

2. Update `Tools()` to include the three new names after `"orch_attention_answer"`:
   ```go
   "orch_checkpoint_list",
   "orch_checkpoint_advance",
   "orch_checkpoint_rollback",
   ```
   Total tool count goes from 14 to 17.

3. Add `CallCheckpointList`, `CallCheckpointAdvance`, `CallCheckpointRollback` exported test helpers following the same pattern as `CallAttentionList` etc.

**Test plan (`mcp/handlers_checkpoint_test.go`):**

- `orch_checkpoint_list` with no items → `{items: []}`.
- `orch_checkpoint_list` with one checkpoint item and one question item → only checkpoint appears.
- `orch_checkpoint_list` filtered by `run_id` → only items for that run.
- `orch_checkpoint_advance` on a known checkpoint → answer is "approve", item is resolved.
- `orch_checkpoint_advance` on unknown ID → error "checkpoint not found".
- `orch_checkpoint_advance` on a non-checkpoint attention item → error "not a checkpoint".
- `orch_checkpoint_rollback` → answer is "reject", item is resolved.
- `orch_checkpoint_advance` with empty `attention_id` → validation error.

**Test plan (`mcp/server_test.go` additions):**

- `srv.Tools()` returns 17 names including all three checkpoint tools.
- Stub branch: when `DB` is nil, checkpoint tools are registered as stubs (not nil panics).

---

## Self-Review Checklist

Before opening the PR, verify each item:

- [ ] `go test ./... -count=1 -timeout 120s` passes with zero failures.
- [ ] `go test -race ./... -count=1 -timeout 120s` passes with zero data race warnings.
- [ ] `golangci-lint run ./...` reports zero new errors.
- [ ] `cli/run.go` exists and `coworker run --help` works without panicking.
- [ ] `coworker daemon --help` shows `--http-port` flag.
- [ ] `runDaemon` no longer contains the comment "Dispatcher is wired in a later plan phase".
- [ ] `RunPhasesResult` has `StoppedAtPhaseClean`, `DirtyPhaseIndex`, `DirtyPhaseName`, `AttentionItemID` fields.
- [ ] `RunPhasesForPlan` returns early (does not call shipper) when any phase is dirty.
- [ ] `core/permission.go` defines `ParsePermission`, `MatchPermission`, `EvaluateAction`, `PermissionDecision`.
- [ ] `coding/dispatch.go` calls permission check before agent dispatch; hard-deny returns error.
- [ ] `mcp/server.go` `Tools()` returns 17 names.
- [ ] `mcp/handlers_checkpoint.go` implements all three handlers.
- [ ] All new handlers have exported `Call*` test helpers.
- [ ] No new exported symbol is missing a test.
- [ ] Architecture invariant test in `tests/architecture/` still passes (`core` does not import `coding`).
- [ ] `docs/specs/001-plan-manifest.md` has Plan 116 entry added.

---

## Code Review

*This section is for Codex to fill in during review. Authors respond inline.*

---

## Post-Execution Report

*Filled in after implementation is complete and tests pass.*

### Summary

### Deviations from plan

### Tests added

### Open items carried forward
