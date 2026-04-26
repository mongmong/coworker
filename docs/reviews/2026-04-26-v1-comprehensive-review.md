# Coworker V1 Comprehensive Review — 2026-04-26

## Race Detector Results

Command run:

```text
go test -race ./... -count=1 -timeout 120s
```

Result: PASS. Package counts: 21 `ok`, 0 failed, 4 `[no test files]`. Test inventory via `go test -list . ./...`: 450 tests. No race detector warnings, data race blocks, or panic lines were emitted.

Verbatim output:

```text
ok  	github.com/chris/coworker/agent	1.050s
ok  	github.com/chris/coworker/cli	1.112s
?   	github.com/chris/coworker/cmd/coworker	[no test files]
ok  	github.com/chris/coworker/coding	3.020s
ok  	github.com/chris/coworker/coding/eventbus	1.070s
ok  	github.com/chris/coworker/coding/humanedit	1.085s
ok  	github.com/chris/coworker/coding/manifest	1.174s
ok  	github.com/chris/coworker/coding/phaseloop	1.781s
ok  	github.com/chris/coworker/coding/policy	1.038s
ok  	github.com/chris/coworker/coding/quality	1.561s
ok  	github.com/chris/coworker/coding/roles	1.025s
ok  	github.com/chris/coworker/coding/session	1.366s
ok  	github.com/chris/coworker/coding/shipper	1.236s
ok  	github.com/chris/coworker/coding/stages	1.014s
ok  	github.com/chris/coworker/coding/supervisor	1.263s
ok  	github.com/chris/coworker/coding/workflow	1.478s
ok  	github.com/chris/coworker/core	1.013s
?   	github.com/chris/coworker/internal/testutil	[no test files]
ok  	github.com/chris/coworker/mcp	7.671s
?   	github.com/chris/coworker/spike/003/cmd/dispatch-cycle	[no test files]
?   	github.com/chris/coworker/spike/003/cmd/sse-listener	[no test files]
ok  	github.com/chris/coworker/store	5.796s
ok  	github.com/chris/coworker/tests/architecture	1.074s
ok  	github.com/chris/coworker/tests/integration	1.486s
ok  	github.com/chris/coworker/tui	1.040s
```

## Blockers (must fix before production)

### B-1: Autopilot is a library, not an end-to-end daemon flow
- Severity: Blocker
- Location: coding/workflow/build_from_prd.go:145
- Finding: `BuildFromPRDWorkflow.Run` returns scheduled plans and explicitly leaves iteration over `ReadyPlans` plus `RunPhasesForPlan` to a caller, but no non-test caller wires this into the daemon or a `coworker run` command. `rg` found `NewBuildFromPRDWorkflow`, `RunPhasesForPlan`, `NewWorktreeManager`, and `Shipper{}` only in implementation files and tests, not in CLI/daemon production wiring.
- Evidence: `coding/workflow/build_from_prd.go:145` says "The caller is responsible for iterating over ReadyPlans"; `cli/daemon.go:72` only creates the MCP server; `cli` has registered commands for `daemon`, `invoke`, `session`, `dashboard`, `watch`, `advance`, `rollback`, `config inspect`, `record-human-edit`, `init`, `plugin install`, and `version`, but no `run`.
- Recommendation: Add a real scheduler/run coordinator owned by the daemon or `coworker run`, instantiate `BuildFromPRDWorkflow`, `WorktreeManager`, `PhaseExecutor`, `Shipper`, policy, stores, and stage registry, and drive ready-plan iteration through completion.

### B-2: Daemon does not serve the HTTP/SSE API used by `watch` and TUI
- Severity: Blocker
- Location: cli/daemon.go:69
- Finding: The daemon creates an in-memory event bus and starts the MCP stdio server, but it never starts an HTTP server, never mounts `eventbus.SSEHandler`, and never exposes `/events`, `/runs`, `/jobs`, or `/attention`. `coworker watch` and the TUI both assume `http://localhost:7700`.
- Evidence: `cli/daemon.go:69` creates `eventbus.NewInMemoryBus()` and `cli/daemon.go:87` runs only `srv.Run(ctx)`; `cli/watch.go:49` defaults to port 7700; `tui/events.go:102` builds `baseURL + "/events"` and `tui/events.go:141` fetches `/runs`.
- Recommendation: Add a daemon HTTP server with lifecycle tied to the daemon context, mount SSE and REST snapshot/attention handlers, and pass the same event bus/store instances into both MCP and HTTP surfaces.

### B-3: Daemon registers `orch_role_invoke` as `not_implemented`
- Severity: Blocker
- Location: cli/daemon.go:73
- Finding: `runDaemon` calls `mcpserver.NewServer` with DB and EventBus but no `Dispatcher`, so `orch_role_invoke` is registered through the stub branch and cannot dispatch roles from MCP clients.
- Evidence: `cli/daemon.go:73` constructs `ServerConfig{DB, EventBus}` and the comment at `cli/daemon.go:76` says Dispatcher is wired later; `mcp/server.go:153` checks `s.cfg.Dispatcher != nil`, otherwise `mcp/server.go:167` returns `stubResult()`.
- Recommendation: Build a production `coding.Dispatcher` in daemon startup, including role/prompt dirs, agent selection, supervisor, policy, DB, and logger, then pass it into `ServerConfig`.

### B-4: Dirty phases can still advance to later phases and ship
- Severity: Blocker
- Location: coding/workflow/build_from_prd.go:229
- Finding: `PhaseExecutor.Execute` returns `Clean=false` with no error after max fix cycles are exhausted, and `RunPhasesForPlan` logs the dirty result but continues through remaining phases and calls the shipper.
- Evidence: `coding/phaseloop/executor.go:203` enters the exhausted path, `coding/phaseloop/executor.go:236` returns `Clean: false, nil`; `coding/workflow/build_from_prd.go:229` only stops on `err`, and `coding/workflow/build_from_prd.go:247` ships after the loop.
- Recommendation: Treat `Clean=false` as a blocking phase-clean checkpoint: persist the attention item, stop the workflow, and resume only after an explicit answer/override path.

### B-5: Role permissions are parsed but not enforced
- Severity: Blocker
- Location: core/role.go:144
- Finding: Role YAML declares `allowed_tools`, `never`, and `requires_human`, but runtime dispatch never checks them. The only Go references outside tests are the data structs and policy loader; no boundary gates shell commands, MCP operations, path writes, or undeclared actions.
- Evidence: `core/role.go:144` defines `RolePermissions`; `coding/dispatch.go:83` loads a role and `coding/dispatch.go:90` validates only required inputs before dispatching; `agent/cli_agent.go:33` starts the configured CLI directly with no permission wrapper.
- Recommendation: Implement a permission evaluator at each controlled boundary: ephemeral CLI wrapper, MCP handler authorization, plugin hook ingestion, and path/write checks. Undeclared actions should create `attention.permission`; `never` actions should hard-fail.

### B-6: Checkpoint tools are documented in plugins but absent from MCP
- Severity: Blocker
- Location: mcp/server.go:377
- Finding: The plugin commands instruct agents to call checkpoint list/advance/rollback tools, but the MCP server does not register any `orch_checkpoint_*` tools. Approving checkpoints from Claude/OpenCode/Codex cannot work through the documented plugin path.
- Evidence: `mcp/server.go:377` lists the complete registered tool set and includes no checkpoint tools; `plugins/coworker-claude/commands/approve.md:192` calls `mcp__coworker__orch_checkpoint_list`; `plugins/coworker-codex/skills/coworker-orchy.md:209` lists `orch_checkpoint_list`, `orch_checkpoint_advance`, and `orch_checkpoint_rollback`.
- Recommendation: Add checkpoint store/schema support and MCP handlers, or remove the plugin commands until a real attention/checkpoint path exists.

## Important Gaps (should fix before wide use)

### I-1: Findings are directly mutable in SQLite
- Severity: Important
- Location: store/finding_store_test.go:125
- Finding: The store API exposes only insert/resolve, but direct SQL can update immutable finding fields. This does not satisfy the review prompt's "can a finding row be UPDATE'd directly?" invariant.
- Evidence: `store/finding_store_test.go:125` states direct SQL update should work; `store/migrations/001_init.sql:431` creates `findings` without triggers that reject updates to `path`, `line`, `severity`, `body`, or `fingerprint`.
- Recommendation: Add SQLite triggers that abort updates to immutable columns and allow only `resolved_by_job_id` / `resolved_at` transitions, then keep the store-layer tests.

### I-2: Stage customization can drop the tester role
- Severity: Important
- Location: coding/workflow/build_from_prd.go:208
- Finding: When a `StageRegistry` is set, `RunPhasesForPlan` copies only `phase-review` roles into `PhaseExecutor.ReviewerRoles`. The default stage registry separates `phase-review` from `phase-test`, so a default registry would run only reviewers and omit tester.
- Evidence: `coding/stages/defaults.go:15` has `phase-review: {"reviewer.arch", "reviewer.frontend"}` and `coding/stages/defaults.go:16` has `phase-test: {"tester"}`; `coding/workflow/build_from_prd.go:208` reads only `"phase-review"`; `coding/phaseloop/executor.go:24` includes tester only in its fallback default.
- Recommendation: Model dev/review/test as separate stage role lists in `PhaseExecutor`, or merge registry `phase-review` and `phase-test` explicitly before fan-out.

### I-3: Role-level `applies_when` is ignored
- Severity: Important
- Location: coding/roles/reviewer_frontend.yaml:16
- Finding: `reviewer_frontend.yaml` declares `applies_when`, but `core.Role` has no `AppliesWhen` field and the phase loop dispatches configured reviewer roles without evaluating role predicates.
- Evidence: `coding/roles/reviewer_frontend.yaml:16` defines `applies_when`; `core/role.go:119` to `core/role.go:130` lists the role fields and omits it; `coding/phaseloop/executor.go:280` dispatches each role directly.
- Recommendation: Add `AppliesWhen` to `core.Role`, load it in `roles.LoadRole`, evaluate it before dispatch, and emit `job.skipped` when false.

### I-4: Supervisor engine errors silently pass jobs
- Severity: Important
- Location: coding/dispatch.go:277
- Finding: If deterministic supervisor evaluation returns an error, the dispatcher logs it and sets `Pass: true`, allowing state advancement. That violates the no-silent-state-advance invariant for supervisor failures.
- Evidence: `coding/dispatch.go:277` calls `d.Supervisor.Evaluate`; `coding/dispatch.go:279` logs the error; `coding/dispatch.go:282` converts it to `core.SupervisorVerdict{Pass: true}`.
- Recommendation: Treat supervisor engine errors as retryable job failures or compliance-breach attention items, not passes.

### I-5: Quality judge errors silently drop rule outcomes
- Severity: Important
- Location: coding/quality/evaluator.go:237
- Finding: Checkpoint-time quality evaluation logs judge errors and continues without writing a verdict event. This leaves no durable record for the failed rule and can advance a checkpoint without the intended quality gate.
- Evidence: `coding/quality/evaluator.go:237` calls `Judge.Evaluate`; `coding/quality/evaluator.go:241` logs the error and `continue`s; verdict events are only written after that at `coding/quality/evaluator.go:245`.
- Recommendation: Emit a `quality.verdict` event with error status and route block-capable rule errors to attention or retry.

### I-6: TUI attention answers call a REST endpoint that does not exist
- Severity: Important
- Location: tui/events.go:109
- Finding: The TUI's `a`, `r`, and `p` keys call `submitAnswer`, which posts to `/attention/{id}/answer`; there is no daemon HTTP server or REST handler for that endpoint. The key works in local model tests but not end-to-end.
- Evidence: `tui/keybindings.go:149` calls `submitAnswer`; `tui/events.go:113` builds `/attention/%s/answer`; `cli/daemon.go:87` only runs MCP stdio.
- Recommendation: Implement the REST endpoint backed by `AttentionStore.AnswerAttention`/`ResolveAttention`, or route TUI answers through an MCP client path.

### I-7: Exec contexts have cancellation but no runtime deadlines
- Severity: Important
- Location: agent/cli_agent.go:33
- Finding: `exec.CommandContext` is used, but role budgets such as `max_wallclock_minutes` are never converted into context deadlines. A stuck CLI can run until user/process cancellation.
- Evidence: `agent/cli_agent.go:33` uses the inherited context; `coding/quality/judge.go:55`, `coding/manifest/worktree.go:222`, `coding/humanedit/recorder.go:36`, and `coding/shipper/gh.go:22` do the same; `core/role.go:151` defines wallclock budget fields.
- Recommendation: Wrap subprocess calls in `context.WithTimeout` from role/job policy, and set `exec.Cmd.WaitDelay` where applicable.

### I-8: OpenCode HTTP-primary path exists only in docs/assets
- Severity: Important
- Location: plugins/coworker-opencode/skills/coworker-orchy.md:6
- Finding: The OpenCode plugin says the daemon creates HTTP sessions, sends messages, listens to OpenCode SSE, and deletes sessions, but no Go implementation exists for the OpenCode HTTP dispatch path.
- Evidence: `plugins/coworker-opencode/skills/coworker-orchy.md:6` describes HTTP-primary worker mode; `rg` found no production Go client for `/session`, `/event`, or `/session/{id}/message`.
- Recommendation: Add an OpenCode agent/router implementation or revise the plugin to use the implemented MCP pull path only.

### I-9: Agent JSONL logs are not persisted
- Severity: Important
- Location: agent/cli_handle.go:103
- Finding: The spec requires per-job JSONL under `.coworker/runs/<run-id>/jobs/<job-id>.jsonl`, and the TUI reads that path, but `CliAgent` only parses stdout/stderr in memory and never writes the stream to disk.
- Evidence: `agent/cli_handle.go:103` parses stdout from the pipe; `tui/events.go:194` expects `.coworker/runs/<runID>/jobs/<jobID>.jsonl`.
- Recommendation: Add a job log writer in the agent wrapper and persist raw stream events before/while parsing them.

### I-10: Event timestamps are dropped when reading from SQLite
- Severity: Important
- Location: store/event_store.go:128
- Finding: `ListEvents` scans `created_at` into `createdAtStr` but never parses or assigns it to `e.CreatedAt`. Consumers of historical events receive zero timestamps.
- Evidence: `store/event_store.go:128` declares `createdAtStr`; `store/event_store.go:129` scans it; `store/event_store.go:137` only sets `e.Kind` before appending.
- Recommendation: Parse `createdAtStr` with the stored timestamp layout and assign `e.CreatedAt`, returning an error on parse failure.

### I-11: Schema is still a subset of the spec data model
- Severity: Important
- Location: store/migrations/001_init.sql:407
- Finding: The actual migrations do not include the spec's `plans`, `checkpoints`, `supervisor_events`, or `cost_events` tables, and `runs`/`jobs` omit multiple spec fields such as `prd_path`, `spec_path`, `plan_id`, `phase_index`, and cost columns.
- Evidence: `store/migrations/001_init.sql:407` defines `runs` with only `id`, `mode`, `state`, timestamps; `store/migrations/001_init.sql:416` defines `jobs` without plan/phase/cost; migrations 002-005 add only attention, dispatches, workers, and nullable event run IDs.
- Recommendation: Either migrate the schema to match the V1 spec or update the spec/manifest to mark those tables and columns as deferred.

## Polish (nice to have)

### P-1: Direct CLI placeholders look shipped
- Severity: Polish
- Location: cli/advance.go:18
- Finding: `coworker advance` and `coworker rollback` are wired commands, but they print "not yet implemented" after opening the DB and checking for a session.
- Evidence: `cli/advance.go:18` calls the command a placeholder and `cli/advance.go:61` prints "not yet implemented"; `cli/rollback.go:81` says rollback is a placeholder and `cli/rollback.go:125` prints "not yet implemented".
- Recommendation: Hide these commands, mark them experimental in help, or implement real attention/checkpoint state transitions.

### P-2: No replay or live-agent test layer is present
- Severity: Polish
- Location: docs/specs/000-coworker-runtime-design.md:860
- Finding: The spec calls for replay tests and optional `COWORKER_LIVE=1` tests, but the tree has only `tests/architecture` and `tests/integration`; no `tests/replay` directory and no `COWORKER_LIVE` references were found.
- Evidence: `find tests -maxdepth 3 -type d` returned only `tests`, `tests/architecture`, and `tests/integration`; `rg COWORKER_LIVE` returned no matches.
- Recommendation: Add replay fixtures and a small live smoke test suite gated by `COWORKER_LIVE=1`.

### P-3: Packages with no test files remain
- Severity: Polish
- Location: cmd/coworker/main.go:7
- Finding: Four packages report `[no test files]`: `cmd/coworker`, `internal/testutil`, `spike/003/cmd/dispatch-cycle`, and `spike/003/cmd/sse-listener`.
- Evidence: Race detector output above; `cmd/coworker/main.go:7` is an untested entry point.
- Recommendation: Add a trivial main package smoke test where useful, or exclude archived spike command packages from `./...` if they are not production packages.

### P-4: Exported-symbol coverage is uneven [UNVERIFIED]
- Severity: Polish
- Location: coding/workflow/build_from_prd.go:23
- Finding: A static name search found exported functions/types with no direct `_test.go` reference, including `BuildFromPRDWorkflow`, `RunPhasesResult`, `BuildFromPRDResult`, `RunRepository`, `RuleSet` in multiple packages, `CliAgent`, `EventBus`, `AttentionItem.IsAnswered`, and `AttentionStore.ResolveAttention`. This is a coarse search and may miss indirect behavioral coverage.
- Evidence: `coding/workflow/build_from_prd.go:23` defines `BuildFromPRDWorkflow`; `core/attention.go:147` defines `IsAnswered`; `store/attention_store.go:173` defines `ResolveAttention`.
- Recommendation: Add targeted tests for exported APIs intended for callers, or make helper types unexported.

## Strengths (what is working well)

- `WriteEventThenRow` writes event rows before projection updates in one transaction and rolls both back on apply failure (`store/event_store.go:28`).
- Artifact storage uses only `id`, `job_id`, `kind`, and `path`, so durable content is not inlined into SQLite (`store/migrations/001_init.sql:448`).
- SSE stream cleanup is sound when used: `SSEHandler` subscribes a bounded channel and defers `Unsubscribe` on disconnect (`coding/eventbus/sse.go:100`).
- The event bus fan-out is non-blocking and drops slow subscribers instead of blocking producers (`coding/eventbus/bus.go:37`).
- Worker registry state is stored in SQLite, not an unsafe shared map; store methods use SQL transactions/events for mutations.
- The race detector passes across all current packages.
- Codex `danger-full-access` risk is surfaced in plugin install output and setup docs (`cli/plugin_install.go:133`, `plugins/coworker-codex/setup.md:47`).

## Missing from V1

- `coworker run <prd.md>` autopilot entry point.
- A daemon scheduler that drives `build-from-prd` ready plans, worktrees, phases, checkpoints, and shipping.
- HTTP/SSE daemon server for `watch`, TUI snapshots, TUI answers, and live event streaming.
- Real `orch_role_invoke` in daemon mode.
- `orch_checkpoint_list`, `orch_checkpoint_advance`, and `orch_checkpoint_rollback`.
- Blocking checkpoint semantics for `phase-clean`, `ready-to-ship`, `spec-approved`, and `plan-approved`.
- Runtime role permission enforcement.
- Role-level `applies_when` loading/evaluation.
- OpenCode HTTP dispatch implementation.
- Per-job JSONL log persistence.
- Replay tests under `tests/replay/`.
- Live-agent tests gated by `COWORKER_LIVE=1`.
- Full spec schema tables/columns for plans, checkpoints, supervisor events, cost ledger, and richer run/job metadata.

## TODO/FIXME Inventory

| file | line | text |
|---|---:|---|
| mcp/server.go | 15 | `// They are nil when DB is nil (stubs active during early plan phases).` |
| mcp/server.go | 29 | `// registers stub handlers regardless.` |
| mcp/server.go | 45 | `// notImplemented is the shared output type for stub tool handlers.` |
| mcp/server.go | 50 | `// stubResult returns the standard not-implemented response.` |
| mcp/server.go | 51 | `func stubResult() notImplemented {` |
| mcp/server.go | 52 | `return notImplemented{Status: "not_implemented"}` |
| mcp/server.go | 55 | `// NewServer creates an MCP server, registers all orch.* tool stubs, and` |
| mcp/server.go | 107 | `// registerTools wires all orch.* tool stubs onto the inner MCP server.` |
| mcp/server.go | 136 | `return nil, stubResult(), nil` |
| mcp/server.go | 146 | `return nil, stubResult(), nil` |
| mcp/server.go | 168 | `return nil, stubResult(), nil` |
| mcp/server.go | 198 | `return nil, stubResult(), nil` |
| mcp/server.go | 208 | `return nil, stubResult(), nil` |
| mcp/server.go | 230 | `return nil, stubResult(), nil` |
| mcp/server.go | 260 | `return nil, stubResult(), nil` |
| mcp/server.go | 270 | `return nil, stubResult(), nil` |
| mcp/server.go | 292 | `return nil, stubResult(), nil` |
| mcp/server.go | 322 | `return nil, stubResult(), nil` |
| mcp/server.go | 332 | `return nil, stubResult(), nil` |
| mcp/server.go | 367 | `return nil, stubResult(), nil` |
| coding/shipper/shipper.go | 26 | `// True blocking is deferred to Plan 103; here we only record.` |
| coding/shipper/shipper.go | 86 | `// In V1, true blocking is deferred — we record and proceed.` |
| coding/phaseloop/executor.go | 19 | `// supply a stub.` |
| coding/phaseloop/executor.go | 36 | `// In production this is *coding.Dispatcher; in tests a stub may be used.` |
| coding/phaseloop/executor.go | 45 | `// is created (true blocking is deferred to Plan 103).` |
| coding/phaseloop/executor.go | 219 | `// answer) is deferred to Plan 103; here we only record the item.` |
| cli/rollback.go | 19 | `This placeholder currently reports that rollback is not implemented.` |
| plugins/coworker-claude/skills/coworker-role-developer.md | 37 | `is not applicable (e.g., pure scaffolding, integration deferred to a later` |
| plugins/coworker-codex/skills/coworker-role-developer.md | 40 | `is not applicable (e.g., pure scaffolding, integration deferred to a later` |
| plugins/coworker-opencode/skills/coworker-role-developer.md | 41 | `is not applicable (e.g., pure scaffolding, integration deferred to a later` |
