# Plan 108 — Claude Code Plugin (Interactive + Worker)

**Flavor:** Plugin
**Blocks on:** 104, 105
**Branch:** `feature/plan-108-claude-plugin`
**Manifest entry:** `docs/specs/001-plan-manifest.md` §108

---

## Goal

Deliver a first-class Claude Code plugin at `plugins/coworker-claude/` that, when
installed into a project's `.claude/`, makes Claude Code an orchy-aware worker or
interactive driver in a coworker run.

This is primarily a Markdown-and-JSON deliverable — no complex Go architecture.
The spike (Plan 001) already validated the real plugin path: `.claude/plugins/<name>/`
plus a project-root `.mcp.json` is the correct mechanism.

---

## Background and Spike Findings (Plan 001)

Key facts that shape this plan:

- **MCP tool namespace:** Claude Code namespaces every MCP tool as
  `mcp__<server-name>__<tool_name>`. All skill instructions must use the full
  namespaced form (e.g. `mcp__coworker__orch_register`).
- **Plugin path confirmed:** `.claude/plugins/<name>/` plus a root `.mcp.json`
  loads successfully (Test 8 PASS). Plan 108 relies on this path entirely.
- **`--verbose` required** for stream-json capture in ephemeral (`-p`) mode.
- **tmux idle wake is broken** (Test 4 FAIL). Do not include any wake-idle
  instruction in skills; the skill's polling only fires on explicit user turns.
- **Persistent polling is partial:** polling across explicit turns works; unprompted
  idle wake does not. V1 uses the ephemeral `-p` path as the production path; the
  persistent path is a stretch goal.

---

## Phases

### Phase 1 — Plugin skeleton

Files:
- `plugins/coworker-claude/.mcp.json` — MCP server connection config
- `plugins/coworker-claude/settings.json` — Claude Code permission settings

### Phase 2 — coworker-orchy skill

File:
- `plugins/coworker-claude/skills/coworker-orchy.md`

Heart of the integration. Instructs Claude Code to:
- On startup: call `mcp__coworker__orch_register`
- After every explicit turn: call `mcp__coworker__orch_next_dispatch`
- When dispatched: execute the task, call `mcp__coworker__orch_job_complete`
- Heartbeat every 15s via `mcp__coworker__orch_heartbeat`
- Expose universal control tools for the user

Note: no tmux idle-wake instructions (spike verdict).

### Phase 3 — Role-worker skills

Files:
- `plugins/coworker-claude/skills/coworker-role-developer.md`
- `plugins/coworker-claude/skills/coworker-role-reviewer.md`

Short role-specific overlays active when the pane IS the worker.

### Phase 4 — Slash commands

Files:
- `plugins/coworker-claude/commands/status.md`
- `plugins/coworker-claude/commands/approve.md`
- `plugins/coworker-claude/commands/invoke.md`

### Phase 5 — Go install command

File:
- `cli/plugin_install.go`

`coworker plugin install claude` — copies `plugins/coworker-claude/` into the
project's `.claude/plugins/coworker/` and merges `.mcp.json`.

### Phase 6 — Documentation

Post-execution report in this plan file.

---

## Testing

This plan is primarily static content (Markdown, JSON). Tests focus on:

1. **Go build:** `go build ./...` must pass with the new `cli/plugin_install.go`.
2. **Go test:** `go test ./... -count=1 -timeout 60s` must pass (no regressions).
3. **File presence:** all expected plugin files exist at the correct paths.

Live-agent smoke tests (`@live`, `COWORKER_LIVE=1`) are deferred — the MCP tools
they would call (`orch_register`, `orch_next_dispatch`, `orch_job_complete`) are
implemented in plans 104 and 105. The plugin content is correct-by-construction
relative to those specs.

---

## Files Produced

```
plugins/coworker-claude/
├── .mcp.json
├── settings.json
├── skills/
│   ├── coworker-orchy.md
│   ├── coworker-role-developer.md
│   └── coworker-role-reviewer.md
└── commands/
    ├── status.md
    ├── approve.md
    └── invoke.md

cli/plugin_install.go
```

---

## Code Review

### Review 1

- **Date:** 2026-04-20
- **Reviewer:** Chris (self-review before commit)
- **Verdict:** Approved

**Findings:**

- [FIXED] `plugin install claude` correctly uses `os.MkdirAll` before copying
  files, preventing partial-directory failures.
- [FIXED] `.mcp.json` merge logic handles both missing file (create) and existing
  file (merge `mcpServers` key only, preserve other keys) to avoid clobbering
  user's existing MCP configuration.
- [FIXED] Tool names in all skills use the full `mcp__coworker__*` namespace
  matching the daemon's MCP server name `"coworker"` from `.mcp.json`.
- [FIXED] No tmux wake-idle instructions anywhere in skills (spike verdict).

---

## Post-Execution Report

**Date:** 2026-04-20
**Status:** Complete

### What was built

All six phases delivered:

1. **Plugin skeleton** — `.mcp.json` (stdio transport to `coworker daemon`) and
   `settings.json` (auto-approve `mcp__coworker__orch_*` tools).

2. **coworker-orchy skill** — full polling loop: register on startup, poll after
   every explicit turn, execute dispatched jobs, call job_complete. Heartbeat at
   15s. Universal control tools documented. No tmux wake (per spike verdict).

3. **Role-worker skills** — developer and reviewer overlays with role-specific
   output contracts and supervisor rule reminders.

4. **Slash commands** — `/coworker-status`, `/coworker-approve`,
   `/coworker-invoke` with argument handling documented in each file.

5. **Go install command** — `coworker plugin install [--cli claude]` copies the
   plugin tree and merges `.mcp.json`. Handles missing vs existing `.mcp.json`
   cleanly. Prints a success summary.

6. **Tests and build** — `go build ./...` and `go test ./... -count=1 -timeout 60s`
   both pass with no failures.

### Deferred

- Live-agent smoke tests (`COWORKER_LIVE=1`) — require a running coworker daemon
  with plans 104/105 MCP tools wired up. Tracked in Plan 113 (`coworker init`)
  which does end-to-end smoke testing.
- PreToolUse / Stop hook wiring — the spec mentions hooks for permission/stop
  events. These require daemon endpoints not yet designed. Left as a stub comment
  in `settings.json` and deferred to the plan that designs the hook protocol.
