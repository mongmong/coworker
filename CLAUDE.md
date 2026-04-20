# Project Instructions

## CRITICAL RULES (never skip)

- **Always create a feature branch** before implementation. Never commit directly to main. Use `git checkout -b feature/plan-NNN-description`.
- **Always code review** before creating a PR. Write findings to the plan file's `## Code Review` section.
- **Always write post-execution report** in the plan file before shipping.
- **Always run full test suite** before pushing. Do not push with failing tests.

## Project Structure

- `platform/api/` — FastAPI API server (run with `PYTHONPATH=platform`)
- `platform/engine/` — Workflow execution engine (standalone service)
- `platform/core/` — Shared business logic (used by both api and engine)
- `gobackend/` — Go backend (API + engine, dual SQLite/PostgreSQL)
- `config.toml` — Non-secret platform configuration (models, plugins, CORS, MCP servers)
- `config_llm.toml` — LLM endpoint routing: named endpoints with type, URL, API key env var, model globs, and mappings
- `apps/` — Business-domain plugins (e.g., `ads_manager/`)
- `web/` — Next.js frontend (package.json is here, not in root)
- `tui/` — Textual TUI chat client
- `cli/` — Unified CLI entry point
- `cli2/` — Fast REPL chat client (Rich + prompt_toolkit)
- `docs/` — Documentation
- `docs/plans/` — Executed implementation plans
- `docs/specs/` — Design specs for large/novel features
- `tests/` — Python tests

## Running the App

- **Python API**: `magicburg api --reload` or `.venv/bin/uvicorn platform.api.main:app --reload --reload-dir platform --port 8000`
  - Must use `--reload-dir platform` to avoid scanning `web/node_modules`
- **Python Engine**: `magicburg engine` or `PYTHONPATH=platform python -m engine.main`
  - Standalone service that executes workflow DAG runs
  - Options: `--poll-interval 5 --max-concurrent 3`
- **Go API** (hot-reload): `cd gobackend && ~/go/bin/air -c .air.api.toml` (port 8001)
- **Go API** (manual): `cd gobackend && go run ./cmd/api/` (port 8001)
- **Go Engine** (hot-reload): `cd gobackend && ~/go/bin/air -c .air.engine.toml`
- **Go Engine** (manual): `cd gobackend && go run ./cmd/engine/`
- **Frontend**: `cd web && bun run dev`
  - Requires Bun ≥ 1.1.30 (install from https://bun.sh/). Bun handles dependency install and script running; Next.js 15 + SWC still run under Bun.
  - Uses `FASTAPI_BASE_URL=http://127.0.0.1:8000` (Python) or `http://127.0.0.1:8001` (Go) — set in `web/.env.local`

## Architecture Decisions

**Always read `docs/architecture/decisions.md` before making changes that touch authentication, media handling, data fetching, configuration, error handling, or persistence.** It is the single source of truth for cross-cutting rules. Update it when a plan introduces new decisions.

Key rules (details in `docs/architecture/decisions.md`):
- **Plugins**: Business-domain features go in `platform/plugins/`, not core. See `docs/plugins/README.md`.
- **API-first**: All clients are pure API consumers. No direct imports of `core.*` or `llm.*`.
- **Tool metadata**: Every tool needs display metadata in `tool-meta.ts` (core) or `manifest.json` (plugin).
- **Message roles**: `directive` (LLM must act), `status` (display only), `command` (slash command output). Use `emit_event()` for directives, `emit_status()` for status. See `docs/directives.md`.

## Coding Conventions

### Python
- Backend: FastAPI, Pydantic models, SQLite via raw `sqlite3`
- Linting: `ruff`
- Keep error handling consistent: FastAPI `HTTPException`

### Go
- Backend: Chi router, `database/sql` via `*db.DB` wrapper (not `*sql.DB` directly)
- Dual database: `DATABASE_URL` env var → PostgreSQL via `pgx/stdlib`, absent → SQLite via `modernc.org/sqlite`
- All stores accept `*db.DB` for dialect-aware placeholder rewriting (`?` → `$1`)
- Use `db.WrapDB(sqlDB, db.SQLiteDialect{})` in unit tests
- Boolean columns: use Go `bool` (works for both SQLite INTEGER and PostgreSQL BOOLEAN)
- Timestamps: `time.RFC3339` format consistently
- Error handling: return `(result, error)`, log errors with `slog`

### Frontend
- TypeScript, Next.js App Router, AssistantUI for chat, shadcn/ui components, next-intl for i18n, SWR for data fetching
- Linting: TypeScript strict mode
- Type-check: `cd web && bun tsc --noEmit`

### General
- Follow existing patterns in the codebase — check how similar features are implemented before writing new code.
- Never hardcode secrets or credentials. Secrets go in `.env`, non-secret config in `config.toml`, LLM endpoint routing in `config_llm.toml`.
- When modifying any logic, proactively search the codebase for similar patterns that should receive the same change. Do not wait to be asked — audit related tools, prompts, handlers, and components for consistency. If a fix applies to image extraction, check if PDF and office extraction need the same fix. If a UI behavior changes in one picker, check all pickers.

## Testing

- When code is added or modified, write or update test cases covering the changes.
- Run the relevant tests to verify they pass before committing.
- Python backend tests: `tests/` directory, run with `pytest`.
- Go backend tests: `cd gobackend && go test ./... -count=1 -p 1 -timeout 120s`
  - Unit tests use in-memory SQLite — fast, no dependencies.
  - **Integration tests: `gobackend/tests/integration/`** — full HTTP handler chain with real DB.
  - **Every new Go API endpoint must have an integration test.** No exceptions.
  - When adding a new feature to the Go backend, add integration tests that cover: happy path, auth check, error cases, and cross-user isolation.
  - **Contract tests: `gobackend/tests/contract/`** — compares Python vs Go response shapes. **Partially deprecated** — useful for one-time verification but not required routinely. Integration tests are the primary verification mechanism.
  - **Live LLM tests**: run by default in local development, skipped in CI (`CI=1`). Tests chat, streaming, tool calling across all configured backends.
  - **Live search tests**: gated by env vars (`TAVILY_API_KEY`, `SERPER_API_KEY`, etc.).
  - **PostgreSQL tests**: gated by `TEST_DATABASE_URL`. Smoke tests the full PG path.
- Frontend type-checking: `cd web && bun tsc --noEmit`.
- Test both happy paths and error/edge cases (invalid input, missing data, unauthorized access).
- Aim for maximum test coverage: every public function, every branch, every error path. If a function has 3 code paths, write at least 3 tests. Do not skip edge cases or treat them as "obvious".

## Documentation

When code changes affect behavior, APIs, architecture, or configuration, update the relevant documentation in `docs/` and any affected `README.md` files (root, `platform/`, `web/`, etc.) to stay in sync. This includes but is not limited to: new endpoints, changed request/response schemas, new features, modified environment variables, and updated setup steps.

## Git

- **Never commit directly to `main`.** Always create a feature branch and PR via `gh pr create`.
- Before merging any PR, check CI status with `gh pr checks <number>`. If any checks fail, fix the errors and push before merging. Never merge a PR with failing checks.
- Do not push to remote unless explicitly asked.
- Write clear, descriptive commit messages — lead with what changed and why, not how.
- Do not commit `.env`, credentials, or large binary files.
- Do not commit every small change individually. Batch related small fixes into a single meaningful commit. Only commit when a logical unit of work is complete.

## Plans

For substantial code changes — new features, re-architecting, multi-file refactors, new integrations, etc. — always enter plan mode first and write a detailed plan before any implementation. Get user approval on the plan before proceeding.

**Execution plans** (phased implementation with file lists and verification steps) go in `docs/plans/`. Numbered sequentially (000, 001, ..., 100, 101, ...). Sub-documents use letter suffixes (106a, 106b).

**Design specs** go in `docs/specs/` — but only for large, novel, or cross-cutting designs that need a standalone reference document. When brainstorming produces a concrete design (components, file lists, phases decided), skip the spec and go straight to an execution plan in `docs/plans/`.

Before writing a new plan, review existing plans in `docs/plans/` to ensure consistency. Check for: reusable patterns and utilities already established, architectural decisions that must be respected, and existing implementations that the new work should build on rather than duplicate. Avoid introducing duplicate code — reuse existing implementations and keep logic in a single source of truth.

## Development Workflow

Follow `docs/development-workflow.md` exactly for every plan (Steps 1–6: Design → Plan → Build → Verify → Review → Ship). Do not skip steps or batch them. Key points:
- **Design before plan** — explore the problem, brainstorm approaches, align with user
- **Plan before code** — write and commit plan file before any implementation
- **Build phase by phase** — implement, test, self-review, document, commit each phase separately
- **Verify before review** — full test suite, cross-phase consistency check
- **Review before ship** — code review findings in plan file, all [OPEN] items resolved
- **Ship cleanly** — post-execution report, update decisions.md + TODO.md, then PR

## Code Review

Follow `docs/code-review.md` for the review process. Key points:
- Reviews go in the plan file's `## Code Review` section
- Reviewers: append findings with `[OPEN]` status and file:line references
- Authors: respond inline with `→ Response:` and `[FIXED]`/`[WONTFIX]`

## Multi-Agent Coordination

Multiple Claude Code sessions share the same local repository. Only one agent should work at a time. To avoid lost work:

- **Always commit before ending a session.** Even partial work — use a `WIP:` prefix. Uncommitted changes are invisible to the next session and will be lost.
- **Check `git status` at session start.** Look for untracked or modified files left by a previous session. Ask the user before discarding them.
- **Each plan uses a feature branch.** Pull before starting work, push before ending. Never leave unpushed commits.
- **Never assume prior session completed its work.** Verify by reading the plan file's post-execution report and checking git log — don't trust claims in conversation summaries alone.
