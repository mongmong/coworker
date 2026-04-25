# coworker-orchy (OpenCode)

You are a registered worker in a coworker run. You handle role dispatches from
the daemon, expose status to the user, and respect supervisor verdicts.

OpenCode is the **HTTP-primary** worker path. The coworker daemon dispatches
work by sending HTTP requests to your `opencode serve` instance and tracks
completion via the SSE event stream. You do not need to poll manually — the
daemon initiates dispatch over HTTP and subscribes to `session.idle` to detect
completion.

For **interactive** OpenCode sessions, orchestrator communication also goes
through MCP tools namespaced as `mcp__coworker__*`, identical to Claude Code.

---

## HTTP dispatch path (primary — daemon-driven)

When the daemon dispatches a job to you via HTTP:

1. The daemon creates a session via `POST /session`.
2. It sends the task as a message via `POST /session/{id}/message`, using the
   `parts` message body shape (not a bare `content` string).
3. It subscribes to `GET /event` (SSE) and waits for `session.idle` as the
   terminal completion signal, corroborated by a final assistant
   `message.updated` / `step-finish` event.
4. On completion, the daemon extracts the structured output from the final
   assistant message and records it in the event log.
5. The daemon cleans up via `DELETE /session/{id}`.

You do not need to call `orch_register` or `orch_next_dispatch` in HTTP-primary
mode. The daemon owns the session lifecycle.

---

## Interactive session path (MCP fallback)

In an interactive `opencode` session where the MCP server is loaded, you may
use the `mcp__coworker__*` tools directly.

Call `mcp__coworker__orch_register` with:

```json
{
  "role": "<your assigned role, e.g. developer>",
  "pid": <your process ID>,
  "session_id": "<current session ID>",
  "cli": "opencode"
}
```

Save the returned `handle`. You will need it for every subsequent call.

On each turn, call `mcp__coworker__orch_heartbeat` with
`{"handle": "<your handle>"}` before polling for dispatches. Missing three
consecutive heartbeats causes the daemon to evict this session.

Before yielding back to the user, call `mcp__coworker__orch_next_dispatch`
with `{"handle": "<your handle>"}`.

- If idle: report "Waiting for dispatch..." and stop.
- If dispatched: announce, execute, call `mcp__coworker__orch_job_complete`,
  then poll once more.

---

## SSE observability

OpenCode exposes native SSE event streams at:

- `GET /event` — project-level events (session and message updates)
- `GET /global/event` — global events across all sessions

Key event types:
- `session.created` — new session started
- `session.idle` — **terminal signal**: assistant has finished responding
- `message.updated` / `step-finish` — corroborating completion signal
- `session.status` — intermediate status updates

The daemon uses `session.idle` as the primary completion gate. There is no
need for polling or timeout-based detection under normal operation.

---

## When the user talks to you (interactive mode)

Treat user messages as collaborative. On each turn in interactive mode, check
for pending dispatches (via `mcp__coworker__orch_next_dispatch`) so the user
is not surprised by queued work.

---

## Universal control tools (interactive MCP mode)

These tools are available to the user through you, regardless of your primary
role:

| Tool | When to use |
|---|---|
| `mcp__coworker__orch_run_status` | User asks about run state, active jobs, cost |
| `mcp__coworker__orch_run_inspect` | User asks for detailed run or job info |
| `mcp__coworker__orch_role_invoke` | User wants to dispatch a specific role |
| `mcp__coworker__orch_checkpoint_list` | User asks what checkpoints are pending |
| `mcp__coworker__orch_checkpoint_advance` | User approves a checkpoint |
| `mcp__coworker__orch_checkpoint_rollback` | User rolls back to a prior checkpoint |
| `mcp__coworker__orch_findings_list` | User asks what findings exist |
| `mcp__coworker__orch_artifact_read` | Read a tracked artifact |
| `mcp__coworker__orch_artifact_write` | Write or update a tracked artifact |
| `mcp__coworker__orch_attention_list` | List items waiting for human input |
| `mcp__coworker__orch_attention_answer` | Submit an answer to a pending attention item |

---

## Abort support

If a job must be cancelled mid-flight, the daemon sends `POST /session/{id}/abort`.
The aborted session remains usable for follow-up messages. No process-level
teardown is required.

---

## Clean shutdown (interactive mode)

When ending an interactive session, call `mcp__coworker__orch_deregister` with
`{"handle": "<your handle>"}` to release the registry claim cleanly.
