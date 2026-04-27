# Plan 124 — B2: Supervisor Role Files

> Implemented inline. Small scope: two new files + decisions.md update.

**Goal:** Close BLOCKER B2 from the 2026-04-27 audit. The spec §Roles defines `supervisor` as one of the canonical roles, but `coding/roles/supervisor.yaml` and `coding/prompts/supervisor.md` do not exist. The implementation runs supervisor in-process (`coding/supervisor/engine.go` for contract rules, `coding/quality/evaluator.go` for LLM-judge quality rules). Both audit lanes flagged the missing role files as a divergence from the spec's "every role is a YAML + prompt template" principle.

**Architecture:**
- Ship `coding/roles/supervisor.yaml` and `coding/prompts/supervisor.md` as **documentation + dispatchable fallback**. The default V1 path is unchanged: `coding/supervisor/engine.go` and `coding/quality/evaluator.go` run in-process. The new files document the contract and let users replace the in-process supervisor with an LLM-backed one if they choose (e.g., a future plan that wires `Dispatcher.Orchestrate(role: "supervisor", ...)` for advanced workflows).
- Update `cli/init.go` so `coworker init` copies the new role + prompt files to `.coworker/roles/supervisor.yaml` and `.coworker/prompts/supervisor.md`. Today the init script copies the rules but not the role file.
- Update `decisions.md` (Decision 12) to document the dual implementation: in-process by default; YAML+prompt available for override.

**Tech Stack:** No new code. Two new asset files + minor `cli/init.go` change.

**Reference:** `docs/specs/000-coworker-runtime-design.md` line 199-208 (role catalog); `docs/reviews/2026-04-27-comprehensive-audit.md` §B2.

---

## Required-API audit

| Surface | Reality |
| --- | --- |
| `coding/roles/*.yaml` | All seven existing roles use the format documented in spec line 215-247. Should match. |
| `cli/init.go::copyInitAssets` | Copies all `*.yaml` from `coding/roles/` to `.coworker/roles/`. So adding a new yaml file there will be picked up automatically — **no `init.go` change needed** unless the yaml needs special handling. |
| In-process supervisor | `coding/supervisor/engine.go` reads `coding/supervisor/rules.yaml` (and the user's `.coworker/rules/supervisor-contract.yaml`). The new role yaml does NOT replace this; it is forward-looking documentation. |

---

## Scope

In scope:

1. `coding/roles/supervisor.yaml` — full role definition matching the spec's canonical catalog.
2. `coding/prompts/supervisor.md` — prompt template that, if dispatched as an LLM-backed supervisor, asks the model to evaluate a job result against contract rules and emit a verdict.
3. `decisions.md` Decision 12 — documents the dual-implementation rationale.
4. Verify `cli/init.go` already copies new role yamls (no change needed if so).

Out of scope:

- Wiring supervisor as a dispatchable role in production. Today the in-process supervisor is invoked from `coding/dispatch.go::executeAttempt` directly; that stays.
- Refactoring the rule engine.
- New tests for the supervisor role files (the existing role-loader tests cover yaml parsing for any role; the file just needs to be valid).

---

## File Structure

**Create:**
- `coding/roles/supervisor.yaml`
- `coding/prompts/supervisor.md`

**Modify:**
- `docs/architecture/decisions.md` — append Decision 12.

---

## Phase 1 — Role yaml + prompt template

**Files:**
- Create: `coding/roles/supervisor.yaml`, `coding/prompts/supervisor.md`

- [ ] **Step 1 — `coding/roles/supervisor.yaml`:**

```yaml
name: supervisor
concurrency: single
cli: codex
prompt_template: prompts/supervisor.md
inputs:
  required:
    - job_id
    - job_outputs_path
    - rules_path
outputs:
  contract:
    verdict_emitted: true
    findings_have_severity: true
  emits:
    verdict: string  # "pass" | "retry" | "escalate"
    rules_evaluated: []
    failed_rules: []
    notes: string
sandbox: read-only
permissions:
  allowed_tools:
    - read
    - grep
    - glob
    - "bash:codex"
  never:
    - write
    - edit
    - "bash:rm"
  requires_human: []
budget:
  max_tokens_per_job: 50000
  max_wallclock_minutes: 5
  max_cost_usd: 1.00
retry_policy:
  on_contract_fail: skip
  on_job_error: retry_once
```

- [ ] **Step 2 — `coding/prompts/supervisor.md`:**

```markdown
# Supervisor Role

You are the **supervisor** in a coworker run. Your job is to evaluate a
completed job's outputs against the workflow contract rules, then emit
a verdict.

## Inputs

- **Job ID**: `{{ .JobId }}`
- **Job outputs**: `{{ .JobOutputsPath }}`
- **Rules**: `{{ .RulesPath }}`

## Instructions

1. Read the rules file. It contains a list of contract rules (assertions
   that must hold for the job's outputs to be acceptable) and may also
   contain quality rules (LLM-judged style/correctness expectations).

2. Read the job outputs file. It is JSON with these top-level fields:
   - `findings`: array of `{path, line, severity, body}` objects.
   - `artifacts`: array of artifact pointers.
   - `exit_code`: integer.
   - `stdout` / `stderr`: agent output text.

3. For each contract rule, evaluate whether the job's outputs satisfy
   the rule. A failed contract rule means the job must retry (or
   escalate after `max_retries`).

4. For each quality rule, render a judgment: pass / soft-fail (note
   only) / hard-fail (escalate to checkpoint). Quality judgments are
   advisory by default; only `quality-gate` checkpoints block.

5. Output a verdict JSON object:

```json
{
  "verdict": "pass",
  "rules_evaluated": ["dev_commits_on_feature_branch", ...],
  "failed_rules": [],
  "notes": "All rules passed."
}
```

## Rules

- **Verdict must be one of**: `pass`, `retry`, `escalate`.
- `retry` triggers a re-dispatch with feedback (the failed_rules list
  is included in the next attempt's prompt).
- `escalate` raises a `compliance-breach` (contract) or `quality-gate`
  (quality) checkpoint that blocks until a human resolves.
- **Do not commit code, edit files, or write to the repo.** You are
  read-only by design.
```

- [ ] **Step 3 — Verify the role loads:**

The existing role-loader tests (`coding/roles/loader_test.go`) parse every yaml in `coding/roles/`. Run them:

```bash
go test -race ./coding/roles -count=1
```

Expected: PASS, with the new file picked up automatically.

- [ ] **Step 4 — Verify `coworker init` copies the new file:**

Read `cli/init.go::copyInitAssets` to confirm it iterates `coding/roles/*.yaml` (it should — that's how the existing seven roles get copied). If yes, no init code change. If no, add the explicit copy.

- [ ] **Step 5 — Commit:**

```bash
go test -race ./... -count=1 -timeout 60s
git add coding/roles/supervisor.yaml coding/prompts/supervisor.md
git commit -m "Plan 124 Phase 1: supervisor.yaml + supervisor.md (role files)"
```

---

## Phase 2 — Decision 12

**Files:** `docs/architecture/decisions.md`

- [ ] **Step 1 — Append:**

```markdown
## Decision 12: Supervisor as Role + In-Process (Plan 124)

**Context:** The 2026-04-27 V1 audit (BLOCKER B2) flagged that `coding/roles/supervisor.yaml` and `coding/prompts/supervisor.md` did not exist, even though the spec §Roles catalog lists `supervisor` as one of the canonical roles. The implementation runs supervisor in-process (rules engine for contract rules + Codex LLM judge for quality rules); a YAML role file was never authored.

**Decision:** Ship the supervisor role files as **documentation + dispatchable fallback**. The default V1 production path is unchanged: `coding/supervisor/engine.go` evaluates contract rules in-process, `coding/quality/evaluator.go` invokes Codex via `coding/quality/judge.go` for quality rules. The new files document the supervisor's contract + provide a prompt that a future plan could wire to dispatch the supervisor as a real role (useful for advanced setups that want a different LLM-backed supervisor or per-repo customization).

**Decision:** `cli/init.go` already copies all `*.yaml` from `coding/roles/` to `.coworker/roles/`, so the new file is scaffolded into per-repo configs without code changes.

**Decision:** Wiring `Dispatcher.Orchestrate(role: "supervisor", ...)` for production use is **deferred**. The in-process implementation is faster (no LLM round-trip for contract rules) and authoritative for the V1 release.

**Status:** Introduced in Plan 124.
```

- [ ] **Step 2 — Commit + verify + merge.**

---

## Self-Review Checklist

- [ ] `coding/roles/supervisor.yaml` parses via the existing role loader.
- [ ] `coding/prompts/supervisor.md` template fields match the role's required inputs (snake_case → PascalCase: `JobId`, `JobOutputsPath`, `RulesPath`).
- [ ] `coworker init` copies both files (verified by reading init.go's copy loop, not by re-running init).
- [ ] In-process supervisor remains the production path; the role yaml does not change runtime behavior.
- [ ] Decision 12 documents the dual implementation.

---

## Code Review

### Codex pre-implementation review (2026-04-27)
All 6 questions cleared. Verdict: READY-TO-IMPLEMENT.

(Plan was small enough that no post-impl review was warranted; verified end-to-end via the role loader test suite + full `-race` run + lint.)

### Verification

```text
$ go build ./...                                  → clean
$ go test -race ./... -count=1 -timeout 180s      → 30 ok, 0 failed, 0 races
$ golangci-lint run ./...                         → 0 issues
```

---

## Post-Execution Report

### Date
2026-04-27

### Implementation summary

Two files added; one decisions entry.

- `coding/roles/supervisor.yaml` — full role definition matching the spec catalog. CLI=codex, sandbox=read-only, three required inputs (`job_id`, `job_outputs_path`, `rules_path`), declared verdict outputs.
- `coding/prompts/supervisor.md` — prompt template for LLM-backed supervisor dispatch. Documents how to evaluate contract + quality rules and emit a verdict.
- `docs/architecture/decisions.md` Decision 12 — documents the dual implementation: in-process (default, faster, V1 production) vs. dispatchable role (forward-looking, opt-in via future plan).

`cli/init.go` already copies `coding/roles/*.yaml` and `coding/prompts/*.md` automatically — no init code changes needed.

### Verification

Existing role-loader tests pass with the new file picked up automatically. Full suite + lint clean.

### Notes / deviations from plan

None. Codex pre-impl review confirmed all six concerns cleared.
