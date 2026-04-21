# Plan 002 — Spike: Codex Persistent MCP Pull

**Goal:** Determine whether Codex can act as a persistent MCP-pull worker — connecting to a custom stdio MCP server, polling `orch.next_dispatch()` at each turn end, returning structured output via `orch.job_complete()`, and being woken from idle via tmux — or whether it should remain ephemeral-only.

**Duration:** ~1 day

**Prerequisites:**
- Plan 000 shipped (Go module, tooling)
- Codex CLI installed at `/home/chris/.nvm/versions/node/v20.20.1/bin/codex`
- OpenAI API key configured (Codex auth working)
- `tmux` installed
- Go 1.25+
- Spike 001 MCP server built (we reuse it — the server is CLI-agnostic)

**Branch:** `feature/spike-002-codex`

**Manifest entry:** `docs/specs/001-plan-manifest.md` §002

---

## Background

Codex CLI supports MCP servers via stdio and streamable HTTP transports, configured in `~/.codex/config.toml` or via `codex mcp add`. Codex launches MCP servers automatically when a session starts and exposes their tools alongside built-ins. The spike validates whether this is sufficient for the persistent pull model.

### What we know from research

- **Codex supports MCP servers** — both stdio (local subprocess) and streamable HTTP. Configuration in `~/.codex/config.toml` under `[mcp_servers.<name>]`.
- **Codex has persistent sessions** — `codex resume` can continue a previous session. Sessions are stored locally.
- **`codex exec`** — non-interactive mode for scripted/CI runs, supports `--json` for JSONL event output.
- **Codex loads MCP servers at session start** — tools are available alongside built-ins during the session.
- **No evidence of MCP notification support** — Codex documentation focuses on tool calls, not server-initiated push.
- **Sandbox modes** — `read-only`, `workspace-write`, `danger-full-access`. The spike server needs `workspace-write` at minimum to write `completed.json`.

### Key uncertainty

Unlike Claude Code, Codex's interactive mode is a TUI — it's unclear whether it supports system-prompt injection for skill-like behavior (persistent polling instructions). If not, the persistent model may not work and Codex would be ephemeral-only (via `codex exec`), which is still valuable for review/test roles.

---

## Test Protocol

### Test 1: MCP server connection + tool discovery

**Question:** Can Codex connect to a custom stdio MCP server and discover its tools?

**Setup:**
Reuse the MCP server from spike 001 (`spike/001/spike-mcp-server`). If not yet built:
```bash
cd spike/001 && go build -o spike-mcp-server . && cd -
```

**Steps:**
1. Register the MCP server with Codex:
   ```bash
   codex mcp add coworker-spike -- /home/chris/workshop/coworker/spike/001/spike-mcp-server
   ```
2. Verify registration: `codex mcp list` — confirm `coworker-spike` appears.
3. Verify with `codex mcp list --json` for machine-readable output.
4. Start Codex interactively and check tool availability:
   ```bash
   codex "List all available MCP tools. Do you see orch_next_dispatch and orch_job_complete?"
   ```
5. Check if Codex can see both tools.

**Expected result:** Codex discovers both tools from the MCP server.

**Failure mode:**
- Server fails to start → check stderr, verify binary path
- Tools not listed → MCP server protocol mismatch; check if Codex needs specific MCP protocol version
- Connection timeout → check `startup_timeout_sec` in config.toml (default 10s)

---

### Test 2: Tool round-trip via `codex exec`

**Question:** Can Codex call `orch_next_dispatch`, process a dispatch, and call `orch_job_complete` in ephemeral mode?

**Setup:**
Set `spike/001/dispatch.json`:
```json
{
  "status": "dispatched",
  "job_id": "codex-test-001",
  "role": "reviewer.arch",
  "prompt": "Review the architecture of spike/001/server.go. Return findings as JSON with keys: summary, findings (array of {file, line, severity, message}).",
  "context": {}
}
```

**Steps:**
1. Run Codex in exec mode:
   ```bash
   codex exec "Call the orch_next_dispatch tool. If it returns a dispatch with status 'dispatched', execute the task in the prompt field. When done, call orch_job_complete with the job_id and your structured findings as JSON." 2>&1 | tee spike/002/exec-output.txt
   ```
2. Check `spike/001/completed.json` for the completion payload.
3. Verify structured JSON output.

**Expected result:** `completed.json` contains structured findings with the correct `job_id`.

**Failure mode:**
- Codex doesn't call MCP tools in exec mode → MCP servers may not load for `codex exec`
- Calls `orch_next_dispatch` but not `orch_job_complete` → prompting issue
- Sandbox blocks file write → need `--sandbox workspace-write` or `--full-auto`

---

### Test 3: `codex exec --json` output capture

**Question:** Does `codex exec --json` produce parseable JSONL events that capture MCP tool calls and results?

**Steps:**
1. Reset `dispatch.json` to the test dispatch.
2. Run:
   ```bash
   codex exec --json \
     "Call orch_next_dispatch. If dispatched, execute the task and call orch_job_complete with findings." \
     2>/dev/null | tee spike/002/exec-jsonl.txt
   ```
3. Parse each line of output as JSON.
4. Identify event types — look for tool-call events, tool-result events, assistant messages.
5. Check if MCP tool calls (`orch_next_dispatch`, `orch_job_complete`) appear in the event stream.

**Expected result:** JSONL output is parseable; MCP tool calls are visible in the event stream.

**Failure mode:**
- JSONL lines not valid JSON → format issue
- MCP tool calls not in stream → may be internal to Codex, not surfaced
- If MCP tool calls are invisible, we need an alternative capture strategy (read `completed.json` directly)

---

### Test 4: Interactive session — persistent polling feasibility

**Question:** Can Codex maintain a polling loop in an interactive session, similar to Claude Code's skill-driven approach?

**Setup:**
Start Codex interactively in a tmux pane.

**Steps:**
1. Set `dispatch.json` to `{"status": "idle"}`.
2. Start Codex in tmux:
   ```bash
   tmux new-session -d -s spike002 "codex 'You are connected to the coworker orchestrator. At the end of every response, you MUST call orch_next_dispatch to check for work. If idle, say Waiting. If dispatched, execute and call orch_job_complete, then poll again.'"
   ```
3. Observe: Does Codex call `orch_next_dispatch` after responding?
4. If yes, update `dispatch.json` with a test dispatch.
5. Send wake: `tmux send-keys -t spike002 Enter`
6. Observe: Does Codex pick up the dispatch?

**Expected result:** Codex maintains the polling behavior across turns.

**Failure mode:**
- Codex doesn't support persistent system prompt injection in interactive mode → persistent model not viable
- Polling works on first turn but not subsequent → instruction lost after turn
- tmux send-keys doesn't wake Codex TUI → Codex TUI may handle input differently than Claude Code
- If persistent polling fails → **Codex is ephemeral-only** (via `codex exec`), which is the expected fallback

---

### Test 5: Session resume with MCP tools

**Question:** If we use `codex resume` to continue a session, are MCP tools still available?

**Steps:**
1. Start an interactive Codex session, call `orch_next_dispatch` once, then exit.
2. Note the session ID from `codex resume --last` or the session output.
3. Resume: `codex resume --last`
4. Ask Codex to call `orch_next_dispatch` again.
5. Verify the MCP server is re-launched and tools are available.

**Expected result:** MCP tools are available in resumed sessions.

**Failure mode:** MCP server not relaunched on resume → session resume doesn't reload MCP config. This would mean persistent sessions can't use MCP tools across restarts, limiting the persistent model.

---

### Test 6: `codex exec` with `--output-schema` for structured output

**Question:** Can we enforce structured JSON output from Codex using `--output-schema`?

**Setup:**
Write `spike/002/findings-schema.json`:
```json
{
  "type": "object",
  "properties": {
    "job_id": {"type": "string"},
    "summary": {"type": "string"},
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "file": {"type": "string"},
          "line": {"type": "integer"},
          "severity": {"type": "string", "enum": ["info", "warning", "error"]},
          "message": {"type": "string"}
        },
        "required": ["file", "severity", "message"]
      }
    }
  },
  "required": ["job_id", "summary", "findings"]
}
```

**Steps:**
1. Reset `dispatch.json`.
2. Run:
   ```bash
   codex exec \
     --output-schema spike/002/findings-schema.json \
     "Call orch_next_dispatch. Execute the dispatched task. Call orch_job_complete. Then output your findings." \
     2>/dev/null | tee spike/002/schema-output.txt
   ```
3. Verify the output conforms to the schema.

**Expected result:** Output is valid JSON matching the schema.

**Failure mode:** Schema not enforced → Codex may ignore `--output-schema` with MCP tools active. Fall back to parsing `completed.json` directly.

---

### Test 7: Sandbox interaction with MCP server

**Question:** How do Codex sandbox modes interact with the MCP server's file I/O?

**Steps:**
1. Run Test 2 with each sandbox mode:
   ```bash
   codex exec --sandbox read-only "Call orch_next_dispatch and orch_job_complete..." 
   codex exec --sandbox workspace-write "Call orch_next_dispatch and orch_job_complete..."
   codex exec --full-auto "Call orch_next_dispatch and orch_job_complete..."
   ```
2. For each mode, check:
   - Does the MCP server start? (stdio subprocess may be affected by sandbox)
   - Can the server write `completed.json`?
   - Are Codex's own file operations sandboxed independently of MCP?

**Expected result:** MCP server operates outside the sandbox (it's a subprocess managed by Codex, not a user command). Codex's own operations are sandboxed per policy.

**Failure mode:** Sandbox blocks MCP server file I/O → need to understand the boundary. MCP servers should run outside the sandbox since Codex launches them as trusted infrastructure.

---

## Decision Matrix

| Dimension | Result | Implication |
|---|---|---|
| MCP tool discovery | yes/no | Foundational |
| `codex exec` tool round-trip | yes/no | Ephemeral mode viability |
| `codex exec --json` capture | parseable/not | Event stream integration |
| Interactive polling | yes/no | Persistent mode viability |
| tmux wake (interactive) | works/broken | If broken + polling fails → ephemeral only |
| Session resume + MCP | yes/no | Persistent session continuity |
| `--output-schema` enforcement | yes/no | Structured output capture path |
| Sandbox + MCP interaction | clean/problematic | Deployment model |

## Verdict Template

Fill in after running:
- `persistent_mcp_pull:` yes | partial | no
- `ephemeral_exec:` yes | partial | no
- `tmux_wake:` reliable | flaky | broken | n/a
- `mcp_notifications:` supported | unsupported
- `output_capture:` jsonl | schema | file-read | none
- `compaction:` acceptable | problematic | n/a
- `recommendation:` persistent | ephemeral-only | persistent-with-workarounds
- `plan_104_impact:` <how this affects the MCP server plan>
- `plan_109_impact:` <how this affects the Codex plugin plan>

---

## Spike Code Location

All prototype code lives in `spike/002/`:
- `exec-output.txt` — raw `codex exec` output
- `exec-jsonl.txt` — JSONL event capture
- `findings-schema.json` — output schema for structured capture test
- `schema-output.txt` — schema-enforced output
- `RESULTS.md` — raw test results

The MCP server binary is shared with spike 001 at `spike/001/spike-mcp-server`.

---

## Post-Execution Report

*(fill in after running the spike)*

### Test Results

| Test | Result | Notes |
|---|---|---|
| 1. MCP connection | | |
| 2. exec tool round-trip | | |
| 3. exec --json capture | | |
| 4. Interactive polling | | |
| 5. Session resume + MCP | | |
| 6. --output-schema | | |
| 7. Sandbox + MCP | | |

### Verdict

*(fill in using verdict template above)*

### Recommendations for Plan 104 / 109

*(fill in based on findings)*
