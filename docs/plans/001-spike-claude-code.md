# Plan 001 — Spike: Claude Code Persistent MCP Pull

**Goal:** Determine whether Claude Code can reliably act as a persistent MCP-pull worker — connecting to a custom MCP server, polling `orch.next_dispatch()` at each turn end via a skill, returning structured output via `orch.job.complete()`, and being woken from idle via tmux send-keys.

**Duration:** ~1 day

**Prerequisites:**
- Plan 000 shipped (Go module, tooling)
- Claude Code CLI installed at `/home/chris/.local/bin/claude`
- Anthropic API key configured (Claude Code auth working)
- `tmux` installed
- Go 1.25+

**Branch:** `feature/spike-001-claude-code`

**Manifest entry:** `docs/specs/001-plan-manifest.md` §001

---

## Background

The coworker runtime's persistent-worker model (spec §Lifecycle) relies on CLI agents calling `orch.next_dispatch()` via MCP after every turn. Claude Code is an MCP client that can connect to external MCP servers configured via `.mcp.json` or `claude mcp add`. The spike validates that this mechanism works end-to-end.

### What we know from research

- **Claude Code supports MCP servers** via stdio, SSE, and HTTP transports. Configured per-project (`.mcp.json`), per-user, or via CLI (`claude mcp add`).
- **Claude Code supports skills** (slash commands) that can instruct behavior patterns.
- **MCP notifications (server-to-client push):** The MCP protocol supports `list_changed` notifications for tool updates. General server-initiated message push (`notifications/claude/channel`) has known issues — Claude Code may not surface unsolicited server notifications reliably. This spike must verify.
- **Compaction:** Claude Code auto-compacts at ~60% context utilization. The orchy skill's polling instructions may survive compaction if placed in CLAUDE.md or a skill system prompt, but this needs verification.
- **Official Go MCP SDK** (`github.com/modelcontextprotocol/go-sdk` v1.5.0) supports stdio transport with `mcp.StdioTransport{}` and tool registration via `mcp.AddTool`.

---

## Test Protocol

### Test 0: CLI availability

**Question:** Is the Claude Code CLI present at the expected path with expected flags?

**Steps:**
1. Verify the binary exists and runs:
   ```bash
   /home/chris/.local/bin/claude --version 2>&1 | tee spike/001/RESULTS.md
   ```
2. Capture help output:
   ```bash
   /home/chris/.local/bin/claude --help 2>&1 >> spike/001/RESULTS.md
   ```
3. Confirm key flags exist: `--system-prompt-file`, `--mcp-config`, `--output-format`, `-p` (print mode).

**Expected result:** Both commands succeed. Key flags are present in `--help` output.

**Failure mode:** Binary not found or flags changed → update paths/flags before proceeding.

---

### Test 1: Minimal MCP server + Claude Code connection

**Question:** Can Claude Code connect to a custom stdio MCP server and discover its tools?

**Setup:**
```bash
mkdir -p spike/common/mcp-server
cd spike/common/mcp-server
go mod init spike-mcp-server
go get github.com/modelcontextprotocol/go-sdk@v1.5.0
```

Write `spike/common/mcp-server/main.go` — a minimal MCP server that registers two tools:
- `orch_next_dispatch` — returns a JSON dispatch object or `{"status": "idle"}` (controlled by a file flag `spike/001/dispatch.json`)
- `orch_job_complete` — accepts `job_id` + `outputs` JSON, writes them to `spike/001/completed.json`, returns `{"status": "ok"}`

The server uses `mcp.StdioTransport{}` and logs all tool calls to stderr.

**Steps:**
1. Build the server: `cd spike/common/mcp-server && go build -o spike-mcp-server .`
2. Write the shared MCP config file at `spike/common/mcp.json`:
   ```json
   {
     "mcpServers": {
       "coworker-spike": {
         "command": "/home/chris/workshop/coworker/spike/common/mcp-server/spike-mcp-server",
         "args": [],
         "type": "stdio"
       }
     }
   }
   ```
3. Register with Claude Code (optional project-scoped registration for inspection):
   ```bash
   /home/chris/.local/bin/claude mcp add-json --scope project coworker-spike '{"type":"stdio","command":"/home/chris/workshop/coworker/spike/common/mcp-server/spike-mcp-server","args":[]}'
   ```
4. Verify registration: `/home/chris/.local/bin/claude mcp list` — confirm `coworker-spike` appears and is healthy.
5. Start Claude Code in print mode to test tool discovery:
   ```bash
   /home/chris/.local/bin/claude -p \
     --mcp-config /home/chris/workshop/coworker/spike/common/mcp.json \
     --strict-mcp-config \
     "List all available MCP tools. Do you see mcp__coworker-spike__orch_next_dispatch and mcp__coworker-spike__orch_job_complete?"
   ```
6. Verify Claude Code sees both tools in their namespaced form:
   - `mcp__coworker-spike__orch_next_dispatch`
   - `mcp__coworker-spike__orch_job_complete`

**Expected result:** Claude Code discovers both tools and can list them in namespaced form.

**Failure mode:** Tools not discovered → check server stderr for connection issues. If stdio transport fails, try HTTP transport as fallback.

---

### Test 2: Tool round-trip (call + structured response)

**Question:** Can Claude Code call `orch_next_dispatch`, receive a dispatch payload, execute it, and call `orch_job_complete` with structured output?

**Setup:**
Write `spike/001/dispatch.json`:
```json
{
  "status": "dispatched",
  "job_id": "test-job-001",
  "role": "reviewer.arch",
  "prompt": "Review the architecture of spike/common/mcp-server/main.go. Return your findings as JSON with keys: summary, findings (array of {file, line, severity, message}).",
  "context": {}
}
```

**Steps:**
1. Ensure `dispatch.json` contains the test dispatch above.
2. Run Claude Code with explicit instruction:
   ```bash
   /home/chris/.local/bin/claude -p \
     --mcp-config /home/chris/workshop/coworker/spike/common/mcp.json \
     --strict-mcp-config \
     "Call the mcp__coworker-spike__orch_next_dispatch tool. If it returns a dispatch with status 'dispatched', execute the role described in the prompt field. When done, call mcp__coworker-spike__orch_job_complete with the job_id from the dispatch and your outputs as structured JSON."
   ```
3. Check `spike/001/completed.json` for the completion payload.
4. Verify the completion contains valid JSON with the expected structure.

**Expected result:** `completed.json` contains `{"job_id": "test-job-001", "outputs": {...}}` with structured findings.

**Failure mode:**
- Claude Code doesn't call the tools → skill/instruction issue
- Calls `orch_next_dispatch` but not `orch_job_complete` → the two-step polling pattern needs reinforcement
- Output is unstructured text instead of JSON → strengthen the prompt or inspect the MCP completion payload directly

---

### Test 3: Skill-driven polling loop (persistent session simulation)

**Question:** Can a Claude Code skill instruct the agent to call `orch.next_dispatch()` at the end of every turn, creating a persistent polling loop?

**Setup:**
Create a skill file at `spike/001/coworker-orchy-skill.md`:
```markdown
# coworker-orchy

## Instructions

You are connected to the coworker orchestrator via MCP. At the END of every response you give, you MUST call the `mcp__coworker-spike__orch_next_dispatch` tool to check for new work.

- If `mcp__coworker-spike__orch_next_dispatch` returns `{"status": "idle"}`, say "Waiting for dispatch..." and stop.
- If it returns a dispatch with `status: "dispatched"`, execute the task described in the `prompt` field, then call `mcp__coworker-spike__orch_job_complete` with the `job_id` and your structured outputs.
- After calling `mcp__coworker-spike__orch_job_complete`, call `mcp__coworker-spike__orch_next_dispatch` again to check for more work.

This polling behavior is mandatory. Never skip the `mcp__coworker-spike__orch_next_dispatch` call at turn end.
```

**Steps:**
1. Set `dispatch.json` to `{"status": "idle"}`.
2. Start Claude Code interactively in a tmux pane with the skill:
   ```bash
   tmux new-session -d -s spike001 "cd /home/chris/workshop/coworker && /home/chris/.local/bin/claude --system-prompt-file /home/chris/workshop/coworker/spike/001/coworker-orchy-skill.md --mcp-config /home/chris/workshop/coworker/spike/common/mcp.json --strict-mcp-config"
   ```
3. In the Claude Code session, type: "Hello, check for work."
4. Observe: Claude should call `orch_next_dispatch`, get idle, and report waiting.
5. Now update `dispatch.json` to the test dispatch from Test 2.
6. Send a second explicit turn first (for example `tmux send-keys -t spike001 "Check for work again." C-m`) to verify the polling instruction survives across turns.
7. Separately test an idle wake nudge with `tmux send-keys -t spike001 Enter` (validated in Test 4).
8. Observe whether Claude continues the polling contract on the explicit second turn, then check whether bare Enter can wake an idle prompt.
9. Check `completed.json` for the output.

**Expected result:** The polling instruction persists across explicit user turns: idle → explicit second turn → dispatch → complete → poll again. Bare Enter wake is a separate gate in Test 4.

**Failure mode:**
- Claude doesn't poll after the first turn → skill instructions not persistent enough; try CLAUDE.md approach
- tmux send-keys doesn't wake the session → Claude Code may need a different wake mechanism
- Claude polls but loses the instruction after compaction → critical issue for persistent sessions

---

### Test 4: tmux send-keys wake-idle reliability

**Question:** Does `tmux send-keys <pane> Enter` reliably wake an idle Claude Code session to start a new turn?

**Setup:** Reuse the tmux session from Test 3.

**Steps:**
1. Let the session sit idle for 30 seconds after it reports "Waiting for dispatch..."
2. Update `dispatch.json` with a new dispatch (different `job_id`).
3. Send: `tmux send-keys -t spike001 Enter`
4. Wait up to 10 seconds. Check if Claude starts a new turn.
5. Repeat steps 1-4 five times with varying idle durations (30s, 60s, 120s).
6. Record success/failure for each attempt.

**Expected result:** At least 4/5 wake attempts succeed within 10 seconds.

**Failure mode:**
- Enter doesn't trigger a new turn → try `tmux send-keys -t spike001 "" Enter` or `C-m`
- Works sometimes but not reliably → document flakiness rate and conditions
- Never works → explore MCP notification-based wake as alternative

---

### Test 5: MCP server-to-client notifications (informational)

**Question:** Does a server-initiated notification trigger Claude Code to start a new turn?

**Framing:** This test is **informational only** and is NOT part of the pass/fail gates for the spike verdict. The goal is to measure whether server-initiated notifications can trigger a new turn. Success here is an optimization (no tmux wake needed for dispatch delivery). No-op is the expected baseline (tmux wake remains the primary mechanism).

**Setup:**
Extend `spike/common/mcp-server/main.go` to send a `notifications/message` or `tools/list_changed` notification when a file watcher detects `dispatch.json` has changed.

**Steps:**
1. Start Claude Code connected to the MCP server (as in Test 3).
2. Let it reach idle state.
3. From a separate terminal, update `dispatch.json` with a new dispatch.
4. The server detects the change and sends a notification.
5. Observe whether Claude Code reacts (starts a new turn, calls `orch_next_dispatch`).

**Expected result:** Document the observed behavior:
- **If Claude Code reacts:** Notification-based wake is viable — an optimization over tmux wake.
- **If Claude Code ignores the notification:** Expected baseline. tmux wake-idle from Test 4 is the primary mechanism. Document the gap.

---

### Test 6: Context-window compaction behavior

**Question:** After compaction, does Claude Code retain the polling instruction from the skill/system prompt?

**Setup:** Reuse the tmux session. We need to fill context to trigger compaction.

**Steps:**
1. Start a fresh Claude Code session with the orchy skill system prompt.
2. Give it a series of tasks that generate substantial output (e.g., "read and summarize every .go file in this repository" repeated with variations) to fill ~60% of context.
3. Watch for compaction (Claude Code shows a compaction indicator).
4. After compaction, check if Claude still calls `orch_next_dispatch` at turn end.
5. If not, test with the instruction in CLAUDE.md instead of system prompt and repeat.

**Expected result:** Polling instruction survives compaction (at least when in CLAUDE.md or system prompt).

**Failure mode:**
- Instruction lost after compaction → need to embed polling in a more durable location (CLAUDE.md, plugin hook, or re-inject via MCP notification)
- Instruction survives but tool names are lost → MCP tool list may need refresh post-compaction

---

### Test 7: stream-json output capture (ephemeral mode baseline)

**Question:** For ephemeral fallback, does `claude -p --output-format stream-json` produce parseable structured output?

**Steps:**
1. Run:
   ```bash
   /home/chris/.local/bin/claude -p --verbose --output-format stream-json \
     --mcp-config /home/chris/workshop/coworker/spike/common/mcp.json \
     --strict-mcp-config \
     "Call mcp__coworker-spike__orch_next_dispatch. If you get a dispatch, execute it and call mcp__coworker-spike__orch_job_complete. Output your findings as JSON." \
     2>/dev/null | tee spike/001/stream-output.jsonl
   ```
2. Parse `stream-output.jsonl` — verify each line is valid JSON.
3. Identify the event types: `assistant`, `tool_use`, `tool_result`, `result`, etc.
4. Verify the final result contains the structured output from `orch_job_complete`.

**Expected result:** Stream-json output is line-delimited JSON with identifiable event types. Tool calls and results are captured.

**Failure mode:** Output not parseable → check format spec. If stream-json doesn't capture MCP tool calls, we may need a different capture strategy.

---

### Test 8: Real plugin/skill loading path

**Question:** Can Claude Code discover and load coworker tools through the real plugin path (`.claude/plugins/` + `.mcp.json`) rather than `--system-prompt-file`? This is the path Plan 108 will rely on.

**Setup:**
1. Create the plugin directory and skill file:
   ```bash
   mkdir -p .claude/plugins/coworker-spike
   ```
2. Write `.claude/plugins/coworker-spike/coworker-orchy.md`:
   ```markdown
   # coworker-orchy

   ## Instructions

   You are connected to the coworker orchestrator via MCP. At the END of every response you give, you MUST call the `mcp__coworker-spike__orch_next_dispatch` tool to check for new work.

   - If `mcp__coworker-spike__orch_next_dispatch` returns `{"status": "idle"}`, say "Waiting for dispatch..." and stop.
   - If it returns a dispatch with `status: "dispatched"`, execute the task described in the `prompt` field, then call `mcp__coworker-spike__orch_job_complete` with the `job_id` and your structured outputs.
   - After calling `mcp__coworker-spike__orch_job_complete`, call `mcp__coworker-spike__orch_next_dispatch` again to check for more work.
   ```
3. Write `.mcp.json` at the project root:
   ```json
   {
     "mcpServers": {
       "coworker-spike": {
         "command": "/home/chris/workshop/coworker/spike/common/mcp-server/spike-mcp-server",
         "args": [],
         "type": "stdio"
       }
     }
   }
   ```

**Steps:**
1. Set `spike/001/dispatch.json` to the test dispatch from Test 2.
2. Launch Claude Code **without** `--system-prompt-file` or `--mcp-config` flags — rely on `.mcp.json` and the plugin directory:
   ```bash
   /home/chris/.local/bin/claude -p "Use the /coworker-orchy skill. Then call mcp__coworker-spike__orch_next_dispatch and follow its instructions."
   ```
3. Verify Claude Code:
   - Discovers the MCP server from `.mcp.json`
   - Loads the skill from `.claude/plugins/coworker-spike/`
   - Calls `orch_next_dispatch` and processes the dispatch
4. Check `spike/001/completed.json` for the output.
5. Clean up: remove `.mcp.json` and `.claude/plugins/coworker-spike/` after the test.

**Expected result:** Claude Code loads the skill and MCP server through the real plugin path. Tool discovery and dispatch work identically to Tests 1-2.

**Failure mode:**
- Plugin not discovered → check `.claude/plugins/` naming conventions; Claude Code may require a different directory structure
- MCP server not loaded from `.mcp.json` → check `.mcp.json` format against Claude Code docs
- Skill loads but MCP tools unavailable → `.mcp.json` and plugin loading may be independent; both need to work together

---

## Pass/Fail Gates

Persistent mode is viable **only if ALL of the following pass:**

| Gate | Required Test(s) | Criterion |
|---|---|---|
| Tool discovery | Test 1 | Claude Code discovers both `orch_next_dispatch` and `orch_job_complete` from the MCP server |
| Dispatch completion | Test 2 | Full round-trip: call `orch_next_dispatch` → execute task → call `orch_job_complete` with structured output |
| Polling across turns | Test 3 | Skill-driven polling persists across at least 3 consecutive turns (idle → dispatch → idle) |
| Wake reliability | Test 4 | tmux send-keys wakes idle session at least 4/5 times within 10 seconds |

**If any gate fails:** persistent mode is not viable. Fall back to ephemeral-only (`claude -p --verbose --output-format stream-json`).

**Informational tests (do not gate the verdict):**
- Test 0 (CLI availability) — prerequisite validation
- Test 5 (MCP notifications) — optimization measurement, not a requirement
- Test 6 (compaction) — informs long-session strategy but not core viability
- Test 7 (stream-json) — ephemeral fallback validation
- Test 8 (plugin path) — informs Plan 108 but not core persistent viability

---

## Decision Matrix

| Dimension | Result | Implication |
|---|---|---|
| MCP tool discovery | yes/no | Foundational — if no, spike fails immediately |
| Tool round-trip (dispatch → complete) | yes/no | Core pull-model viability |
| Skill-driven polling | yes/no/partial | Determines if persistent worker model is viable |
| tmux wake-idle | reliable/flaky/broken | Fallback wake mechanism; flaky is acceptable if notifications work |
| MCP notifications | supported/unsupported | If unsupported, tmux wake is sole mechanism (informational) |
| Compaction resilience | survives/lost | If lost, need CLAUDE.md or re-injection strategy |
| stream-json capture | parseable/not | Ephemeral fallback viability |
| Plugin path loading | works/broken | Informs Plan 108 implementation approach |

## Verdict Template

Fill in after running:
- `persistent_mcp_pull:` yes | partial | no
- `tmux_wake:` reliable | flaky | unnecessary
- `mcp_notifications:` supported | unsupported *(informational — does not affect verdict)*
- `compaction:` acceptable | problematic
- `ephemeral_stream_json:` parseable | not
- `plugin_path:` works | broken
- `recommendation:` persistent | ephemeral-only | persistent-with-workarounds
- `plan_104_impact:` <how this affects the MCP server plan>
- `plan_108_impact:` <how this affects the Claude Code plugin plan>

---

## Spike Code Location

All prototype code lives in `spike/001/` and `spike/common/`:
- `spike/common/mcp-server/main.go` — shared MCP server (used by spikes 001 and 002)
- `spike/common/mcp-server/go.mod` / `go.sum` — independent module (not in main go.mod)
- `spike/common/mcp.json` — shared MCP config file referencing the server binary
- `spike/001/coworker-orchy-skill.md` — test skill file
- `spike/001/dispatch.json` — test dispatch payload (mutable during tests)
- `spike/001/completed.json` — output from `orch_job_complete` (written by server)
- `spike/001/stream-output.jsonl` — captured stream-json output
- `spike/001/RESULTS.md` — raw test results (filled during execution)

This directory is gitignored from the main build. Prototype code is disposable.

---

## Post-Execution Report

### Test Results

| Test | Result | Notes |
|---|---|---|
| 0. CLI availability | PASS | `claude --version` returned `2.1.117`; required flags were present in `--help`. |
| 1. MCP connection | PASS | Both MCP tools were discovered in namespaced form. Project registration via `claude mcp add-json` also worked. |
| 2. Tool round-trip | PASS | Claude called `mcp__coworker-spike__orch_next_dispatch`, executed the review prompt, and completed via `mcp__coworker-spike__orch_job_complete`. |
| 3. Skill-driven polling | PARTIAL | Polling instructions persisted across explicit subsequent turns. Claude handled idle -> explicit second turn -> dispatch -> complete -> poll again. |
| 4. tmux wake-idle | FAIL | Bare `tmux send-keys ... Enter` did not start a new turn in controlled 30s and 60s idle tests; earlier exploratory `Enter`/`C-m` nudges also failed. |
| 5. MCP notifications (informational) | NOT RUN | Deferred once wake-idle already failed and explicit-turn polling had been characterized. |
| 6. Compaction resilience | NOT RUN | Deferred; useful for long-session hardening but not needed to decide the core verdict. |
| 7. stream-json capture | PASS | `claude -p --verbose --output-format stream-json` produced parseable JSONL with tool-use and tool-result events. `--verbose` was required. |
| 8. Plugin path loading | PASS | `.claude/plugins/...` plus root `.mcp.json` loaded successfully; Claude used `/coworker-orchy` and completed the dispatched job. |

### Verdict

- `persistent_mcp_pull:` partial
- `tmux_wake:` broken
- `mcp_notifications:` untested
- `compaction:` untested
- `ephemeral_stream_json:` parseable
- `plugin_path:` works
- `recommendation:` ephemeral-only
- `plan_104_impact:` Claude Code can execute MCP pull jobs and produce structured completions, but the spec's idle wake assumption is not validated. Plan 104 should not assume tmux Enter can wake an idle Claude worker. Either keep Claude in ephemeral mode, use a stronger wake primitive, or redesign the pull contract around explicit dispatch turns.
- `plan_108_impact:` The real plugin path works: `.claude/plugins/` plus `.mcp.json` is a viable loading mechanism for Claude-side orchestration instructions. Plugin packaging should use namespaced MCP tool names and document that `--verbose` is required for stream-json capture in CLI fallback paths.

### Recommendations for Plan 104 / 108

1. Treat Claude persistent mode as **not production-viable** until a reliable wake mechanism exists.
2. Keep the Claude integration capable of **ephemeral pull execution** using `-p --verbose --output-format stream-json` for deterministic capture.
3. Use the real plugin path (`.claude/plugins/` + `.mcp.json`) for Plan 108, not just `--system-prompt-file`.
4. Do not model production queue semantics on the file-based spike harness. The spike server still has known limitations: read-and-consume is not atomic across multiple pollers, and completion storage is overwrite-oriented rather than append-only.

---

## Code Review

### Review 1

- **Date**: 2026-04-21
- **Reviewer**: Codex
- **PR**: N/A — pre-PR review for plan 001 spike execution updates
- **Verdict**: Approved

**Must Fix / Should Fix / Nice to Have**

No open findings. The plan now matches the executed Claude Code behavior: namespaced MCP tool names, `claude mcp add-json` registration, `--verbose` for stream-json, and a split between explicit-turn polling success and idle-wake failure. The remaining limitations are captured in the verdict and recommendations rather than left implicit.

### Review 2

- **Date**: 2026-04-21
- **Reviewer**: Codex
- **PR**: N/A — pre-PR review for spec alignment on top of plan 001 findings
- **Verdict**: Approved

**Must Fix / Should Fix / Nice to Have**

No open findings. The follow-on spec edits are coherent with the spike result: V1 scope now treats Codex as worker-only by default, the runtime spec no longer assumes tmux wake is a valid generic fallback, and the bulletin-board design stays additive rather than rewriting the scheduler model. Residual risk remains in unrun spikes 002 and 003, but that uncertainty is explicitly preserved as roadmap dependency rather than hidden in the spec text.
