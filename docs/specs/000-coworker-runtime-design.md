# Coworker Runtime — Design Spec

**Date:** 2026-04-20
**Status:** Draft
**Context:** A local-first runtime that coordinates multiple CLI coding agents (Claude Code, Codex, OpenCode) as role-typed workers, driven either by a PRD (autopilot) or by direct user interaction, with strict workflow enforcement through a supervisor role.

---

## Motivation

Three CLI coding agents — Claude Code, Codex, OpenCode — each have strengths the others lack: Claude Code's hooks and subagents, Codex's strong sandbox and deep reasoning, OpenCode's server mode and provider flexibility. Running them independently makes each one's weakness a ceiling. Wired together, they become a team: one writes, others review, a third tests, a fourth audits the process.

Existing options don't fit:

- **GitHub Actions / PR-based coordination** is durable and human-visible, but its 60–180s CI round-trip kills iteration flow. Reviewers run stateless, cold, every time.
- **In-process agent frameworks** (LangGraph, CrewAI, Autogen) assume Python-embedded agents, not CLI tools. They can't drive a running `claude` or `codex` session.
- **Ad-hoc hook scripts** (Claude Code's Stop hook → shell command) work for one workflow, don't generalize, and break when any tool's surface changes.

The gap is a **local-first, CLI-agent-first, sub-10-second-round-trip runtime** that coordinates external coding agents as role-typed workers, with explicit phase boundaries and human checkpoints.

This spec defines that runtime: the **coworker** daemon.

## Non-Goals

- **Not a team-shared service in V1.** Single-user dev tool, local daemon, one user's repos. Multi-tenancy, auth, cloud deployment are deferred.
- **Not a replacement for PR-based review.** Coworker is the inner loop; GitHub PRs remain the outer loop for durable history and human review.
- **Not a new coding agent.** It orchestrates the three existing ones; it does not embed an LLM of its own (except rules-based checks and optional LLM quality audits via existing CLIs).
- **Not a general workflow engine.** Workflows are code (Python), not YAML; the runtime is narrow (coding workflows with spec→plan→phase structure), not a Temporal-competitor.

---

## Core Model

Six nouns define the entire runtime. Everything else is derived.

| Noun | Definition |
|---|---|
| **Agent** | A headless CLI binary + invocation template + sandbox mode. E.g., `{cli: claude-code, cmd: "claude -p --output-format stream-json", sandbox: workspace-write}`. |
| **Role** | A named job description. Binds an agent to a prompt template, inputs/outputs contract, sandbox override, concurrency rule, and skill set. |
| **Job** | One execution of one role. The atomic unit of retry, cost, and audit. |
| **Run** | A correlated tree of jobs sharing a run-id, a context store, and a workflow. A PRD-to-PRs autopilot is one run; an interactive session is also one run. |
| **Checkpoint** | A pause point where the runtime *may* request human approval. Defined by policy: always / on-failure / never / custom. |
| **Workflow** | The state machine for a run — ordered stages plus their fan-out/fan-in rules. `build-from-prd` is the flagship autopilot workflow; `freeform` backs interactive sessions. |

**Separation of agent from role** is deliberate: one CLI can serve many roles, and a role can be rebound to a different CLI without changing the workflow.

---

## Workflow State Machine

### `build-from-prd` (autopilot)

```
[PRD] ──▶ [architect] ──▶ spec + plan manifest (ordered, with blocking flags)
                 │
                 ◆ spec-approved  (block by default; user edits manifest,
                 │                 sets blocking/non-blocking per plan)
                 ▼
          ┌── DAG scheduler ──┐
          │                    │
          ▼                    ▼
    ready plans → parallel tracks (bounded by max_parallel_plans)
          │
          ▼   (each track, independently)
      [planner] → refine plan_N
          ◆ plan-approved
          ▼
      phase loop:
          [developer] → [rev.arch ∥ rev.frontend ∥ tester] → dedupe
             → fix-loop (≤ max_fix_cycles_per_phase)
          ◆ phase-clean (on-failure by default)
          ▼
      [shipper] → PR_N (on feature branch)
          ◆ ready-to-ship (block by default)
          ▼
      mark done → unblock downstream plans
          │
          ▼
    all plans done → run terminates
```

Five checkpoints in the default policy: **spec-approved**, **plan-approved** (per plan), **phase-clean** (per phase, on-failure only), **ready-to-ship** (per plan), plus the supervisor-raised **compliance-breach** and **quality-gate** (always blocking).

### Spec → plans decomposition

- The **architect** produces both a markdown spec and a **structured plan manifest** (ordered list of plans, each with a phase skeleton and a `blocks_on` list).
- At `spec-approved`, the user may edit the manifest: rename/reorder/add/remove plans, adjust phase skeletons, flip blocking flags.
- The **planner** runs per-plan, just-in-time, taking one skeleton and elaborating it into a detailed phased plan file.

Example manifest:

```yaml
spec_path: docs/specs/2026-04-20-coworker.md
plans:
  - id: 100
    title: "Core runtime + job queue"
    phases: ["SQLite schema", "event intake", "worker shell-out"]
    blocks_on: []
  - id: 101
    title: "Review workflow"
    phases: ["dispatch", "fan-in dedupe", "fix-loop"]
    blocks_on: [100]
  - id: 102
    title: "TUI dashboard"
    phases: ["layout", "event subscription", "controls"]
    blocks_on: []   # runs in parallel to 101
```

### Parallel plans

Non-blocking plans execute concurrently, each on its own feature branch (`feature/plan-NNN-<slug>`). Base branch for a non-blocking plan is `main` at plan-start time (matches human git-flow). The `blocks_on` list is the user's declared file-independence; the runtime trusts it but surfaces merge conflicts if they occur.

### `freeform` (interactive session, no PRD)

For ad-hoc work: bug fixes, one-off reviews, manual role invocation. No spec, no plan, no structured phases — just direct role dispatches from the user. `coworker session` is the entry point.

---

## Roles

### Canonical catalog

| Role | Inputs | Outputs | Default CLI | Sandbox |
|---|---|---|---|---|
| `architect` | PRD, repo, `decisions.md` | Spec + plan manifest | Codex (deep-think) | read-only + write `docs/specs/` |
| `planner` | Spec, plan skeleton, prior post-exec reports | Detailed plan file | Claude Code | read-only + write `docs/plans/` |
| `developer` | Plan (current phase), repo | Commits on feature branch | Claude Code | workspace-write |
| `reviewer.arch` | Diff, spec, `decisions.md` | Findings JSON | Codex | read-only |
| `reviewer.frontend` | Diff, design system | Findings JSON | OpenCode | read-only |
| `tester` | Code + plan | Test files + run results | Claude Code | workspace-write + shell |
| `shipper` | Plan + diff | PR URL + post-exec report | Claude Code | git + `gh` |
| `supervisor` | Any job's outputs + workflow rules | Verdict (pass / retry / escalate) + adherence report | Rules engine + Codex (for quality) | read-only |

### Role definition format

Every role is a YAML file plus a prompt template file. New roles drop in without touching runtime code.

```yaml
# .coworker/roles/developer.yaml
name: developer
concurrency: single              # single | many
cli: claude-code                 # default CLI for ephemeral mode
boot_skills:                     # loaded when CLI is started as persistent worker
  - coworker-orchy
  - coworker-role-developer
prompt_template: prompts/developer.md
inputs:
  required:
    - plan_path
    - phase_index
    - run_context_ref
outputs:
  contract:                      # supervisor validates these
    - new_commits_on_feature_branch: true
    - phase_tag_in_commit_msg: true
    - tests_added_or_justified: true
    - no_commits_to_main: true
  emits:
    - commits: []
    - touched_files: []
    - notes: string
sandbox: workspace-write
permissions:                     # declares expected permission surface (see §Attention)
  allowed_tools: [read, write, edit, grep, glob, bash:git, bash:gofmt]
  never: [bash:sudo, bash:rm -rf /*]
  requires_human: [bash:curl, network]
budget:
  max_tokens_per_job: 200000
  max_wallclock_minutes: 30
  max_cost_usd: 5.00
retry_policy:
  on_contract_fail: retry_with_feedback
  on_job_error: retry_once
```

### Concurrency (`single` vs `many`)

| Role's concurrency | Live claims | Dispatch behavior |
|---|---|---|
| `single` | 1 live | Route to that session |
| `single` | 0 live | Ephemeral spawn (one) |
| `single`, new registration arrives | already claimed | Evict prior (last-write-wins); `--replace` flag required to override |
| `many` | ≥1 live | Dispatch to **all** live claims in parallel |
| `many` | 0 live | Ephemeral spawn (one) |

Default concurrency per role: architect/planner/developer/tester/shipper/supervisor.quality = `single`; reviewer.* = `many` (the usecase: multiple reviewers with different models giving diverse opinions).

Cost implication: `many` dispatches are N× cost by construction. The cost ledger surfaces per-role fan-out width so the user sees "phase review spent $0.45 across 3 reviewers."

### Lifecycle — persistent vs ephemeral

**Lifecycle is emergent from the registry, not declared in YAML.** At dispatch time:

```
dispatch(job):
  if role.backing == in_process:       # e.g., supervisor.contract (pure rules)
      run directly, no CLI
  elif registry.has_live(job.role):    # user has a live CLI claimed for this role
      inject job into that session  →  persistent mode
  else:
      spawn per role.cli, with prompt_template rendered from context
                                       →  ephemeral mode
```

The user controls mode by what they launch. Spawn an architect session → architect jobs go there. Kill it → next architect job is ephemeral. No restart required.

### Registration protocol

Persistent workers register via the orchy skill on boot:

```
orch.register(role, pid, session_id, cli)  →  handle
orch.heartbeat(handle)                     # every 15s; miss 3 → evict
orch.deregister(handle)                    # clean shutdown
```

Registry table in SQLite tracks live/stale/evicted state. Heartbeat timeout evicts; in-flight dispatches on an evicted session are requeued.

---

## Supervisor

### Two sub-behaviors, one role

| Sub-behavior | Catches | Implementation | Runs |
|---|---|---|---|
| **Contract** | Output shape, git invariants, phase-tag presence, branch correctness, contract-declared fields | Deterministic rules | After **every** job |
| **Quality** | Adherence to TDD, spec self-consistency, review coverage depth, post-exec report substantiveness | LLM judge (Codex) | At **every checkpoint** |

Both default to **veto** enforcement: contract failure blocks and retries with feedback; quality failure with `block`-severity category blocks the checkpoint. Max-retry ceiling (`supervisor_limits.max_retries_per_job = 3`) prevents oscillation — after 3 failed retries, escalate to human regardless of policy.

### Rule catalog (excerpt)

```yaml
# .coworker/rules/supervisor-contract.yaml
rules:
  dev_commits_on_feature_branch:
    applies_to: [developer]
    check: git_current_branch_matches("^feature/plan-\\d+-")
    message: "Developer committed to non-feature branch"

  dev_phase_tag_in_commit:
    applies_to: [developer]
    check: last_commit_msg_contains("Phase {phase_index}:")

  reviewer_findings_line_anchored:
    applies_to: [reviewer.*]
    check: all_findings_have(["path", "line"])

  shipper_post_exec_report_present:
    applies_to: [shipper]
    check: plan_file_contains_section("## Post-Execution Report")

  shipper_pr_not_against_main:
    applies_to: [shipper]
    check: pr_head_branch_matches("^feature/")

  shipper_uses_non_interactive_gh:
    applies_to: [shipper]
    check: commit_log_shows("gh pr create --title")
```

Rules are simple predicates over the job's outputs + git state. On failure, the `message` is fed back to the role as part of its retry prompt (self-healing).

### Adherence report

Supervisor writes to `.coworker/runs/<run-id>/adherence.md` after every pass — a live-updated, human-readable audit log of how faithfully the run followed the workflow. Diffable, pastable, postmortem-ready.

---

## Checkpoints and Policy

### Default policy (A + E: always + policy-driven overrides)

```yaml
# .coworker/policy.yaml
checkpoints:
  spec-approved:      block         # user approves spec + plan manifest
  plan-approved:      block         # user approves detailed plan
  phase-clean:        on-failure    # only block if fix-loop didn't converge
  ready-to-ship:      block         # user approves PR creation
  compliance-breach:  block         # supervisor contract escalation (hard-coded)
  quality-gate:       block         # supervisor quality escalation (hard-coded)

supervisor_limits:
  max_retries_per_job: 3
  max_fix_cycles_per_phase: 5

concurrency:
  max_parallel_plans: 2
  max_parallel_reviewers: 3         # per phase fan-out

permissions:
  on_undeclared: block              # permission not in role YAML → human input required
```

### Enforcement

- `block`: halts progress; requires user decision via attention queue
- `on-failure`: blocks only if the prior step failed or converged poorly
- `auto` / `never`: proceed without pause
- `compliance-breach` and `quality-gate` cannot be weakened by policy

---

## Context Store

Two tiers.

| Tier | Home | Purpose |
|---|---|---|
| **Canonical artifacts** | Filesystem + git | Human-readable, durable. Single source of truth for anything a human reads. Spec, plan files, code, commits, PRs. |
| **Run context store** | SQLite (`.coworker/state.db`) | Machine-readable, per-run plumbing. Findings JSON, job outputs, cost ledger, checkpoint history, supervisor verdicts, run metadata. |

Roles never query SQLite directly. The runtime assembles a **context snapshot** per-job (based on `role.inputs.required`) and passes it to the role's prompt template as `{{ context }}`. The runtime fails fast if required inputs aren't available.

```jsonc
// Example context snapshot for a developer job
{
  "run":  { "id": "run_…", "mode": "autopilot" },
  "plan": { "id": 101, "title": "Review workflow",
            "branch": "feature/plan-101-review-workflow" },
  "phase": { "index": 2, "summary": "fan-in dedupe" },
  "artifacts": {
    "spec":                { "path": "docs/specs/2026-04-20-coworker.md" },
    "plan":                { "path": "docs/plans/101-review-workflow.md" },
    "prior_findings":      [ … ],
    "prior_phase_reports": [ … ]
  },
  "budget_remaining": { "tokens": 180000, "usd": 4.20, "wallclock_s": 1680 }
}
```

---

## Modes

### Run vs session entry points

| Entry | When | Mode |
|---|---|---|
| `coworker run <prd.md>` | Have a PRD → structured PRD-to-PR workflow | Autopilot (default), flippable to interactive mid-run |
| `coworker session` | Ad-hoc task, no PRD | Always interactive (no workflow spine to automate) |

Runs default to **autopilot**. The first checkpoint (`spec-approved`) hits within minutes of dispatch, so "autopilot" never means "walk away from step one." Override flags:

```
coworker run <prd.md> --pause-before architect   # pause before first job
coworker run <prd.md> --mode interactive         # start paused, user drives
coworker resume <run-id>                         # resumes prior mode
```

### Mode-flip mechanics

**The run is the state machine; mode is just who dispatches jobs.** Context, queue, supervisor, registry — all mode-agnostic.

Autopilot → interactive:
```
user issues "pause"
  → scheduler stops picking new jobs
  → in-flight jobs either complete or are user-killed
  → run.mode = "paused-interactive"
  → dashboard surfaces state + user commands become available:
    coworker invoke <role> [--plan N] [--phase M] --prompt "…"
    coworker redo <role>
    coworker edit <artifact>       # opens $EDITOR, runtime watches fs
    coworker advance               # skip/approve current checkpoint
    coworker rollback <checkpoint-id>
    coworker inspect <job-id>
```

Interactive → autopilot:
```
user issues "resume"
  → scheduler re-engages from current state
  → if user's hand-edits invalidated a prior artifact,
    supervisor mandates re-run of affected jobs
  → run.mode = "autopilot"
```

**User hand-edits are first-class jobs.** When the user edits a file directly, a synthetic `human-edit` job is recorded (commit SHA, diff) so it appears in the adherence report and event log alongside agent-authored work.

---

## Observability

### SSE event stream is the foundation

Every state change in the runtime emits an event to a typed bus. All surfaces consume from this stream. SQLite persists the event log (append-only), enabling exact replay.

Event shape:
```jsonc
{ "ts": "…", "run": "run_…", "kind": "job.started",
  "payload": { "job_id": "…", "role": "developer", "plan": 101, "phase": 2 } }
```

Canonical event kinds: `run.*`, `plan.*`, `job.*`, `supervisor.verdict`, `checkpoint.*`, `cost.*`, `worker.*`, `attention.*`, `human-edit`.

### Surfaces

| Surface | Answers | Best for |
|---|---|---|
| **TUI dashboard** (Textual) | "What's happening? What needs me?" | Live, parallel-state visualization, checkpoint approvals |
| **CLI** (`status`, `watch`, `logs`, `inspect`) | "State of run X?" / scripting | Shell pipelines, CI |
| **File artifacts** (`adherence.md`, spec/plan files, commits) | "What happened, in human-readable form?" | Postmortem, git-diffable audit |
| **Notifications** (desktop, webhook) | "Tell me when I need to act" | Overnight autopilot |
| **Web dashboard** (deferred) | Richer DAG / timeline viz | Long runs, team sharing |

### Per-agent observability

Each role-job's stream output (Claude's `stream-json`, Codex's stdout, OpenCode's RPC events) is persisted under `.coworker/runs/<run-id>/jobs/<job-id>.jsonl` and viewable via:
- TUI: drill into job, watch live or replay
- CLI: `coworker logs <job-id> --follow`
- File: direct JSONL consumption

---

## Reusing Live CLI Sessions (Claude Code / Codex / OpenCode)

**The user's interactive partner is a real Claude Code or OpenCode session**, not a custom TUI chat surface. The runtime is exposed as an MCP server; the CLIs (already MCP clients) call into it.

### Topology

```
┌────────── tmux window: run_<id> ─────────────────────────────┐
│                                                               │
│ ┌──────────────┬──────────────────┬──────────────────────┐   │
│ │ coworker TUI │ claude           │ claude               │   │
│ │              │ (architect)      │ (developer)          │   │
│ │ Run: …       │                  │                      │   │
│ │ Plans: 1/3   │ > write spec…    │ > implement phase 2…│   │
│ │ ◆ checkpoint │                  │                      │   │
│ │ [p][r][a][q] │                  │                      │   │
│ └──────┬───────┴────────┬─────────┴─────┬────────────────┘   │
│        │ SSE            │ MCP + role    │ MCP + role         │
│        ▼                ▼                ▼                   │
│  ┌─────────────────────────────────────────────────────┐     │
│  │              coworker daemon                        │     │
│  │ registry: {architect: pid=4201, dev: pid=4202}      │     │
│  └─────────────────────────────────────────────────────┘     │
│                                                               │
└───────────────────────────────────────────────────────────────┘
```

The TUI is read-mostly (subscribes to SSE). The CLI sessions are read-write (call MCP tools). Any persistent CLI can serve as a user control point — they all have the orchy skill, which exposes the orchestrator tools regardless of the role's primary purpose.

### `coworker run --with-chat` sets up the layout

Opens tmux, splits panes, spawns the TUI on the left and the user's preferred CLI (configurable per `config.yaml`) on the right, pre-configured with the coworker MCP endpoint and orchy skill. User doesn't hand-wire anything.

Alternatives:

| Preference | Command |
|---|---|
| Dashboard only | `coworker run --no-chat` |
| Chat only | `coworker run --chat-only` |
| Headless | `coworker run --autopilot-strict` (CI-suitable) |
| Remote | SSH + attach tmux; daemon on local socket |

---

## Plugins and `coworker init`

Ship first-class plugins for each CLI, for two distinct modes.

| Plugin | Installs into | Purpose |
|---|---|---|
| coworker-claude (interactive) | `.claude/plugins/coworker/` | Makes Claude Code a valid orchy-aware control session (orchy skill, MCP config, slash commands) |
| coworker-claude (worker) | Same dir, different skill | Makes Claude Code a compliant worker when dispatched as developer/planner/shipper |
| coworker-opencode (interactive + worker) | `.opencode/` | Same, for OpenCode |
| coworker-codex (worker only) | `~/.codex/coworker/` | Codex as reviewer / supervisor-quality backend |

Codex has no interactive plugin because it's not positioned as a conversational driver — only as a worker (architectural review, stateless sandboxed execution).

### The orchy skill (heart of the integration)

Every registered CLI loads it at boot. It's what makes any CLI a first-class citizen of the runtime.

```markdown
---
name: coworker-orchy
description: You are a registered worker in a coworker run. Handle role
  dispatches from the daemon, expose status to the user, respect supervisor
  verdicts.
---

# On startup
Call orch.register(role="{{ role }}", pid=$$, session_id=$$). Heartbeat
every 15s via orch.heartbeat(handle).

# When the daemon dispatches work to you
Before yielding back to the user, call orch.next_dispatch(). If it returns
a job, announce it to the user ("the orchestrator has queued a …"), do
the work per your role's output contract, then call
orch.job.complete(job_id, outputs). Poll once more. If null, yield to user.

# When the user talks to you
Treat user messages as collaborative. You may edit files, ask clarifying
questions, or co-author artifacts. On each turn, check for pending
dispatches so the user isn't surprised.

# Universal control tools (user invokes through you)
orch.run.*, orch.checkpoint.*, orch.role.invoke(other_role, …),
orch.findings.*, orch.artifact.read/write, orch.attention.*
```

### `coworker init` scaffolds everything

One command sets up a repo:

```bash
cd magicburg
coworker init             # project-scoped
coworker init --global    # also installs plugins to ~/.claude, etc.
```

Creates:

```
.coworker/
├── config.yaml
├── policy.yaml
├── roles/*.yaml               # default role catalog
├── prompts/*.md
├── rules/supervisor-contract.yaml
└── runs/                      # (gitignored) per-run state, SQLite

.claude/plugins/coworker/      # Claude Code plugin
├── .mcp.json
├── skills/{coworker-orchy,coworker-role-*}.md
├── commands/{status,approve,invoke,pause,resume}.md
└── settings.json              # PreToolUse + Stop hooks → daemon

.opencode/coworker/            # OpenCode plugin (same shape)
.codex/coworker/               # Codex worker prompts

.gitignore                     # adds .coworker/runs/
```

---

## Message Injection Mechanics

This is the area of the design most likely to require validation (see **Validation Spikes**). None of the three CLIs were built for external message injection; we rely on different mechanisms per CLI.

### Per-CLI mechanism

| CLI | Dispatch | Output capture | Wake-idle |
|---|---|---|---|
| **OpenCode** | HTTP POST to `opencode serve` | Server event stream | Not needed (server holds state) |
| **Claude Code** | Pull via `orch.next_dispatch()` MCP tool (agent polls at each turn end, per orchy skill) | `orch.job.complete()` MCP tool (structured JSON) | `tmux send-keys <pane> C-m` (Enter keystroke triggers fresh turn) |
| **Codex** | Same as Claude Code | Same | Same |

### The pull model (central to Claude Code and Codex)

Rather than pushing messages into sessions (which requires terminal injection and state-sensitive screen scraping), **sessions pull work from the daemon** via MCP. The orchy skill instructs the agent to call `orch.next_dispatch()` after every turn and handle any returned job before yielding to the user.

Why pull works better than push:
- No external-input hacks (no tmux send-keys for message content)
- Structured outputs via `orch.job.complete()` (no terminal scraping)
- Works identically across Claude Code, Codex, OpenCode (all MCP clients)
- Session state is respected (agent polls when ready)

Push (tmux send-keys of actual message content) is avoided; send-keys is used only for wake-idle nudges (a lone Enter to trigger a new turn when session is otherwise idle).

### Ephemeral fallback

For roles dispatched without a live claim, direct headless invocation (`claude -p`, `codex exec`, `opencode run`) bypasses injection entirely. No persistent session, no message-injection surface.

---

## Attention Queue

Four distinct kinds of "waiting for human" flow through a single `attention` table, presenting a uniform surface to the user.

| Kind | Source | Detection | Surfacing |
|---|---|---|---|
| `permission` | CLI's permission model (Claude Code, Codex sandbox) | PreToolUse / permission-callback hook in each plugin | TUI, notification, optional prompt in pane |
| `subprocess` | Interactive subprocess (`gh pr`, `git rebase -i`) | `tmux pipe-pane` scraper watching for prompt patterns | Same |
| `question` | Agent-initiated via `orch.ask_user(question, options?)` | First-class MCP tool | Same |
| `checkpoint` | Workflow gate | Runtime-raised | Same |

Prevention first: the orchy skill instructs agents to use non-interactive subprocess flags (`gh pr create --title ... --body ...`), structured questions via `orch.ask_user()` instead of prose, and supervisor contract rules enforce this discipline across runs.

### Response channels (symmetric)

Once an item is queued, the user can answer from any of:

- **TUI** (arrow keys + Enter)
- **The live pane of the agent that's waiting** (natural conversation; the orchy skill detects `/answer` prefix or explicit pattern and forwards via `orch.attention.answer`)
- **Any other registered CLI** ("tell developer: yes, delete it" from the architect's pane)

The waiting agent doesn't care which channel delivered the answer — it's blocked on `orch.ask_user()` (or the permission hook), and that call returns when *any* channel submits.

### Permission policy (role-declared + block fallback)

Each role YAML declares its expected permission surface (`allowed_tools`, `never`, `requires_human`). At dispatch time:

- Tool in `allowed_tools` → auto-approve
- Tool in `never` → auto-deny
- Tool in `requires_human` OR not declared at all → attention item, block until user answers

Most autopilot runs should hit zero permission prompts because roles declare their surface comprehensively. Overnight autopilot with undeclared permissions pauses and waits for human.

---

## Data Model

Everything lives in `.coworker/state.db` (SQLite). One file, crash-safe, git-ignorable.

```sql
-- Top-level run
runs(id, prd_path, spec_path, mode, state, started_at, ended_at,
     cost_usd, budget_usd)

-- Plans within a run (DAG nodes)
plans(id, run_id, number, title, blocks_on JSON, branch, pr_url, state)

-- Jobs = role invocations (including human-edit synthetic jobs)
jobs(id, run_id, plan_id, phase_index, role, state, dispatched_by, cli,
     started_at, ended_at, cost_usd)
  -- state: pending | dispatched | running | complete | failed | cancelled
  -- dispatched_by: 'scheduler' | 'user' | 'supervisor-retry' | 'self'

-- Dispatches link a job to the worker session/process that ran it
dispatches(id, job_id, worker_handle, mode, dispatched_at, acknowledged_at)
  -- mode: persistent | ephemeral | in-process

-- Artifacts (pointers to files or inline JSON)
artifacts(id, job_id, kind, path, json)

-- Findings (fan-in deduped by fingerprint; immutable once written)
findings(id, run_id, plan_id, phase_index, reviewer_handle, path, line,
         severity, body, fingerprint, resolved_by_job_id, resolved_at)

-- Checkpoints
checkpoints(id, run_id, plan_id, kind, state, decision, decided_by,
            decided_at, notes)

-- Supervisor verdicts
supervisor_events(id, job_id, kind, verdict, rule_id, message, created_at)

-- Attention queue (unified human-input surface)
attention(id, kind, source, job_id, question, options JSON,
          presented_on JSON, answered_on, answered_by, answer,
          created_at, resolved_at)

-- Worker registry (live CLI claims)
workers(handle, role, pid, session_id, cli, registered_at,
        last_heartbeat_at, state)

-- Cost ledger
cost_events(id, job_id, provider, model, tokens_in, tokens_out, usd,
            created_at)

-- SSE event log (source of truth for replay)
events(id, ts, run_id, kind, payload JSON)
```

### Integrity rules

1. `events` is append-only and written **before** the row update it describes. Daemon crash between event-write and DB-update → replay restores consistency.
2. File artifacts are referenced by `artifacts.path`, never inlined. Filesystem + git are the durable side; SQLite holds pointers.
3. Findings are immutable once written. "Resolved" means a fix job linked back; the original row persists, giving a full audit trail.

---

## Error Handling and Recovery

| Failure | Detection | Recovery |
|---|---|---|
| Agent job fails (CLI crash, timeout, API error) | Non-zero exit / MCP timeout | Retry per `role.retry_policy` (backoff on API); max-retry → compliance-breach checkpoint |
| Contract veto | Supervisor rule fail | Retry role with rule's `message` injected into next prompt; max `supervisor_limits.max_retries_per_job` → escalate |
| Quality veto | LLM supervisor emits `block` | Same pattern; max-retry → escalate |
| Persistent session dies | Heartbeat timeout | Evict from registry; in-flight dispatch requeued; next dispatch falls back to ephemeral OR waits `reclaim_window_s` for user respawn |
| Phase fix-loop doesn't converge | Same finding fingerprint repeats N cycles | Escalate to quality-gate checkpoint with stuck finding shown |
| PR creation fails | Shipper job returns non-success | Treated as agent job fail; same retry path |
| Budget soft limit (80%) | Cost ledger | Non-blocking warning to TUI + chat |
| Budget hard limit (100%) | Cost ledger | Pause scheduler, open compliance-breach checkpoint |
| Daemon crash | Next startup sees incomplete jobs | Replay `events` log → reconcile with DB → mark in-flight jobs `interrupted` → reschedule per retry policy; registry cleared (sessions re-register on reconnect) |
| User abort | Explicit command | Cancel in-flight dispatches, mark run `aborted`, leave artifacts + commits in place |

Invariant: **no failure silently advances state.** Every stuck path terminates either in a successful retry or a human checkpoint.

---

## Testing

Three layers, each with a clear purpose and cost profile.

### Layer 1 — Unit tests (fast, deterministic, no agents)

Pure-code tests on state-machine transitions, fan-in dedupe, rule predicates, schema validation, scheduler DAG correctness. ~80% of the suite. Must pass on every commit.

### Layer 2 — Integration tests with mock agents

Swap real CLIs with mock binaries (`.coworker/test/mocks/<cli-name>`) that take a prompt on stdin and emit canned stream-json. Tests end-to-end dispatch, registry eviction, supervisor loops, crash recovery, dynamic fan-out width. Runs in seconds, no API cost.

### Layer 3 — Replay tests (recorded real-agent transcripts)

Record real autopilot runs. Scrub + store under `tests/replay/<run-name>/`. Play back via a replay-mode CLI wrapper. Catches workflow ordering bugs and prompt regressions. Gated to nightly CI.

### Optional: Live-agent E2E

Tests tagged `@live`, enabled by `COWORKER_LIVE=1`. Pre-release smoke check. Single-digit dollars per full run.

### Event log as test fixture

Every test driving the runtime produces a deterministic `events` log. Snapshot-testing catches regressions that don't show in return values or DB state ("supervisor used to fire here and doesn't anymore").

---

## Validation Spikes (Phase 0)

Three spikes must pass **before** implementation begins. If any fails, the "living session" scope for the affected CLI shrinks to ephemeral-only; the rest of the design remains valid.

| Spike | Goal | Effort |
|---|---|---|
| Claude Code persistent + MCP pull | Skill-driven polling works; `orch.job.complete` round-trips; tmux newline wake is reliable | ~1 day |
| Codex persistent + MCP pull | Same for Codex; verify its MCP client behaves | ~1 day |
| OpenCode server dispatch | Baseline the clean path is as clean as expected | ~½ day |

Specifically verify (both Claude Code and Codex):
- MCP notification support for turn-wake (push-initiated new turns). If supported → cleaner than tmux-based wake nudges.
- Context-window compaction behavior across long-running sessions
- Reliability of `orch.job.complete()` being called — supervisor contract rule enforces it, but the primary path must not depend on scraping

---

## V1 Scope

Shipped in V1:

- Daemon (Python, asyncio, SQLite, Textual TUI)
- MCP server exposing orch.* tools
- Three role plugins (claude, codex, opencode) each with interactive + worker modes
- `build-from-prd` and `freeform` workflows
- Full role catalog (architect, planner, developer, reviewer.arch, reviewer.frontend, tester, shipper, supervisor)
- Supervisor contract + quality with veto enforcement
- Permission policy with role-declared surface + block fallback
- Attention queue unifying permission, subprocess, question, checkpoint
- SSE event stream + CLI (`status`, `watch`, `logs`, `inspect`)
- File-based adherence reports
- `coworker init` scaffolding
- Unit + integration (mocked) tests

Deferred to V2+:

- Team/multi-user mode (auth, shared state, per-user views)
- Web dashboard (live DAG viz, timeline, finding browser)
- Cross-repo runs (single orchestrator coordinating multiple repos)
- Automatic compaction strategies for long-running persistent sessions
- Slack/Discord notification integrations
- Per-provider cost rotation and budget enforcement
- Replay-recording tooling beyond manual transcript capture

---

## Open Questions

Issues intentionally deferred to plan-writing or runtime discovery, not blocking on the spec:

1. **Compaction strategy for persistent sessions** — Claude Code has auto-compaction; Codex is more manual; OpenCode has its own approach. How does the orchy skill interact with each? Worth prototyping in the Phase 0 spikes.
2. **MCP notification support** — whether Claude Code / Codex support server-initiated notifications reliably. Determined by spike; fallback is tmux wake.
3. **Budget rotation across providers** — if one provider rate-limits, can the runtime re-bind a role to a different CLI mid-run? Deferred; V1 honors the role's declared CLI only.
4. **Plugin publishing** — V1 ships plugins in the coworker repo; community distribution (Claude Code plugin registry, etc.) is V2.

---

## Appendix A — Directory Layout After `coworker init`

```
<repo>/
├── .coworker/
│   ├── config.yaml
│   ├── policy.yaml
│   ├── roles/
│   │   ├── architect.yaml
│   │   ├── planner.yaml
│   │   ├── developer.yaml
│   │   ├── reviewer.arch.yaml
│   │   ├── reviewer.frontend.yaml
│   │   ├── tester.yaml
│   │   ├── shipper.yaml
│   │   └── supervisor.yaml
│   ├── prompts/
│   │   └── <role>.md
│   ├── rules/
│   │   └── supervisor-contract.yaml
│   ├── state.db                 # gitignored — shared across runs
│   └── runs/                    # gitignored
│       └── <run-id>/
│           ├── adherence.md
│           └── jobs/
│               └── <job-id>.jsonl
├── .claude/plugins/coworker/
│   ├── .mcp.json
│   ├── skills/
│   ├── commands/
│   └── settings.json
├── .opencode/coworker/
└── .codex/coworker/
```

## Appendix B — Minimal `config.yaml` example

```yaml
# .coworker/config.yaml
daemon:
  bind: local_socket
  data_dir: .coworker

cli_selection:
  interactive_driver: claude-code   # claude-code | opencode
  fallback_driver: opencode

providers:
  claude-code:
    rate_limit_concurrent: 3
  codex:
    sandbox_default: workspace-write
    rate_limit_concurrent: 2
  opencode:
    server_url: http://127.0.0.1:7777
    rate_limit_concurrent: 4

telemetry:
  event_log_retention_days: 90
  cost_ledger_retention_days: 365
```
