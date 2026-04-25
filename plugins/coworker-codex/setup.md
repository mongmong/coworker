# coworker-codex — Installation

This document explains how to configure Codex to work with coworker as an MCP
worker. Codex uses `~/.codex/config.toml` for MCP server registration.

---

## Prerequisites

- Codex CLI installed (tested against `codex-cli 0.122.0`+)
- `coworker` binary on your PATH
- OpenAI API key configured for Codex

---

## Register the MCP server

Run the following command once per machine:

```bash
codex mcp add \
  coworker \
  -- coworker daemon
```

This adds an entry to `~/.codex/config.toml`. See `settings.toml` in this
directory for the equivalent manual snippet.

> **Note:** `codex mcp list --json` returns `[]` in codex-cli 0.122.0 even
> after successful registration. Verify with `codex mcp list` (plain text) or
> by inspecting `~/.codex/config.toml` directly.

---

## Copy skill files

Copy the skills from this directory into your Codex instructions directory:

```bash
coworker plugin install --cli codex
```

This copies `skills/` and `commands/` into `~/.codex/coworker/`.

---

## Sandbox requirement

Codex requires `--sandbox danger-full-access` for MCP tool calls to complete
in non-interactive (`codex exec`) mode. Interactive sessions require per-tool
approval inside the TUI.

```bash
# Ephemeral dispatch (non-interactive)
codex exec --sandbox danger-full-access \
  "Call orch_next_dispatch. If dispatched, execute the task. Call orch_job_complete."

# Interactive worker session
codex "You are a coworker worker. Load your role from ~/.codex/coworker/skills/."
```

---

## Verification

After setup, verify the MCP server is reachable:

```bash
codex exec --sandbox danger-full-access \
  "Call orch_next_dispatch and report the result." \
  2>/dev/null
```

You should see either `{"status":"idle"}` or a dispatch payload.
