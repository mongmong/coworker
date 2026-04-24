# Spike Rerun Guide

This document shows how to rerun the three CLI spikes from the command line.

These are **research / validation flows**, not part of the currently shipped thin runtime.

## Before you start

Common prerequisites:

- Go 1.25+
- `tmux`
- the relevant CLI installed and authenticated
- repo root at `/home/chris/workshop/coworker`

Build the shared MCP server first:

```bash
cd /home/chris/workshop/coworker
cd spike/common/mcp-server
go build -o spike-mcp-server .
cd /home/chris/workshop/coworker
```

## Spike 001 — Claude Code MCP pull

Plan:

- [docs/plans/001-spike-claude-code.md](/home/chris/workshop/coworker/docs/plans/001-spike-claude-code.md)

Current verdict:

- Claude Code MCP tool access works
- explicit polling across turns works
- idle wake is not reliable
- recommendation: **ephemeral-only for now**

Key commands:

Check CLI:

```bash
/home/chris/.local/bin/claude --version
/home/chris/.local/bin/claude --help
```

Write shared MCP config:

```bash
cat > /home/chris/workshop/coworker/spike/common/mcp.json <<'EOF'
{
  "mcpServers": {
    "coworker-spike": {
      "command": "/home/chris/workshop/coworker/spike/common/mcp-server/spike-mcp-server",
      "args": [],
      "type": "stdio"
    }
  }
}
EOF
```

Register and inspect:

```bash
/home/chris/.local/bin/claude mcp add-json --scope project coworker-spike '{"type":"stdio","command":"/home/chris/workshop/coworker/spike/common/mcp-server/spike-mcp-server","args":[]}'
/home/chris/.local/bin/claude mcp list
```

Discovery:

```bash
/home/chris/.local/bin/claude -p \
  --mcp-config /home/chris/workshop/coworker/spike/common/mcp.json \
  --strict-mcp-config \
  "List all available MCP tools. Do you see mcp__coworker-spike__orch_next_dispatch and mcp__coworker-spike__orch_job_complete?"
```

Round-trip:

```bash
cat > /home/chris/workshop/coworker/spike/001/dispatch.json <<'EOF'
{
  "status": "dispatched",
  "job_id": "test-job-001",
  "role": "reviewer.arch",
  "prompt": "Review the architecture of spike/common/mcp-server/main.go. Return your findings as JSON with keys: summary, findings (array of {file, line, severity, message}).",
  "context": {}
}
EOF

/home/chris/.local/bin/claude -p \
  --mcp-config /home/chris/workshop/coworker/spike/common/mcp.json \
  --strict-mcp-config \
  "Call the mcp__coworker-spike__orch_next_dispatch tool. If it returns a dispatch with status 'dispatched', execute the role described in the prompt field. When done, call mcp__coworker-spike__orch_job_complete with the job_id from the dispatch and your outputs as structured JSON."
```

Check completion:

```bash
cat /home/chris/workshop/coworker/spike/001/completed.json
```

## Spike 002 — Codex MCP pull

Plan:

- [docs/plans/002-spike-codex.md](/home/chris/workshop/coworker/docs/plans/002-spike-codex.md)

Current verdict:

- `codex exec` works
- persistent behavior is only **explicit-turn-only**
- idle wake is broken
- recommendation: **ephemeral-primary, persistent-explicit-turn-only**

Key commands:

Check CLI:

```bash
/home/chris/.nvm/versions/node/v20.20.1/bin/codex --version
/home/chris/.nvm/versions/node/v20.20.1/bin/codex --help
```

Register shared MCP server for Spike 002:

```bash
/home/chris/.nvm/versions/node/v20.20.1/bin/codex mcp add \
  --env SPIKE_DIR=/home/chris/workshop/coworker/spike/002 \
  coworker-spike -- /home/chris/workshop/coworker/spike/common/mcp-server/spike-mcp-server
```

Inspect:

```bash
/home/chris/.nvm/versions/node/v20.20.1/bin/codex mcp list
/home/chris/.nvm/versions/node/v20.20.1/bin/codex mcp list --json
```

Tool discovery:

```bash
/home/chris/.nvm/versions/node/v20.20.1/bin/codex exec --json \
  "List the exact MCP tool names available from the coworker-spike server. Return only the tool names."
```

Round-trip setup:

```bash
cat > /home/chris/workshop/coworker/spike/002/dispatch.json <<'EOF'
{
  "status": "dispatched",
  "job_id": "codex-test-001",
  "role": "reviewer.arch",
  "prompt": "Review the architecture of spike/common/mcp-server/main.go. Return findings as JSON with keys: summary, findings (array of {file, line, severity, message}).",
  "context": {}
}
EOF
```

Ephemeral run:

```bash
/home/chris/.nvm/versions/node/v20.20.1/bin/codex exec \
  --sandbox danger-full-access \
  "Call orch_next_dispatch. If it returns a dispatch with status 'dispatched', execute the task in the prompt field. When done, call orch_job_complete with the job_id and your structured findings as JSON."
```

JSON capture:

```bash
/home/chris/.nvm/versions/node/v20.20.1/bin/codex exec --json \
  --sandbox danger-full-access \
  "Call orch_next_dispatch. If dispatched, execute the task and call orch_job_complete with findings."
```

Check completion:

```bash
cat /home/chris/workshop/coworker/spike/002/completed.json
```

Important caveat:

- the Spike 002 result showed a meaningful security constraint around sandbox mode; treat the Codex path as researched but not yet production-shaped in coworker

## Spike 003 — OpenCode HTTP dispatch

Plan:

- [docs/plans/003-spike-opencode.md](/home/chris/workshop/coworker/docs/plans/003-spike-opencode.md)

Current verdict:

- `http_dispatch: yes`
- `sse_capture: rich`
- `completion_signal: clear`
- `abort_support: yes`
- `reconnect: reliable`
- `worktree_isolation: clean`
- `ephemeral_run_json: yes`
- recommendation: **http-primary**

Check CLI:

```bash
/home/chris/.opencode/bin/opencode --version
/home/chris/.opencode/bin/opencode --help
```

Start server:

```bash
/home/chris/.opencode/bin/opencode serve --port 4096 --print-logs --log-level DEBUG
```

In another shell, health check:

```bash
curl -sS http://127.0.0.1:4096/global/health
```

Capture OpenAPI:

```bash
curl -sS http://127.0.0.1:4096/doc > /home/chris/workshop/coworker/spike/003/openapi.json
```

Create a session:

```bash
curl -sS -X POST http://127.0.0.1:4096/session \
  -H 'Content-Type: application/json' \
  -d '{"title":"spike-003-test"}'
```

Send a message:

```bash
curl -sS -X POST http://127.0.0.1:4096/session/<SESSION_ID>/message \
  -H 'Content-Type: application/json' \
  -d '{"parts":[{"type":"text","text":"What is 2+2? Reply with just the number."}]}'
```

Subscribe to events:

```bash
curl -sN http://127.0.0.1:4096/event
```

Ephemeral JSON run:

```bash
/home/chris/.opencode/bin/opencode run --format json "Reply with just hello."
```

Abort a session:

```bash
curl -sS -X POST http://127.0.0.1:4096/session/<SESSION_ID>/abort
```

Delete a session:

```bash
curl -sS -X DELETE http://127.0.0.1:4096/session/<SESSION_ID>
```

Important caveat:

- `/doc` is incomplete in `opencode 1.4.7`; trust live route behavior and startup smoke tests over the published OpenAPI alone

## Recommended reading after reruns

- [docs/tutorial.md](/home/chris/workshop/coworker/docs/tutorial.md)
- [docs/specs/000-coworker-runtime-design.md](/home/chris/workshop/coworker/docs/specs/000-coworker-runtime-design.md)
- [docs/specs/001-plan-manifest.md](/home/chris/workshop/coworker/docs/specs/001-plan-manifest.md)
