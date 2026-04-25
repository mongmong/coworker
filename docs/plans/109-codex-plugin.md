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

## Code Review

### Review 1
- **Date**: 2026-04-24
- **Reviewer**: Claude (retrospective review)
- **Verdict**: Approved

Retrospective review against shipped files in `plugins/coworker-codex/` and `cli/plugin_install.go`.

- **Bare tool names**: All `orch_*` references in `skills/coworker-orchy.md`, `skills/coworker-role-developer.md`, and `skills/coworker-role-reviewer.md` correctly use bare names (e.g. `orch_register`, not `mcp__coworker__orch_register`), matching the spike finding for codex-cli 0.122.0. [PASS]
- **`danger-full-access` documented**: The sandbox requirement is called out prominently in both `setup.md` and the `coworker-orchy.md` skill header. The `plugin install` output also surfaces it. [PASS]
- **No idle-wake polling**: The orchy skill correctly omits any timer-based polling instruction; persistent mode relies on explicit user turns only, per spike finding. [PASS]
- **`--output-schema` tip**: Developer and reviewer skills remind Codex that `additionalProperties: false` and fully enumerated `required` arrays are needed — this matches the spike finding on schema enforcement. [PASS]
- **Plugin structure**: File layout (`setup.md`, `settings.toml`, `skills/`, `commands/`) mirrors the Claude Code plugin pattern for consistency. [PASS]
- **`installCodexPlugin` Go branch**: Copies to `~/.codex/coworker/` and prints MCP registration instructions; does not attempt `.mcp.json` merge since Codex uses `~/.codex/config.toml`. [PASS]

---

## Post-Execution Report

Implemented as part of Plans 109+110 in a single commit on branch
`feature/plan-109-110-codex-opencode-plugins`. All files created. Go build
and tests pass. See Plan 110 for OpenCode parallel implementation.
