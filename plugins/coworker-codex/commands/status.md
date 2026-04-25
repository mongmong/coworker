# coworker-status (Codex)

Show the current state of the active coworker run: active jobs, pending
checkpoints, cost so far, and any attention items waiting for human input.

> Codex uses **bare MCP tool names**. Use `orch_run_status`, not
> `mcp__coworker__orch_run_status`.

## Steps

1. Call `orch_run_status` with `{}`.
2. If there are pending checkpoints, list them with their kind and blocking
   status.
3. If there are attention items, list them with their kind and question.
4. Summarize the active job count, total cost, and estimated remaining budget.

## Example output

```
Run: run_abc123  Mode: autopilot  State: running
Plans: 1 active, 0 complete
Jobs: 2 running (developer/plan-101, reviewer.arch/plan-101)

Checkpoints pending:
  - phase-clean (on-failure) — plan 101, phase 2

Attention items:
  (none)

Cost: $1.23 / $10.00 budget
```
