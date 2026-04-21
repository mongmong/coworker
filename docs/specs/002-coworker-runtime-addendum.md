# Coworker Runtime Addendum

**Date:** 2026-04-21
**Status:** Draft addendum
**Applies to:** `docs/specs/000-coworker-runtime-design.md`, `docs/specs/001-plan-manifest.md`
**Purpose:** Proposed clarifications and adjustments identified during spec review. This file does not replace the runtime spec or plan manifest; it records recommended amendments for explicit approval before those source specs are edited.

---

## Summary

The core design remains sound: Coworker is a local-first runtime that coordinates existing CLI coding agents through roles, jobs, checkpoints, supervisor rules, and an event log.

The main ambiguities to resolve before implementing the runtime core are:

1. Permission and security enforcement boundaries.
2. Persistent worker dispatch wording and lease semantics.
3. Parallel plan workspace isolation.
4. Event log replay and projection rules.
5. Human edit recording.
6. LLM quality-gate blocking behavior.
7. Fan-in aggregation contracts.
8. Configuration layering.

These are mostly clarification issues, but the first three are load-bearing enough that they should be settled before or during Plan 100.

---

## Proposed Amendment 1: Security and Permission Enforcement

### Problem

The runtime spec defines role-level permission declarations:

```yaml
permissions:
  allowed_tools: [read, write, edit, grep, glob, bash:git, bash:gofmt]
  never: [bash:sudo, bash:rm -rf /*]
  requires_human: [bash:curl, network]
```

and policy-level behavior:

```yaml
permissions:
  on_undeclared: block
```

However, it does not define where these rules are enforced. Because Coworker coordinates external CLIs, some actions may happen inside the CLI's own tool system, some through MCP tools, some through shell commands, and some through plugin hooks.

### Recommendation

Add a first-class `Security Model` section to the runtime spec.

### Proposed Text

```markdown
## Security Model

Coworker is a local single-user tool in V1, but it still treats agent execution as untrusted automation. The daemon is the policy authority for every action it can observe or mediate.

### Trust boundaries

- The Coworker daemon and SQLite state are trusted local components.
- Role YAML, prompt templates, and policy files are trusted only after local validation.
- External CLI agents are semi-trusted workers: they may be useful, but their requested actions are subject to role contracts, permission policy, and supervisor checks.
- MCP clients are not trusted merely because they can connect. Tool calls must be associated with a registered worker handle or an explicit user-control session.
- Repository contents, PRDs, issue text, web content, and model output are untrusted input.

### Enforcement points

Coworker enforces permissions at every boundary it controls:

1. MCP tools: validate caller handle, role, run, job, and allowed operation.
2. Runtime shell wrappers: for ephemeral jobs, invoke CLIs through a wrapper that records command, environment, cwd, stdout/stderr, and exit status.
3. Plugin hooks: persistent worker plugins must report tool requests and command execution events when the host CLI exposes them.
4. Supervisor contract: after every job, validate declared output contracts and git invariants.

If a CLI performs an action that Coworker cannot observe, Coworker cannot claim enforcement for that action. The adherence report must mark the job as `partially-observed`.

### Permission matching

Permission strings are structured as:

- `read`
- `write`
- `edit`
- `network`
- `bash:<command>`
- `mcp:<tool_name>`

Shell permission matching is command-based, not substring-based. `bash:git` allows the executable `git`; it does not allow `sh -c "git ..."`. Shell wrappers must capture argv as structured data where possible.

### Default-deny behavior

If an action is not explicitly allowed by the role and not permitted by policy, Coworker records an `attention.permission` item and blocks the job until the user approves, denies, or amends policy.

### Hard deny behavior

Actions matching `never` fail immediately and cannot be approved at runtime. Changing a hard deny requires editing role or policy configuration and starting a new job attempt.

### Environment handling

Ephemeral jobs receive a minimized environment. Secrets are not passed unless explicitly named in role configuration. Recorded logs redact configured secret names and common token patterns.

### Path handling

File writes are limited to the active workspace or plan worktree. Roles with narrower write scopes, such as `architect` writing only under `docs/specs/`, are enforced by path allowlists.

### Network handling

Network access is denied unless the role declares `network` or a specific network-requiring action and policy permits it. Network access requests create attention items by default in V1.
```

### Plan Impact

- Add a Plan 100 phase or subphase for minimal security primitives: path validation, role permission parsing, and audit event shape.
- Add Plan 101 rules for `partially-observed` jobs and hard-deny violations.
- Keep full CLI-plugin enforcement in plugin plans, but define the model early.

---

## Proposed Amendment 2: Persistent Worker Dispatch Uses Pull, Not Injection

### Problem

The lifecycle pseudocode says the runtime will "inject job into that session." Later sections correctly say persistent workers pull jobs via `orch.next_dispatch()` and that tmux `send-keys` must not carry message content.

### Recommendation

Replace "inject job into that session" with "enqueue job for worker pull" wherever it appears.

### Proposed Text

```markdown
dispatch(job):
  if role.backing == in_process:
      run directly, no CLI
  elif registry.has_live(job.role):
      enqueue job for a live worker claim
      worker receives it by calling orch.next_dispatch()
  else:
      spawn per role.cli, with prompt_template rendered from context
```

Persistent dispatch is a lease-based pull protocol:

1. The scheduler enqueues a dispatch for a role.
2. A registered worker calls `orch.next_dispatch(handle)`.
3. The daemon grants a lease and emits `job.leased`.
4. The worker completes with `orch.job.complete(job_id, outputs)`.
5. The daemon records completion, releases the lease, and emits `job.completed`.
6. If the worker heartbeat expires before completion, the lease expires and the job is requeued.

Tmux wakeups may send only an empty Enter key to prompt an idle worker turn. Tmux must never carry job content.
```

### Plan Impact

- Plan 104 should define MCP tool contracts for `orch.next_dispatch` and `orch.job.complete`.
- Plan 105 should implement leases, heartbeat expiry, and requeue.

---

## Proposed Amendment 3: Parallel Plans Use Git Worktrees

### Problem

The spec says non-blocking plans execute concurrently on separate feature branches. A single checkout cannot have multiple branches active at the same time.

### Recommendation

Use one git worktree per active plan in V1.

### Proposed Text

```markdown
## Workspace Model

The repo root remains the control checkout. Parallel plan execution happens in isolated git worktrees.

For each active plan, Coworker creates:

`.coworker/worktrees/plan-NNN-<slug>/`

Each worktree checks out the plan feature branch:

`feature/plan-NNN-<slug>`

Jobs for that plan run with cwd set to the plan worktree. File artifacts still store repo-relative paths plus the worktree identity. The TUI and CLI display both the logical path and active worktree.

### Worktree lifecycle

1. At plan start, create branch and worktree from the configured base branch.
2. All plan jobs run inside that worktree.
3. At ready-to-ship, the user approves PR creation from the feature branch.
4. After ship, keep the worktree by default for inspection.
5. `coworker cleanup` removes shipped or abandoned worktrees after confirmation.

### Conflict detection

The scheduler may trust `blocks_on`, but before starting a plan it should compare declared or inferred path reservations with other active plans. Overlapping reservations produce a warning or checkpoint according to policy.
```

### Plan Impact

- Plan 106 should add worktree lifecycle before DAG parallelism.
- Add path reservations to the plan manifest schema, either in V1 or as a near-term extension:

```yaml
plans:
  - id: 101
    title: "Review workflow"
    paths:
      likely_touch: ["core/review/**", "docs/plans/101-*.md"]
```

---

## Proposed Amendment 4: Event Log and Replay Semantics

### Problem

The spec says event-log-before-state is load-bearing, but it does not define whether row tables are projections, how event ordering works, or what replay does on disagreement.

### Recommendation

Make the event log authoritative for workflow history. Treat row tables as projections plus indexed convenience state.

### Proposed Text

```markdown
## Event Log Semantics

The `events` table is the authoritative history of a run. Other tables are projections or indexed convenience state unless explicitly documented otherwise.

Each event has:

- `event_id`: stable unique ID
- `run_id`
- `sequence`: monotonic per run
- `kind`
- `schema_version`
- `idempotency_key`
- `causation_id`: command or event that caused this event
- `correlation_id`: run/job/checkpoint grouping
- `payload_json`
- `created_at`

### Write discipline

State transitions write the event first, then update projections in the same transaction whenever possible. If a crash occurs between event append and projection update, replay repairs the projection.

### Idempotency

External commands that may be retried must provide an idempotency key. Repeating a command with the same key returns the original result or no-ops safely.

### Replay

Replay rebuilds projection tables from events for one run or all runs. If existing projection state disagrees with replayed state, replay wins and emits `projection.repaired`.

### Schema evolution

Event payloads are versioned. Migrations may add projection columns, but old events remain readable. If an event kind changes shape, add a new schema version and decoder.
```

### Plan Impact

- Plan 100 should include event IDs, per-run sequence, and idempotency keys.
- Plan 102 snapshot tests should assert event sequence and replayed projection state.

---

## Proposed Amendment 5: Human Edits Are Commit-Based in V1

### Problem

The spec says human hand-edits are first-class jobs, with post-commit hook and optional fs-watch fallback. Raw filesystem watching will be noisy and unreliable across editors, generated files, rebases, and uncommitted changes.

### Recommendation

Make V1 human-edit recording explicit and commit-based.

### Proposed Text

```markdown
## Human Edit Recording

In V1, `human-edit` jobs are recorded from explicit user actions:

1. `coworker edit <artifact>`
2. commits observed through the Coworker-installed post-commit hook
3. manual `coworker record-human-edit --commit <sha>`

Filesystem watch events may be used to mark a run as dirty, but they do not create durable `human-edit` jobs by themselves.

Uncommitted edits are surfaced as `workspace.dirty` attention items. They become auditable only after commit or explicit recording.
```

### Plan Impact

- Plan 103 should not rely on fs-watch for correctness.
- `coworker edit` should be the preferred path for auditable artifact edits.

---

## Proposed Amendment 6: LLM Quality Gate Starts Advisory

### Problem

The supervisor quality behavior is defined as an LLM judge that can veto checkpoints. This may create noisy blocking and high cost before the deterministic workflow is mature.

### Recommendation

In V1, deterministic contract failures are blocking. LLM quality findings are advisory by default, with a small allowlist of block-capable categories.

### Proposed Text

```markdown
## Supervisor Quality Enforcement

Supervisor contract checks are deterministic and blocking.

Supervisor quality checks are advisory by default in V1. A quality check may block only when it returns one of the configured block-capable categories:

- `missing_required_tests`
- `spec_contradiction`
- `security_sensitive_unreviewed_change`
- `shipper_report_missing`

All other quality findings are recorded in the adherence report and surfaced at checkpoints without stopping the run.
```

### Plan Impact

- Plan 112 should implement advisory-first quality gates.
- Policy can later allow stricter quality blocking once false-positive rates are known.

---

## Proposed Amendment 7: Fan-In Aggregation Contracts

### Problem

The spec defines `many` roles and review dedupe, but output aggregation rules are not general.

### Recommendation

Define fan-in behavior by output type.

### Proposed Text

```markdown
## Fan-In Aggregation

When a stage dispatches multiple jobs, outputs are aggregated according to output type:

- Findings: merged by fingerprint; duplicates preserve all source job IDs.
- Test results: aggregated by command and status; any failing required test fails the stage.
- Artifacts: must not write the same artifact path unless the stage declares an artifact merge strategy.
- Notes: appended chronologically and attributed to source job.
- Costs: summed by role, job, stage, plan, and run.

Reviewer disagreement is not discarded. Dedupe removes duplicate findings but keeps conflicting severity or recommendation metadata for human review.
```

### Plan Impact

- Plan 106 fan-in dedupe should preserve source metadata.
- Plan 111 role contracts should declare output types.

---

## Proposed Amendment 8: Configuration Layering

### Problem

The spec references built-in defaults, repo `.coworker/`, global plugin installs, role files, policy files, and CLI flags, but does not define precedence.

### Recommendation

Define deterministic config layering and an inspection command.

### Proposed Text

```markdown
## Configuration Layering

Configuration loads in this order, with later layers overriding earlier layers:

1. built-in defaults
2. global user config
3. repository `.coworker/config.yaml`
4. repository `.coworker/policy.yaml`
5. role YAML files
6. run manifest overrides
7. CLI flags

The daemon validates the fully merged config before starting a run. `coworker config inspect` prints the effective config with source annotations for each field.
```

### Plan Impact

- Plan 103 policy loader should include source tracking.
- Plan 113 `coworker init` should avoid overwriting user-edited config and should preserve source comments where practical.

---

## Proposed Amendment 9: Split Plan 106

### Problem

Plan 106 currently includes manifest schema, architect/planner roles, DAG scheduler, branch management, phase loop, review/test fan-in, dedupe, fix loop, shipper, PR creation, checkpoints, customization, and tests. This is too much for one implementation plan.

### Recommendation

Split Plan 106 into three runtime plans.

### Proposed Plan Split

#### 106 — Build-from-PRD manifest and DAG scheduler

- Plan manifest schema, loader, validator.
- Architect and planner role YAML/prompt stubs.
- DAG scheduler for ready plans.
- Worktree creation per plan.
- Tests for manifest validation and scheduling.

#### 114 — Phase loop and fan-in

- Developer/reviewer/tester stage execution.
- Findings fingerprint and dedupe.
- Fan-in aggregation contracts.
- Fix-loop with `max_fix_cycles_per_phase`.
- `phase-clean` checkpoint.
- Event snapshot tests.

#### 115 — Shipper and workflow customization

- Shipper role.
- `ready-to-ship` checkpoint gating PR creation.
- `gh pr create` integration.
- Level 1 workflow override support.
- End-to-end build-from-PRD smoke test.

### Rationale

This keeps the first autopilot milestone focused on scheduling and workspaces, then adds phase execution, then adds PR shipping and customization.

---

## Open Decision List

1. Should Plan 100 include a minimal security model, or should security be a new Plan 099 spike?
2. Should git worktrees be mandatory for all plan execution, or only when `max_parallel_plans > 1`?
3. Should `human-edit` require commits in V1, or should `coworker edit` be allowed to create synthetic jobs from uncommitted diffs?
4. Should LLM quality blocking be advisory-first as proposed, or remain blocking by default?
5. Should path reservations be added to the manifest schema now, or deferred until parallel conflicts appear in dogfood?

