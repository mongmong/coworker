# Bulletin Board Reactive Dispatch

**Date:** 2026-04-21
**Status:** Addon spec
**Applies to:** `docs/specs/000-coworker-runtime-design.md`, `docs/specs/001-plan-manifest.md`
**Purpose:** Define an additive coordination model where role agents publish structured work summaries to a central bulletin board, and the Coworker daemon routes related board messages into follow-up jobs.

---

## Summary

This addendum extends the current runtime model without replacing it.

The base runtime remains:

```text
workflow scheduler -> job dispatch -> role agent -> structured output -> supervisor -> next state
```

The bulletin-board extension adds:

```text
role agent -> board.message -> daemon routing rule -> job dispatch -> role agent
```

Agents may publish to and read from the board, but **only the daemon creates dispatches**. This preserves the current design's auditability, retry control, permission model, and event-log semantics while enabling a more collaborative, reactive workflow.

## Maturity Position

This is a post-core collaboration addon, not a prerequisite for the thin runtime, MCP server, worker registry, or first `build-from-prd` implementation.

Recommended sequencing:

1. Build the core event log, jobs, role loader, supervisor, MCP tools, and worker registry.
2. Dogfood direct scheduler-driven workflows.
3. Add bulletin-board routing as Plan 116.
4. Optionally use board messages inside the PRD phase loop as Plan 117.

The design is intentionally additive: removing or delaying the board addon must not invalidate the base runtime.

---

## Motivation

The current workflow state machine is explicit and phase-driven. That is good for deterministic autopilot runs, but it can make agent cooperation feel less natural than a team room where contributors post updates and react to each other's work.

The desired cooperation pattern:

1. Multiple agents work as different roles, such as `coder`, `reviewer`, and `tester`.
2. Each agent publishes a work summary to a central board with role name, referenced PR, plan ID, branch or local workspace, and detailed summary.
3. Other agents monitor board messages relevant to their role and respond automatically:
   - `coder` responds to review findings and failed test reports.
   - `reviewer` responds to commits, PR updates, and coder summaries.
   - `tester` responds to commits, fixes, and changed test scope.

The extension should support that pattern without allowing uncontrolled agent-to-agent loops.

---

## Non-Goals

- **Not a replacement for jobs.** Board messages do not directly execute work. They are inputs to routing rules that may enqueue jobs.
- **Not unmediated agent autonomy.** Agents do not spawn arbitrary work by reading the board. The daemon owns dispatch.
- **Not a general chat system.** Messages are structured coordination records, not freeform conversation history.
- **Not a team/shared cloud feature in V1.** The board is local to one Coworker daemon and one user's runs.

---

## Core Concept

The bulletin board is a structured, append-only coordination layer.

Every board message is also emitted as an event:

```text
events.kind = "board.message"
```

A projection table may index messages for efficient querying:

```sql
board_messages(
  id,
  run_id,
  plan_id,
  phase_index,
  channel_id,
  worktree_id,
  branch,
  pr_ref,
  author_role,
  author_worker_handle,
  message_type,
  related_job_id,
  related_commit,
  related_finding_id,
  related_test_run_id,
  summary,
  details_json,
  changed_files_json,
  severity,
  causation_id,
  correlation_id,
  idempotency_key,
  created_at
)
```

The event log remains authoritative. `board_messages` is a projection.

---

## Channel Identity

A channel identifies the local collaboration context for a plan or branch.

Default channel ID:

```text
run_id + plan_id + worktree_id
```

Human-readable metadata:

- `branch`
- `pr_ref`
- `repo_path`
- `worktree_path`

Branch names and PR refs are useful labels, but not sufficient identity on their own because:

- multiple local worktrees may share branch ancestry;
- PR refs may not exist until late in the workflow;
- freeform sessions may not have PRs;
- retries may occur before a branch is pushed.

---

## Message Types

Initial message types should stay small and workflow-oriented.

| Message type | Typical author | Meaning |
|---|---|---|
| `code.committed` | `coder` / `developer` | New commits are available for review/test. |
| `work.summary` | any role | Human-readable summary of completed role work. |
| `review.findings` | `reviewer.*` | Review findings were produced. |
| `review.clean` | `reviewer.*` | Reviewer found no blocking issues. |
| `test.report` | `tester` | Test run results are available. |
| `test.failed` | `tester` | Required tests failed or coverage is insufficient. |
| `fix.applied` | `coder` / `developer` | Fixes were committed in response to findings/tests. |
| `pr.opened` | `shipper` | PR was created or updated. |
| `question.raised` | any role | Agent needs clarification; also links to attention queue if human input is required. |

Board messages can reference existing domain objects:

- `job_id`
- `finding_id`
- `commit`
- `artifact_id`
- `checkpoint_id`
- `pr_ref`

---

## Required Message Fields

Every board message must include:

- `message_type`
- `author_role`
- `run_id`
- `channel_id`
- `summary`
- `details_json`
- `idempotency_key`

Messages associated with plan work should include:

- `plan_id`
- `phase_index`
- `branch`
- `worktree_id`

Messages associated with review or testing should include:

- `related_commit` or `related_job_id`
- `changed_files_json`
- severity or status where applicable

---

## Role Subscription Rules

Roles declare subscriptions in role YAML. A subscription says which board messages are relevant to that role.

Example:

```yaml
name: developer
subscriptions:
  - when:
      message_type: review.findings
      severity_in: [error, warning]
    action: enqueue_job
    prompt_template: prompts/developer.respond-to-review.md
  - when:
      message_type: test.failed
    action: enqueue_job
    prompt_template: prompts/developer.fix-tests.md
```

Example:

```yaml
name: reviewer.arch
subscriptions:
  - when:
      message_type: code.committed
      changes_touch: ["core/**", "store/**", "mcp/**"]
    action: enqueue_job
    prompt_template: prompts/reviewer.arch.review-commit.md
```

Example:

```yaml
name: tester
subscriptions:
  - when:
      message_type: code.committed
    action: enqueue_job
    prompt_template: prompts/tester.run-tests.md
  - when:
      message_type: fix.applied
    action: enqueue_job
    prompt_template: prompts/tester.verify-fix.md
```

The existing `applies_when` predicate DSL can be reused, but board routing should be a separate concept:

- `applies_when`: should this role run for a planned stage?
- `subscriptions`: should this role react to a board message?

---

## Routing Semantics

The daemon owns routing.

When a `board.message` event is appended:

1. The routing engine loads subscriptions for roles in the run.
2. It filters subscriptions by channel, plan, phase, message type, changed files, severity, and correlation metadata.
3. For each matching subscription, it computes an idempotency key.
4. If a matching job already exists or completed for that key, routing no-ops.
5. Otherwise it enqueues a job with `dispatched_by = 'board-router'`.
6. The job is delivered through the normal dispatch path:
   - persistent worker pull via `orch.next_dispatch()`;
   - ephemeral fallback if no live worker is registered.

Agents may call board read tools to gather context, but they do not decide dispatch.

---

## MCP Tools

Add MCP tools under `orch.board.*`.

```text
orch.board.publish(message)
orch.board.list(channel_id, filters)
orch.board.get(message_id)
orch.board.subscribe(role, channel_id, filters)   # optional; mostly for live panes
```

`orch.board.publish` validates:

- caller worker handle;
- role authorization;
- run/channel membership;
- required fields;
- idempotency key;
- permission policy.

Live workers do not need true push subscriptions for correctness. They continue to poll jobs through `orch.next_dispatch()`. Board subscription is primarily for context display and optional live-pane awareness.

---

## Relationship to Existing Objects

### Events

Every board message is an event:

```jsonc
{
  "kind": "board.message",
  "run": "run_...",
  "payload": {
    "message_id": "msg_...",
    "channel_id": "run_.../plan_101/wt_...",
    "author_role": "tester",
    "message_type": "test.failed",
    "summary": "Unit tests failed in store replay",
    "related_job_id": "job_...",
    "related_commit": "abc123"
  }
}
```

### Jobs

Board-triggered jobs use:

```text
jobs.dispatched_by = 'board-router'
```

The job context snapshot includes the triggering board message plus recent related messages from the same channel.

### Findings

`review.findings` board messages should link to immutable `findings` rows. The board message summarizes the finding set; it does not replace the findings table.

### Attention

If a board message requires human input, the agent should use `orch.ask_user()`. The board message may link to the resulting attention item.

### Adherence Report

The adherence report should include a board activity section showing:

- message sequence;
- which routing rules fired;
- jobs created from messages;
- suppressed duplicate routes;
- loops stopped by policy.

---

## Loop Control

Reactive dispatch can easily create loops. V1 must include explicit controls.

### Idempotency

Each routing decision uses an idempotency key such as:

```text
route:<run_id>:<channel_id>:<message_id>:<target_role>:<subscription_id>
```

For finding or test repair loops, include stable fingerprints:

```text
route:<run_id>:<finding_fingerprint>:developer:respond-to-review
```

### Max Depth

Every board message carries `correlation_id` and optional `thread_depth`.

Policy:

```yaml
board:
  max_thread_depth: 8
  max_jobs_per_message: 5
  max_jobs_per_correlation: 20
```

If a limit is exceeded, the daemon emits `board.route_suppressed` and opens a checkpoint or attention item.

### Duplicate Suppression

The router suppresses duplicate work when:

- same target role;
- same channel;
- same related commit/finding/test run;
- same subscription;
- existing live or completed job with same idempotency key.

### Human Escalation

Repeated failed fix cycles continue to use existing supervisor limits:

- `max_fix_cycles_per_phase`
- `max_retries_per_job`
- `phase-clean`
- `compliance-breach`
- `quality-gate`

---

## Example Flow

### Coder Publishes Commit Summary

```jsonc
{
  "message_type": "code.committed",
  "author_role": "developer",
  "run_id": "run_123",
  "plan_id": 101,
  "phase_index": 2,
  "channel_id": "run_123/plan_101/wt_plan_101",
  "branch": "feature/plan-101-review-workflow",
  "worktree_id": "wt_plan_101",
  "related_commit": "abc123",
  "summary": "Implemented findings fingerprint dedupe",
  "details_json": {
    "commits": ["abc123"],
    "notes": "Added deterministic fingerprinting and duplicate source tracking."
  },
  "changed_files_json": ["core/findings/fingerprint.go", "core/findings/fingerprint_test.go"],
  "idempotency_key": "developer:code.committed:abc123"
}
```

### Router Enqueues Reviewer and Tester

Matching subscriptions:

- `reviewer.arch` subscribes to `code.committed` touching `core/**`.
- `tester` subscribes to all `code.committed` messages.

Daemon creates:

- `job_reviewer_arch_...`
- `job_tester_...`

### Reviewer Publishes Findings

```jsonc
{
  "message_type": "review.findings",
  "author_role": "reviewer.arch",
  "related_job_id": "job_reviewer_arch_...",
  "related_commit": "abc123",
  "summary": "One blocking issue: fingerprint ignores severity changes",
  "details_json": {
    "finding_ids": ["finding_001"]
  },
  "severity": "error",
  "idempotency_key": "reviewer.arch:review.findings:job_reviewer_arch_..."
}
```

### Router Enqueues Developer Fix

`developer` subscription matches `review.findings` with severity `error`. The daemon creates a fix job.

### Tester Publishes Report

```jsonc
{
  "message_type": "test.report",
  "author_role": "tester",
  "related_commit": "def456",
  "summary": "All store replay tests passed",
  "details_json": {
    "commands": ["go test ./core/findings ./store"],
    "status": "passed"
  },
  "idempotency_key": "tester:test.report:def456"
}
```

No follow-up job is created unless a role subscribes to clean test reports.

---

## Policy

Default policy:

```yaml
board:
  enabled: true
  route_automatically: false
  max_thread_depth: 8
  max_jobs_per_message: 5
  max_jobs_per_correlation: 20
  duplicate_suppression: true
  require_structured_messages: true
```

For opt-in automatic routing:

```yaml
board:
  route_automatically: true
```

When `route_automatically` is false, matching routes create `attention.checkpoint` items instead of jobs. Early dogfood should start with this conservative default. Repositories can opt into automatic routing once subscriptions and loop limits have proven stable.

---

## Workflow Integration

### Freeform

The board is most useful in `freeform` sessions because there is no rigid phase spine. A user can start:

```bash
coworker session
coworker invoke developer --prompt "Fix failing store replay"
```

The developer publishes `code.committed`, and the board router can either propose or trigger reviewer/tester jobs depending on `board.route_automatically`.

### Build From PRD

For `build-from-prd`, the board should augment the phase loop rather than replace it.

Existing phase loop:

```text
developer -> reviewers/tester -> dedupe -> fix-loop
```

Board-augmented phase loop:

```text
developer publishes code.committed
board router enqueues reviewers/tester
reviewers/tester publish findings/reports
board router enqueues developer fixes
supervisor controls convergence and checkpoints
```

The observable behavior becomes board-driven, but the workflow still owns phase boundaries and convergence.

---

## Testing

Add tests in the first plan that implements board routing:

1. Publishing a valid board message appends a `board.message` event.
2. Projection rebuild creates the same `board_messages` rows from events.
3. A `code.committed` message routes to reviewer/tester jobs.
4. A `review.findings` message routes to developer fix job.
5. Duplicate message/routing idempotency suppresses duplicate jobs.
6. Max thread depth suppresses runaway loops.
7. Board-triggered jobs use normal dispatch and supervisor paths.

Mock-agent integration tests should simulate:

```text
developer -> board.message(code.committed)
reviewer -> board.message(review.findings)
developer -> board.message(fix.applied)
tester -> board.message(test.report)
```

and assert the resulting event sequence.

---

## Plan Impact

Recommended as an addon after the thin runtime, event bus, freeform workflow, MCP tools, worker registry, and role catalog exist.

Minimal implementation plan:

### 116 — Bulletin board and reactive dispatch

**Blocks on:** 102, 103, 104, 105, 111

- `board.message` event kind.
- `board_messages` projection.
- `orch.board.publish/list/get`.
- Role subscription schema.
- Board router with idempotency and duplicate suppression.
- Freeform integration.
- Mock-agent tests.

Optional follow-up:

### 117 — Board-augmented phase loop

**Blocks on:** 114, 115, 116

- Use board messages inside `build-from-prd` phase execution.
- Convert developer/reviewer/tester outputs into board summaries.
- Add TUI board panel.
- Add adherence report board section.

---

## Decisions and Remaining Questions

Decisions in this draft:

1. Board routing is an addon after core runtime work, not a core Plan 100 dependency.
2. `board_messages` is a projection table; `events` remains authoritative.
3. Role subscriptions live in role YAML. Policy may disable or override automatic routing.
4. Automatic routing is disabled by default for initial dogfood; matching routes create checkpoints until enabled.
5. `build-from-prd` keeps the direct phase-loop scheduler first. Board routing may later become the observable coordination layer inside that loop.

Remaining questions:

1. Which repositories or runs should opt into automatic routing by default after dogfood?
2. Should board messages be rendered into durable markdown artifacts, or are event log + TUI + adherence report sufficient?
3. Should external webhooks eventually publish board messages, or should V1/V1.5 remain local-only?
