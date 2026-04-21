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
- **`blocks_on`:** declared file/runtime independence. The spec's DAG scheduler trusts the list and surfaces merge conflicts if they occur.
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

- **Level 1** — configurable role list per named stage (`policy.yaml → workflow_overrides`). Lands in Plan 106, phase 11.
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
- **Purpose:** Verify orchy-style polling, `orch.job.complete` round-trip, tmux wake reliability.
- **Process:** Throwaway branch; prototype a minimal MCP server + skill; observe behavior; write report as the plan file's post-exec section.
- **Decision output:** Does Claude Code support persistent MCP pull reliably? Result feeds `phase-0-verdict` checkpoint.

#### 002 — Spike: Codex persistent + MCP pull
- **Flavor:** Spike
- **Blocks on:** 000
- **Purpose:** Same, for Codex. Verify its MCP client behavior and context-window compaction.

#### 003 — Spike: OpenCode server dispatch
- **Flavor:** Spike
- **Blocks on:** 000
- **Purpose:** Baseline the HTTP-dispatch path via `opencode serve`. Verify server event stream.

---

### Phase B — Runtime core

#### 100 — Thin end-to-end
- **Flavor:** Runtime
- **Blocks on:** 000
- **Purpose:** Minimum viable product. `coworker invoke reviewer.arch --diff <path> --spec <path>` spawns ephemeral Codex, captures findings JSON, persists via event-log-before-state. Load-bearing invariants introduced here.
- **Phases:**
  1. SQLite schema (minimum viable): `runs`, `jobs`, `events`, `findings`, `artifacts`, `schema_migrations`. `store/` package with migration runner.
  2. Event-first write helper (`store.WriteEventThenRow`) + crash-injection test (kill between event + row; replay reconciles).
  3. Role loader: YAML schema (Go struct + validator tags), prompt template resolution, ship one role (`reviewer.arch.yaml` + `prompts/reviewer.arch.md`).
  4. `Agent` protocol in `core/agent/` + `CliAgent` in `agent/` (Codex only for now; `os/exec`, stdout pipe, `json.Decoder` streaming).
  5. Ephemeral dispatch path: context snapshot assembly → prompt render → `agent.Dispatch` → findings capture → persist.
  6. Findings fingerprint + immutability (schema constraint — only `resolved_by_job_id` / `resolved_at` mutable; enforced by store layer, tested).
  7. `coworker invoke` cobra command.
  8. Tests: unit + Layer 2 mock-CLI harness (shell-script mock under `testdata/mocks/codex`).
- **Load-bearing invariants introduced:** event-before-state, findings immutable, file artifacts as pointers.

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
  1. `policy.yaml` loader + schema (checkpoints, concurrency bounds, supervisor limits, workflow_overrides — overrides used only after 106).
  2. `attention` table + API; 4 kinds (permission, subprocess, question, checkpoint). Presented-on / answered-on channel fields.
  3. `Workflow` protocol + `FreeformWorkflow` (minimal: dispatch a role with synthesized run/plan/phase context from CLI args).
  4. Run envelope for session-less work: `coworker session` creates a run ID; subsequent invocations in the same session attach via env var or lock file.
  5. `human-edit` synthetic jobs via post-commit git hook (shipped with `coworker init` later) + optional fs-watch fallback (`fsnotify`).
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
  4. `orch.next_dispatch()` + `orch.job.complete(job_id, outputs)` — pull model.
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

#### 106 — `build-from-prd` workflow
- **Flavor:** Runtime
- **Blocks on:** 101, 102
- **Parallel-safe with:** 103, 104, 105, 107
- **Purpose:** The PRD-driven autopilot. Architect → plan manifest → DAG scheduler → per-plan tracks → phase loop → shipper.
- **Phases:**
  1. Plan manifest schema (Go struct) + loader + validator. Matches the shape of *this very document*.
  2. `architect` role YAML + prompt (produces spec + manifest).
  3. `planner` role YAML + prompt (elaborates skeletons).
  4. DAG scheduler (ready plans → parallel tracks, `max_parallel_plans` bound).
  5. Per-plan feature-branch management (branch at plan-start, non-blocking rebase policy from `policy.yaml`).
  6. Phase loop: `developer` → `[reviewer.arch ∥ reviewer.frontend ∥ tester]` → dedupe.
  7. Fan-in finding dedupe by fingerprint.
  8. Fix-loop with `max_fix_cycles_per_phase` + `phase-clean` checkpoint.
  9. `shipper` role + `gh pr create --title ... --body ...` invocation.
  10. `ready-to-ship` checkpoint gating PR creation.
  11. **Level 1 customization:** named-stage registry + `workflow_overrides` loader.
  12. Tests.

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

#### 112 — Supervisor quality
- **Flavor:** Runtime
- **Blocks on:** 111
- **Purpose:** LLM judge for checkpoint-time quality adherence.
- **Phases:**
  1. Quality-rule schema + loader.
  2. LLM judge via Codex (ephemeral) invocation + structured verdict parsing.
  3. Checkpoint-time hook (runs at every `block` / `on-failure` checkpoint).
  4. Escalation path (max-retry → `quality-gate` checkpoint).
  5. Tests.

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

## Critical path

`000 → 100 → 101 → 103 → 104 → 105 → 108 → 111 → 112 → 113` — 10 plans sequential. Everything else parallelizes off it.

Parallel branches off the critical path:
- `102` after `100`, alongside `101`
- `106` after `101 & 102`, alongside `103 → 104 → 105 → 108`
- `107` after `102`, alongside everything through `110`
- `109, 110` after `104 & 105`, alongside `108`

Expected real-world pace with a single developer working sequentially: the plan count is ~17, but many phases are small (the spikes are <1 day each). The critical path is ~10 plans deep; with parallel tracks a team could shave 3–4 plans of wall time.

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
| File artifacts are pointers | 100 | schema constraint + lint rule on `artifacts.path` |
| Findings immutable | 100 | store-layer allow-list on UPDATE; unit test |
| No silent state advance | 101 | supervisor contract rules + retry ceiling |
| Pull model for dispatch | 104 | supervisor rule forbidding `send-keys` of message content |

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

## Spec amendments already applied

- §Non-Goals — "Workflow topology is code (Go)" clarified; stage contents noted as configurable.
- §V1 Scope — daemon language specified: Go + `modernc.org/sqlite` + Bubble Tea + cobra.
- §Workflow State Machine — added §Workflow customization subsection with Level 1/2 mechanisms.
- (queued) §Role definition format — `applies_when` field to be added when Plan 111 ships.

## How this manifest is maintained

- **Plan completion:** when a plan ships, append a one-line status to its entry here (e.g., `**Status:** shipped 2026-05-12 · PR #7`). The plan file itself holds the post-exec report.
- **Scope change:** if a plan's scope materially shifts during execution, update its `Phases` list here and note the shift in the plan file's post-exec report.
- **New plan:** insert into the ordering with dependencies; check the critical path hasn't lengthened unexpectedly.
- This file is a living reference — keep it honest.
