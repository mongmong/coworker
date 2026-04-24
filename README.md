# coworker

`coworker` is a local-first runtime for coordinating CLI coding agents as typed workers.

Today, the implemented path is a **thin end-to-end ephemeral invoke flow**:

1. load a role from YAML
2. render a prompt template
3. invoke a CLI agent
4. parse stream-JSON findings
5. persist runs, jobs, findings, and events to SQLite

The best way to try it is the tutorial in [docs/tutorial.md](/home/chris/workshop/coworker/docs/tutorial.md).

## Current Status

What works today:

- `coworker invoke <role>`
- bundled role loading from `coding/roles/`
- bundled prompt loading from `coding/prompts/`
- SQLite persistence in `.coworker/state.db`
- event-log-before-state persistence for runs, jobs, and findings

What is not built yet:

- multi-agent scheduling
- persistent worker registry / leasing
- TUI control plane
- bulletin-board routing
- integrated OpenCode HTTP runtime

## Prerequisites

- Go 1.25+
- Unix-like shell for the mock tutorial flow
- optional: a real CLI agent such as Codex, Claude Code, or OpenCode for manual experiments

## Build

```bash
make build
./coworker version
```

You can also run it without building:

```bash
go run ./cmd/coworker --help
```

## Quick Start

The fastest deterministic path uses the bundled mock Codex script:

```bash
go run ./cmd/coworker invoke reviewer.arch \
  --diff go.mod \
  --spec docs/specs/000-coworker-runtime-design.md \
  --cli-binary ./testdata/mocks/codex \
  --role-dir coding/roles \
  --prompt-dir coding
```

Expected output looks like:

```text
Run: <run-id>
Job: <job-id>
Findings: 2
```

The command also creates `.coworker/state.db`.

## Documents

- [docs/tutorial.md](/home/chris/workshop/coworker/docs/tutorial.md) — step-by-step tutorial you can follow from the command line
- [docs/spike-rerun-guide.md](/home/chris/workshop/coworker/docs/spike-rerun-guide.md) — how to rerun spikes 001-003
- [docs/specs/000-coworker-runtime-design.md](/home/chris/workshop/coworker/docs/specs/000-coworker-runtime-design.md) — runtime architecture
- [docs/specs/001-plan-manifest.md](/home/chris/workshop/coworker/docs/specs/001-plan-manifest.md) — plan manifest
- [docs/development-workflow.md](/home/chris/workshop/coworker/docs/development-workflow.md) — development workflow used in this repo
