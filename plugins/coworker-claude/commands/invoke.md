# /coworker-invoke

Dispatch a role manually. Useful in interactive mode to kick off a specific
role without waiting for the autopilot scheduler.

## Usage

```
/coworker-invoke <role>
/coworker-invoke <role> --plan <plan-id>
/coworker-invoke <role> --plan <plan-id> --phase <phase-index>
/coworker-invoke <role> --prompt "custom instructions"
```

## Examples

```
/coworker-invoke reviewer.arch
/coworker-invoke developer --plan 101 --phase 2
/coworker-invoke tester --plan 101
```

## Steps

1. Parse the role name and any additional arguments from the user message.
2. Call `mcp__coworker__orch_role_invoke` with:
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
4. If the user wants to follow progress, suggest `/coworker-status`.

## Notes

- If a live worker is registered for the role, the dispatch routes there.
- If no live worker exists, the job runs ephemerally (spawns a new claude -p
  process).
- The supervisor contract still applies to manually invoked jobs.
