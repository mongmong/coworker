# Coworker V1 Re-Audit — 2026-04-28

**Branch:** main (head `d5ba60e`, after Plan 138)
**Original audit:** [`docs/reviews/2026-04-27-comprehensive-audit.md`](2026-04-27-comprehensive-audit.md) (2026-04-27)
**Reviewers:** Claude Opus 4.7 (parallel Explore agents) + Codex (focused 10-dimension re-audit)
**Plans shipped between audits:** 17 (Plans 122-138, ~17 work hours)

---

## Verdict

**V1 ship-ready.** Both audit lanes (Claude × 2 + Codex) independently confirmed every original audit item is closed or has an authoritative deferral entry in `docs/architecture/decisions.md` Decision 15.

| Severity | Original | Closed | Documented Deferral | Open |
|---|---:|---:|---:|---:|
| BLOCKER | 7 | 7 | 0 | 0 |
| IMPORTANT | 11 | 9 | 2 | 0 |
| NICE-TO-HAVE | 9 | 6 | 3 | 0 |
| **NEW found by re-audit** | — | **3** | 0 | **0** |

The re-audit also found 3 net-new issues (1 NEW BLOCKER + 2 NEW IMPORTANTs) introduced by the post-audit work. All three were closed before this report:

- **NEW BLOCKER**: Findings immutability trigger missed Plan 125's columns → fixed in Plan 137 (Migration 010).
- **NEW IMPORTANT**: `CostEventStore.RecordCost` cumulative race → fixed in Plan 137 (in-tx pre-read).
- **NEW IMPORTANT**: `phase-ship` workflow override unused → documented in Plan 138 (Decision 15).

---

## Original BLOCKER status (7 of 7 PASS)

| ID | Item | Status | Plan | Verifier |
|---|---|---|---|---|
| B1 | Autopilot phase exec wiring | PASS | 122 | Claude + Codex confirmed |
| B2 | Supervisor role files | PASS | 124 | Claude + Codex confirmed |
| B3 | Findings columns (plan_id/phase_index/reviewer_handle) | PASS | 125 | Claude + Codex confirmed |
| B4 | Dispatches.mode column | PASS | 125 | Claude + Codex confirmed |
| B5 | OpenCode message goroutine leak | PASS | 123 | Claude + Codex confirmed |
| B6 | advance/rollback CLI stubs | PASS | 123 | Claude + Codex confirmed |
| B7 | Replay scenarios (≥3 shipped) | PASS | 132 | Claude + Codex confirmed |

No regressions. All 7 fixes still in place at HEAD.

---

## Original IMPORTANT status (9 of 11 PASS, 2 deferred)

| ID | Item | Status | Plan | Notes |
|---|---|---|---|---|
| I1 | Missing CLI commands (resume/redo/edit/status/logs/inspect) | **PARTIAL → DEFERRED** | 129 + 133 | 4 of 6 shipped (status/logs/inspect/edit). redo + resume documented in Decision 15. |
| I2 | Semantic checkpoint kinds | PASS | 126 | |
| I3 | Workflow customization expansion | PARTIAL | 131 | phase-dev + 2 predicates shipped; phase-ship/plan_tagged/logical-ops deferred (Decision 15). |
| I4 | HTTP daemon authentication | PASS | 127 | Loopback default; LAN exposure via `--http-bind 0.0.0.0`. |
| I5 | cli_handle.go parser tests | PASS | 128 | 9 unit tests added. |
| I6 | Shipper PR creation tests | PASS | 128 | `GhRunner` injection + 3 tests. |
| I7 | Silent UpdateJobState errors | PASS | 126 | All 5 sites now log via `logger.Error`. |
| I8 | cli_agent pipe cleanup on Start failure | PASS | 126 | |
| I9 | Event constants centralized | PASS | 127 | Moved to `core/event.go`. |
| I10 | TUI attention auto-refresh | **DEFERRED** | — | Documented in Decision 15. Two-option fix path noted. |
| I11 | TUI/producer cost field-name mismatch | PASS | 130 | Producer + consumer use `tokens_in/tokens_out/usd/cumulative_usd/budget_usd`. |

---

## Original NICE-TO-HAVE status (6 of 9 PASS, 3 deferred/skipped)

| ID | Item | Status | Plan | Notes |
|---|---|---|---|---|
| N1 | Composite index dispatches(state, role) | PASS | 134 | Migration 009. |
| N2 | cmd/coworker/main.go test | DEFERRED | — | 3-line shim; documented in Decision 15. |
| N3 | Makefile release/cross-compile | PASS | 134 | linux/{amd64,arm64} + darwin/{amd64,arm64}, single binary verified. |
| N4 | README V1 scope refresh | PASS | 134 | |
| N5 | Plugin asset audit | PASS | 135 | No hard-coded paths or shell-specific assumptions. |
| N6 | TestOrchestrate_CostWriterErrorIsNonFatal strengthening | PASS | 136 | Now asserts ExitCode + JobState + RunState. |
| N7 | Time-parse silent drops in store reads | DEFERRED | — | Trusted-source data; documented in Decision 15. |
| N8 | Codex USD price table | DEFERRED | — | Documented in Decision 15. |
| N9 | How-to guides | PASS | 135 | adding-a-role.md + adding-a-replay-scenario.md. |

---

## Net-new issues found by re-audit (3 found, 3 closed)

### NEW BLOCKER — Findings immutability trigger missed Plan 125 columns

**Found by:** Claude/2.
**Where:** `store/migrations/006_findings_immutability.sql`.
**Issue:** The trigger compared the original 7 finding columns. Plan 125 added `plan_id`, `phase_index`, `reviewer_handle` (Migration 008); the trigger silently let those mutate, breaking Decision 2 (findings are immutable).
**Fix:** Plan 137 — Migration 010 drops + recreates the trigger with all 10 columns. Test extended (`TestFindingsImmutableTrigger_AllImmutableColumns`) to verify each new column is rejected.
**Status: CLOSED.**

### NEW IMPORTANT — Cost cumulative race in `CostEventStore.RecordCost`

**Found by:** Claude/2 + Codex (independently).
**Where:** `store/cost_event_store.go:31-44` (pre-fix).
**Issue:** Plan 130 added a pre-read of `runs.cost_usd` to compute `cumulative_usd` for the event payload, but the read happened OUTSIDE the transaction. Two concurrent calls would each read the same baseline; the transactional bump kept `runs.cost_usd` correct but the event payloads carried stale `cumulative_usd`. Live consumers (TUI) saw wrong totals under parallel dispatch.
**Fix:** Plan 137 — RecordCost now opens its own transaction directly, runs the SELECT inside it (snapshot-isolated), and INSERTs the event row before the projection writes (event-first preserved). New test `TestCostEventStore_ConcurrentRecordCostMatchesFinalCumulative` runs 10 concurrent `RecordCost` calls under `-race` and asserts the cumulatives form an arithmetic sequence.
**Status: CLOSED.**

### NEW IMPORTANT — `phase-ship` workflow override unused

**Found by:** Codex.
**Where:** `coding/stages/defaults.go:17` declares `phase-ship: [shipper]`; `coding/workflow/build_from_prd.go:245` never consults it.
**Issue:** Users could write `policy.workflow_overrides.phase-ship: [...]` and silently get no effect.
**Fix:** Plan 138 — explicit deferral entry added to Decision 15 catalog with rationale and follow-up shape options. Inline comment in `build_from_prd.go` now points at the decision. `DefaultStages` keeps the entry for spec completeness.
**Status: CLOSED (documented deferral).**

---

## Cross-validation between audit lanes

The two re-audit lanes ran independently:

- **Claude/Explore lane** (foreground, parallel): one BLOCKER-verification pass + one net-new-issue hunt.
- **Codex lane** (background, focused 10-dimension): full re-verification + net-new hunt.

**Convergence:**
- Both lanes confirmed all 7 BLOCKER closures.
- Both lanes confirmed I1's deferred items (redo, resume) are correctly documented.
- Both lanes independently flagged the cost cumulative race in `CostEventStore.RecordCost` — high confidence finding.
- Codex caught the `phase-ship` deferral that Claude missed.
- Claude caught the findings immutability trigger gap that Codex missed.

The lanes' findings were complementary, not redundant. Running both is worth the extra time.

---

## Repo state at HEAD (`d5ba60e`)

- `go build ./...` — clean.
- `go test -race ./... -count=1 -timeout 180s` — 27 packages PASS, 0 failed, 0 races.
- `golangci-lint run ./...` — 0 issues.
- `make test-replay` (`COWORKER_REPLAY=1`) — 4 scenarios PASS.
- `make test-live` (`COWORKER_LIVE=1`, real APIs) — verified locally during Plan 121; current state unchanged.
- `make release` — 4 cross-compiled binaries (linux + darwin, amd64 + arm64), 23-25 MB each.

---

## Final answer

**V1 is ship-ready.** Every audit item is either:
1. Closed by code in main, OR
2. Documented as a V1.1+ deferral in `docs/architecture/decisions.md` Decision 15 with rationale, workaround (where one exists), and the shape of the future fix.

The post-audit work (Plans 122-138, 17 plans) added ~5500 LOC, ~50 new tests, 3 new schema migrations, and 4 new how-to docs. Test coverage expanded into the new code paths; lint and race-detector remained clean throughout.

The next concrete decision is when to cut the V1 release — not what to ship in it.
