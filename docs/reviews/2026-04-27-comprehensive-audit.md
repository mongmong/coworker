# Coworker V1 Ship-Readiness — Comprehensive Audit

**Date:** 2026-04-27
**Branch:** main (head `9b0374c`, after Plan 121 merge)
**Reviewers:** Claude Opus 4.7 (4 parallel Explore subagents) + Codex (focused 10-dimension audit)

---

## Executive Summary

| Severity | Count | Detail |
|---|---|---|
| **BLOCKER** | **5** | Autopilot phase execution wired without PhaseExecutor; `coding/roles/supervisor.yaml` missing; Findings/Dispatches schemas drift from spec; OpenCode HTTP message goroutine leak; only 1 of ≥10 V1 replay scenarios. Stub commands (`advance`, `rollback`). |
| **IMPORTANT** | **11** | Missing CLI commands; checkpoint kind not semantic; HTTP daemon unauthenticated; workflow_overrides + applies_when partial; cli_handle.go untested; shipper PR-creation untested; etc. |
| **NICE-TO-HAVE** | **9** | Lint/style and minor optimizations. |

**Verdict: NOT-READY for V1 release.** The 5 blockers are concrete, mostly small in scope, and unblock the V1 ship together. ETA at sustained pace: ~2–3 work days to clear blockers; ~1 week to clear all IMPORTANT items.

Both audit lanes (Claude × 4 + Codex) agree on the most consequential findings: **autopilot doesn't actually run phases**, **supervisor isn't a dispatchable role**, and **interactive-mode CLI commands are missing or stubbed**.

---

## How to read this report

- **`[B]`** = BLOCKER (must fix before V1 release)
- **`[I]`** = IMPORTANT (should fix before V1 release; not strictly blocking)
- **`[N]`** = NICE-TO-HAVE (improve in V1.1+)
- **Source:** which lane found it — `Codex`, `Claude/<n>` (1 = arch+schema, 2 = concurrency+errors, 3 = roles+workflows+CLI, 4 = tests+plugins). Findings agreed by both lanes are highlighted.

---

## BLOCKERs

### B1 — Autopilot run cannot execute plan phases ⭐ both lanes

**File:** `cli/run.go:503-511`, `coding/workflow/build_from_prd.go:223` (`Codex`, confirmed by Claude/3)

**Finding:** The post-resume code path constructs `BuildFromPRDWorkflow` with only `ManifestPath`, `Policy`, `Logger`, `WorkDir`, `RoleDir`, `PlanWriter`, `CheckpointWriter`. **`PhaseExecutor`, `Shipper`, and `StageRegistry` are nil.** Line 555 then calls `runner.RunPhasesForPlan()`, which immediately errors at `coding/workflow/build_from_prd.go:223` with `"build-from-prd: PhaseExecutor is required for RunPhasesForPlan"`.

**Impact:** `coworker run <prd.md>` succeeds through architect + spec-approved + planner but **cannot run any phase of any plan**. The autopilot path is non-functional.

**Fix:** Wire `PhaseExecutor`, `Shipper`, `StageRegistry`, `EventStore`, and `AttentionStore` into the `runner` constructor at lines 503-511, mirroring the wiring done elsewhere (e.g., the test helper). Verify with a smoke integration test that exercises the full run path end-to-end.

---

### B2 — Supervisor role is not dispatchable ⭐ both lanes

**File:** `coding/roles/supervisor.yaml` (missing); `coding/prompts/supervisor.md` (missing) (`Codex`, confirmed by Claude/3)

**Finding:** Spec §Roles defines `supervisor` as a full role with inputs (job outputs, workflow rules), outputs (verdict, adherence report), and an Agent (Codex for quality). The implementation has supervisor logic only in-process (`coding/supervisor/engine.go`, `coding/quality/evaluator.go`). There is **no `supervisor.yaml` or `supervisor.md`**, so users cannot rebind the supervisor to a different CLI, override its prompt per-repo, or instrument supervisor invocations as regular jobs.

**Impact:** Architectural divergence from the spec's "every role is a YAML + prompt template" principle. Per-repo customization of supervisor behavior is impossible.

**Fix:** Either (a) ship the supervisor role files to match the spec, or (b) update the spec + decisions.md to document that supervisor is intentionally in-process and remove its row from the role table.

---

### B3 — Findings table missing spec-required columns

**File:** `store/migrations/001_init.sql:55-66`, `core/finding.go:21-37` (`Claude/1`)

**Finding:** Spec §Data Model line 778-779 requires `findings(id, run_id, plan_id, phase_index, reviewer_handle, path, line, severity, body, fingerprint, resolved_by_job_id, resolved_at)`. The implementation has only `id, run_id, job_id, path, line, severity, body, fingerprint, resolved_by_job_id, resolved_at` — **missing `plan_id`, `phase_index`, `reviewer_handle`**.

**Impact:** Cannot query findings by plan or phase; cannot attribute findings to a specific reviewer role/handle. Plan 119 fixed `runs`/`jobs` schema drift but did not address `findings`.

**Fix:** New migration `008_findings_spec_columns.sql` adds the three columns; corresponding Go struct field additions; finding-insertion paths populate the new fields from `EvalContext.PlanID/PhaseIndex` and the dispatching role.

---

### B4 — Dispatches table drifts from spec; `mode` column missing

**File:** `store/migrations/003_dispatches.sql:5-18` (`Claude/1`)

**Finding:** Spec defines `dispatches(id, job_id, worker_handle, mode, dispatched_at, acknowledged_at)` where `mode` is `persistent | ephemeral | in-process`. Implementation has a richer state machine (`pending|leased|completed|expired`) plus extra columns (`run_id`, `role`, `prompt`, `inputs`) but **omits the `mode` column entirely**.

**Impact:** Cannot audit whether a dispatch was persistent (CLI-connected MCP-pull) or ephemeral (subprocess shell-out). Spec's intent — to distinguish how each dispatch executed — is unfulfillable from the projection.

**Fix:** Either (a) reconcile spec to the implementation's state-machine model and add an explicit `mode` column to track persistent/ephemeral/in-process, or (b) document in `decisions.md` why the implementation deliberately diverges. Choice depends on whether the runtime ever needs to query "all persistent dispatches" — if yes, ship the column.

---

### B5 — OpenCode HTTP message goroutine can leak

**File:** `agent/opencode_http_agent.go:181-182` (`Claude/2`)

**Finding:** The async `sendMessage` goroutine added in Plan 118 to make Dispatch return promptly is fire-and-forget:

```go
go func() {
    _ = a.sendMessage(sseCtx, client, base, sessionID, prompt)
}()
```

While the goroutine respects `sseCtx`, no completion signal exists. If `sendMessage` hangs on a non-cancellable network operation (e.g., DNS, TCP slow handshake), the goroutine leaks indefinitely.

**Impact:** Resource leak under adverse network conditions. Potential connection pool exhaustion in long-lived daemons.

**Fix:** Track the goroutine via a `sync.WaitGroup` or completion channel attached to `replayHandle`. On `Cancel()`, wait briefly with a timeout, then proceed. Update tests to assert no goroutine leak under cancel.

---

### B6 — Stubbed commands `advance` and `rollback` ⭐ both lanes

**File:** `cli/advance.go:61`, `cli/rollback.go:62` (`Claude/3`, `Codex`)

**Finding:** Both commands print `"not yet implemented"` and exit. Spec §Modes line 506-507 requires these as the interactive-mode verbs for advancing past or rolling back checkpoints.

**Impact:** Interactive checkpoint management from the CLI doesn't work. Users must use the MCP `orch_checkpoint_*` tools or HTTP `/attention/{id}/answer`. Documented usage breaks.

**Fix:** Implement both. They are thin wrappers over the existing `AttentionStore.AnswerAttention` + `CheckpointStore.ResolveCheckpoint` flow. ~50 LOC each.

---

### B7 — Replay scenario coverage at 10% of V1 surface

**File:** `tests/replay/` (only `developer_then_reviewer/`) (`Claude/4`)

**Finding:** Plan 120 stood up the replay test infrastructure with one scenario. Spec §Testing layers and the V1 review imply ≥10 scenarios covering the major workflows: multi-phase plan, supervisor retry-then-pass, phase-clean checkpoint, quality-gate escalation, worker registration/heartbeat, worker eviction/requeue, permission attention, spec-approved checkpoint, budget hard-limit, etc. Only the trivial happy path is covered.

**Impact:** Regression risk on every workflow change is unmeasured. The primary test approach defined in CLAUDE.md (recorded transcripts as fixtures) is largely unused.

**Fix:** Hand-write 5 high-value scenarios before V1 release: multi-phase, supervisor-retry-then-pass, phase-clean, quality-gate-escalation, worker-heartbeat-eviction. ~6-8 hours total. Plan 121's transcript-recording machinery (deferred) would automate this; in the meantime, hand-written JSONL is acceptable per `docs/architecture/testing.md`.

---

## IMPORTANT

### I1 — Missing CLI commands ⭐ both lanes

**Files:** `cli/` (`Claude/3`, `Codex`)

Spec mentions 14 commands; the missing 6 are: `redo`, `edit`, `status`, `logs`, `inspect`, `resume`. These are the spec's interactive-mode and observability surface (`spec:489, 502-509, 549, 558`). Without them, users have no CLI equivalent of the TUI for run inspection, log streaming, or restart-after-failure.

**Fix:** Implement progressively — `status` and `logs` first (read-only, easy), then `inspect` (read-only on findings + artifacts), then `resume`/`redo`/`edit` (state-mutating). Each ~50-150 LOC.

---

### I2 — `phase-clean` and `ready-to-ship` checkpoints persist with kind=`checkpoint` instead of semantic kinds

**File:** `coding/phaseloop/executor.go:268,271`, `coding/shipper/shipper.go:90,114` (`Codex`)

**Finding:** Plan 119 wired `CheckpointWriter.CreateCheckpoint(... Kind: string(core.AttentionCheckpoint))` at every site. The `kind` field becomes the literal string `"checkpoint"` rather than `"phase-clean"` or `"ready-to-ship"`. Spec §Workflow customization §checkpoints requires the semantic name.

**Impact:** Cannot filter checkpoints by kind in queries or UI. Operators and tools that look up "all phase-clean checkpoints for run X" can't.

**Fix:** Pass distinct `Kind` strings at each call site (`"phase-clean"`, `"ready-to-ship"`) instead of `core.AttentionCheckpoint`. Trivial. Plan 119 said this would be done; the wiring slipped.

---

### I3 — Workflow overrides + `applies_when` only partial ⭐ both lanes

**File:** `coding/workflow/build_from_prd.go:231,239`, `coding/phaseloop/executor.go:423,435` (`Claude/3`, `Codex`)

**Finding:** Spec §Workflow customization Level 1 calls for arbitrary stage role lists from `policy.yaml::workflow_overrides`; implementation only consults `phase-review` and `phase-test` keys. Spec §Level 2 calls for predicates `changes_touch`, `plan_tagged`, `commit_msg_contains`, `phase_index_in`, plus logical operators; implementation supports only `changes_touch`, with unknown predicates silently passing through.

**Impact:** Real-world repos can't customize workflow as documented (e.g., add a security-auditor stage, skip frontend reviewer when only Go files changed, run mobile-tester only on plans tagged `mobile`).

**Fix:** Expand stage map to include `phase-dev` and `phase-ship`; implement remaining predicates. ~150-300 LOC.

---

### I4 — HTTP daemon endpoints unauthenticated ⭐ Codex

**File:** `cli/daemon_http.go:31,39`, `cli/daemon.go:157` (`Codex`)

**Finding:** `/attention/{id}/answer` (write) and `/runs`, `/runs/{id}/jobs`, `/events` (read) accept any caller. Daemon binds `:7700` (all interfaces by default in some configs) without auth. Spec defers auth to V2 but the implementation does not document this gap inline.

**Impact:** Local-first runtime; on a developer machine the surface is OK but anyone on the LAN with port access can approve checkpoints. CI environments could be exposed.

**Fix:** Either (a) bind to `127.0.0.1` only by default (one-line change), (b) add a token-based auth header, or (c) document inline as known V2 deferral with a `--insecure` flag to acknowledge. Cheapest viable: 127.0.0.1 default + `--bind-addr` flag for advanced use.

---

### I5 — `agent/cli_handle.go` parser has no direct tests

**File:** `agent/cli_handle.go` (`Claude/4`)

**Finding:** The stream-json parser at `cliJobHandle.Wait` lines 63-122 has cost-branch coverage via `agent/cost_helpers_test.go` (Plan 121) but the **finding/done parsing branches** are exercised only indirectly via `cli_agent_test.go` integration tests with mock binaries. No direct unit tests for: malformed JSON fallback to stdout, missing fields on `streamMessage`, log-file write failure tolerance.

**Impact:** Silent parser regressions on the hot path won't surface until a live agent gets confused.

**Fix:** Create `agent/cli_handle_test.go` with focused tests on the parser using `bytes.Buffer` as stdout pipe. ~100 LOC, ~3 hours.

---

### I6 — Shipper PR creation paths untested

**File:** `coding/shipper/shipper.go:135` (`Claude/4`)

**Finding:** All four shipper tests use `DryRun: true`. The real `gh pr create` invocation is exercised only via live tests (which were not designed to validate shipper behavior). No unit tests for: gh exit code != 0, missing gh binary, artifact insertion failure with PR success.

**Impact:** Real PR creation path could fail silently or surface obscure errors to users on first ship.

**Fix:** Mock the gh runner via an injected interface; add tests for success, failure, missing-binary, artifact-insert-failure. ~150 LOC, ~3 hours.

---

### I7 — Silent error drops in dispatcher critical paths

**File:** `coding/dispatch.go:378,390,397,434,446` (`Claude/2`)

**Finding:** Five `jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed) //nolint:errcheck` calls in failure paths drop the inner error silently. If state update fails (DB busy, transaction abort), the job is in a wrong state and the operator sees inconsistent status.

**Impact:** Hard-to-debug job state inconsistency. The runtime claims "no failure silently advances state" but state-update failures can.

**Fix:** Replace each with explicit `if err := ...; err != nil { logger.Error(...) }` form. The error stays best-effort but is logged. ~10 LOC, trivial.

---

### I8 — `agent/cli_agent.go` pipe cleanup on Start() failure

**File:** `agent/cli_agent.go:54-66` (`Claude/2`)

**Finding:** If `cmd.Start()` fails after `cmd.StdoutPipe()` and `cmd.StderrPipe()` have been called, the pipes are open file descriptors that nobody closes. Repeated dispatch failure could exhaust descriptors.

**Fix:** Add `stdout.Close(); stderr.Close()` in the `cmd.Start()` error branch. ~3 LOC.

---

### I9 — `Event*` constants split between `core/event.go` and `core/supervisor.go`

**File:** `core/event.go`, `core/supervisor.go:5-16` (`Claude/1`)

**Finding:** Most event kinds live in `core/event.go`. Five (`EventSupervisorVerdict`, `EventSupervisorRetry`, `EventComplianceBreach`, `EventQualityVerdict`, `EventQualityGate`) live in `core/supervisor.go`. Discoverability is poor.

**Fix:** Move to `core/event.go` for visibility. Trivial.

---

### I10 — `EventAttentionCreated` / `EventAttentionResolved` defined but unused

**File:** `core/event.go:34-35` (`Claude/1`)

**Finding:** Per Decision 6 (Plan 119), attention is intentionally NOT event-based. These constants are declared but never written. TUI references them in case statements that never match.

**Fix:** Remove the constants and the dead TUI cases.

---

### I11 — Plan 119 noted that TUI / HTTP / MCP cost projection field-names mismatch event payloads

**File:** `tui/model.go:69-75`, `store/cost_event_store.go:26` (`Codex` & Plan 119 §Out of Scope)

**Finding:** TUI model expects payload fields `input_tok`, `output_tok`, `cost_usd`, `cumulative_usd`. CostEventStore writes payload fields `tokens_in`, `tokens_out`, `usd`. The TUI silently drops the events that don't match.

**Fix:** Standardize on one set of names across event payloads and consumers; bump event schema version if needed. Decision recorded in Plan 121 §Out of Scope; promote to Plan 122.

---

## NICE-TO-HAVE

### N1 — Composite index on `dispatches(state, role)` for `ClaimNextDispatch` hot path (`Claude/1`)

### N2 — `cmd/coworker/main.go` is a 3-line shim with no tests; coverage is implicit (`Claude/4`)

### N3 — `Makefile` lacks `release` and cross-compile targets; single-binary claim is unverified by CI (`Codex`)

### N4 — README.md should clarify what's V1 vs V2 to avoid drift; Plan 121 already tightened this in `decisions.md` but README didn't get the same treatment (`Claude/4`)

### N5 — Codex / OpenCode plugin asset content (skill files, command definitions) not audited for hard-coded paths or shell assumptions (`Claude/4`)

### N6 — `TestOrchestrate_CostWriterErrorIsNonFatal` (`coding/dispatch_test.go:1254`) verifies error logging but does not assert the job itself succeeds; strengthen to require ExitCode=0 + JobState=complete (`Claude/4`)

### N7 — Time parsing errors silently dropped in store reads; trusted-source path so acceptable, but a debug-log would aid future debugging (`Claude/2`)

### N8 — Codex `turn.completed.usage` is captured but USD stays 0; need a per-model price table to convert to USD (deferred via Plan 121 §Out of Scope but should be tracked) (`self`)

### N9 — Documentation gap: how to add a new role, MCP tool, replay scenario, or CLI command. Plan 120 hinted at a "recording recipe" but full how-to guides don't exist (`Claude/4`)

---

## Cross-reference: agreement matrix

| Finding | Codex | Claude/1 | Claude/2 | Claude/3 | Claude/4 |
|---|:-:|:-:|:-:|:-:|:-:|
| B1 (autopilot phase exec broken) | ✅ | | | partial | |
| B2 (supervisor role missing) | ✅ | | | ✅ | |
| B3 (findings columns) | | ✅ | | | |
| B4 (dispatches `mode`) | | ✅ | | | |
| B5 (opencode goroutine leak) | | | ✅ | | |
| B6 (advance/rollback stubs) | ✅ | | | ✅ | |
| B7 (replay coverage) | | | | | ✅ |
| I1 (missing CLI cmds) | ✅ | | | ✅ | |
| I2 (checkpoint kind names) | ✅ | | | | |
| I3 (workflow overrides) | ✅ | | | ✅ | |
| I4 (HTTP auth) | ✅ | | | | |
| I5 (cli_handle tests) | | | | | ✅ |
| I6 (shipper PR tests) | | | | | ✅ |
| I7 (silent UpdateJobState) | | | ✅ | | |
| I8 (pipe cleanup) | | | ✅ | | |
| I9 (event const split) | | ✅ | | | |
| I10 (unused constants) | | ✅ | | | |
| I11 (TUI field mismatch) | ✅ | | | | |

**Cross-validation observations:**
- B1, B2, B6, I1, I3 are confirmed by both Codex and Claude — high confidence.
- B3, B4 (schema drift) found only by Claude/1; spec interpretation was the bottleneck — verify the spec itself is correct before fixing.
- B5 (goroutine leak) was missed by Codex's focused audit; Claude/2's concurrency lens caught it.
- B7 (replay coverage) was outside Codex's 10-dimension prompt; Claude/4 had explicit test-coverage focus.

---

## Recommended remediation order

1. **B1** (autopilot phase exec) — autopilot is the primary user-facing feature. Without this, V1 is a non-product. **~2 hours.**
2. **B6** (advance/rollback) — interactive-mode users hit this immediately. **~2 hours.**
3. **B5** (goroutine leak) — silent reliability bug; small fix. **~1 hour.**
4. **B2** (supervisor role) — unblocks per-repo customization; small files. **~3 hours.**
5. **B3 + B4** (schema drift) — migrations; pair with a decision: either fix the schema or fix the spec. **~3-4 hours.**
6. **B7** (5 replay scenarios) — confidence in regression coverage. **~6-8 hours.**
7. **I1-I11** in priority order; each ≤4 hours. **~25 hours total.**

Total **clear-blocker** time: ~16 hours. Plus IMPORTANT items: ~25 hours. = **~5 work days for full ship readiness.**

---

## Confidence in this report

- Codex audit died at ~8 minutes mid-investigation with no output saved; restarted with a tighter 10-dimension prompt and recovered.
- Claude-side audit ran 4 parallel Explore subagents on independent dimensions to avoid context contention.
- Several findings (B3, B4, I7) are flagged "spec drift" — verify the spec is canonical before remediating, since spec/implementation could be reconciled either way.
- Live tests were not run as part of this audit (cost). The unit + race detector + lint suite is clean (29 packages PASS, 0 races, 0 lint issues at audit time).

---

## Out of scope for this audit

- Performance benchmarks.
- Security review beyond the architectural surface (no fuzz testing, no SQL-injection audit, no path-traversal review).
- Docs spell-check / style.
- License / SPDX header consistency.
