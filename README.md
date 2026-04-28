# coworker

`coworker` is a local-first runtime for coordinating CLI coding agents (Claude Code, Codex, OpenCode) as role-typed workers. It is driven either by a PRD (autopilot) or by direct user interaction, with strict workflow enforcement through a supervisor role.

Design spec: [`docs/specs/000-coworker-runtime-design.md`](docs/specs/000-coworker-runtime-design.md). Architecture decisions: [`docs/architecture/decisions.md`](docs/architecture/decisions.md). Latest comprehensive audit: [`docs/reviews/2026-04-27-comprehensive-audit.md`](docs/reviews/2026-04-27-comprehensive-audit.md).

The best way to try it is the tutorial in [docs/tutorial.md](docs/tutorial.md).

## What works today (V1)

**Runtime + persistence**
- SQLite event log + projection tables (`runs`, `jobs`, `plans`, `checkpoints`, `findings`, `artifacts`, `dispatches`, `workers`, `attention`, `supervisor_events`, `cost_events`).
- Event-log-before-state writes (`EventStore.WriteEventThenRow`).
- Findings immutability (SQL trigger).
- Pure-Go SQLite (`modernc.org/sqlite`) — single static binary, no cgo.

**Agents**
- `agent.CliAgent` — shells out to a CLI binary, parses stream-json.
- `agent.OpenCodeHTTPAgent` — REST + SSE against an OpenCode server.
- `agent.ReplayAgent` — replays recorded transcripts in tests.

**Dispatch + workflow**
- `coding.Dispatcher` with role loading, prompt rendering, supervisor evaluation, retry-with-feedback, cost capture (Claude USD direct, Codex tokens-only).
- `phaseloop.PhaseExecutor` developer → reviewer/tester fan-out → dedupe → fix-loop with `applies_when` filtering (changes_touch / commit_msg_contains / phase_index_in).
- `workflow.BuildFromPRDWorkflow` for the autopilot path; `shipper.Shipper` opens the PR.
- `stages.StageRegistry` for `policy.workflow_overrides` (phase-dev / phase-review / phase-test).

**MCP server** — full `orch_*` tool surface: `next_dispatch`, `job_complete`, `attention_*`, `checkpoint_*`, `findings_list`, `artifact_*`, `register`/`heartbeat`/`deregister`, `run_status`/`run_inspect`/`role_invoke`.

**HTTP/SSE server** — read-only events stream + run/job/attention REST. Binds 127.0.0.1 by default.

**CLI commands** — `daemon`, `run <prd.md>`, `session`, `init`, `invoke <role>`, `record-human-edit`, `watch`, `dashboard`, `status`, `logs <job-id> [--follow]`, `inspect <job-id>`, `advance`, `rollback <id>`, `edit <path>`, `version`, `plugin install`, `config inspect`.

**Plugins** — `plugins/coworker-{claude,codex,opencode}/` with skills, commands, and settings for each CLI.

**Test layers** — unit (next to source), integration with mocks (`tests/integration/`), replay (`tests/replay/<scenario>/`, gated by `COWORKER_REPLAY=1`), live E2E (`tests/live/`, gated by build tag `live` + `COWORKER_LIVE=1`).

**Build & dist** — `make release` cross-compiles linux/amd64, linux/arm64, darwin/amd64, darwin/arm64.

## Deferred to V1.1+

- Codex USD pricing (per-model price table).
- OpenCode cost capture (no token data in the SSE stream).
- Runtime budget enforcement (`runs.budget_usd` is recorded but not enforced).
- TUI attention auto-refresh (events not yet emitted; HTTP polling works).
- `redo` CLI command (use `invoke <role>` with explicit inputs).
- HTTP endpoint authentication (loopback default; LAN exposure requires `--http-bind 0.0.0.0`).
- Phase-loop replay scenarios in `tests/replay/` (single-role scenarios are shipped).
- Filesystem watch on `coworker edit` (manual `record-human-edit` works).

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
