# Project Instructions

## Project Overview

**coworker** is a local-first runtime that coordinates CLI coding agents (Claude Code, Codex, OpenCode) as role-typed workers. It is driven either by a PRD (autopilot) or direct user interaction, with strict workflow enforcement through a supervisor role.

Design spec: `docs/specs/000-coworker-runtime-design.md`. Read it before planning or implementing.

Stack (V1): Go (goroutines + channels) + SQLite (`modernc.org/sqlite`, pure Go, no cgo) + Bubble Tea TUI + cobra CLI + MCP server exposing `orch.*` tools, plus plugins for each CLI (Claude Code, Codex, OpenCode).

## CRITICAL RULES (never skip)

- **Always create a feature branch** before implementation. Never commit directly to main. Use `git checkout -b feature/plan-NNN-description`.
- **Always code review** before creating a PR. Write findings to the plan file's `## Code Review` section.
- **Always write post-execution report** in the plan file before shipping.
- **Always run full test suite** before pushing. Do not push with failing tests.

## Project Structure (intended — most does not yet exist)

Go module layout (to be created during implementation):

- `go.mod` at repo root; module path `github.com/<org>/coworker`
- `cmd/coworker/` — CLI binary entry point (main.go)
- `core/` — **domain-neutral primitives** (runs, jobs, events, supervisor framework, attention queue, worker registry, cost ledger, agent protocol). Imports flow core → coding, never the reverse.
- `coding/` — coding-specific roles, rules, workflows, plugins. Depends on `core`.
- `cli/` — cobra command definitions (`run`, `session`, `init`, `status`, `watch`, `logs`, `inspect`, `resume`, `invoke`, `redo`, `edit`, `advance`, `rollback`)
- `tui/` — Bubble Tea dashboard (`charmbracelet/bubbletea` + `lipgloss` + `bubbles`)
- `mcp/` — MCP server exposing `orch.*` tools
- `store/` — SQLite layer (`modernc.org/sqlite`), schema migrations, event-first write helpers
- `agent/` — `Agent` protocol + `CliAgent` (shell-out + stream-json parser)
- `plugins/coworker-claude/` — Claude Code plugin (skills, commands, settings — not Go code)
- `plugins/coworker-codex/` — Codex worker prompts (not Go code)
- `plugins/coworker-opencode/` — OpenCode plugin (not Go code)
- `testdata/` — test fixtures (mock CLI binaries, golden event logs, role YAMLs)
- `internal/` — private helpers not meant for external import
- `docs/` — specs, plans, architecture decisions

Tests live next to the package they cover (`*_test.go`), with fixtures in `testdata/`. Integration tests live in a top-level `tests/` directory.

Per-repo scaffolding (created by `coworker init`, see spec Appendix A):

- `.coworker/config.yaml`, `.coworker/policy.yaml`
- `.coworker/roles/*.yaml`, `.coworker/prompts/*.md`, `.coworker/rules/*.yaml`
- `.coworker/state.db` (gitignored), `.coworker/runs/` (gitignored)
- `.claude/plugins/coworker/`, `.opencode/coworker/`, `.codex/coworker/`

## Running the App (once built)

- **Daemon**: `coworker daemon` — runs the scheduler, MCP server, event bus
- **Autopilot**: `coworker run <prd.md>` — PRD → spec → plans → PRs
- **Interactive**: `coworker session` — ad-hoc role dispatch, no PRD
- **Dashboard**: launched automatically by `coworker run --with-chat` (tmux layout with TUI + live CLI pane)
- **Observability CLIs**: `coworker status`, `coworker watch`, `coworker logs <job-id> --follow`, `coworker inspect <job-id>`

Defer any exact invocation details to the plans that implement them; this list exists for orientation.

## Architecture Decisions

**Always read `docs/architecture/decisions.md` before making cross-cutting changes.** It is the single source of truth for runtime rules (context store boundaries, event-log-before-state invariant, registry semantics, supervisor enforcement). Create it when the first plan introduces a cross-cutting decision. Update it whenever a later plan introduces or revises one.

Load-bearing invariants from the spec (violations must be caught by supervisor rules):

- **Event log before state update.** `events` rows are written before the DB row they describe. Daemon crash mid-update → replay restores consistency.
- **File artifacts are pointers.** `artifacts.path` references files on disk; nothing durable is inlined into SQLite.
- **Findings are immutable.** Resolution links a fix job; the original row persists.
- **No failure silently advances state.** Every stuck path terminates in a successful retry or a human checkpoint.
- **Pull model for message delivery.** Persistent CLI workers pull dispatches via `orch.next_dispatch()`. Terminal `send-keys` is only for wake-idle nudges, never for message content.

## Coding Conventions

### Go (runtime, CLI, TUI, MCP)

- **Concurrency:** goroutines + channels. Contexts (`context.Context`) everywhere for cancellation and deadlines. No naked goroutines — every `go func()` has a defined lifecycle (wait group, error channel, or select-on-ctx.Done).
- **Data model:** SQLite via `modernc.org/sqlite` (pure Go, no cgo — preserves single-binary distribution). Raw `database/sql` + prepared statements; no ORM. Schema migrations are explicit and versioned (numbered .sql files + a tiny runner).
- **TUI:** Bubble Tea (`charmbracelet/bubbletea`), Lip Gloss (`charmbracelet/lipgloss`) for styling, Bubbles (`charmbracelet/bubbles`) for stock widgets.
- **CLI:** `spf13/cobra` for command hierarchy, `spf13/viper` only if we outgrow flat config (prefer simple YAML+struct decoding).
- **MCP server:** official `modelcontextprotocol/go-sdk` if viable; fall back to `mark3labs/mcp-go` if the official SDK lags features we need (decide in Plan 104 based on spike findings).
- **YAML + validation:** `gopkg.in/yaml.v3` for parsing, `go-playground/validator` for struct-tag validation, or hand-rolled validation if the surface is small.
- **Subprocess:** `os/exec` with explicit `cmd.StdoutPipe()` streaming; parse stream-json with `json.Decoder` in a loop. Never buffer the whole output.
- **Linting:** `golangci-lint` with `govet`, `staticcheck`, `errcheck`, `gosec`, `gocyclo` enabled.
- **Error handling:** `fmt.Errorf("...: %w", err)` for wrapping; `errors.Is` / `errors.As` at inspection sites. No silent error drops. Structured logging via `slog`.
- **Prompt/role templates** live in the filesystem under `.coworker/prompts/` and `.coworker/roles/` — loaded at daemon start, not embedded in Go source.
- **Package layout posture:** `core/` and `coding/` are separate packages; `coding/` may import `core/` but never the reverse. Enforced by import tests.
- **Production-quality code required.** Go code must cover the SAME business logic, edge cases, fallback chains, defensive patterns, and generalization as the Python implementation — not just the happy path. Do NOT strip defensive logic, pre-validation, health probes, retry paths, or fallback chains in the name of Go minimalism or YAGNI. When porting from Python, read the Python implementation's INTERNAL logic (not just the API shape) and port ALL defensive paths. When writing new Go code with no Python counterpart, handle: what happens on failure? partial input? upstream down? concurrent access? empty collections? Think like a production SRE, not a tutorial writer.

### General

- Follow existing patterns in the codebase — check how similar features are implemented before writing new code.
- Never hardcode secrets. `.env` for secrets; `config.yaml` / `policy.yaml` for non-secret configuration.
- When modifying any logic, proactively search the codebase for similar patterns that should receive the same change. Audit related roles, rules, prompts, and handlers for consistency.
- Don't duplicate logic. If a utility exists, reuse it. Keep a single source of truth.
- **Never defer fixes without a follow-up plan.** If a known issue is identified during implementation, fix it NOW in the same commit/PR. Do NOT label it "acceptable trade-off", "follow-up", "TODO", or "deferred" unless a concrete follow-up plan has been drafted in `docs/plans/` with a plan number, scope, and phases. Unfiled deferrals rot — they become invisible tech debt that surfaces only during live user testing.
- **Always implement the long-term solution, not the short-term workaround.** When two approaches exist — a quick hack that unblocks now vs a proper fix that solves the root cause — choose the proper fix. Short-term workarounds create architectural debt that compounds across plans (e.g., dual agentic loops, dual streaming states, dual event channels). If the proper fix is genuinely too large for the current scope, draft the follow-up plan immediately and get user approval before shipping the workaround.

## Testing

Three layers, per the spec:

1. **Unit** (`*_test.go` next to source files) — state-machine transitions, fan-in dedupe, rule predicates, schema validation, scheduler DAG. Fast, deterministic, no agents. ~80% of the suite. Must pass on every commit. Run via `go test ./... -count=1 -timeout 60s`.
2. **Integration with mocks** (`tests/integration/`) — swap real CLIs with mock binaries under `testdata/mocks/<cli-name>` (shell scripts or compiled Go binaries) that emit canned stream-json. Exercises dispatch, registry eviction, supervisor loops, crash recovery, fan-out width. Runs in seconds, no API cost.
3. **Replay** (`tests/replay/<run-name>/`) — recorded real-agent transcripts. Played back via a replay-mode CLI wrapper. Gated to nightly CI.

**Event log as fixture.** Every test that drives the runtime should snapshot the resulting `events` log and diff against the golden file. This catches ordering regressions that return-value / DB-state assertions miss.

**Optional live E2E** — tag `@live`, enabled by `COWORKER_LIVE=1`. Single-digit dollars per full run. Pre-release only.

When code is added or modified, write or update tests covering the changes. Test happy paths, error paths, and edge cases. Every public function, every branch, every error path.

Run the relevant tests before committing. Run the full suite before pushing.

## Documentation

When code changes affect behavior, APIs, architecture, or configuration, update the relevant documentation in `docs/` and any affected `README.md` files. This includes: new MCP tools, changed role contracts, new checkpoint kinds, modified config fields, supervisor rule additions, updated setup steps.

## Git

- **Never commit directly to `main`.** Always create a feature branch and PR via `gh pr create`.
- Before merging any PR, check CI status with `gh pr checks <number>`. If any checks fail, fix and push before merging. Never merge a PR with failing checks.
- Do not push to remote unless explicitly asked.
- Write clear, descriptive commit messages — lead with what changed and why.
- Do not commit `.env`, credentials, recorded replay transcripts containing secrets, or large binaries.
- Batch related small fixes into a single meaningful commit. Only commit when a logical unit is complete.

## Plans

For substantial changes — new role, new workflow, supervisor rule class, MCP tool addition, persistent-worker protocol change, etc. — always enter plan mode first and write a detailed plan. Get user approval before implementing.

**Execution plans** go in `docs/plans/`, numbered sequentially (000, 001, ..., 100, 101, ...). Sub-documents use letter suffixes (106a, 106b).

**Design specs** go in `docs/specs/` — only for large, novel, or cross-cutting designs. When brainstorming produces a concrete design, skip the spec and go straight to an execution plan.

Before writing a new plan, review existing plans in `docs/plans/` for reusable patterns, established conventions, and prior decisions. Respect them or explicitly amend them in the new plan.

## Development Workflow

Follow `docs/development-workflow.md` exactly for every plan (Steps 1–6: Design → Plan → Build → Verify → Review → Ship). Do not skip or batch. Key points:

- **Design before plan** — explore the problem, brainstorm approaches, align with user.
- **Plan before code** — commit the plan file before any implementation.
- **Build phase by phase** — implement, test, self-review, document, commit each phase.
- **Verify before review** — full test suite, cross-phase consistency check.
- **Review before ship** — code review findings in plan file, all `[OPEN]` items resolved.
- **Ship cleanly** — post-execution report, update `decisions.md` + `TODO.md`, then PR.

## Code Review

Follow `docs/code-review.md`. Key points:

- Reviews go in the plan file's `## Code Review` section.
- Reviewers: append findings with `[OPEN]` status and file:line references.
- Authors: respond inline with `→ Response:` and `[FIXED]` / `[WONTFIX]`.

## Multi-Agent Coordination (building-the-builder caveat)

This project builds a multi-agent runtime, and we ourselves will often work on it with multiple Claude Code sessions against the same repo. Until coworker exists to coordinate its own development, the same handoff rules apply:

- **Always commit before ending a session.** Use `WIP:` prefix for partial work. Uncommitted changes are invisible to the next session.
- **Check `git status` at session start.** Investigate untracked or modified files from a prior session before discarding.
- **Each plan uses a feature branch.** Pull before starting work, push before ending. Never leave unpushed commits.
- **Never assume prior session completed its work.** Verify via the plan file's post-execution report and `git log` — don't trust conversation summaries alone.
