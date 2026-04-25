# coworker-invoke (Codex)

Dispatch a role manually. Useful in interactive mode to kick off a specific
role without waiting for the autopilot scheduler.

> Codex uses **bare MCP tool names**. Use `orch_role_invoke`, not
> `mcp__coworker__orch_role_invoke`.

## Usage

```
Call orch_role_invoke with {"role": "<role>"}
Call orch_role_invoke with {"role": "<role>", "plan_id": <id>, "phase_index": <n>}
Call orch_role_invoke with {"role": "<role>", "prompt_override": "custom instructions"}
```

## Examples

```
Invoke reviewer.arch
Invoke developer for plan 101 phase 2
Invoke tester for plan 101
```

## Steps

1. Parse the role name and any additional arguments from the user message.
2. Call `orch_role_invoke` with:
   ```json
   {
     "role": "<role>",
     "plan_id": <plan-id or null>,
     "phase_index": <phase-index or null>,
     "prompt_override": "<custom prompt or null>"
   }
   ```
3. Report the job ID returned and whether the job was dispatched to a live
   persistent worker or will run ephemerally.
4. If the user wants to follow progress, call `orch_run_status`.

## Notes

- If a live worker is registered for the role, the dispatch routes there.
- If no live worker exists, the job runs ephemerally (spawns a new
  `codex exec` process).
- The supervisor contract still applies to manually invoked jobs.
- Codex ephemeral dispatch requires `--sandbox danger-full-access` on the
  spawned process.
