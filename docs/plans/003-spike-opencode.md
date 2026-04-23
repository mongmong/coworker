# Plan 003 — Spike: OpenCode Server Dispatch

**Goal:** Determine whether OpenCode's `opencode serve` HTTP server can be used as a dispatch target — creating sessions, sending task messages, receiving structured output via SSE event stream, and managing session lifecycle programmatically.

**Duration:** ~1 day

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

### Scope for this spike

This plan is intentionally split into:

- **Core viability tests** — Tests 0-5 and 9-12. These decide whether the HTTP server path is usable for coworker.
- **Optional / informational tests** — Tests 6 and 8. Useful if the core path works, but not required for the main verdict.
- **Concurrency hardening** — Test 7. Only run after the core path is proven.

The spike should answer one primary question first: can a single OpenCode server instance, bound to a single repo/worktree, accept a prompt over HTTP and provide a programmatically detectable completion signal over SSE?

---

## Test Protocol

### Test 0: CLI availability

**Question:** Is the OpenCode CLI present at the expected path with expected flags?

**Steps:**
1. Verify the binary exists and runs:
   ```bash
   /home/chris/.opencode/bin/opencode --version 2>&1 | tee spike/003/RESULTS.md
   ```
2. Capture help output:
   ```bash
   /home/chris/.opencode/bin/opencode --help 2>&1 >> spike/003/RESULTS.md
   ```
3. Confirm key subcommands exist: `serve`, `run`, `mcp`.

**Expected result:** Both commands succeed. Key subcommands are present in `--help` output.

**Failure mode:** Binary not found or flags changed → update paths/flags before proceeding.

---

### Test 1: Server startup and OpenAPI discovery

**Question:** Does `opencode serve` start reliably and expose a usable API?

**Steps:**
1. Start the server on a fixed port:
   ```bash
   /home/chris/.opencode/bin/opencode serve --port 4096 --print-logs --log-level DEBUG 2>&1 | tee spike/003/server.log &
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

### Contract Freeze (required before Tests 2-12)

After Test 1, write `spike/003/api-contract.md` capturing the actual server contract discovered from OpenAPI and live responses:

- health endpoint
- OpenAPI endpoint
- auth mode observed in this environment (unauthenticated vs password required)
- session create/list/delete endpoints
- prompt/message endpoint
- abort/cancel endpoint
- SSE endpoints (`/event`, `/global/event`, or other)
- fields that identify session ID, message ID, completion state, and assistant text

Every later test must use the discovered endpoints and fields from `api-contract.md`, not the guessed paths in the draft plan.

---

### Test 2: Session creation and message sending

**Question:** Can we programmatically create a session and send a message via the REST API?

**Setup:**
Write a test script `spike/003/test_api.sh` using the paths frozen in `spike/003/api-contract.md`.
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
1. Start `/home/chris/.opencode/bin/opencode serve --port 4096` in background.
2. Run the test script using the actual endpoint paths from `api-contract.md`.
3. Verify a session is created and a message is accepted.
4. Check if the response is synchronous (blocks until LLM responds) or returns immediately with a handle.

**Expected result:** Session created successfully; message accepted. Response is either synchronous (with LLM output) or async (with a message/job ID to poll).

**Failure mode:**
- 404 on endpoints → paths differ from expected; consult OpenAPI spec
- 401 → authentication required; restart the test with `OPENCODE_SERVER_PASSWORD` set and use basic auth consistently for all later HTTP requests
- 500 → provider error; check server logs

---

### Test 3: SSE event stream subscription

**Question:** Can we subscribe to the SSE event stream and receive real-time updates for session activity?

**Setup:**
Write `spike/003/cmd/sse-listener/main.go` — a minimal Go program that:
1. Connects to the SSE endpoint recorded in `spike/003/api-contract.md`
2. Reads SSE events line by line
3. Parses and prints each event's type and data
4. Exits after 60 seconds or 20 events

**Steps:**
1. Start `/home/chris/.opencode/bin/opencode serve --port 4096` in background.
2. Start the SSE listener in background: `go run ./spike/003/cmd/sse-listener &`
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
Write `spike/003/cmd/dispatch-cycle/main.go` — a Go program that:
1. Starts by connecting to the server
2. Creates a session
3. Subscribes to SSE for that session
4. Sends a message asking OpenCode to review `spike/common/mcp-server/main.go` and return findings as JSON
5. Collects SSE events until the assistant message is complete
6. Extracts the final assistant response
7. Deletes/closes the session

**Steps:**
1. Start `/home/chris/.opencode/bin/opencode serve --port 4096`.
2. Run the dispatch test: `go run ./spike/003/cmd/dispatch-cycle`
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
   /home/chris/.opencode/bin/opencode run --format json \
     "Review spike/common/mcp-server/main.go and return findings as JSON with keys summary and findings." \
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

### Test 6: OpenCode MCP client support (hybrid path, optional)

**Question:** Can OpenCode connect to the coworker MCP server as a client, enabling the same pull model as Claude Code?

**Setup:**
Reuse the MCP server from spike 001. Build if not already built:
```bash
cd spike/common/mcp-server && go build -o spike-mcp-server . && cd -
```

**Steps:**
1. Register the MCP server with OpenCode:
   ```bash
   /home/chris/.opencode/bin/opencode mcp add
   ```
   (Follow the interactive prompts, or find the config file format from docs.)
2. Start OpenCode with the MCP server configured.
3. Verify MCP tools (`orch_next_dispatch`, `orch_job_complete`) are available.
4. Test a tool call via `/home/chris/.opencode/bin/opencode run "Call orch_next_dispatch"`.

**Expected result:** OpenCode can connect to MCP servers and call custom tools.

**Failure mode:**
- MCP config format unclear → check OpenCode docs or config
- Tools not discovered → protocol version mismatch
- This test is informational only — the primary OpenCode path is HTTP, not MCP pull. Do not let this block the main verdict.

---

### Test 7: Server session lifecycle and concurrency

**Question:** Can the server handle multiple concurrent sessions? What happens when sessions are abandoned?

**Gate:** Run this only if Tests 2-4 and 11 all pass.

**Steps:**
1. Start `/home/chris/.opencode/bin/opencode serve --port 4096`.
2. Create 3 sessions via the API.
3. Send a message to each session concurrently (using `&` in shell).
4. Verify all 3 responses are received and correctly correlated to their originating session IDs.
5. Abandon one session (don't close it), create a 4th.
6. After 5 minutes, check server memory/responsiveness: `curl -s http://localhost:4096/health` or equivalent.
7. Clean up all sessions.

**Expected result:** Multiple concurrent sessions work. Abandoned sessions don't crash the server.

**Failure mode:**
- Server errors under concurrent requests → serialization issue
- Memory leak with abandoned sessions → need periodic cleanup
- SSE stream corruption → the known disconnection issue

---

### Test 8: Go SDK viability (optional)

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
1. Start `/home/chris/.opencode/bin/opencode serve --port 4096`.
2. Run: `cd spike/003 && go test -v -run TestSDK -timeout 120s`
3. Note which operations the SDK supports and which require raw HTTP.

**Expected result:** SDK provides typed access to at least session CRUD and message sending.

**Failure mode:**
- SDK out of date → version mismatch with current server API
- SDK doesn't cover message sending → need raw HTTP for some operations
- If SDK is unusable, raw HTTP + generated client from OpenAPI is the fallback. This does not block the primary HTTP verdict.

---

### Test 9: Workspace/worktree isolation

**Question:** Does `opencode serve` correctly bind to a git worktree root and maintain isolation between sessions with different project roots?

**Setup:**
Create a git worktree at a temp path:
```bash
WORKTREE_DIR=$(mktemp -d)
git worktree add "$WORKTREE_DIR" HEAD --detach
```

**Steps:**
1. Start `/home/chris/.opencode/bin/opencode serve --port 4097 --print-logs` from within the worktree:
   ```bash
   cd "$WORKTREE_DIR" && /home/chris/.opencode/bin/opencode serve --port 4097 --print-logs 2>&1 | tee /home/chris/workshop/coworker/spike/003/worktree-server.log &
   WORKTREE_PID=$!
   sleep 3
   ```
2. Create a session and send a message that references a file path:
   ```bash
   curl -s -X POST http://localhost:4097/session \
     -H "Content-Type: application/json" \
     -d '{"title": "worktree-test"}'
   ```
3. Verify the session binds to the worktree root, not the main checkout:
   - Ask the session "What is your current working directory?" or check server logs for the project root.
   - Create a file in the worktree and verify the session can see it.
   - Verify the session does NOT see uncommitted files from the main checkout.
4. Start a second server instance on the main checkout (port 4096) and verify:
   - Sessions on port 4096 see the main checkout.
   - Sessions on port 4097 see the worktree.
   - No state leaks between the two (different session lists, different file views).
5. Clean up:
   ```bash
   kill $WORKTREE_PID
   git worktree remove "$WORKTREE_DIR"
   ```

**Expected result:** Each `opencode serve` instance binds to its own project root. No state leaks between instances with different roots.

**Failure mode:**
- Server always uses the main checkout → OpenCode resolves the repo root, not cwd
- State leaks between instances → shared state directory; need `--data-dir` or similar isolation
- Worktree not recognized as valid project → OpenCode may require `.git` directory (worktrees use `.git` file)

---

### Test 10: Cancel/abort in-flight prompt

**Question:** Can we abort a running prompt via the API? How does the server signal cancellation?

**Steps:**
1. Start `/home/chris/.opencode/bin/opencode serve --port 4096`.
2. Create a session and subscribe to SSE events.
3. Send a long-running prompt (e.g., "Write a comprehensive 5000-word essay on the history of computing"):
   ```bash
   SESSION_ID=<from session creation>
   curl -s -X POST "http://localhost:4096/session/$SESSION_ID/message" \
     -H "Content-Type: application/json" \
     -d '{"content": "Write a comprehensive 5000-word essay on the history of computing."}' &
   MSG_PID=$!
   sleep 5
   ```
4. Abort the message via the API (check OpenAPI spec for abort endpoint — likely `POST /session/$SESSION_ID/abort` or `DELETE /session/$SESSION_ID/message/<msg_id>`):
   ```bash
   curl -s -X POST "http://localhost:4096/session/$SESSION_ID/abort"
   ```
5. Observe:
   - Does the SSE stream emit a cancellation event?
   - Does the API return a success/failure response?
   - Is the session still usable after abort (can we send a new message)?
6. Record the exact event types and timing.

**Expected result:** Abort succeeds. SSE stream signals cancellation. Session remains usable.

**Failure mode:**
- No abort endpoint → cancellation not supported via API; document the gap
- Abort succeeds but session is corrupted → need to create a new session after abort
- SSE stream hangs after abort → the known disconnection issue

---

### Test 11: Terminal completion detection via SSE

**Question:** How does the SSE event stream signal that a message/response is fully complete?

**Steps:**
1. Start `/home/chris/.opencode/bin/opencode serve --port 4096`.
2. Subscribe to SSE events with detailed logging.
3. Send a simple prompt and capture the full SSE event sequence.
4. Identify the **terminal event** — the event that signals "the assistant is done responding." Look for:
   - A `message.updated` with a `status: "complete"` field
   - A `session.updated` with an idle/ready state
   - A distinct `message.done` or `message.completed` event type
   - The SSE stream closing
5. Send a prompt that triggers tool use (if OpenCode has built-in tools) and capture the event sequence through tool call → tool result → final response.
6. Document the exact completion signal for both simple and tool-use responses.

**Expected result:** A clear, programmatically detectable signal that the assistant response is complete. Document the exact event type and fields.

**Failure mode:**
- No clear completion signal → must poll session state via REST to detect completion
- Completion signal is unreliable → need timeout-based detection as fallback
- Different signals for simple vs tool-use responses → document both paths

---

### Test 12: SSE reconnection after disconnect

**Question:** Can we reliably reconnect to the SSE stream after a client-side disconnect? (This addresses the known bug.)

**Steps:**
1. Start `/home/chris/.opencode/bin/opencode serve --port 4096`.
2. Subscribe to SSE events.
3. Disconnect the SSE client (close the HTTP connection).
4. Wait 5 seconds.
5. Reconnect to the SSE endpoint.
6. Send a new message and verify events are received on the new connection.
7. Repeat the disconnect/reconnect cycle 3 times.

---

## Pass/Fail Gates

OpenCode HTTP dispatch is viable only if all of the following pass:

| Gate | Required Test(s) | Criterion |
|---|---|---|
| Server/API discovery | Tests 0-1 | `opencode serve` starts and exposes a usable OpenAPI/HTTP surface |
| Session create/send | Test 2 | A session can be created and a prompt can be accepted via HTTP |
| Event capture | Test 3 | SSE provides observable real-time events for the session |
| End-to-end dispatch | Test 4 | The full create -> send -> capture -> cleanup cycle works |
| Completion detection | Test 11 | There is a clear, programmatically detectable terminal completion signal |
| Worktree isolation | Test 9 | A server instance binds to its own project/worktree root without state leakage |

Additional qualifiers:

| Qualifier | Required Test(s) | Criterion |
|---|---|---|
| Ephemeral fallback | Test 5 | `opencode run --format json` yields usable machine-readable output |
| Abort support | Test 10 | In-flight cancellation works and leaves the session usable |
| SSE resilience | Test 12 | Disconnect/reconnect works reliably enough for client-side recovery |
| Concurrency | Test 7 | Multiple sessions can run concurrently without session/event leakage |

Optional/informational only:

- Test 6: OpenCode MCP client support
- Test 8: Go SDK viability

---

## Decision Matrix

| Dimension | Result | Implication |
|---|---|---|
| Server startup + OpenAPI | yes/no | Foundational |
| Session create/send | yes/no | HTTP dispatch viability |
| SSE event richness | sufficient/insufficient | Whether SSE alone can drive runtime state |
| Terminal completion signal | clear/unclear | Whether completion can be detected without polling hacks |
| Cancel/abort | yes/no | Runtime control surface completeness |
| Reconnect behavior | reliable/flaky/broken | Client resilience requirements |
| Worktree isolation | clean/leaky | Multi-plan deployment model |
| `opencode run --format json` | usable/not | Ephemeral fallback path |
| MCP hybrid path | yes/no | Informational only |
| Go SDK viability | usable/partial/unusable | Implementation ergonomics |

## Verdict Template

Fill in after running:

- `http_dispatch:` yes | partial | no
- `sse_capture:` rich | partial | poor
- `completion_signal:` clear | ambiguous | none
- `abort_support:` yes | partial | no
- `reconnect:` reliable | flaky | broken
- `worktree_isolation:` clean | partial | broken
- `ephemeral_run_json:` yes | partial | no
- `recommendation:` http-primary | http-primary-with-caveats | ephemeral-only | not-viable
- `plan_104_impact:` <how this affects the HTTP agent/server plan>
- `plan_110_impact:` <how this affects the OpenCode role integration plan>

---

## Spike Code Location

All prototype code lives in `spike/003/`:
- `cmd/sse-listener/main.go` — SSE event stream consumer
- `cmd/dispatch-cycle/main.go` — full dispatch cycle test
- `test_api.sh` — shell script for quick API testing
- `abort_probe.sh` — cancel / recovery probe
- `reconnect_probe.sh` — SSE reconnect probe
- `concurrency_probe.sh` — concurrent-session probe
- `api-contract.md` — frozen endpoint/field contract from Test 1
- `openapi.json` — captured OpenAPI spec from server
- `run-output.jsonl` — `opencode run` JSON output
- `run-output-simple.jsonl` — simple `opencode run` JSON output
- `reconnect-cycle-*.log` — SSE reconnect evidence
- `reconnect-cycle-*.json` — synchronous responses from reconnect test
- `RESULTS.md` — raw test results

---

## Post-Execution Report

### Test Results

| Test | Result | Notes |
|---|---|---|
| 0. CLI availability | PASS | `opencode 1.4.7`; `serve`, `run`, `mcp`, `attach`, and `session` subcommands present. |
| 1. Server startup + OpenAPI | PASS WITH CAVEAT | Server started on `127.0.0.1:4096`; `/doc` exists but omits live session routes in `1.4.7`. |
| 2. Session create/send | PASS | `POST /session` and `POST /session/{id}/message` worked directly; message body must use `parts`, not `content`. |
| 3. SSE subscription | PASS | `/event` streamed `session.created`, `message.updated`, `message.part.updated`, `message.part.delta`, `session.status`, `session.idle`, `session.deleted`. |
| 4. Full dispatch cycle | PASS | `go run spike/003/dispatch_cycle.go` created a session, captured a review response, and deleted the session. |
| 5. `run --format json` | PASS | Simple prompt completed as compact JSONL; review prompt also completed with tool-use and final JSON output. |
| 6. MCP hybrid (optional) | NOT RUN | Deferred; HTTP path is sufficient for the primary verdict. |
| 7. Concurrency | PASS | Three sessions completed concurrently with correct session correlation and no obvious leakage. |
| 8. Go SDK (optional) | NOT RUN | Deferred; raw HTTP is already viable and the installed SDK route bundle was enough to confirm hidden endpoints. |
| 9. Worktree isolation | PASS | A second server bound to `/tmp/coworker-spike003-W89fHP` and created sessions rooted in that worktree, not the main checkout. |
| 10. Cancel/abort | PASS | `POST /session/{id}/abort` returned `true`; aborted message surfaced `MessageAbortedError`; same session accepted a follow-up prompt. |
| 11. Completion detection | PASS | Normal completion is observable via final assistant `message.updated` / `step-finish` and `session.idle`. |
| 12. SSE reconnect | PASS | Three disconnect/reconnect cycles succeeded; each new listener received fresh session events and terminal `session.idle`. |

### Verdict

- `http_dispatch: yes`
- `sse_capture: rich`
- `completion_signal: clear`
- `abort_support: yes`
- `reconnect: reliable`
- `worktree_isolation: clean`
- `ephemeral_run_json: yes`
- `recommendation: http-primary`
- `plan_104_impact:`
  - OpenCode should be modeled as a first-class HTTP worker, not an MCP-pull worker.
  - Runtime code should not rely on `/doc` alone for route discovery in `opencode 1.4.7`; prefer a fixed client against known routes, validated by startup smoke tests.
  - Completion detection can use `session.idle` plus final assistant message updates instead of polling hacks.
- `plan_110_impact:`
  - OpenCode role integration can dispatch with `POST /session` + `POST /session/{id}/message`, subscribe to `/event`, and clean up with `DELETE /session/{id}`.
  - Abort is available and leaves the session reusable, so long-running jobs do not require process-level teardown.
  - Per-plan worktree servers are viable because cwd/root binding stayed isolated in the worktree test.

### Recommendations for Plan 104 / 110

1. Treat OpenCode as the cleanest persistent worker path in coworker. The daemon can own HTTP dispatch and SSE subscription directly.
2. Encode the `parts` message shape explicitly in the runtime client. Do not assume a simple `content` string request body.
3. Add startup smoke tests that validate the live route surface because `/doc` is incomplete in `opencode 1.4.7`.
4. Use `session.idle` as the primary terminal signal, with final assistant `message.updated` / `step-finish` as corroboration.
5. Keep `opencode run --format json` as a lightweight ephemeral fallback for simple roles or degraded mode.

---

## Code Review

### Review 1

- **Date**: 2026-04-22
- **Reviewer**: Codex (self-review)
- **PR**: N/A — pre-PR review for plan 003 spike execution updates
- **Verdict**: Approved

No open findings. The report and artifacts align with the executed OpenCode behavior.
