# Plan Manifest — coworker V1

**Status:** Draft
**Spec:** `docs/specs/000-coworker-runtime-design.md`
**Role:** Companion to the runtime spec. Every `docs/plans/NNN-*.md` cites its entry here.

---

## Purpose

Ordered decomposition of the V1 scope from the runtime spec into shippable plans, with dependencies, process variants, and testing discipline. This is the artifact the spec's `architect` role is meant to produce — produced manually for the bootstrap phase because no runtime exists yet to run it.

## Stack

Go. Single static binary. `modernc.org/sqlite` (pure Go), Bubble Tea TUI, cobra CLI, MCP SDK via `modelcontextprotocol/go-sdk` or `mark3labs/mcp-go` (decided in Plan 104 based on spike findings).

See `CLAUDE.md` §Coding Conventions for concurrency, error handling, and package layout discipline.

## Conventions

- **Plan IDs:** `000` = project bootstrap; `001–099` = Phase 0 spikes and pre-implementation work; `100–199` = V1 runtime; `200+` reserved for V2.
- **`blocks_on`:** declared dependency order. It does not prove file independence. The scheduler enforces `blocks_on`, uses optional path reservations when present, and otherwise surfaces git conflicts or dirty-worktree overlap when they occur.
- **Plan flavors** (three distinct process variants):
  - *Spike.* Explore → observe → document → decide. Output is a report + go/no-go, not product code. Full workflow steps 1–6 are not applied — the spike itself IS the design phase for downstream plans.
  - *Runtime.* Full 6-step workflow from `docs/development-workflow.md`, phase-by-phase commits.
  - *Plugin.* Same as runtime plus live-agent smoke tests in a target CLI (gated by `COWORKER_LIVE=1`).
- **Per-plan checkpoints:** every plan has `plan-approved` (step 2 gate) and `ready-to-ship` (step 6 gate).
- **Cross-plan checkpoint — `phase-0-verdict`:** fires after spikes 001–003 complete. The user decides whether Plan 104 proceeds as-specified or shrinks scope (e.g., Codex ephemeral-only if persistent MCP-pull is infeasible).

## Architecture posture (locked at 000)

Two cheap bets preserve future flexibility without over-engineering:

1. **`core/` vs `coding/` package split.** `core/` holds domain-neutral primitives (runs, jobs, events, supervisor framework, attention queue, worker registry, cost ledger, `Agent` protocol). `coding/` holds coding-specific roles, rules, workflows, plugins. Imports flow core → coding, never the reverse. Enforced by an import test.
2. **`Agent` is a protocol, not a CLI-binary struct.** Shipped with one implementation (`CliAgent`) in Plan 100. Future HTTP-backed agents or library agents drop in without touching dispatch code.

These are deliberately cheap investments — ~50 LOC each, no plan cost.

## Workflow customization (V1)

Level 1 and Level 2 customization ship in V1 (see spec §Workflow customization). Level 3 (user-defined workflow classes in Go) is deferred to V2.

- **Level 1** — configurable role list per named stage (`policy.yaml → workflow_overrides`). Lands in Plan 115, phase 4.
- **Level 2** — per-role `applies_when` predicate. Lands in Plan 111, phase 6.

---

## Plans (ordered)

### Phase A — Foundation & Phase 0 spikes

#### 000 — Project bootstrap
- **Flavor:** Runtime (small)
- **Blocks on:** —
- **Purpose:** Go module skeleton, tooling, CI, minimum CLI entry.
- **Phases:**
  1. `go.mod` + package layout: `cmd/coworker/`, `core/`, `coding/`, `cli/`, `tui/`, `mcp/`, `store/`, `agent/`, `internal/`, `testdata/`.
  2. Tooling: `golangci-lint` config (govet + staticcheck + errcheck + gosec + gocyclo), `Makefile` with `test`, `lint`, `build`.
  3. `coworker --version` + cobra root command.
  4. CI skeleton: GitHub Actions — lint + test on push. No coverage gate yet.
  5. Import-discipline test: `core` must not import `coding`.
- **Tests:** build skeleton only.

#### 001 — Spike: Claude Code persistent + MCP pull
- **Flavor:** Spike
- **Blocks on:** 000
- **Plan file:** `docs/plans/001-spike-claude-code.md`
- **Purpose:** Verify orchy-style polling, `orch.job.complete` round-trip, tmux wake reliability, MCP notification support, and compaction resilience.
- **Process:** Throwaway branch; prototype a minimal MCP server (Go, official SDK) + skill; observe behavior; write report as the plan file's post-exec section.
- **Decision output:** Does Claude Code support persistent MCP pull reliably? Result feeds `phase-0-verdict` checkpoint.
- **Tests:**
  1. MCP server connection + tool discovery (stdio transport)
  2. Tool round-trip: `orch_next_dispatch` → execute → `orch_job_complete`
  3. Skill-driven polling loop in persistent session
  4. tmux send-keys wake-idle reliability (5 trials, varying idle durations)
  5. MCP server-to-client notifications (push wake)
  6. Context-window compaction — polling instruction survival
  7. `claude -p --output-format stream-json` ephemeral baseline

#### 002 — Spike: Codex persistent + MCP pull
- **Flavor:** Spike
- **Blocks on:** 000
- **Plan file:** `docs/plans/002-spike-codex.md`
- **Purpose:** Same questions as 001 for Codex. Additionally: verify `codex exec` ephemeral mode with MCP, `--json` event capture, `--output-schema` for structured output, and sandbox interaction with MCP servers.
- **Process:** Reuses spike 001 MCP server binary. If persistent polling fails, document why and recommend ephemeral-only.
- **Decision output:** Codex persistent or ephemeral-only? Result feeds `phase-0-verdict` checkpoint.
- **Tests:**
  1. MCP server connection + tool discovery
  2. `codex exec` tool round-trip
  3. `codex exec --json` JSONL event capture
  4. Interactive session persistent polling feasibility
  5. Session resume with MCP tools
  6. `--output-schema` structured output enforcement
  7. Sandbox mode interaction with MCP server file I/O

#### 003 — Spike: OpenCode server dispatch
- **Flavor:** Spike
- **Blocks on:** 000
- **Plan file:** `docs/plans/003-spike-opencode.md`
- **Purpose:** Baseline the HTTP-dispatch path via `opencode serve`. Verify REST API session/message lifecycle, SSE event stream richness, concurrent sessions, Go SDK viability, and `opencode run --format json` as ephemeral alternative.
- **Process:** Prototype Go client against the server API; test dispatch cycle end-to-end.
- **Decision output:** Is HTTP dispatch via `opencode serve` viable? Go SDK usable or raw HTTP? Result feeds `phase-0-verdict` checkpoint.
- **Tests:**
  1. Server startup + OpenAPI discovery
  2. Session creation + message sending via REST
  3. SSE event stream subscription + real-time updates
  4. Full dispatch cycle: create → send → capture → close
  5. `opencode run --format json` ephemeral alternative
  6. MCP client support (hybrid path)
  7. Server session lifecycle + concurrency
  8. Go SDK (`github.com/sst/opencode-sdk-go`) viability

---

### Phase B — Runtime core

#### 100 — Thin end-to-end
- **Flavor:** Runtime
- **Blocks on:** 000
- **Purpose:** Minimum viable product. `coworker invoke reviewer.arch --diff <path> --spec <path>` spawns ephemeral Codex, captures findings JSON, persists via event-log-before-state. Load-bearing invariants introduced here.
- **Phases:**
  1. SQLite schema (minimum viable): `runs`, `jobs`, `events` (with `sequence`, `idempotency_key`, `causation_id`, `correlation_id`, `schema_version` per §Event Log Semantics), `findings`, `artifacts`, `schema_migrations`. `store/` package with migration runner.
  2. Event-first write helper (`store.WriteEventThenRow`) + crash-injection test (kill between event + row; replay reconciles). Idempotency key support.
  3. Role loader: YAML schema (Go struct + validator tags), prompt template resolution, permission parsing (§Security Model), ship one role (`reviewer.arch.yaml` + `prompts/reviewer.arch.md`).
  4. `Agent` protocol in `core/agent/` + `CliAgent` in `agent/` (Codex only for now; `os/exec`, stdout pipe, `json.Decoder` streaming). Ephemeral shell wrapper records command, env, cwd, stdout/stderr, exit status.
  5. Minimal security primitives: path validation against role write scope, permission matching (command-based), `partially-observed` audit event shape.
  6. Ephemeral dispatch path: context snapshot assembly → prompt render → `agent.Dispatch` → findings capture → persist.
  7. Findings fingerprint + immutability (schema constraint — only `resolved_by_job_id` / `resolved_at` mutable; enforced by store layer, tested).
  8. `coworker invoke` cobra command.
  9. Tests: unit + Layer 2 mock-CLI harness (shell-script mock under `testdata/mocks/codex`).
- **Load-bearing invariants introduced:** event-before-state, findings immutable, file artifacts as pointers, event idempotency.

#### 101 — Supervisor contract
- **Flavor:** Runtime
- **Blocks on:** 100
- **Purpose:** Deterministic rule engine, retry-with-feedback, seed rule catalog.
- **Phases:**
  1. Rule predicate mini-DSL (helpers: `git_current_branch_matches`, `last_commit_msg_contains`, `pr_head_branch_matches`, `all_findings_have`, etc.).
  2. `supervisor-contract.yaml` loader + rule evaluator.
  3. After-job hook: evaluate rules; veto → retry with `message` injected into next prompt.
  4. Max-retry ceiling from `policy.yaml` + escalation to `compliance-breach` checkpoint (checkpoint surface deferred to 103).
  5. Seed rules for `reviewer.arch` (findings line-anchored, findings have severity).
  6. Tests (rule unit tests, retry loop integration).
- **Load-bearing invariant introduced:** no silent state advance.

#### 102 — Event bus + SSE
- **Flavor:** Runtime
- **Blocks on:** 100
- **Purpose:** Formalize the event bus. SSE HTTP endpoint. `coworker watch` CLI.
- **Phases:**
  1. Event bus module (typed `Kind`, append-only log, retention from `policy.yaml`).
  2. SSE endpoint via `net/http` + `http.Flusher`.
  3. `coworker watch` cobra command (subscribe, filter by kind/run, pretty-print).
  4. Event-log snapshot testing helper; retrofit Plan 100's tests to assert golden event sequences.
  5. Tests.

---

### Phase C — Interactive dogfood (scenario works end-to-end at 108)

#### 103 — Freeform workflow + attention queue
- **Flavor:** Runtime
- **Blocks on:** 101, 102
- **Purpose:** Unstructured interactive mode. Attention queue unifying 4 kinds. Policy loader. `human-edit` synthetic jobs.
- **Phases:**
  1. `policy.yaml` loader + schema with source tracking (per §Configuration Layering: built-in → global → repo config → policy → role → manifest → CLI flags). `coworker config inspect` command.
  2. `attention` table + API; 4 kinds (permission, subprocess, question, checkpoint). Presented-on / answered-on channel fields.
  3. `Workflow` protocol + `FreeformWorkflow` (minimal: dispatch a role with synthesized run/plan/phase context from CLI args).
  4. Run envelope for session-less work: `coworker session` creates a run ID; subsequent invocations in the same session attach via env var or lock file.
  5. `human-edit` synthetic jobs — commit-based only in V1: post-commit git hook + `coworker edit <artifact>` + manual `coworker record-human-edit --commit <sha>`. No fs-watch for correctness. `workspace.dirty` attention items for uncommitted changes.
  6. `coworker session` + `coworker advance` / `coworker rollback` cobra commands.
  7. Tests (session lifecycle, attention queue flows, synthetic human-edit recording).

#### 104 — MCP server + `orch.*` tools
- **Flavor:** Runtime
- **Blocks on:** 103, 001, 002, 003
- **Purpose:** MCP surface. Persistent CLI workers pull dispatches; user panes drive the runtime.
- **Phases:**
  1. MCP SDK decision: official Go SDK vs `mark3labs/mcp-go` (based on spike findings). Server skeleton bound to daemon unix socket.
  2. `orch.run.*` tools (status, inspect).
  3. `orch.role.invoke(role, …)` — synchronous dispatch or job handle.
  4. `orch.next_dispatch()` + `orch.job.complete(job_id, outputs)` — lease-based pull protocol (enqueue → pull → lease → complete → release; heartbeat expiry → requeue).
  5. `orch.ask_user(question, options?)` + attention integration.
  6. `orch.attention.*`, `orch.findings.*`, `orch.artifact.read/write`.
  7. Degraded-mode implementation: if spike verdict shrank scope for a CLI, that CLI stays ephemeral-only.
  8. Tests.
- **Load-bearing invariant introduced:** pull model (supervisor rule: dispatches never carry message content via tmux send-keys).

#### 105 — Worker registry + persistent pull dispatch
- **Flavor:** Runtime
- **Blocks on:** 104
- **Purpose:** Live CLI worker claims, heartbeat-driven eviction, in-flight requeue.
- **Phases:**
  1. `workers` table + registry API in `core/registry/`.
  2. `orch.register` / `orch.heartbeat` / `orch.deregister`.
  3. Heartbeat watchdog + eviction (3× miss → evict).
  4. Scheduler: dispatch routing (`single` vs `many`, live claim vs ephemeral).
  5. In-flight requeue on eviction.
  6. Tests (heartbeat timing, eviction, concurrent claim semantics).

#### 108 — Claude Code plugin (interactive + worker)
- **Flavor:** Plugin
- **Blocks on:** 104, 105
- **Purpose:** First-class Claude Code integration. **User's scenario lives end-to-end after this plan.**
- **Phases:**
  1. Plugin skeleton (`.claude/plugins/coworker/`): `.mcp.json`, `settings.json`.
  2. `coworker-orchy` skill (polling, register/heartbeat, universal control tools).
  3. Role-worker skills (`coworker-role-developer`, etc., for when the pane IS the worker).
  4. Slash commands (`status`, `approve`, `invoke`, `pause`, `resume`).
  5. PreToolUse + Stop hooks → daemon.
  6. Live-agent smoke tests (`@live`, `COWORKER_LIVE=1`).

---

### Phase D — Parallel tracks

#### 107 — TUI dashboard
- **Flavor:** Runtime
- **Blocks on:** 102
- **Parallel-safe with:** 103, 104, 105, 106, 108, 109, 110
- **Purpose:** Live dashboard for state + checkpoint approvals.
- **Phases:**
  1. Bubble Tea `Model` / `Update` / `View` skeleton. Lipgloss layout (runs, plans, jobs, checkpoints panes).
  2. SSE subscription → `tea.Cmd` → model updates. Incremental render.
  3. Checkpoint approval UI ([p]/[r]/[a]/[q] bindings).
  4. Cost ledger view.
  5. Attention-queue panel with answer affordance.
  6. Snapshot tests (Bubble Tea `teatest` golden-output).

#### 106 — `build-from-prd` manifest and DAG scheduler
- **Flavor:** Runtime
- **Blocks on:** 101, 102
- **Parallel-safe with:** 103, 104, 105, 107, 114
- **Purpose:** First autopilot milestone — scheduling and workspaces, not full phase execution yet.
- **Phases:**
  1. Plan manifest schema (Go struct) + loader + validator. Matches the shape of *this very document*.
  2. `architect` role YAML + prompt (produces spec + manifest).
  3. `planner` role YAML + prompt (elaborates skeletons).
  4. DAG scheduler (ready plans → parallel tracks, `max_parallel_plans` bound).
  5. Worktree creation per plan (when `max_parallel_plans > 1`).
  6. Per-plan feature-branch management (branch at plan-start, non-blocking rebase policy from `policy.yaml`).
  7. Tests (manifest validation, scheduling, worktree lifecycle).

#### 114 — Phase loop and fan-in
- **Flavor:** Runtime
- **Blocks on:** 106
- **Parallel-safe with:** 107, 109, 110
- **Purpose:** Execute phases within a plan: developer → reviewers/tester → dedupe → fix-loop.
- **Phases:**
  1. Phase loop: `developer` → `[reviewer.arch ∥ reviewer.frontend ∥ tester]` → dedupe.
  2. Fan-in finding dedupe by fingerprint (preserving source metadata per §Fan-In Aggregation).
  3. Fan-in aggregation for test results, artifacts, notes, costs.
  4. Fix-loop with `max_fix_cycles_per_phase` + `phase-clean` checkpoint.
  5. Event snapshot tests for phase execution ordering.

#### 115 — Shipper and workflow customization
- **Flavor:** Runtime
- **Blocks on:** 114
- **Parallel-safe with:** 107
- **Purpose:** Complete the autopilot path: PR creation + stage customization.
- **Phases:**
  1. `shipper` role YAML + prompt.
  2. `ready-to-ship` checkpoint gating PR creation.
  3. `gh pr create --title ... --body ...` integration.
  4. **Level 1 customization:** named-stage registry + `workflow_overrides` loader.
  5. End-to-end `build-from-prd` smoke test (manifest → schedule → phase → ship).
  6. Tests.

#### 109 — Codex plugin (worker-only)
- **Flavor:** Plugin
- **Blocks on:** 104, 105
- **Parallel-safe with:** 106, 107, 108, 110
- **Purpose:** Codex as reviewer.arch, supervisor-quality backend, ephemeral exec target.
- **Phases:**
  1. Codex config (`~/.codex/coworker/`): worker prompts, sandbox defaults.
  2. Orchy-skill equivalent for Codex's execution model.
  3. Live smoke tests.

#### 110 — OpenCode plugin (interactive + worker)
- **Flavor:** Plugin
- **Blocks on:** 104, 105
- **Parallel-safe with:** 106, 107, 108, 109
- **Purpose:** OpenCode as driver or worker, leveraging its HTTP server mode.
- **Phases:**
  1. Plugin skeleton (`.opencode/coworker/`).
  2. Orchy skill for OpenCode.
  3. HTTP-dispatch path (distinct from pull model — OpenCode server holds state).
  4. Live smoke tests.

---

### Phase E — Completeness

#### 111 — Full role catalog
- **Flavor:** Runtime
- **Blocks on:** 106, 108
- **Purpose:** Complete the V1 role roster. YAMLs, prompts, per-role supervisor rules.
- **Phases:**
  1. `developer.yaml` + prompt + rules (feature-branch, phase-tag, tests-added-or-justified, no-commits-to-main).
  2. `planner.yaml` + prompt + rules (plan file shape, phase skeleton present).
  3. `reviewer.frontend.yaml` + prompt + rules (line-anchored findings, design-system references).
  4. `tester.yaml` + prompt + rules.
  5. `shipper.yaml` + prompt + rules (non-interactive `gh`, post-exec report present, PR not vs main).
  6. **Level 2 customization:** `applies_when` predicate DSL + evaluator + `job.skipped` event.
  7. Cross-role integration tests.

#### 112 — Supervisor quality (advisory-first)
- **Flavor:** Runtime
- **Blocks on:** 111
- **Purpose:** LLM judge for checkpoint-time quality adherence. Advisory by default; block-capable for a small allowlist of categories.
- **Phases:**
  1. Quality-rule schema + loader.
  2. LLM judge via Codex (ephemeral) invocation + structured verdict parsing.
  3. Advisory vs block-capable category routing (`missing_required_tests`, `spec_contradiction`, `security_sensitive_unreviewed_change`, `shipper_report_missing`).
  4. Checkpoint-time hook (runs at every `block` / `on-failure` checkpoint).
  5. Escalation path (max-retry → `quality-gate` checkpoint).
  6. Tests.

#### 113 — `coworker init`
- **Flavor:** Runtime
- **Blocks on:** 108, 109, 110, 111, 112
- **Purpose:** One-command repo scaffolding (project-scoped + `--global`).
- **Phases:**
  1. `init` command: creates `.coworker/` with default `config.yaml`, `policy.yaml`, `roles/`, `prompts/`, `rules/`.
  2. Plugin installation per CLI (project-local or `--global`).
  3. `.gitignore` augmentation (adds `.coworker/runs/`, `.coworker/state.db`).
  4. Idempotency (re-running merges safely; tracks installed version).
  5. Tests.

---

### Phase F — Optional collaboration addon

#### 116 — Bulletin board and reactive dispatch
- **Flavor:** Runtime addon
- **Blocks on:** 102, 103, 104, 105, 111
- **Spec:** `docs/specs/003-bulletin-board-reactive-dispatch.md`
- **Purpose:** Add structured board messages and daemon-owned routing from messages to follow-up jobs. Supports coder/reviewer/tester cooperation without allowing agents to spawn work directly.
- **Phases:**
  1. `board.message` event kind + `board_messages` projection.
  2. `orch.board.publish`, `orch.board.list`, `orch.board.get`.
  3. Role subscription schema (`subscriptions`) separate from `applies_when`.
  4. Board router with idempotency, duplicate suppression, max-depth limits, and `dispatched_by = board-router`.
  5. Freeform integration: board messages can trigger reviewer/tester/developer follow-up jobs.
  6. Tests (projection replay, routing, duplicate suppression, loop suppression, normal supervisor/dispatch path).
- **Load-bearing invariant introduced:** only the daemon routes board messages into jobs; agents may publish/read board messages but cannot directly dispatch work.

#### 117 — Board-augmented phase loop
- **Flavor:** Runtime addon
- **Blocks on:** 114, 115, 116
- **Spec:** `docs/specs/003-bulletin-board-reactive-dispatch.md`
- **Purpose:** Use board messages as the observable coordination layer inside `build-from-prd` phase execution.
- **Phases:**
  1. Developer output publishes `code.committed` / `work.summary` board messages.
  2. Reviewer/tester outputs publish `review.findings`, `review.clean`, `test.report`, `test.failed`.
  3. Phase loop consumes board-routed follow-up jobs while preserving supervisor convergence and checkpoints.
  4. TUI board panel.
  5. Adherence report board activity section.
  6. Event snapshot tests for board-augmented phase ordering.

---

## Critical path

`000 → 100 → 101 → 103 → 104 → 105 → 108 → 111 → 112 → 113` — 10 plans sequential on the interactive-dogfood path. The autopilot path adds `106 → 114 → 115` which runs in parallel. The bulletin-board addon adds `116 → 117` after the core workflow is stable.

Parallel branches off the critical path:
- `102` after `100`, alongside `101`
- `106 → 114 → 115` after `101 & 102`, alongside `103 → 104 → 105 → 108`
- `107` after `102`, alongside everything through `110`
- `109, 110` after `104 & 105`, alongside `108`
- `116 → 117` after the core runtime and role catalog; optional for V1, recommended for V1.5

**Core plan count:** 19. **With bulletin-board addon:** 21.

Expected real-world pace with a single developer working sequentially: the core plan count is ~19, but many phases are small (the spikes are <1 day each). The critical path is ~10 plans deep before optional addons; with parallel tracks a team could shave 3–4 plans of wall time.

## Testing discipline

| Layer | Introduced | Maintained by |
|---|---|---|
| Unit (`*_test.go`) | 000 | every subsequent plan |
| Mock-CLI harness (`testdata/mocks/`) | 100 | every subsequent runtime plan |
| Event-log snapshot (golden files under `testdata/events/`) | 102 | every workflow-touching plan |
| Replay (`tests/replay/<run-name>/`) | 103 | every workflow-touching plan |
| Live-agent (`@live`, gated by `COWORKER_LIVE=1`) | 108 | every plugin plan (108, 109, 110) |

**Event-log snapshot testing is load-bearing** — from Plan 102 onward, every test that drives the runtime captures the resulting `events` sequence and diffs against a golden file. Catches ordering regressions invisible to return-value or DB-state assertions.

## Load-bearing invariants and their introducing plan

| Invariant | First enforced in | Enforcement mechanism |
|---|---|---|
| Event-log before state update | 100 | crash-injection test (kill between event-write and row-update; replay reconciles) |
| Event idempotency | 100 | idempotency key on external commands; repeated key = no-op |
| File artifacts are pointers | 100 | schema constraint + lint rule on `artifacts.path` |
| Findings immutable | 100 | store-layer allow-list on UPDATE; unit test |
| Security: default-deny permissions | 100 | permission matching + attention.permission on undeclared |
| No silent state advance | 101 | supervisor contract rules + retry ceiling |
| Pull model for dispatch | 104 | lease-based protocol; supervisor rule forbids `send-keys` of message content |
| Board routing is daemon-owned | 116 | board messages route through idempotent daemon rules; agents cannot directly spawn jobs |

## Checkpoint policy

Applied uniformly across all plans:

- **`plan-approved`** — user reviews the plan file before implementation (workflow step 2).
- **`ready-to-ship`** — user reviews the post-exec report before PR merge (workflow step 6).

Cross-plan gate:

- **`phase-0-verdict`** — after spikes 001–003 complete, user decides whether Plan 104 proceeds as specified or shrinks scope. First real architectural fork point.

## Parked / deferred to V2

- Compaction strategy for long-lived persistent sessions (spec OQ #1) — each plugin plan (108/109/110) addresses its CLI's native compaction; V2 may unify.
- Budget rotation across providers (spec OQ #3).
- Web dashboard (live DAG viz, timeline, finding browser).
- Team/multi-user mode (auth, shared state, per-user views).
- Cross-repo runs (one orchestrator over multiple repos).
- Workflow Level 3 customization (user-defined workflow classes in Go).
- Replay-recording tooling beyond manual transcript capture.
- Slack/Discord notification integrations.
- Path reservations in the plan manifest schema (defer until parallel conflicts appear in dogfood).
- Filesystem-watch-based human-edit recording (V1 is commit-based only).
- Team-shared bulletin boards or cloud-backed board channels (local-only addon first).

## Spec amendments applied

- §Non-Goals — "Workflow topology is code (Go)" clarified; stage contents noted as configurable.
- §V1 Scope — daemon language specified: Go + `modernc.org/sqlite` + Bubble Tea + cobra.
- §Workflow State Machine — added §Workflow customization subsection with Level 1/2 mechanisms.
- §Workflow State Machine — added §Workspace model (worktrees for parallel plans, daemon-controlled cwd).
- §Lifecycle — replaced "inject job into that session" with lease-based pull protocol.
- §Security Model — new section (trust boundaries, enforcement points, permission matching, default-deny, hard deny, environment/path/network handling).
- §Data Model — expanded `events` table with `sequence`, `idempotency_key`, `causation_id`, `correlation_id`, `schema_version`. Added §Event Log Semantics.
- §Modes — human-edit recording narrowed to commit-based only in V1.
- §Supervisor — quality checks advisory by default; block-capable allowlist for 4 categories.
- §Fan-In Aggregation — new section (findings by fingerprint, tests by status, artifacts by path, notes chronological, costs summed).
- §Configuration Layering — new section (7-level precedence, `coworker config inspect`).
- §Bulletin Board Reactive Dispatch — new additive spec in `003`; roadmap plans 116/117 added as optional collaboration addon.
- (queued) §Role definition format — `applies_when` field to be added when Plan 111 ships.

## How this manifest is maintained

- **Plan completion:** when a plan ships, append a one-line status to its entry here (e.g., `**Status:** shipped 2026-05-12 · PR #7`). The plan file itself holds the post-exec report.
- **Scope change:** if a plan's scope materially shifts during execution, update its `Phases` list here and note the shift in the plan file's post-exec report.
- **New plan:** insert into the ordering with dependencies; check the critical path hasn't lengthened unexpectedly.
- This file is a living reference — keep it honest.
