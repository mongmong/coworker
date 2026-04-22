# Plan 002 — Spike: Codex Persistent MCP Pull

**Goal:** Determine whether Codex can act as a persistent MCP-pull worker — connecting to a custom stdio MCP server, polling `orch.next_dispatch()` at each turn end, returning structured output via `orch.job_complete()`, and surviving both explicit follow-up turns and idle wake attempts — or whether it should remain ephemeral-only.

**Duration:** ~1 day

**Prerequisites:**
- Plan 000 shipped (Go module, tooling)
- Codex CLI installed at `/home/chris/.nvm/versions/node/v20.20.1/bin/codex`
- OpenAI API key configured (Codex auth working)
- `tmux` installed
- Go 1.25+
- Shared MCP server source at `spike/common/mcp-server/` (build if not already built — see Test 1 setup)

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

### Test 0: CLI availability

**Question:** Is the Codex CLI present at the expected path with expected flags?

**Steps:**
1. Verify the binary exists and runs:
   ```bash
   /home/chris/.nvm/versions/node/v20.20.1/bin/codex --version 2>&1 | tee spike/002/RESULTS.md
   ```
2. Capture help output:
   ```bash
   /home/chris/.nvm/versions/node/v20.20.1/bin/codex --help 2>&1 >> spike/002/RESULTS.md
   ```
3. Confirm key subcommands exist: `exec`, `mcp add`, `mcp list`, `resume`.

**Expected result:** Both commands succeed. Key subcommands are present in `--help` output.

**Failure mode:** Binary not found or flags changed → update paths/flags before proceeding.

---

### Test 1: MCP server connection + tool discovery

**Question:** Can Codex connect to a custom stdio MCP server and discover its tools?

**Setup:**
Build the shared MCP server if not already built:
```bash
cd spike/common/mcp-server && go build -o spike-mcp-server . && cd -
```

The shared spike server defaults to `spike/001` unless `SPIKE_DIR` is set. For this plan, Codex must register the server with:

```text
SPIKE_DIR=/home/chris/workshop/coworker/spike/002
```

**Steps:**
1. Register the MCP server with Codex:
   ```bash
   /home/chris/.nvm/versions/node/v20.20.1/bin/codex mcp add \
     --env SPIKE_DIR=/home/chris/workshop/coworker/spike/002 \
     coworker-spike -- /home/chris/workshop/coworker/spike/common/mcp-server/spike-mcp-server
   ```
2. Verify registration: `/home/chris/.nvm/versions/node/v20.20.1/bin/codex mcp list` — confirm `coworker-spike` appears.
3. Verify with `/home/chris/.nvm/versions/node/v20.20.1/bin/codex mcp list --json` for machine-readable output.
4. Confirm the configured server in machine-readable form:
   ```bash
   /home/chris/.nvm/versions/node/v20.20.1/bin/codex mcp list --json | tee spike/002/mcp-list.json
   ```
5. Run a non-interactive discovery prompt and capture the **exact** tool names Codex exposes:
   ```bash
   /home/chris/.nvm/versions/node/v20.20.1/bin/codex exec --json \
     "List the exact MCP tool names available from the coworker-spike server. Return only the tool names." \
     2>/dev/null | tee spike/002/tool-discovery.jsonl
   ```
6. Record the actual names discovered for use in the remaining tests:
   - `<next_dispatch_tool>`
   - `<job_complete_tool>`

**Important:** Every later test must use the exact discovered tool names. Do **not** assume bare `orch_next_dispatch` / `orch_job_complete`; if Codex namespaces MCP tools, use the namespaced forms everywhere.

**Expected result:** Codex discovers both tools from the MCP server, and the exact callable names are known before Tests 2–8 run.

**Failure mode:**
- Server fails to start → check stderr, verify binary path
- Tools not listed → MCP server protocol mismatch; check if Codex needs specific MCP protocol version
- Connection timeout → check `startup_timeout_sec` in config.toml (default 10s)

---

### Test 2: Tool round-trip via `codex exec`

**Question:** Can Codex call `orch_next_dispatch`, process a dispatch, and call `orch_job_complete` in ephemeral mode?

**Setup:**
Set `spike/002/dispatch.json`:
```json
{
  "status": "dispatched",
  "job_id": "codex-test-001",
  "role": "reviewer.arch",
  "prompt": "Review the architecture of spike/common/mcp-server/main.go. Return findings as JSON with keys: summary, findings (array of {file, line, severity, message}).",
  "context": {}
}
```

**Steps:**
1. Run Codex in exec mode:
   ```bash
   /home/chris/.nvm/versions/node/v20.20.1/bin/codex exec \
     "Call the <next_dispatch_tool> tool. If it returns a dispatch with status 'dispatched', execute the task in the prompt field. When done, call <job_complete_tool> with the job_id and your structured findings as JSON." \
     2>&1 | tee spike/002/exec-output.txt
   ```
2. Check `spike/002/completed.json` for the completion payload.
3. Verify structured JSON output.

**Expected result:** `completed.json` contains structured findings with the correct `job_id`.

**Failure mode:**
- Codex doesn't call MCP tools in exec mode → MCP servers may not load for `codex exec`
- Calls `<next_dispatch_tool>` but not `<job_complete_tool>` → prompting issue
- Sandbox blocks file write → need `--sandbox workspace-write` or `--full-auto`

---

### Test 3: `codex exec --json` output capture

**Question:** Does `codex exec --json` produce parseable JSONL events that capture MCP tool calls and results?

**Steps:**
1. Reset `spike/002/dispatch.json` to the test dispatch.
2. Run:
   ```bash
   /home/chris/.nvm/versions/node/v20.20.1/bin/codex exec --json \
     "Call <next_dispatch_tool>. If dispatched, execute the task and call <job_complete_tool> with findings." \
     2>/dev/null | tee spike/002/exec-jsonl.txt
   ```
3. Parse each line of output as JSON.
4. Identify event types — look for tool-call events, tool-result events, assistant messages.
5. Check if MCP tool calls (`<next_dispatch_tool>`, `<job_complete_tool>`) appear in the event stream.

**Expected result:** JSONL output is parseable; MCP tool calls are visible in the event stream.

**Failure mode:**
- JSONL lines not valid JSON → format issue
- MCP tool calls not in stream → may be internal to Codex, not surfaced
- If MCP tool calls are invisible, we need an alternative capture strategy (read `completed.json` directly)

---

### Test 4: Interactive session — explicit-turn persistence

**Question:** Can Codex preserve the polling instruction across an explicit second turn in an interactive session?

**Setup:** Start Codex interactively in a tmux pane.

**Steps:**
1. Set `spike/002/dispatch.json` to `{"status": "idle"}`.
2. Start Codex in tmux:
   ```bash
   tmux new-session -d -s spike002 "cd /home/chris/workshop/coworker && /home/chris/.nvm/versions/node/v20.20.1/bin/codex 'You are connected to the coworker orchestrator. At the end of every response, you MUST call <next_dispatch_tool> to check for work. If idle, say Waiting. If dispatched, execute and call <job_complete_tool>, then poll again.'"
   ```
3. Send an initial prompt in the tmux pane, for example: `Hello, check for work.`
4. Observe whether Codex calls `<next_dispatch_tool>`, reaches idle, and remains ready for another turn.
5. Update `spike/002/dispatch.json` with a test dispatch.
6. Send an explicit second turn, for example: `tmux send-keys -t spike002 "Check for work again." C-m`
7. Observe whether Codex preserves the polling instruction, handles the dispatch, calls `<job_complete_tool>`, and returns to idle.

**Expected result:** Codex preserves the polling behavior across explicit consecutive turns: idle → explicit second turn → dispatch → complete → poll again.

**Failure mode:**
- Codex doesn't support persistent system prompt injection in interactive mode → persistent model not viable
- Polling works on the first turn but not the second → instruction lost after turn
- If explicit-turn persistence fails → persistent polling is not viable and Codex is **ephemeral-only**

---

### Test 5: Interactive session — idle wake reliability

**Question:** If Codex reaches an idle prompt after Test 4, can `tmux send-keys ... Enter` start a new turn reliably enough to make persistent mode useful?

**Setup:** Reuse the tmux session from Test 4 if explicit-turn persistence succeeded.

**Steps:**
1. Let the session sit idle for 30 seconds after it reports waiting / reaches its idle prompt.
2. Update `spike/002/dispatch.json` with a new dispatch.
3. Send: `tmux send-keys -t spike002 Enter`
4. Wait up to 10 seconds. Check whether Codex starts a new turn.
5. Repeat with longer idle times (for example 60s and 120s) if the first attempt works at all.
6. Record success/failure for each attempt.

**Expected result:** Codex starts a new turn reliably enough that the wake mechanism is operationally useful.

**Failure mode:**
- Enter does not trigger a new turn → idle wake is broken for the current Codex TUI path
- Wake is flaky → document the rate and downgrade the recommendation accordingly

---

### Test 6: Session resume with MCP tools

**Question:** If we use `codex resume` to continue a session, are MCP tools still available?

**Steps:**
1. Start an interactive Codex session, call `<next_dispatch_tool>` once, then exit.
2. Note the session ID from `/home/chris/.nvm/versions/node/v20.20.1/bin/codex resume --last` or the session output.
3. Resume: `/home/chris/.nvm/versions/node/v20.20.1/bin/codex resume --last`
4. Ask Codex to call `<next_dispatch_tool>` again.
5. Verify the MCP server is re-launched and tools are available.

**Expected result:** MCP tools are available in resumed sessions.

**Failure mode:** MCP server not relaunched on resume → session resume doesn't reload MCP config. This would mean persistent sessions can't use MCP tools across restarts, limiting the persistent model.

---

### Test 7: `codex exec` with `--output-schema` for structured output

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
1. Reset `spike/002/dispatch.json`.
2. Run:
   ```bash
   /home/chris/.nvm/versions/node/v20.20.1/bin/codex exec \
     --output-schema spike/002/findings-schema.json \
     "Call <next_dispatch_tool>. Execute the dispatched task. Call <job_complete_tool>. Then output your findings." \
     2>/dev/null | tee spike/002/schema-output.txt
   ```
3. Verify the output conforms to the schema.

**Expected result:** Output is valid JSON matching the schema.

**Failure mode:** Schema not enforced → Codex may ignore `--output-schema` with MCP tools active. Fall back to parsing `completed.json` directly.

---

### Test 8: Sandbox interaction with MCP server

**Question:** How do Codex sandbox modes interact with the MCP server's file I/O?

**Framing:** The goal is to **measure** whether MCP subprocesses can read/write outside the Codex workspace. Document the boundary. Feed findings into the security model (docs/specs/000 §Security Model).

**Steps:**
1. Run Test 2 with each real sandbox mode:
   ```bash
   /home/chris/.nvm/versions/node/v20.20.1/bin/codex exec --sandbox read-only "Call <next_dispatch_tool> and <job_complete_tool>..."
   /home/chris/.nvm/versions/node/v20.20.1/bin/codex exec --sandbox workspace-write "Call <next_dispatch_tool> and <job_complete_tool>..."
   /home/chris/.nvm/versions/node/v20.20.1/bin/codex exec --sandbox danger-full-access "Call <next_dispatch_tool> and <job_complete_tool>..."
   ```
2. Optionally run one additional ergonomics check with `--full-auto` to confirm that its alias behavior matches `--sandbox workspace-write` for this use case.
3. For each mode, record:
   - Does the MCP server start? (stdio subprocess may be affected by sandbox)
   - Can the server write `spike/002/completed.json`?
   - Can the server read `spike/002/dispatch.json`?
   - Are Codex's own file operations sandboxed independently of MCP?
4. Document the exact sandbox boundary — which operations are blocked, which pass through.

**Expected result:** Neutral observation of the boundary. Record whether MCP server I/O is sandboxed or independent for each mode.

**Failure mode:** If sandbox blocks all MCP server I/O, Codex may require `--full-auto` for the coworker use case. Document this constraint for the security model.

---

## Pass/Fail Gates

Persistent mode is viable **only if BOTH of the following pass:**

| Gate | Required Test(s) | Criterion |
|---|---|---|
| Explicit-turn persistence | Test 4 | Codex maintains polling behavior across at least 2 consecutive explicit turns |
| Session resume | Test 6 | MCP tools are available after `codex resume` |

**If either gate fails:** Codex is **ephemeral-only** (via `codex exec`). This is the expected fallback and is still a valuable operating mode for review/test roles.

**Wake reliability is a separate qualifier:**

| Gate | Required Test(s) | Criterion |
|---|---|---|
| Idle wake | Test 5 | `tmux send-keys ... Enter` is reliable enough to make an idle persistent session operationally useful |

If Test 4 passes but Test 5 fails, record the result as **persistent-with-workarounds** or **explicit-turn-only**, not a clean persistent success.

**Ephemeral mode requires:**

| Gate | Required Test(s) | Criterion |
|---|---|---|
| `codex exec` round-trip | Test 2 | Codex calls both tools and produces structured output |
| Output capture | Test 3 or Test 7 | At least one of `--json` JSONL or `--output-schema` produces parseable output |

---

## Decision Matrix

| Dimension | Result | Implication |
|---|---|---|
| MCP tool discovery | yes/no | Foundational |
| `codex exec` tool round-trip | yes/no | Ephemeral mode viability |
| `codex exec --json` capture | parseable/not | Event stream integration |
| Explicit-turn persistence | yes/no | Persistent polling viability |
| tmux wake (interactive) | works/flaky/broken | Qualifies whether idle persistent mode is practical |
| Session resume + MCP | yes/no | Persistent session continuity |
| `--output-schema` enforcement | yes/no | Structured output capture path |
| Sandbox + MCP interaction | clean/problematic | Deployment model |

## Verdict Template

Fill in after running:
- `persistent_mcp_pull:` yes | partial | no
- `ephemeral_exec:` yes | partial | no
- `tmux_wake:` reliable | flaky | broken | n/a
- `output_capture:` jsonl | schema | file-read | none
- `sandbox_boundary:` <description of what is/isn't sandboxed>
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

The MCP server binary is shared at `spike/common/mcp-server/spike-mcp-server`. If not already built:
```bash
cd spike/common/mcp-server && go build -o spike-mcp-server . && cd -
```

---

## Post-Execution Report

### Test Results

| Test | Result | Notes |
|---|---|---|
| 0. CLI availability | PASS | `codex-cli 0.122.0`; expected subcommands and flags were present in `--help`. |
| 1. MCP connection | PASS | `codex mcp list` and `~/.codex/config.toml` confirmed registration; discovery prompt exposed bare `orch_next_dispatch` and `orch_job_complete`. `codex mcp list --json` returned `[]`, which appears to be a CLI quirk. |
| 2. exec tool round-trip | PARTIAL | Default noninteractive exec cancelled MCP calls. The full round-trip succeeded with `--sandbox danger-full-access` and wrote `completed.json`. |
| 3. exec --json capture | PASS | `spike/002/exec-jsonl.txt` is parseable JSONL and includes MCP tool-call events plus results. |
| 4. Explicit-turn persistence | PASS | Interactive Codex preserved the polling instruction across an explicit second turn and repolled after completion. |
| 5. Idle wake reliability | FAIL | After 30 seconds idle, bare `tmux send-keys ... Enter` did not start a new turn. |
| 6. Session resume + MCP | PASS | `codex resume --last` restored MCP tool availability and successfully called `orch_next_dispatch` again. |
| 7. --output-schema | PASS | Structured JSON output worked once the schema added `additionalProperties: false` and required every declared property. |
| 8. Sandbox + MCP | PARTIAL | `read-only` and `workspace-write` both cancelled noninteractive MCP calls; `danger-full-access` completed the job and file I/O. |

### Verdict

- `persistent_mcp_pull:` explicit-turn-only
- `ephemeral_exec:` partial
- `tmux_wake:` broken
- `output_capture:` jsonl + schema
- `discovered_tool_names:` `orch_next_dispatch`, `orch_job_complete` (bare MCP tool names; no namespace prefix in prompts)
- `sandbox_boundary:` In `codex exec`, `read-only` and `workspace-write` both failed at the MCP tool-call stage with `user cancelled MCP tool call`; `danger-full-access` allowed the full dispatch/complete flow. In interactive mode, Codex could use MCP tools after explicit per-tool approval inside the TUI.
- `recommendation:` ephemeral-primary, persistent-explicit-turn-only
- `plan_104_impact:` Codex can preserve explicit-turn polling and resume MCP-backed sessions, but the current empty-enter wake assumption does not hold. Plan 104 should not rely on idle wake for Codex. If Codex is kept as a persistent worker, it needs a deterministic explicit wake phrase or human-driven turns; unattended pull workers should remain non-Codex for now.
- `plan_109_impact:` The Codex integration should treat `danger-full-access` and approval behavior as first-class constraints. Codex ephemeral dispatch currently required `--sandbox danger-full-access` in this spike, so the security model should treat Codex jobs as partially observed until sandbox behavior improves. Plugin/runtime work should also record that `codex mcp list --json` was misleading in `codex-cli 0.122.0` and that `--output-schema` requires a stricter schema profile than the plan originally assumed.

### Recommendations for Plan 104 / 109

1. Treat Codex as **not cleanly autonomous-persistent** under the current tmux wake design. Its polling contract survives explicit turns, but bare idle wake is broken.
2. Keep Codex viable for **ephemeral review/test roles**, but document prominently that the working noninteractive path currently requires `--sandbox danger-full-access`.
3. If Plan 104 wants interactive Codex workers, allow a deterministic fixed wake phrase rather than relying on empty Enter. Do not send job content through tmux; only send a neutral wake command.
4. Tighten any schema-based output capture in Plan 109: Codex requires `additionalProperties: false` and fully enumerated `required` arrays for `--output-schema`.
5. Plan 109 should document that Codex ephemeral dispatch was only proven with `--sandbox danger-full-access`; until sandbox behavior improves, treat Codex job execution as partially observed in the security model.
6. Do not trust `codex mcp list --json` alone as the source of truth for registration in `codex-cli 0.122.0`; confirm with `codex mcp list`, tool discovery, or config inspection.

---

## Code Review

### Review 1

- **Date**: 2026-04-22
- **Reviewer**: Codex (self-review)
- **PR**: N/A — pre-PR review for plan 002 spike execution updates
- **Verdict**: Approved

No open findings. The executed report reflects the real Codex behavior.

### Review 2

- **Date**: 2026-04-22
- **Reviewer**: Claude (cross-review of PR #3)
- **PR**: #3 — Document Spike 002 execution verdict and review
- **Verdict**: Approved with suggestions

**Should Fix**

1. `[FIXED]` **Verdict `persistent_mcp_pull: partial` is ambiguous — should be `explicit-turn-only`.** "Partial" implies some core tests passed and some didn't. The reality is more specific: explicit-turn persistence works (Test 4 PASS), idle-wake is broken (Test 5 FAIL). Change to `persistent_mcp_pull: explicit-turn-only` and update the recommendation to `ephemeral-primary, persistent-explicit-turn-only` so Plan 104/109 know exactly what works.
   → Response: Updated the verdict to `explicit-turn-only` and changed the top-level recommendation to `ephemeral-primary, persistent-explicit-turn-only`.

2. `[FIXED]` **`danger-full-access` requirement is a security-model-level finding that needs more prominence.** Currently buried in the `sandbox_boundary` verdict line. This has direct implications for `docs/specs/000-coworker-runtime-design.md` §Security Model — if Codex ephemeral dispatch requires `danger-full-access` to complete MCP tool calls, the spec's per-role permission surface is undermined for Codex roles. Add a top-level recommendation: "Plan 109 must document that Codex ephemeral dispatch currently requires `--sandbox danger-full-access`. The security model should treat Codex jobs as `partially-observed` by default until sandbox modes improve."
   → Response: Promoted this into both `plan_109_impact` and the numbered recommendations so the security implication is explicit rather than implicit in `sandbox_boundary`.

**Nice to Have**

3. `[WONTFIX]` Verify no stale `spike/001/` path references remain in the final file.
   → Response: Checked the remaining `spike/001` reference. It is intentional documentation of the shared spike server default before `SPIKE_DIR` override, not a stale copy/paste path.

4. `[FIXED]` Add "Discovered tool names: `orch_next_dispatch`, `orch_job_complete` (bare, no namespace prefix)" near the verdict section so readers don't have to scroll to Test 1 notes. Key difference from Claude Code (which namespaces as `mcp__coworker-spike__orch_next_dispatch`).
   → Response: Added `discovered_tool_names` to the verdict block.

5. `[FIXED]` Pin `codex mcp list --json` returning `[]` as a known bug against `codex-cli 0.122.0` so Plan 109 knows to work around it.
   → Response: Tied the workaround note explicitly to `codex-cli 0.122.0` in the recommendations and `plan_109_impact`.

6. `[FIXED]` Review 1 says "Reviewer: Codex" — clarified in Review 2 header as self-review.
   → Response: Review 1 already reads `Codex (self-review)`, so reviewer identity is unambiguous.

**Key takeaway for downstream plans:**
- **Plan 104:** Do not rely on idle-wake (`tmux send-keys Enter`) for Codex persistent workers. If Codex is kept persistent, it needs explicit wake phrases or human-driven turns.
- **Plan 109:** Treat `danger-full-access` as a first-class constraint. Document the `--output-schema` strict profile requirement (`additionalProperties: false`, fully enumerated `required`).
