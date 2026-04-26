# Plan 116 — V1 Wiring + Blocker Hardening

**Status:** Draft  
**Flavor:** Runtime  
**Branch:** `feature/plan-116-v1-wiring-blockers`  
**Blocks on:** 115 (Shipper and workflow customization — must be complete for RunPhasesForPlan to exist)  
**Manifest entry:** add to `docs/specs/001-plan-manifest.md` under Phase E after Plan 115

---

## Goal

Address all six production blockers identified in `docs/reviews/2026-04-26-v1-comprehensive-review.md` (B-1 through B-6). After this plan the binary is end-to-end runnable: `coworker run <prd.md>` drives the full autopilot workflow starting from architect role dispatch, the daemon serves HTTP/SSE, MCP role invocation dispatches real jobs, dirty phases block advancement, role permissions are enforced at boundaries with default-deny semantics, and agents can call `orch_checkpoint_*` tools to advance workflow checkpoints with proper approve/reject gating.

No Important or Polish items from the review are addressed here — they are candidates for Plan 117 or later.

---

## Architecture

### What changes

| Component | Before | After |
|---|---|---|
| `cli/run.go` | Missing | New cobra command: accepts PRD, dispatches architect role, iterates manifest, gates on plan-approved checkpoints |
| `cli/daemon.go` | MCP stdio only, no HTTP | HTTP + SSE + REST on `--http-port` (default 7700), concurrent with MCP stdio; errgroup with mutual cancel |
| `mcp/server.go` + `cli/daemon.go` | `Dispatcher` always nil in daemon | Real `*coding.Dispatcher` built and passed at daemon startup |
| `coding/workflow/build_from_prd.go` | `Clean==false` logs and continues | `Clean==false` stops iteration, returns `StoppedAtPhaseClean: true` with attention ID |
| `core/role.go` + new `core/permission.go` | `RolePermissions` parsed, never evaluated | Permission evaluator; `never` hard-fails, `requires_human` hard-fails (creates attention.permission item), undeclared hard-fails unless policy sets `on_undeclared: warn` |
| `mcp/server.go` + new `mcp/handlers_checkpoint.go` | No `orch_checkpoint_*` tools | Three checkpoint tools registered; advance writes `answer="approve"`, rollback writes `answer="reject"`, resume gates on `answer=="approve"` |

### Invariants preserved

- Event log before state update: all new store writes go through `WriteEventThenRow` or use existing store methods that already do so.
- No silent state advance: `Clean==false` → workflow stops, not silent. Rejected checkpoints abort the run.
- Pull model: HTTP REST endpoints are for state reads and attention answers; MCP stdio remains the dispatch channel.
- File artifacts as pointers: no changes to the artifacts table or schema.
- Default-deny: undeclared actions hard-fail unless the operator explicitly opts in to `on_undeclared: warn` in policy for development use.

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

Same as the rest of the project: Go stdlib `net/http` for the HTTP server (no external router — the surface is small enough for `http.NewServeMux`), `net/http/httptest` for tests, existing store layer. `golang.org/x/sync/errgroup` for concurrent goroutine lifecycle management.

---

## Phases

### Phase 0 — Shared types

**Files to create / modify:**

- `coding/workflow/build_from_prd.go` — add fields to `RunPhasesResult`
- `core/permission.go` — new file: `PermissionDecision` enum
- `core/attention.go` (or existing attention constants file) — add `AttentionAnswerApprove` / `AttentionAnswerReject` constants

**Step-by-step actions:**

1. Add fields to `RunPhasesResult`:
   ```go
   StoppedAtPhaseClean bool   // true when a phase returned Clean==false
   DirtyPhaseIndex     int    // zero-based index of the dirty phase
   DirtyPhaseName      string // human-readable name
   AttentionItemID     string // ID of the phase-clean attention item created by PhaseExecutor
   ```

2. Add answer constants in `core/` (e.g., `core/attention.go`):
   ```go
   const (
       AttentionAnswerApprove = "approve"
       AttentionAnswerReject  = "reject"
   )
   ```

3. Add `PermissionDecision` enum in `core/permission.go` (full type definition lands in Phase 5, but the enum is declared here so Phases 1 and 6 can reference it without import cycles):
   ```go
   type PermissionDecision int
   const (
       PermDecisionAllow         PermissionDecision = iota
       PermDecisionHardDeny                         // matched never
       PermDecisionRequiresHuman                    // matched requires_human
       PermDecisionUndeclared                       // not in any list
   )
   ```

No tests in this phase (pure type declarations). The types are exercised by the phases that use them.

---

### Phase 1 — B-4: Block dirty phases at phase-clean

**Files to modify:**

- `coding/workflow/build_from_prd.go` — `RunPhasesForPlan` stop logic + `AttentionStore.GetUnansweredCheckpointForRun`
- `store/attention.go` — add `GetUnansweredCheckpointForRun(ctx, runID, source string) (*AttentionItem, error)`
- `coding/workflow/build_from_prd_test.go` — extend existing tests

**Step-by-step actions:**

1. Add `GetUnansweredCheckpointForRun(ctx context.Context, runID, source string) (*core.AttentionItem, error)` to `store.AttentionStore`:
   - Queries `attention` table WHERE `run_id = ? AND kind = 'checkpoint' AND source = ? AND answered_at IS NULL ORDER BY created_at DESC LIMIT 1`.
   - Returns nil (not error) when no row matches.

2. In `RunPhasesForPlan`, after `phaseResults = append(phaseResults, result)`:
   ```go
   if !result.Clean {
       out := &RunPhasesResult{
           PhaseResults:        phaseResults,
           StoppedAtPhaseClean: true,
           DirtyPhaseIndex:     i,
           DirtyPhaseName:      phaseName,
       }
       // Retrieve the phase-clean checkpoint item specifically (not any attention item).
       if w.PhaseExecutor.AttentionStore != nil {
           item, getErr := w.PhaseExecutor.AttentionStore.GetUnansweredCheckpointForRun(ctx, runID, "phase-loop")
           if getErr == nil && item != nil {
               out.AttentionItemID = item.ID
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

4. Ensure `Shipper.Ship` is NOT called when `StoppedAtPhaseClean == true` (the early return before the ship block already ensures this).

**Test plan:**

- `GetUnansweredCheckpointForRun` returns nil when no checkpoint exists for a run.
- `GetUnansweredCheckpointForRun` returns nil when a checkpoint exists but is already answered.
- `GetUnansweredCheckpointForRun` returns nil when an unanswered attention item exists but has `kind != "checkpoint"` or `source != "phase-loop"`.
- `GetUnansweredCheckpointForRun` returns the correct item when all filters match.
- Stub `PhaseExecutor` that returns `Clean==false` on phase 0: `RunPhasesForPlan` returns `StoppedAtPhaseClean==true`, `DirtyPhaseIndex==0`, no `ShipResult`.
- Stub returns `Clean==false` on phase 1 of 3: stops at phase 1, phases 2 and 3 never execute.
- After `StoppedAtPhaseClean`, the shipper is not called (assert mock shipper call count == 0).
- Clean phases: `StoppedAtPhaseClean==false`, shipper called once.
- `AttentionItemID` is populated when `AttentionStore` is set and a matching checkpoint item exists.
- `AttentionItemID` is empty string (not panic) when `AttentionStore` is nil.
- `AttentionItemID` is empty string when the unanswered item has the wrong kind or source (permission/question items do not surface as the phase-clean checkpoint).

---

### Phase 2 — B-1: `coworker run <prd.md>` autopilot entry point

**Files to create / modify:**

- `cli/run.go` — new file (approximately 280 lines)
- `cli/run_test.go` — new file

**Step-by-step actions:**

1. Declare package-level flag variables:
   - `runDBPath string`
   - `runPolicyPath string`
   - `runMaxParallelPlans int`
   - `runNoShip bool`
   - `runDryRun bool`
   - `runManifestPath string` — optional bypass for testing; when set, skips architect dispatch and loads the manifest directly
   - `runResumeAfterAttention string` — attention ID to resume from after a phase-clean block

2. Register `runCmd` cobra command with `Use: "run <prd.md>"`, `Args: cobra.ExactArgs(1)`.  
   Bind all flags in `init()`. Add to `rootCmd`.

3. Implement `runAutopilot(cmd *cobra.Command, prdPath string) error`:

   **Step 1 — Validate PRD:**
   - Resolve and validate `prdPath` — return descriptive error if the file does not exist.

   **Step 2 — Dispatch architect role (unless `--manifest` is provided):**
   - If `--manifest <path>` is set, skip to Step 4 and load the manifest directly. Log a warning: `"--manifest flag bypasses architect dispatch; for production use omit this flag"`.
   - Otherwise: open DB, load policy, build dispatcher (same as Phase 4, `buildDispatcher`).
   - Call `dispatcher.DispatchRole(ctx, "architect", map[string]string{"prd_path": prdPath})` — this starts the architect role synchronously and waits for completion.
   - Architect role produces:
     - Spec markdown at `docs/specs/<NNN>-<slug>.md`
     - Plan manifest at a path written to the job output (read via job's output artifacts).
   - Read manifest path from the architect job's output (e.g., from an artifact with `label="manifest"`).

   **Step 3 — `spec-approved` checkpoint:**
   - Insert an attention item with `kind=checkpoint`, `source=run-command`, `label="spec-approved"`, `run_id=runID`.
   - Print: `"Spec generated at <spec_path>. Review and run 'coworker run --resume-after-attention <id>' to continue, or answer via HTTP POST /attention/<id>/answer."`.
   - Block: return immediately with exit 0 (the user must resume; this is an intentional pause — the run row is persisted so resume picks it up).
   - Note: if `--dry-run` is set at any point, validate the PRD exists, print the planned schedule, and exit 0 without dispatching or creating any DB rows.

   **Step 4 — Load manifest and iterate ready plans:**
   - Parse the manifest YAML/Markdown from the resolved manifest path.
   - Initialise `completed := map[int]bool{}` and `active := map[int]bool{}`.

   **Step 5 — For each ready plan: planner → plan-approved → phases → ship:**
   ```
   for {
       ready := scheduler.ReadyPlans(manifest, completed, active)
       if len(ready) == 0 { break }
       for _, plan := range ready {
           active[plan.ID] = true
           // a. Dispatch planner role to elaborate the plan skeleton
           plannerResult, err := dispatcher.DispatchRole(ctx, "planner", map[string]string{"plan_id": plan.ID})
           if err != nil { return err }
           // b. Insert plan-approved checkpoint (block-by-default)
           checkpointID := insertCheckpoint(ctx, as, runID, "plan-approved", plan.ID)
           fmt.Printf("Plan %d elaborated. Review and resume with --resume-after-attention %s\n", plan.ID, checkpointID)
           return nil // pause; resume will re-enter here
       }
   }
   ```
   - On resume (see Step k below), re-enter the loop with the persisted `completed`/`active` state from DB event log replay.

   **Step 6 — After all phases clean, call shipper:**
   - Once all plans complete, if `--no-ship` is not set, call `Shipper.Ship(ctx, runID)`.

   **Step j — `--dry-run` path:**
   - Validate PRD exists, print planned schedule (manifest + phase count), exit 0 without dispatching.

   **Step k — `--resume-after-attention <id>` path:**
   - Open DB. Look up attention item via `AttentionStore.GetAttentionByID(ctx, id)`.
   - If not found: print error and exit non-zero.
   - If `item.AnsweredAt == nil` (not yet answered): print `"Attention item <id> is still pending human review. Answer it via 'POST /attention/<id>/answer' then re-run with --resume-after-attention <id>."` and exit non-zero.
   - If `item.Answer == core.AttentionAnswerReject`: print `"Checkpoint was rejected. Run aborted."`, update run state to `aborted` in DB, exit non-zero.
   - If `item.Answer == core.AttentionAnswerApprove`: reconstruct scheduler state from event log (see Phase 2 resume reconstruction below) and re-enter the main loop.

4. **Resume context reconstruction:**
   - Read the run row from DB using `item.RunID`.
   - Replay the run's event log (`SELECT * FROM events WHERE run_id = ? ORDER BY id ASC`) to determine which plan IDs have `plan.completed` events vs `plan.started` events — rebuild `completed` and `active` maps.
   - Re-create any missing worktrees idempotently via `worktree.Open(planID)` (idempotent: no-op if worktree already exists).
   - Document: "Run state is fully recoverable from event log + filesystem worktrees; no in-memory state needs to be serialised."

5. `buildInputs(worktrees map[int]string, plan manifest.PlanEntry) map[string]string`:  
   Returns `{"branch": "feature/plan-NNN-slug", "plan_id": "NNN", "worktree_path": "..."}` — standard inputs expected by phase executor.

**Test plan (`cli/run_test.go`):**

- `--dry-run` with a valid PRD file returns exit 0 and prints the schedule without touching the DB.
- `--dry-run` with missing PRD file returns descriptive error.
- `--manifest <path>` flag with a valid manifest bypasses architect dispatch, logs a warning, and proceeds directly to the plan loop.
- `--manifest` with a missing file returns descriptive error.
- Architect dispatch failure → command returns non-zero with the dispatcher error message.
- `spec-approved` checkpoint is inserted after successful architect dispatch; command exits 0 (paused state).
- `--resume-after-attention <id>` when item is not found → prints error and exits non-zero.
- `--resume-after-attention <id>` when item is unanswered → prints "waiting for human answer" and exits non-zero.
- `--resume-after-attention <id>` when item answer is `"reject"` → prints "aborted" and exits non-zero; run state set to `aborted`.
- `--resume-after-attention <id>` when item answer is `"approve"` → reconstructs scheduler state, continues loop (mock dispatcher returns immediately).
- `StoppedAtPhaseClean == true` → command exits non-zero and prints attention item ID.
- Stub dispatcher (implement `phaseloop.Orchestrator` in test); run one plan with one phase; assert `completed[planID] == true` after loop.

---

### Phase 3 — B-2: Daemon HTTP/SSE server

**Files to modify:**

- `cli/daemon.go` — add HTTP server construction and concurrent run with mutual-cancel errgroup
- `cli/daemon_http.go` — new file (handler implementations)
- `cli/daemon_http_test.go` — new file (HTTP endpoint tests)

**Step-by-step actions:**

1. Add flag `daemonHTTPPort int` (default 7700) to `daemonCmd.Flags()`.

2. In `runDaemon`, after creating the event bus and before running the MCP server, build the HTTP mux:

   ```go
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

4. Implement handler functions in `cli/daemon_http.go`:
   - `handleListRuns(rs *store.RunStore) http.HandlerFunc` — calls `rs.ListRuns(ctx)`, encodes JSON.
   - `handleGetRun(rs *store.RunStore) http.HandlerFunc` — parses `{id}` from `r.PathValue("id")`, calls `rs.GetRun(ctx, id)`, 404 on nil.
   - `handleListJobs(js *store.JobStore) http.HandlerFunc` — parses `{id}`, calls `js.ListJobsByRun(ctx, id)`, encodes JSON.
   - `handleListAttention(as *store.AttentionStore) http.HandlerFunc` — calls `as.ListAllPending(ctx)`, encodes JSON.
   - `handleAnswerAttention(as *store.AttentionStore) http.HandlerFunc` — decodes `{answer, answered_by}` from request body, calls `as.AnswerAttention` then `as.ResolveAttention`.

5. Start the HTTP server concurrently with the MCP stdio server using `errgroup` with mutual cancel:
   ```go
   ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
   defer cancel()

   g, gCtx := errgroup.WithContext(ctx)
   g.Go(func() error {
       defer cancel() // signal peer to exit when MCP server stops
       return srv.Run(gCtx)
   })
   g.Go(func() error {
       defer cancel() // signal peer to exit when HTTP server stops
       httpSrv := &http.Server{
           Addr:    fmt.Sprintf(":%d", daemonHTTPPort),
           Handler: mux,
       }
       go func() { <-gCtx.Done(); httpSrv.Shutdown(context.Background()) }()
       if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
           return err
       }
       return nil
   })
   return g.Wait()
   ```
   The `defer cancel()` inside each goroutine ensures that whichever goroutine exits first (including a clean MCP disconnect) cancels `gCtx`, which triggers the other goroutine's shutdown path.

6. Ensure graceful shutdown: `httpSrv.Shutdown` is called on context cancel via the inline goroutine.

**Test plan (`cli/daemon_http_test.go`):**

- Use `httptest.NewServer(buildHTTPMux(...))` with an in-memory DB.
- `GET /events` — connect, write one event to bus, read SSE line, verify JSON matches.
- `GET /runs` — create a run in DB, GET, assert it appears in JSON.
- `GET /runs/{id}` — 200 for known ID, 404 for unknown.
- `GET /runs/{id}/jobs` — create run + job, verify list.
- `GET /attention` — insert unanswered item, verify it appears.
- `POST /attention/{id}/answer` — answer an item, re-fetch via `GET /attention`, verify it is gone from pending list.
- Malformed JSON body on `POST /attention/{id}/answer` → 400.
- `GET /runs/{id}` with non-existent ID → 404.
- Errgroup mutual-cancel: simulate MCP exit by cancelling the parent context; assert HTTP server shuts down (no goroutine leak).

---

### Phase 4 — B-3: Wire production Dispatcher into MCP daemon

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

### Phase 5 — B-5: Role permission enforcement at runtime

**Files to create / modify:**

- `core/permission.go` — complete the file started in Phase 0: `Permission` type, parser, matcher, `EvaluateAction`
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
   (Uses the `PermissionDecision` enum declared in Phase 0.)
   Logic:
   - Parse `perms.Never` → if `action` matches any → return `PermDecisionHardDeny`.
   - Parse `perms.RequiresHuman` → if matches → return `PermDecisionRequiresHuman`.
   - Parse `perms.AllowedTools` → if matches → return `PermDecisionAllow`.
   - Otherwise → return `PermDecisionUndeclared`.

#### 5b — Enforcement at ephemeral CLI dispatch (`coding/dispatch.go`)

1. Add a new method `checkPermissions(role *core.Role, actions []core.Permission) error` on `Dispatcher`.

2. Read the `on_undeclared` policy setting from `d.Policy.Permissions.OnUndeclared`. Valid values: `"deny"` (default) and `"warn"`. Any other value is treated as `"deny"`.

3. In `executeAttempt`, before calling `d.Agent.Dispatch`:
   - Build the set of actions implied by the role's CLI binary. For V1 this is a single `bash:<cli-binary>` permission (e.g., `bash:codex`).
   - Call `core.EvaluateAction` for each action against `role.Permissions`.
   - **`PermDecisionHardDeny`**: return `fmt.Errorf("permission denied (never): action %s is explicitly forbidden for role %s", action.Raw, role.Name)` — hard-fail.
   - **`PermDecisionRequiresHuman`**: create an `attention.permission` item in the attention store with `kind="permission"`, `source="dispatch"`. Then return `fmt.Errorf("permission requires human approval: action %s for role %s (attention ID: %s)", action.Raw, role.Name, attentionID)` — hard-fail. True blocking (waiting for the answer) is deferred to Plan 117 when the attention queue is wired into the wait path.
   - **`PermDecisionUndeclared`**: if `policy.OnUndeclared == "warn"`, log a structured `slog.Warn` with fields `role`, `action`, and proceed. Otherwise (default `"deny"`), hard-fail: `fmt.Errorf("permission denied (undeclared): action %s is not declared for role %s; set policy.permissions.on_undeclared=warn to allow in development", action.Raw, role.Name)`.

4. Note: this does not yet intercept tool calls *within* the agent session; that requires hook integration (Claude Code's PreToolUse hook). V1 only checks the top-level CLI binary invocation.

#### 5c — Enforcement at MCP `orch_role_invoke` (`mcp/handlers_dispatch.go`)

1. In `handleRoleInvoke`, add a pre-invocation check using the same `EvaluateAction` logic:
   - Load the role via `roles.LoadRole(d.RoleDir, in.Role)` at the top of the handler (the dispatcher's `RoleDir` field is accessible via the closure).
   - Build the action `bash:<role.CLI>`.
   - Read `on_undeclared` from the dispatcher's policy.
   - `PermDecisionHardDeny` → return `fmt.Errorf(...)` before dispatching.
   - `PermDecisionRequiresHuman` → create attention.permission item, return hard-fail error.
   - `PermDecisionUndeclared` → if `on_undeclared == "warn"`, log and proceed; otherwise hard-fail.

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
- Dispatcher with undeclared action and `on_undeclared=deny` (default): returns error, no subprocess started.
- Dispatcher with undeclared action and `on_undeclared=warn`: logs warning, proceeds (mock agent returns success).
- Dispatcher with `requires_human` match: creates attention.permission item, returns hard-fail error, no subprocess started.

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
       RunID string `json:"run_id"` // required; use admin flag for cross-run listing
   }
   ```
   Output:
   ```go
   type checkpointListOutput struct {
       Items []attentionItemOutput `json:"items"`
   }
   ```
   Implementation (`handleCheckpointList`):
   - Validate `RunID` non-empty; return error "run_id is required" if absent. (An admin-only "list across all runs" endpoint, if needed in future, would be a separate tool or flag — keeping `run_id` required maintains symmetry with `orch_attention_list`.)
   - Call `as.ListAttentionByRun(ctx, runID, &core.AttentionCheckpoint)` to retrieve checkpoint-kind items for this run.
   - Convert to `attentionItemOutput` slice via `convertAttentionItems` (package-level unexported helper).
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
   - Call `as.AnswerAttention(ctx, id, core.AttentionAnswerApprove, answeredBy)` — writes `"approve"`.
   - Call `as.ResolveAttention(ctx, id)`.
   - Return `{Status: "approved", AttentionID: id}`.

3. **`orch_checkpoint_rollback`**

   Same structure as advance but calls `as.AnswerAttention(ctx, id, core.AttentionAnswerReject, answeredBy)`.
   Returns `{Status: "rejected", AttentionID: id}`.

   The resume logic in Phase 2 (`--resume-after-attention`) distinguishes `"approve"` vs `"reject"` via `core.AttentionAnswerApprove` / `core.AttentionAnswerReject` constants from Phase 0. A rejected checkpoint aborts the run (sets run state to `aborted`).

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

- `orch_checkpoint_list` with no `run_id` → validation error "run_id is required".
- `orch_checkpoint_list` with a valid `run_id` and no items → `{items: []}`.
- `orch_checkpoint_list` with one checkpoint item and one question item for the same run → only checkpoint appears.
- `orch_checkpoint_list` filtered by `run_id` → only items for that run, not items from another run.
- `orch_checkpoint_advance` on a known checkpoint → answer is `"approve"` (matching `core.AttentionAnswerApprove`), item is resolved.
- `orch_checkpoint_advance` on unknown ID → error "checkpoint not found".
- `orch_checkpoint_advance` on a non-checkpoint attention item → error "not a checkpoint".
- `orch_checkpoint_rollback` → answer is `"reject"` (matching `core.AttentionAnswerReject`), item is resolved.
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
- [ ] `coworker run --help` shows `--manifest` flag with a note that it bypasses architect dispatch.
- [ ] `coworker daemon --help` shows `--http-port` flag.
- [ ] `runDaemon` no longer contains the comment "Dispatcher is wired in a later plan phase".
- [ ] `runDaemon` uses `signal.NotifyContext` and the mutual-cancel errgroup pattern.
- [ ] `RunPhasesResult` has `StoppedAtPhaseClean`, `DirtyPhaseIndex`, `DirtyPhaseName`, `AttentionItemID` fields.
- [ ] `RunPhasesForPlan` returns early (does not call shipper) when any phase is dirty.
- [ ] `AttentionStore.GetUnansweredCheckpointForRun` filters by `kind=checkpoint AND source=phase-loop`.
- [ ] `core/permission.go` defines `ParsePermission`, `MatchPermission`, `EvaluateAction`, `PermissionDecision`.
- [ ] `core/attention.go` (or equivalent) defines `AttentionAnswerApprove = "approve"` and `AttentionAnswerReject = "reject"`.
- [ ] `coding/dispatch.go` calls permission check before agent dispatch; hard-deny returns error; undeclared defaults to hard-fail unless policy `on_undeclared=warn`.
- [ ] `requires_human` match creates an `attention.permission` item before hard-failing.
- [ ] Resume logic checks `item.Answer == core.AttentionAnswerApprove`; rejects abort the run.
- [ ] `orch_checkpoint_advance` writes `core.AttentionAnswerApprove`; `orch_checkpoint_rollback` writes `core.AttentionAnswerReject`.
- [ ] `orch_checkpoint_list` requires `run_id` parameter (returns validation error if absent).
- [ ] `mcp/server.go` `Tools()` returns 17 names.
- [ ] `mcp/handlers_checkpoint.go` implements all three handlers.
- [ ] All new handlers have exported `Call*` test helpers.
- [ ] No new exported symbol is missing a test.
- [ ] Architecture invariant test in `tests/architecture/` still passes (`core` does not import `coding`).
- [ ] `docs/specs/001-plan-manifest.md` has Plan 116 entry added.

---

## Code Review

### Pre-Implementation Review (Codex, 2026-04-26)

**Must Fix**

1. `[FIXED]` **B-1: `coworker run` should accept a PRD, not a manifest.** The spec mandates `coworker run <prd.md>` with the architect role producing the spec + manifest. Plan currently bypasses architect/planner and loads a pre-built manifest, which means the PRD-to-PR autopilot never runs the spec generation phase. Fix: Phase 1 must dispatch the architect role first to produce the spec + manifest, then iterate ready plans. Add `--manifest` only as an optional skip-architect path for testing.

   → Response: Phase 2 (formerly Phase 1) now implements the full PRD → architect dispatch → spec-approved checkpoint → planner per-plan → plan-approved checkpoint → RunPhasesForPlan flow. The `--manifest <path>` flag is kept as an optional bypass with an explicit logged warning that it is for testing only. Resume logic reconstructs state from the event log (see Phase 2 resume context reconstruction).

2. `[FIXED]` **B-5: Undeclared actions must default-deny per spec, not warn-and-proceed.** The spec §Security Model is explicit: "If an action is not explicitly allowed by the role and not permitted by policy, Coworker records an `attention.permission` item and blocks the job." Plan's "warn and proceed for V1" violates this. Fix: hard-fail on undeclared actions with a clear error message OR create a blocking attention.permission and wait. The lack of true attention-blocking infrastructure is not justification for downgrading the security model — better to hard-fail than silently allow.

   → Response: Phase 5 now implements default-deny for all undeclared actions. `PermDecisionHardDeny` and `PermDecisionRequiresHuman` both hard-fail (the latter also creates an `attention.permission` item before failing). `PermDecisionUndeclared` also hard-fails by default. Operators may set `policy.permissions.on_undeclared: warn` to opt into the old warn-and-proceed behaviour for development environments. This opt-in is explicit and documented in the flag error message.

3. `[FIXED]` **B-4 + B-1 resume: rollback (`reject`) should not resume as if approved.** Plan's resume logic only checks `attention.answer != ""`; a rejected checkpoint resolves with `"reject"` and would resume the workflow. Fix: resume must check `attention.answer == "approve"` (or whatever the advance writes), not just non-empty. Also distinguish advance/rollback in MCP contract.

   → Response: Phase 0 defines `core.AttentionAnswerApprove = "approve"` and `core.AttentionAnswerReject = "reject"`. Phase 6 updates `orch_checkpoint_advance` to write `AttentionAnswerApprove` and `orch_checkpoint_rollback` to write `AttentionAnswerReject`. Phase 2 resume logic explicitly checks `item.Answer == core.AttentionAnswerApprove`; a `"reject"` answer aborts the run and sets run state to `aborted`.

4. `[FIXED]` **B-2: errgroup lifecycle bug — MCP exit doesn't cancel HTTP.** If MCP stdio exits cleanly (e.g., on disconnect), the derived `errgroup.WithContext` doesn't propagate. Fix: explicitly cancel the parent context when either goroutine returns, OR use a `signal.NotifyContext` + close-broadcast pattern.

   → Response: Phase 3 now uses `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` as the root context, and each goroutine in the errgroup calls `defer cancel()` so that whichever exits first cancels the other. The pattern matches the exact snippet from the review finding.

**Should Fix**

5. `[FIXED]` **B-1 resume context reconstruction is hand-waved.** The plan accepts `--resume-after-attention <id>` but doesn't specify how scheduler maps, worktree state, and partial plan progress are reconstructed. Fix: persist run-level scheduler state in DB (or rely on event-log replay) so resume is a function of run_id + attention_id, not just attention_id.

   → Response: Phase 2 now specifies resume context reconstruction in detail: read run row via `item.RunID`, replay `events` table to rebuild `completed`/`active` maps, re-create missing worktrees via idempotent `worktree.Open(planID)`. Documented invariant: "Run state is fully recoverable from event log + filesystem worktrees."

6. `[FIXED]` **B-4: dirty-phase detection should filter by checkpoint kind.** Plan grabs the last unanswered attention item; could surface a permission/question item incorrectly. Fix: query specifically for `kind=checkpoint` AND `source=phase-loop`.

   → Response: Phase 1 adds `AttentionStore.GetUnansweredCheckpointForRun(ctx, runID, source string)` which queries with `WHERE kind = 'checkpoint' AND source = ? AND answered_at IS NULL`. The call site in `RunPhasesForPlan` passes `source = "phase-loop"`. Test cases assert that permission/question items with the same `run_id` do not surface.

7. `[FIXED]` **Phase ordering: Phase 1 depends on Phase 4's `StoppedAtPhaseClean` field.** Cannot implement in declared order; Phase 4 must move before Phase 1 OR the field must land in a "Phase 0" prep step. Fix: reorder phases as 0 (shared types) → 4 (workflow stop) → 1 (run command) → 2 (HTTP) → 3 (Dispatcher) → 5 (permissions) → 6 (checkpoint MCP).

   → Response: Phases reordered exactly as recommended: Phase 0 (shared types), Phase 1 (B-4 workflow stop), Phase 2 (B-1 run command), Phase 3 (B-2 HTTP daemon), Phase 4 (B-3 Dispatcher), Phase 5 (B-5 permissions), Phase 6 (B-6 checkpoint MCP). The architecture overview and self-review checklist updated accordingly.

8. `[FIXED]` **B-6: `orch_checkpoint_list` should accept a `run_id` filter consistent with `orch_attention_list`.** Plan's spec for list-without-run_id-returns-all is asymmetric with the attention list contract. Fix: require `run_id` for symmetry, with a documented "list across runs" admin path if needed.

   → Response: Phase 6 now makes `run_id` a required field on `orch_checkpoint_list`. The handler returns a validation error `"run_id is required"` if absent. A potential future admin-only "list across runs" path is noted as a separate tool/flag if needed. Test cases assert the validation error.

**Nice to Have**

9. `[WONTFIX]` Plan does not adopt a router library. Codex confirmed plain `http.ServeMux` is fine; no change needed.

10. `[WONTFIX]` SSE backpressure is already handled (eventbus `select default` drops to slow subscribers). No change needed.

11. `[WONTFIX]` `buildDispatcher` already handles missing role-dir/prompt-dir via existing role loader. No change needed.

---

## Post-Execution Report

*Filled in after implementation is complete and tests pass.*

### Summary

### Deviations from plan

### Tests added

### Open items carried forward
