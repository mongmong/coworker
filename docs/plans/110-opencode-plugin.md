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

## Post-Execution Report

Implemented as part of Plans 109+110 in a single commit on branch
`feature/plan-109-110-codex-opencode-plugins`. All files created. Go build
and tests pass. See Plan 109 for Codex parallel implementation.
