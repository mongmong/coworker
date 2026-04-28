# Plan 135 — NICE-TO-HAVE batch 2

> Plugin asset audit + two how-to guides for new contributors.

## N5 — Plugin asset audit

`grep -rn '/home/\|/Users/\|/tmp/\|/var/\|cd /\|export.*=/' plugins/` → no hits.
`grep -rn 'bash -c\|/bin/sh\|/bin/bash\|#!/bin\|zsh\|fish ' plugins/` → no hits.

Plugin assets (skills, commands, settings) are clean: no hard-coded absolute paths, no shell-specific assumptions. No code changes.

## N9 — Two how-to guides

- `docs/architecture/adding-a-role.md` — full walkthrough for adding a new role (YAML + prompt template + optional applies_when + workflow_overrides + plugin updates), with a worked `reviewer.security` example.
- `docs/architecture/adding-a-replay-scenario.md` — when to use replay vs other layers, file layout, transcript format, test boilerplate, and tips for deterministic ordering / dedup / cost wiring / role attribution.

The other deferred how-tos (`adding-mcp-tool.md`, `adding-cli-command.md`) are smaller — pattern-match the existing tools/commands. They can be added when a contributor actually needs them.

## Verification

```
go build ./...                                  → clean
go test -race ./... -count=1 -timeout 180s      → 33 ok, 0 failed, 0 races
golangci-lint run ./...                         → 0 issues
```
