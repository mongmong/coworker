# Plan 113 — `coworker init` Scaffolding

**Branch:** `feature/plan-113-coworker-init`
**Flavor:** Runtime
**Blocks on:** 108, 109, 110, 111, 112
**Manifest entry:** `docs/specs/001-plan-manifest.md` §113

---

## Purpose

`coworker init` is the one-command on-ramp for a new repository. It creates the
`.coworker/` directory tree with default config, policy, roles, prompts, rules,
updates `.gitignore`, and optionally installs CLI plugins. Re-running it is
idempotent.

---

## Background

From the design spec (Appendix A) and §Plugins, `coworker init` should produce:

```
.coworker/
├── .version
├── config.yaml
├── policy.yaml
├── roles/          (7 YAML files from coding/roles/)
├── prompts/        (7 .md files from coding/prompts/)
└── rules/
    ├── supervisor-contract.yaml   (from coding/supervisor/rules.yaml)
    └── quality.yaml               (from coding/quality/rules.yaml)
```

Plus `.gitignore` augmentation and optional plugin installation.

---

## Architecture

```
cli/
├── init.go         # cobra command + runInit
└── init_test.go    # comprehensive tests (6 test functions)
```

No `init_assets.go` needed — asset-finding follows the same runtime filesystem
pattern used by `plugin_install.go` (`findInitAssets`). This avoids build-time
file duplication and keeps the source-of-truth in `coding/`.

### Source file resolution

`findInitAssets` mirrors `findPluginSource`:
1. `<binary-dir>/coding/` — for installed binaries
2. `<cwd>/coding/` — for development (running from repo root)

This means `coworker init` works out of the box when run from the repo during
development, and works in production if the installed binary is adjacent to a
`coding/` tree (documented trade-off, acceptable for V1).

### Options

```go
type initOptions struct {
    Force       bool   // overwrite existing files
    WithPlugins bool   // install all three CLI plugins
    Global      bool   // install plugins to global dirs (~/.claude, etc.)
    Dir         string // output dir (default: .coworker in cwd)
}
```

---

## Phases

### Phase 1 — `coworker init` command skeleton + directory structure

Files:
- `cli/init.go` — command definition, `runInit`, directory creation
- `cli/init_test.go` — initial tests for directory structure

Creates `.coworker/{roles,prompts,rules}/` directories and writes
`config.yaml`, `policy.yaml`, `.version`.

### Phase 2 — Copy role/prompt/rule assets

Extend `cli/init.go` with `findInitAssets` and copy helpers.
Tests: verify role YAMLs, prompt MDs, and rule YAMLs are copied correctly.

### Phase 3 — `.gitignore` augmentation

Idempotently append `.coworker/state.db` and `.coworker/runs/` to `.gitignore`
if they are not already present (exact-string match per line).
Tests: augmentation adds entries, re-run does not duplicate, pre-existing
entries are detected.

### Phase 4 — Idempotency + `--force`

- Re-running without `--force`: existing files are skipped with a notice.
- Re-running with `--force`: all files are overwritten.
- `.version` is always updated.
Tests: idempotency (no overwrite without force), force overwrite.

### Phase 5 — `--with-plugins` flag

Delegates to `runPluginInstall` (reusing `plugin_install.go`) for claude,
codex, and opencode in sequence. `--global` is threaded through; for plugin
installs the project-local vs global distinction is handled by the existing
`installClaudePlugin` / `installCodexPlugin` / `installOpenCodePlugin`
functions (codex is always global; claude/opencode use cwd). For `--global`
we document that behaviour is the same as repeated `coworker plugin install`
calls — the flag is stored on `initOptions` but currently its effect on
non-codex plugins is a no-op (those already install project-locally).
Tests: `--with-plugins` triggers plugin installation.

---

## Default config.yaml

```yaml
daemon:
  bind: local_socket
  data_dir: .coworker

cli_selection:
  interactive_driver: claude-code
  fallback_driver: opencode

providers:
  claude-code:
    rate_limit_concurrent: 3
  codex:
    sandbox_default: workspace-write
    rate_limit_concurrent: 2
  opencode:
    server_url: http://127.0.0.1:7777
    rate_limit_concurrent: 4

telemetry:
  event_log_retention_days: 90
  cost_ledger_retention_days: 365
```

## Default policy.yaml

Derived from `coding/policy/defaults.go`:

```yaml
checkpoints:
  spec-approved: block
  plan-approved: block
  phase-clean: on-failure
  ready-to-ship: block
  compliance-breach: block
  quality-gate: block

supervisor_limits:
  max_retries_per_job: 3
  max_fix_cycles_per_phase: 5

concurrency:
  max_parallel_plans: 2
  max_parallel_reviewers: 3

permissions:
  on_undeclared: block
```

---

## Testing Plan

`cli/init_test.go`:

| Test | What it verifies |
|---|---|
| `TestRunInit_BasicStructure` | All expected dirs and files created; config.yaml, policy.yaml, .version present |
| `TestRunInit_RolesPrompsRulesCopied` | Role YAMLs, prompt MDs, rule YAMLs copied from source tree |
| `TestRunInit_GitignoreAugmented` | `.coworker/state.db` and `.coworker/runs/` appended |
| `TestRunInit_GitignoreIdempotent` | Re-run does not duplicate .gitignore entries |
| `TestRunInit_IdempotentSkipsExisting` | Second run without `--force` skips existing files |
| `TestRunInit_ForceOverwrites` | `--force` overwrites existing files |

---

## Trade-offs and Known Limitations

- **Source discovery at runtime:** `findInitAssets` searches for `coding/` relative to
  the binary or cwd. Production distributions must ship the `coding/` tree alongside
  the binary or embed assets via a build step. This is an acceptable V1 trade-off,
  documented here and in the output of `coworker init` when sources are not found.
- **`--global` plugin scope:** For claude/opencode plugins, `--global` currently has
  no distinct effect (those install into the project cwd regardless). The flag exists
  for forward-compatibility and passes through to plugin install calls. Codex is always
  global by design.
- **`--with-plugins` error handling:** If a plugin install fails, `runInit` logs the
  error and continues (non-fatal). Missing plugin sources are a common state during
  development.

---

## Verification

```
go build ./...
go test -race ./cli/... -count=1 -timeout 60s
```

All existing and new tests must pass.

---

## Code Review

_To be filled in during review._

---

## Post-Execution Report

_To be filled in after implementation._
