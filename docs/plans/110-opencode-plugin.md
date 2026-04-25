# Plan 110 — OpenCode Plugin (interactive + worker)

**Branch:** `feature/plan-109-110-codex-opencode-plugins`

**Goal:** Create `plugins/coworker-opencode/` with skill and command files that
make OpenCode a first-class coworker worker, equal to Claude Code (Plan 108).
Extend `coworker plugin install --cli opencode` to copy files into
`.opencode/coworker/` and merge `.mcp.json`.

---

## Context

Spike findings (Plan 003):

- OpenCode is **HTTP-primary**: `opencode serve` exposes a REST API with SSE
  event streams. The coworker daemon dispatches via HTTP, not MCP pull.
- Dispatch uses `POST /session` + `POST /session/{id}/message` (body must use
  `parts`, not bare `content` string).
- Completion signal: `session.idle` SSE event, corroborated by final assistant
  `message.updated` / `step-finish`.
- Abort: `POST /session/{id}/abort` — session remains usable after abort.
- Worktree isolation: each `opencode serve` instance binds to its cwd; clean
  per-plan isolation is confirmed viable.
- MCP client support: OpenCode can also connect to MCP servers. Tool naming
  follows the same namespaced pattern as Claude Code (`mcp__coworker__*`).
- `opencode run --format json` is a lightweight ephemeral fallback.

---

## Files created

```
plugins/coworker-opencode/
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
```

## Go changes

- `cli/plugin_install.go`: added `installOpenCodePlugin` branch for
  `--cli opencode`. Copies files to `.opencode/coworker/` in the project root.
  Merges `.mcp.json` (same as Claude path). Prints instructions for HTTP
  dispatch via `opencode serve`.

---

## Key design decisions

1. **HTTP-primary documented clearly.** The orchy skill explains both the HTTP
   dispatch path (daemon-driven) and the interactive MCP fallback.
2. **SSE observability.** The orchy skill documents key event types so that
   operators understand the completion detection contract (`session.idle`).
3. **Namespaced tool names.** Interactive MCP mode uses `mcp__coworker__*`
   names, consistent with Claude Code. The spike did not contradict this.
4. **Worktree isolation noted.** Developer and reviewer skills remind the agent
   that file operations are scoped to the bound worktree root.
5. **Abort support.** The invoke command and orchy skill mention
   `POST /session/{id}/abort` so that agents know long-running jobs can be
   cancelled without process teardown.

---

## Code Review

### Review 1
- **Date**: 2026-04-24
- **Reviewer**: Claude (retrospective review)
- **Verdict**: Approved

Retrospective review against shipped files in `plugins/coworker-opencode/` and `cli/plugin_install.go`.

- **HTTP dual-mode documented**: `skills/coworker-orchy.md` clearly separates the HTTP-primary path (daemon-driven via `POST /session` + `POST /session/{id}/message`) from the interactive MCP fallback, with the daemon's `session.idle` completion gate explained. [PASS]
- **SSE event types documented**: The orchy skill lists key SSE event types (`session.created`, `session.idle`, `message.updated`/`step-finish`, `session.status`) and identifies `session.idle` as the primary terminal signal. [PASS]
- **Namespaced tool names**: Interactive MCP mode uses `mcp__coworker__*` names throughout, consistent with Claude Code — the spike did not contradict this for OpenCode's MCP client path. [PASS]
- **Abort support**: `skills/coworker-orchy.md` and `commands/invoke.md` both document `POST /session/{id}/abort` and note that the aborted session remains usable. [PASS]
- **Plugin structure**: File layout (`.mcp.json`, `settings.json`, `skills/`, `commands/`) mirrors the Claude Code plugin pattern. [PASS]
- **`installOpenCodePlugin` Go branch**: Copies to `.opencode/coworker/` in project root and merges `.mcp.json` using the same merge logic as the Claude Code path. [PASS]

---

## Post-Execution Report

**Shipped:** 2026-04-20 on `feature/plan-109-110-codex-opencode-plugins`.

**Files created:**
- `plugins/coworker-opencode/.mcp.json` — MCP server registration for `orch` tools
- `plugins/coworker-opencode/settings.json` — OpenCode settings snippet
- `plugins/coworker-opencode/skills/coworker-orchy.md` — HTTP dual-mode (daemon + interactive MCP) dispatch
- `plugins/coworker-opencode/skills/coworker-role-developer.md` — OpenCode developer role skill (worktree-scoped)
- `plugins/coworker-opencode/skills/coworker-role-reviewer.md` — OpenCode reviewer role skill
- `plugins/coworker-opencode/commands/{status,approve,invoke}.md` — command definitions with abort support

**Go changes:** `cli/plugin_install.go` extended with `installOpenCodePlugin` branch. Copies plugin files to `.opencode/coworker/` in project root, merges `.mcp.json` (same logic as Claude Code path). Prints HTTP dispatch instructions.

**Tests:** Test coverage for `.mcp.json` merge logic. Full suite: 0 failures, 0 regressions. `golangci-lint run ./...` — 0 issues.

**Key implementation notes:**
- HTTP-primary dispatch (`POST /session` + `POST /session/{id}/message`) documented alongside interactive MCP fallback.
- SSE event types documented (`session.idle` as primary completion signal).
- Interactive MCP mode uses namespaced tool names (`mcp__coworker__*`), consistent with Claude Code.
- Worktree isolation documented in role skills — each `opencode serve` binds to its cwd.
- Abort support (`POST /session/{id}/abort`) documented in invoke command and orchy skill.

**Deferred:** HTTP dispatch daemon loop (Plan 114+) implements session lifecycle, SSE polling, and completion gate.
