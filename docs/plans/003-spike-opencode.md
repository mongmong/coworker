# Plan 003 — Spike: OpenCode Server Dispatch

**Goal:** Determine whether OpenCode's `opencode serve` HTTP server can be used as a dispatch target — creating sessions, sending task messages, receiving structured output via SSE event stream, and managing session lifecycle programmatically.

**Duration:** ~0.5 day

**Prerequisites:**
- Plan 000 shipped (Go module, tooling)
- OpenCode CLI installed at `/home/chris/.opencode/bin/opencode`
- At least one AI provider configured for OpenCode (e.g., Anthropic, OpenAI)
- Go 1.25+

**Branch:** `feature/spike-003-opencode`

**Manifest entry:** `docs/specs/001-plan-manifest.md` §003

---

## Background

OpenCode is architecturally different from Claude Code and Codex. It has a first-class HTTP server mode (`opencode serve`) that exposes a RESTful API with SSE event streams, documented via OpenAPI 3.1. There are official SDKs in TypeScript (`@opencode-ai/sdk`) and Go (`github.com/sst/opencode-sdk-go`). This makes OpenCode the "clean path" — no MCP polling, no tmux wake hacks. The spike validates that this path works as expected.

### What we know from research

- **`opencode serve`** runs a headless HTTP server on a configurable port (`--port`, default random). Exposes OpenAPI 3.1 spec at `/doc`.
- **REST API** includes session CRUD, message sending (`session.prompt()`), session forking, aborting, and sharing.
- **SSE event streams** at `/event` (project-level) and `/global/event` (global). Event types include `session.created`, `session.updated`, `session.deleted`, `message.updated`, `part.updated`, `todo.updated`.
- **Go SDK** at `github.com/sst/opencode-sdk-go` provides typed client methods.
- **Authentication** via `OPENCODE_SERVER_PASSWORD` env var (HTTP basic auth, username defaults to `opencode`).
- **MCP server support** — OpenCode can also connect to MCP servers (like Claude Code and Codex), but the primary dispatch path for coworker should be HTTP since it's cleaner.
- **`opencode run`** — non-interactive mode, supports `--format json` for raw JSON events.
- **Known issue:** Server can become unresponsive if SSE clients disconnect abruptly during streaming. Need to handle client-side reconnection.

### Key question

The spec (§Per-CLI mechanism) says OpenCode uses HTTP POST for dispatch and server event stream for output capture, with no tmux wake needed. This spike validates that the API surface is sufficient and the event stream provides the data we need.

---

## Test Protocol

### Test 1: Server startup and OpenAPI discovery

**Question:** Does `opencode serve` start reliably and expose a usable API?

**Steps:**
1. Start the server on a fixed port:
   ```bash
   opencode serve --port 4096 --print-logs --log-level DEBUG 2>&1 | tee spike/003/server.log &
   OPENCODE_PID=$!
   sleep 3
   ```
2. Check the server is responding:
   ```bash
   curl -s http://localhost:4096/health || curl -s http://localhost:4096/
   ```
3. Fetch the OpenAPI spec:
   ```bash
   curl -s http://localhost:4096/doc > spike/003/openapi.json
   ```
4. Examine the spec: list all available endpoints, note session and message paths.
5. Clean up: `kill $OPENCODE_PID`

**Expected result:** Server starts, responds to HTTP requests, and exposes an OpenAPI spec with session/message endpoints.

**Failure mode:**
- Server fails to start → check if a provider is configured; OpenCode may require at least one LLM provider
- No `/doc` endpoint → try `/openapi.json` or `/swagger`; the endpoint path may have changed
- Port conflict → use `--port 0` and parse the log for the assigned port

---

### Test 2: Session creation and message sending

**Question:** Can we programmatically create a session and send a message via the REST API?

**Setup:**
Write a test script `spike/003/test_api.sh`:
```bash
#!/bin/bash
BASE="http://localhost:4096"

# Create a session
SESSION=$(curl -s -X POST "$BASE/session" \
  -H "Content-Type: application/json" \
  -d '{"title": "spike-003-test"}')
echo "Session: $SESSION"
SESSION_ID=$(echo "$SESSION" | jq -r '.id // .sessionID // .session_id')
echo "Session ID: $SESSION_ID"

# Send a message
RESPONSE=$(curl -s -X POST "$BASE/session/$SESSION_ID/message" \
  -H "Content-Type: application/json" \
  -d '{"content": "What is 2+2? Reply with just the number."}')
echo "Response: $RESPONSE"
```

**Steps:**
1. Start `opencode serve --port 4096` in background.
2. Run the test script. Adjust endpoint paths based on OpenAPI spec from Test 1.
3. Verify a session is created and a message is accepted.
4. Check if the response is synchronous (blocks until LLM responds) or returns immediately with a handle.

**Expected result:** Session created successfully; message accepted. Response is either synchronous (with LLM output) or async (with a message/job ID to poll).

**Failure mode:**
- 404 on endpoints → paths differ from expected; consult OpenAPI spec
- 401 → authentication required; set `OPENCODE_SERVER_PASSWORD` and use basic auth
- 500 → provider error; check server logs

---

### Test 3: SSE event stream subscription

**Question:** Can we subscribe to the SSE event stream and receive real-time updates for session activity?

**Setup:**
Write `spike/003/sse_listener.go` — a minimal Go program that:
1. Connects to `http://localhost:4096/event` (or the correct SSE endpoint from the OpenAPI spec)
2. Reads SSE events line by line
3. Parses and prints each event's type and data
4. Exits after 60 seconds or 20 events

**Steps:**
1. Start `opencode serve --port 4096` in background.
2. Start the SSE listener in background: `go run spike/003/sse_listener.go &`
3. Create a session and send a message (via curl, as in Test 2).
4. Observe the SSE listener output.
5. Note event types received: `session.created`, `message.updated`, `part.updated`, etc.
6. Verify events contain enough data to track message completion (assistant response text, tool calls, completion status).

**Expected result:** SSE events arrive in real-time. Events include message content updates and completion signals.

**Failure mode:**
- No events received → wrong endpoint; check OpenAPI spec for SSE paths
- Events arrive but lack content → events may be IDs-only (requiring a GET to fetch details)
- Connection drops → known reconnection issue; implement retry logic

---

### Test 4: Full dispatch cycle via HTTP

**Question:** Can we simulate the full coworker dispatch cycle: create session → send task prompt → receive structured output → close session?

**Setup:**
Write `spike/003/dispatch_test.go` — a Go program that:
1. Starts by connecting to the server
2. Creates a session
3. Subscribes to SSE for that session
4. Sends a message: "Review the following Go code and return findings as JSON: `{code snippet}`"
5. Collects SSE events until the assistant message is complete
6. Extracts the final assistant response
7. Deletes/closes the session

**Steps:**
1. Start `opencode serve --port 4096`.
2. Run the dispatch test: `go run spike/003/dispatch_test.go`
3. Verify:
   - Session was created
   - Message was sent
   - SSE events tracked the response
   - Final response was captured as text
   - Session was cleaned up

**Expected result:** Full cycle works. We get structured text back from the LLM via the server API.

**Failure mode:**
- Session not cleaned up → check if there's a delete/close endpoint
- Response incomplete → SSE stream may end before full response; need a completion signal event
- Timeout → LLM takes too long; set appropriate timeouts

---

### Test 5: `opencode run --format json` as alternative capture

**Question:** Does `opencode run --format json` provide a simpler alternative to the server API for ephemeral dispatch?

**Steps:**
1. Run:
   ```bash
   opencode run --format json \
     "Review this code and return findings as JSON with keys summary and findings: $(cat spike/001/server.go)" \
     2>/dev/null | tee spike/003/run-output.jsonl
   ```
2. Parse each line/block as JSON.
3. Identify event types and final output.
4. Compare data richness with the SSE approach from Test 4.

**Expected result:** JSON output contains structured events with final assistant response.

**Failure mode:**
- Not valid JSON → format may be different from expected
- Missing tool call events → `opencode run` may not expose all events
- If this works well, it may be simpler than the server API for ephemeral roles

---

### Test 6: OpenCode MCP client support (hybrid path)

**Question:** Can OpenCode connect to the coworker MCP server as a client, enabling the same pull model as Claude Code?

**Setup:**
Reuse the MCP server from spike 001.

**Steps:**
1. Register the MCP server with OpenCode:
   ```bash
   opencode mcp add
   ```
   (Follow the interactive prompts, or find the config file format from docs.)
2. Start OpenCode with the MCP server configured.
3. Verify MCP tools (`orch_next_dispatch`, `orch_job_complete`) are available.
4. Test a tool call via `opencode run "Call orch_next_dispatch"`.

**Expected result:** OpenCode can connect to MCP servers and call custom tools.

**Failure mode:**
- MCP config format unclear → check OpenCode docs or config
- Tools not discovered → protocol version mismatch
- This test is informational — the primary OpenCode path is HTTP, not MCP pull

---

### Test 7: Server session lifecycle and concurrency

**Question:** Can the server handle multiple concurrent sessions? What happens when sessions are abandoned?

**Steps:**
1. Start `opencode serve --port 4096`.
2. Create 3 sessions via the API.
3. Send a message to each session concurrently (using `&` in shell).
4. Verify all 3 responses are received.
5. Abandon one session (don't close it), create a 4th.
6. After 5 minutes, check server memory/responsiveness: `curl -s http://localhost:4096/health` or equivalent.
7. Clean up all sessions.

**Expected result:** Multiple concurrent sessions work. Abandoned sessions don't crash the server.

**Failure mode:**
- Server errors under concurrent requests → serialization issue
- Memory leak with abandoned sessions → need periodic cleanup
- SSE stream corruption → the known disconnection issue

---

### Test 8: Go SDK viability

**Question:** Does the official Go SDK (`github.com/sst/opencode-sdk-go`) work for our use case?

**Setup:**
```bash
cd spike/003
go mod init spike003
go get github.com/sst/opencode-sdk-go
```

Write `spike/003/sdk_test.go` that uses the Go SDK to:
1. Create a client
2. List sessions
3. Create a session
4. Send a message (if the SDK supports it)
5. Subscribe to events (if the SDK supports SSE)

**Steps:**
1. Start `opencode serve --port 4096`.
2. Run: `cd spike/003 && go test -v -run TestSDK -timeout 120s`
3. Note which operations the SDK supports and which require raw HTTP.

**Expected result:** SDK provides typed access to at least session CRUD and message sending.

**Failure mode:**
- SDK out of date → version mismatch with current server API
- SDK doesn't cover message sending → need raw HTTP for some operations
- If SDK is unusable, raw HTTP + generated client from OpenAPI is the fallback

---

## Decision Matrix

| Dimension | Result | Implication |
|---|---|---|
| Server startup | reliable/flaky | Foundational |
| Session CRUD via API | yes/no | Core dispatch viability |
| Message sending + response | sync/async/no | Determines dispatch model (fire-and-wait vs fire-and-poll) |
| SSE event stream | rich/sparse/broken | Output capture strategy |
| Full dispatch cycle | yes/no | End-to-end viability |
| `opencode run --format json` | usable/not | Ephemeral alternative |
| MCP client support | yes/no | Hybrid path option |
| Concurrent sessions | yes/no | Multi-worker viability |
| Go SDK | usable/partial/broken | Implementation convenience |

## Verdict Template

Fill in after running:
- `server_dispatch:` yes | partial | no
- `sse_capture:` rich | sparse | broken
- `session_lifecycle:` clean | leaky | broken
- `concurrent_sessions:` yes | no
- `go_sdk:` usable | partial | raw-http-needed
- `opencode_run_json:` usable | not
- `mcp_client:` yes | no
- `recommendation:` http-dispatch | mcp-pull | hybrid | opencode-run-only
- `plan_104_impact:` <how this affects the MCP server plan>
- `plan_110_impact:` <how this affects the OpenCode plugin plan>

---

## Spike Code Location

All prototype code lives in `spike/003/`:
- `go.mod` / `go.sum` — independent module
- `sse_listener.go` — SSE event stream consumer
- `dispatch_test.go` — full dispatch cycle test
- `sdk_test.go` — Go SDK viability test
- `test_api.sh` — shell script for quick API testing
- `openapi.json` — captured OpenAPI spec from server
- `server.log` — server output log
- `run-output.jsonl` — `opencode run` JSON output
- `RESULTS.md` — raw test results

---

## Post-Execution Report

*(fill in after running the spike)*

### Test Results

| Test | Result | Notes |
|---|---|---|
| 1. Server startup | | |
| 2. Session + message | | |
| 3. SSE event stream | | |
| 4. Full dispatch cycle | | |
| 5. opencode run --format json | | |
| 6. MCP client support | | |
| 7. Session lifecycle | | |
| 8. Go SDK | | |

### Verdict

*(fill in using verdict template above)*

### Recommendations for Plan 104 / 110

*(fill in based on findings)*
