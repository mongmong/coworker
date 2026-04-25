# /coworker-invoke (OpenCode)

Dispatch a role manually. Useful in interactive mode to kick off a specific
role without waiting for the autopilot scheduler.

> In interactive MCP mode, OpenCode uses the same namespaced tool names as
> Claude Code: `mcp__coworker__orch_role_invoke`.

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
   persistent HTTP worker or will run ephemerally via `opencode run`.
4. If the user wants to follow progress, suggest `/coworker-status`.

## Notes

- If a live `opencode serve` instance is registered for the role, the dispatch
  routes there via HTTP.
- If no live server exists, the job runs ephemerally via `opencode run --format json`.
- The supervisor contract still applies to manually invoked jobs.
- OpenCode supports abort via `POST /session/{id}/abort` — the session remains
  usable after cancellation.
