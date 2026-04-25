# Plan 109 — Codex Plugin (interactive + worker)

**Branch:** `feature/plan-109-110-codex-opencode-plugins`

**Goal:** Create `plugins/coworker-codex/` with skill and command files that
make Codex a first-class coworker worker, equal to Claude Code (Plan 108).
Extend `coworker plugin install --cli codex` to copy files into
`~/.codex/coworker/`.

---

## Context

Spike findings (Plan 002):

- Codex exposes MCP tools as **bare names** (`orch_register`, not
  `mcp__coworker__orch_register`). All skill instructions use bare names.
- Non-interactive `codex exec` requires `--sandbox danger-full-access` for MCP
  tool calls to complete. Interactive sessions require per-tool TUI approval.
- `codex mcp list --json` returns `[]` in codex-cli 0.122.0 even after
  successful registration — verify with plain `codex mcp list` or config
  inspection.
- Idle wake (`tmux send-keys Enter`) is broken. Codex is
  **ephemeral-primary, persistent-explicit-turn-only**.
- `--output-schema` requires `additionalProperties: false` and fully enumerated
  `required` arrays.

---

## Files created

```
plugins/coworker-codex/
├── setup.md              — installation instructions
├── settings.toml         — config.toml snippet for MCP registration
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

- `cli/plugin_install.go`: added `installCodexPlugin` branch for `--cli codex`.
  Copies files to `~/.codex/coworker/`. Prints instructions for MCP
  registration (`codex mcp add`) and the `danger-full-access` requirement.
  No `.mcp.json` merge — Codex uses `~/.codex/config.toml`.

---

## Key design decisions

1. **Bare tool names everywhere.** All `orch_*` references in skill/command
   files omit the `mcp__coworker__` namespace prefix.
2. **Sandbox documented prominently.** Both `setup.md` and the install command
   output surface the `--sandbox danger-full-access` requirement.
3. **No idle-wake instructions.** Codex persistent mode requires explicit turns;
   the orchy skill does not instruct timer-based polling.
4. **`--output-schema` tip in role skills.** Developer and reviewer skills
   remind Codex that strict schema enforcement (`additionalProperties: false`,
   fully enumerated `required`) is needed.

---

## Post-Execution Report

Implemented as part of Plans 109+110 in a single commit on branch
`feature/plan-109-110-codex-opencode-plugins`. All files created. Go build
and tests pass. See Plan 110 for OpenCode parallel implementation.
