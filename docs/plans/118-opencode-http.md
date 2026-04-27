# Plan 118 — OpenCode HTTP Dispatch

**Goal:** Implement `OpenCodeHTTPAgent`, a `core.Agent` backed by OpenCode's REST/SSE HTTP server, so the coworker daemon can dispatch jobs to OpenCode without shelling out to the CLI binary. Wire the agent into the existing `Dispatcher.CLIAgents` map for the "opencode" binding in both `daemon` and `run` commands.

**Branch:** `feature/plan-118-opencode-http`

**Addresses:** Review finding I-8 from `docs/reviews/2026-04-26-v1-comprehensive-review.md`

**Prior art:**
- Spike findings: `docs/plans/003-spike-opencode.md` (all pass gates met; recommendation: http-primary)
- SSE event contract: `spike/003/api-contract.md` + `spike/003/reconnect-cycle-1.log`
- SSE parse pattern: `cli/watch.go`
- Agent interface: `core/agent.go`
- Pattern to follow: `agent/cli_agent.go` + `agent/cli_handle.go`

---

## Architecture

### OpenCodeHTTPAgent

```
agent/
  opencode_http_agent.go      — OpenCodeHTTPAgent struct + Dispatch()
  opencode_http_agent_test.go — httptest-backed unit tests
```

`OpenCodeHTTPAgent` implements `core.Agent`. Its `Dispatch()` method:

1. POST `/session` with `{"title": "<jobID>"}` → get session ID
2. POST `/session/{id}/message` with `{"parts":[{"type":"text","text":prompt}]}` → synchronous; get first assistant message text from the response
3. Subscribe to GET `/event` SSE stream
4. Filter events to the target session ID; collect `message.updated` (assistant, `finish:"stop"`) and `message.part.updated` (text parts)
5. On `session.idle` for the target session → mark complete; extract final assistant text
6. Parse the final text for findings (same JSONL stream-message format as CliAgent)
7. DELETE `/session/{id}` for cleanup (best-effort, non-fatal)
8. Return a `JobHandle` whose `Wait()` returns the `JobResult`

### Note on synchronous message response

The spike showed that `POST /session/{id}/message` blocks until the LLM responds and returns the full materialized message object. However, SSE events may arrive _before_ the POST returns (OpenCode streams to SSE while the HTTP response is building). The implementation uses a goroutine for SSE subscription that races with the POST response; `session.idle` is the definitive completion signal. The synchronous response body is a secondary confirmation used to extract the final text when SSE is unavailable or races.

Chosen strategy (simple, correct):
- Start SSE subscription goroutine _before_ POST /message.
- POST /message in the main goroutine (blocks until OpenCode finishes).
- SSE goroutine captures `session.idle` and the final assistant text.
- When `Wait()` is called, block until SSE goroutine signals done _or_ ctx is cancelled.
- Use SSE-captured text as the authoritative assistant output (richer than the sync response for tool-use cases). Fall back to sync response text if SSE goroutine never observed `session.idle` before context cancellation.

### JobHandle

```go
type openCodeJobHandle struct {
    sessionID  string
    serverURL  string
    client     *http.Client
    done       <-chan struct{}        // closed when SSE goroutine completes
    resultCh   <-chan *core.JobResult // buffered(1); SSE goroutine writes once
    cancel     context.CancelFunc    // cancels SSE goroutine
}
```

`Wait(ctx)` — selects on done/ctx.Done; returns result or ctx.Err.
`Cancel()` — calls `cancel()` (cancels SSE goroutine) then POSTs `/session/{id}/abort`.

### SSE event types used

From the spike contract (`session.idle` = definitive terminal signal):

```
session.idle                 → complete; extract final text
message.updated (role:assistant, finish:stop) → corroboration
message.part.updated (type:text) → accumulate assistant text
session.error                → surface in result.Stderr, still wait for session.idle
```

Events from _other_ sessions are silently ignored (filter by `properties.sessionID`).

### Findings parsing

The assistant's text output is expected to contain one JSONL `finding` record per line (same format as CliAgent). The SSE handler accumulates the final `text` part of the assistant message and feeds it through the same `streamMessage` decoder loop used in `cli_handle.go`.

If the text is not valid JSONL (e.g., the role returns free-form prose), no findings are recorded and `result.Stdout` contains the raw text. This matches CliAgent behaviour.

### Dispatcher wiring

`buildDaemonDispatcher` (cli/daemon.go) and `buildRunDispatcher` (cli/run.go) currently put a `CliAgent("opencode")` in the "opencode" slot. Plan 118 replaces that entry with an `OpenCodeHTTPAgent` when `--opencode-server` is specified (or the default `http://127.0.0.1:7777` is reachable — V1 always uses the flag value; liveness check is out of scope).

New flags added to both `daemon` and `run` commands:
- `--opencode-server` (default `http://127.0.0.1:7777`) — OpenCode server URL

When `--opencode-server` is set to a non-empty value, the "opencode" CLIAgents slot is populated with `OpenCodeHTTPAgent{ServerURL: flag}`. When the flag is empty string (opt-out), the slot falls back to the existing `CliAgent`.

---

## Tech Stack

- `net/http` stdlib — POST/DELETE requests + SSE GET (no new libraries)
- `bufio.Reader` — SSE line reader (same as `cli/watch.go`)
- `encoding/json` — event and message decoding
- `net/http/httptest` — mock OpenCode server in tests

---

## Phases

### Phase 1 — HTTPAgent core (`agent/opencode_http_agent.go`)

1. Define `OpenCodeHTTPAgent` struct with `ServerURL string`, `HTTPClient *http.Client` (nil → use default).
2. Implement `Dispatch(ctx, job, prompt) (JobHandle, error)`:
   a. Create session via POST /session
   b. Spawn SSE goroutine (subscribes to /event, filters by sessionID, collects events)
   c. POST /session/{id}/message (synchronous; use the response body as fallback text)
   d. Return `openCodeJobHandle`
3. Implement `openCodeJobHandle.Wait(ctx)` and `Cancel()`.
4. Implement SSE goroutine: parse SSE stream, extract events, detect `session.idle`.
5. Implement `parseAssistantText(text string) *core.JobResult` — same decoder loop as cli_handle.go.
6. Implement `deleteSession(ctx, id)` — best-effort DELETE /session/{id}.

### Phase 2 — Dispatcher integration (`cli/daemon.go`, `cli/run.go`)

1. Add `daemonOpenCodeServer` / `runOpenCodeServer` package-level string vars.
2. Register `--opencode-server` flag in `init()` for both commands.
3. In `buildDaemonDispatcher`: when `openCodeServer != ""`, replace the "opencode" CLIAgents slot with `&agent.OpenCodeHTTPAgent{ServerURL: openCodeServer}`.
4. In `buildRunDispatcher`: same.
5. Keep the CliAgent fallback when the flag is empty.

### Phase 3 — Tests (`agent/opencode_http_agent_test.go`)

Test cases using `httptest.NewServer`:

1. `TestOpenCodeHTTPAgent_HappyPath` — full dispatch: session created, message sent, SSE events (message.part.updated, session.idle), findings parsed, session deleted.
2. `TestOpenCodeHTTPAgent_Cancel` — cancel via context cancellation; verify abort POST is sent.
3. `TestOpenCodeHTTPAgent_SSEReconnect` — SSE connection drops mid-stream; verify goroutine reconnects and eventually receives session.idle.
4. `TestOpenCodeHTTPAgent_NoSessionIdle_ContextTimeout` — session.idle never arrives; context times out; Wait returns ctx.Err.
5. `TestOpenCodeHTTPAgent_SessionError` — session.error event arrives before session.idle; result.Stderr contains error; Wait still returns (no hang).
6. `TestOpenCodeHTTPAgent_NonJSONLOutput` — assistant text is free-form prose; no findings; result.Stdout contains text.

### Phase 4 — Docs (`agent/doc.go`)

Update the package doc comment to mention `OpenCodeHTTPAgent` as the first HTTP-backed agent implementation and describe the pattern for future HTTP-based agents.

---

## Code Review

### Review 1

**Date:** 2026-04-20
**Reviewer:** Claude Code (post-implementation self-review)

#### Blocker #1: Dispatch blocks on synchronous POST [FIXED]

`agent/opencode_http_agent.go` — `Dispatch()` previously blocked waiting for the `sendMessage` goroutine to return before returning the `JobHandle`. This meant `Cancel()` could not interrupt an in-flight run.

→ Fixed: `sendMessage` is now fire-and-forget inside a goroutine that uses `sseCtx`. `Dispatch` returns the `JobHandle` immediately after starting the SSE goroutine and the message goroutine. `Wait()` still blocks on `resultCh`; `Cancel()` can now interrupt because both goroutines respect the shared `sseCtx`.

#### Blocker #2: message.part.updated accepted all text parts regardless of role [FIXED]

`agent/opencode_http_agent.go` — `processSSEEvent` previously accepted every `message.part.updated` event with `type:"text"`, including the user's own prompt text (which also arrives as a `message.part.updated` event). This could contaminate the assistant's output with the prompt.

→ Fixed: Added `message.updated` event handling that registers assistant message IDs (from events with `role:"assistant"`) into a `map[string]struct{}`. `message.part.updated` events are now only collected when `part.messageID` is in the assistant ID set. The map is threaded through `sseLoop` → `sseStream` → `processSSEEvent` and is shared across reconnects. New type `openCodeMessageUpdatedProps` added. Tests updated with `messageUpdatedAssistantEvent` helper and new unit tests for the filter.

#### Blocker #3: --opencode-server flag default was empty string [FIXED]

`cli/daemon.go` and `cli/run.go` — flag default was `""`, which meant the dispatcher fell back to `CliAgent` by default instead of using `OpenCodeHTTPAgent`.

→ Fixed: Default changed to `agent.DefaultOpenCodeServerURL` (`"http://127.0.0.1:7777"`) in both flag registrations. The dispatcher will now use `OpenCodeHTTPAgent` by default; passing `--opencode-server ""` opts out to the CLI binary fallback.

#### Important #1: sendMessage response body drain [WONTFIX]

`sendMessage` drains and discards the POST response body. A "fallback parse if SSE didn't deliver findings" was noted as a nice-to-have.

→ No change: SSE is the authoritative findings source. The drain is documented with a comment in the code. Adding a fallback parse would complicate the code with little benefit since SSE is reliable per the spike.

#### Important #2: Cancel() races SSE cleanup with abort POST [FIXED]

`Cancel()` previously called `h.cancel()` first, which could cause the SSE goroutine to run its DELETE cleanup before the abort POST was sent.

→ Fixed: `Cancel()` now POSTs `/abort` first (best-effort, using `context.Background` so it is not cancelled by `h.cancel()`), then calls `h.cancel()`. Error from abort is silently discarded — the session may already be complete.

---

## Post-Execution Report

### Date
2026-04-20

### Implementation summary

All four phases completed in one session:

**Phase 1 — `agent/opencode_http_agent.go`**
- `OpenCodeHTTPAgent` struct with `ServerURL` and `HTTPClient` fields.
- `Dispatch()`: creates session, spawns SSE goroutine, posts message synchronously in a goroutine so context cancellation can interrupt it, returns `openCodeJobHandle`.
- `openCodeJobHandle.Wait()` / `Cancel()`: Wait selects on resultCh/ctx.Done; Cancel calls sseCancel then POSTs /session/{id}/abort.
- `sseLoop()`: reconnect loop with exponential backoff (250ms → 5s); exits on `session.idle` or ctx cancelled.
- `sseStream()`: single SSE connection; filters events by sessionID; handles `session.idle` (done), `message.part.updated` (text accumulation), `session.error` (surfaced in result.Stderr).
- `parseAssistantText()`: tries JSONL decoder; falls back to result.Stdout for free-form text.
- Session cleanup via DELETE uses `context.Background` (intentional, annotated with `//nolint:gosec`).

**Phase 2 — Dispatcher integration**
- Added `--opencode-server` flag to both `daemon` and `run` commands.
- `buildDaemonDispatcher` and `buildRunDispatcher` both accept the server URL and put `&agent.OpenCodeHTTPAgent{...}` in the "opencode" CLIAgents slot when non-empty; fall back to CliAgent when empty.
- Signature of `buildDaemonDispatcher` updated to accept `openCodeServer string`.

**Phase 3 — Tests (`agent/opencode_http_agent_test.go`)**
- 14 test cases using `httptest.NewServer` covering:
  - Happy path with JSONL findings
  - Cancel / abort
  - SSE reconnect after connection drop
  - Context timeout before session.idle
  - session.error propagation to result.Stderr
  - Free-form (non-JSONL) prose output
  - Message send failure (500 from server)
  - Unit tests for `parseAssistantText`, `processSSEEvent`, `serverURL`

**Phase 4 — `agent/doc.go`**
- Updated package comment to document both implementations and the HTTP Agent pattern for future additions.

### Verification

```
go build ./...                     → clean
go test -race ./... -count=1 -timeout 120s → 24 ok, 0 failed, 0 races
golangci-lint run ./...            → 0 issues
```

### Notes / deviations from plan

- The spike showed `POST /session/{id}/message` is synchronous and blocks until OpenCode responds. To allow context cancellation during the send, the POST is run in its own goroutine with a select. This is slightly different from the plan description but more robust.
- `openCodeMessageUpdatedProps` and `sseResult` types were removed (unused) to satisfy `unused` linter.
- SSE loop refactored to `for ctx.Err() == nil` to satisfy staticcheck QF1006.
- No new test infrastructure was required; `httptest.NewServer` with Go 1.25 method-pattern routing (`POST /path/{var}`) was sufficient.
