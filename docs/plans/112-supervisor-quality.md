# Plan 112 — Supervisor Quality (Advisory-First)

**Branch:** `feature/plan-112-supervisor-quality`
**Blocks on:** 111 (full role catalog)
**Parallel-safe with:** 107, 113

---

## Purpose

Add the LLM-judge quality sub-behavior of the supervisor. Contract checks (Plan 101) are deterministic and run after every job. Quality checks are LLM-evaluated and run at every checkpoint. Advisory by default; block-capable for four allowlisted categories from the spec.

---

## Background

The spec (§Supervisor) defines two sub-behaviors:
- **Contract**: deterministic rules, always blocking, after every job. (Plan 101)
- **Quality**: LLM judge (Codex ephemeral), at every checkpoint, advisory by default.

Block-capable categories (from spec):
- `missing_required_tests`
- `spec_contradiction`
- `security_sensitive_unreviewed_change`
- `shipper_report_missing`

All other quality findings are recorded in the adherence report and surfaced without blocking.

---

## Architecture

```
coding/
└── quality/
    ├── schema.go          # QualityRule, QualityRuleSet, QualityCategory, QualityVerdict, QualityFinding, QualityResult
    ├── loader.go          # LoadQualityRules from YAML file or bytes
    ├── loader_test.go
    ├── judge.go           # QualityJudge interface, CLIQualityJudge (shells to codex), MockQualityJudge
    ├── judge_test.go
    ├── evaluator.go       # QualityEvaluator — runs all rules, routes advisory vs block, creates attention items
    ├── evaluator_test.go
    └── rules.yaml         # Default quality rules (YAML embedded via os.ReadFile path)

core/
└── supervisor.go          # ADD EventQualityVerdict, EventQualityGate constants
```

---

## Phases

### Phase 1 — Quality-rule schema + loader

`coding/quality/schema.go`: `QualityCategory`, `BlockCapableCategories`, `QualityRule`, `QualityRuleSet`, `QualityVerdict`, `QualityFinding`, `QualityResult`.

`coding/quality/loader.go`: `LoadQualityRulesFromFile`, `LoadQualityRulesFromBytes`. Validates that each rule has a non-empty category, prompt, and severity ("block" or "advisory").

`coding/quality/loader_test.go`: valid YAML round-trip, missing fields error, unknown severity error.

### Phase 2 — LLM judge

`coding/quality/judge.go`:
- `QualityJudge` interface: `Evaluate(ctx, rule, diff, context) (*QualityVerdict, error)`
- `CLIQualityJudge`: shells out to `codex exec --json` with a rendered prompt that includes the rule's prompt + diff. Parses structured JSON verdict from stdout using `json.Decoder`.
- `MockQualityJudge`: returns pre-configured verdicts keyed by rule name for tests.

`coding/quality/judge_test.go`: mock round-trip, CLIQualityJudge with a fake binary.

### Phase 3 — Advisory vs block-capable routing

`coding/quality/evaluator.go`: `QualityEvaluator` struct with `Judge`, `Rules`, `AttentionStore`, `EventStore`, `Logger`.

`EvaluateAtCheckpoint(ctx, runID, diff, context) (*QualityResult, error)`:
1. Run every rule through the judge.
2. For each verdict:
   - If `pass == true`: no action, record in adherence log.
   - If `pass == false` and category is block-capable: create attention item (kind=checkpoint), append to `BlockingFindings`.
   - If `pass == false` and category is advisory: append to `AdvisoryFindings`, write `quality.verdict` event (advisory).
3. Return `QualityResult{Pass: len(BlockingFindings)==0, ...}`.

### Phase 4 — Checkpoint-time hook + event kinds

Add to `core/supervisor.go`:
```go
EventQualityVerdict EventKind = "quality.verdict"
EventQualityGate    EventKind = "quality-gate"
```

The evaluator writes a `quality.verdict` event for every rule verdict (advisory or blocking) and a `quality-gate` event when blocking findings cause escalation.

### Phase 5 — Escalation path

`QualityEvaluator.EvaluateAtCheckpoint` tracks retry counts externally (passed in via `CheckpointContext`). When blocking findings remain after max retries (default 3, matching `DefaultMaxRetries`), emit a `quality-gate` event. The `quality-gate` event is always-blocking per spec and cannot be weakened by policy.

The evaluator also handles the case where the max-retry ceiling is reached for a given checkpoint, promoting the checkpoint to `quality-gate` regardless of the verdict.

### Phase 6 — Tests

- `loader_test.go`: valid/invalid YAML, all field validations.
- `judge_test.go`: mock invocation, structured JSON parsing, CLI parse error path.
- `evaluator_test.go`:
  - Advisory finding: no attention item created, advisory result populated.
  - Block-capable finding: attention item created, blocking result populated.
  - Mixed: both paths in one evaluation.
  - Pass: all verdicts pass, QualityResult.Pass=true.
  - Escalation: blocking finding + retry_count >= max_retries → quality-gate event emitted.

---

## Key Design Decisions

- **No real API calls in tests.** `MockQualityJudge` is the only judge used in tests.
- **CLIQualityJudge shells to `codex exec --json`.** Parses JSON from stdout. Non-zero exit → error. Invalid JSON → error.
- **Quality evaluation is checkpoint-time only**, not post-every-job (unlike contract checks).
- **Advisory findings emit events** but do not create attention items.
- **Block-capable findings create attention items** (kind=`checkpoint`) with source=`quality-judge`.
- **`quality-gate` cannot be weakened** — it is hard-coded as always-blocking per spec. Policy has no field to override it.
- **`QualityEvaluator` does not import `coding/supervisor`** — it is a sibling package. Both may import `core` and `store`.

---

## Default `rules.yaml`

```yaml
quality_rules:
  missing_required_tests:
    category: missing_required_tests
    prompt: "Review the diff. Are there new functions or methods without corresponding test coverage? Answer with JSON: {\"pass\": bool, \"category\": \"missing_required_tests\", \"findings\": [...], \"confidence\": float}"
    severity: block

  spec_contradiction:
    category: spec_contradiction
    prompt: "Does the implementation contradict the spec requirements provided in context? Answer with JSON: {\"pass\": bool, \"category\": \"spec_contradiction\", \"findings\": [...], \"confidence\": float}"
    severity: block

  security_sensitive_unreviewed_change:
    category: security_sensitive_unreviewed_change
    prompt: "Does the diff contain security-sensitive changes (auth, crypto, secrets handling, SQL, network) that have not been reviewed? Answer with JSON: {\"pass\": bool, \"category\": \"security_sensitive_unreviewed_change\", \"findings\": [...], \"confidence\": float}"
    severity: block

  shipper_report_missing:
    category: shipper_report_missing
    prompt: "For shipper jobs: does the plan file contain a non-empty '## Post-Execution Report' section? Answer with JSON: {\"pass\": bool, \"category\": \"shipper_report_missing\", \"findings\": [...], \"confidence\": float}"
    severity: block

  spec_adherence:
    category: spec_adherence
    prompt: "Does the implementation broadly match the spec requirements in spirit and structure? Answer with JSON: {\"pass\": bool, \"category\": \"spec_adherence\", \"findings\": [...], \"confidence\": float}"
    severity: advisory

  review_coverage_depth:
    category: review_coverage_depth
    prompt: "Are reviewer findings substantive and line-anchored, covering the key risk areas in the diff? Answer with JSON: {\"pass\": bool, \"category\": \"review_coverage_depth\", \"findings\": [...], \"confidence\": float}"
    severity: advisory
```

---

## Post-Execution Report

**Status:** Complete  
**Date:** 2026-04-20  
**Tests:** 28 new tests in `coding/quality/`, all passing. Full suite (24 packages) clean under `-race`.  
**Lint:** 0 issues from golangci-lint (govet, staticcheck, errcheck, gosec, revive, gocyclo).

### What was built

**`coding/quality/` package** — 7 files:

- `schema.go`: `Category` type, 4 block-capable category constants, `BlockCapableCategories` map, `IsBlockCapable()`, `Rule`, `RuleSet`, `Verdict`, `Finding`, `Result` structs.
- `loader.go`: `LoadRulesFromFile`, `LoadRulesFromBytes` — YAML parsing with validation (category non-empty, prompt non-empty, severity must be "block" or "advisory"). Sorted output for deterministic evaluation order.
- `loader_test.go`: 10 tests covering valid YAML, empty rules, missing fields, invalid severity, invalid YAML, file I/O.
- `judge.go`: `Judge` interface, `CLIJudge` (shells to `codex exec --json`, streams JSON verdict via `json.Decoder`), `MockJudge` (test double with Calls recorder), `renderJudgePrompt` (combines rule prompt + diff + context).
- `judge_test.go`: 8 tests covering mock verdict return, default pass, call recording, CLI judge with fake binary, invalid JSON error, non-zero exit error, prompt rendering.
- `evaluator.go`: `Evaluator` struct, `CheckpointContext` (RunID, JobID, RetryCount, MaxRetries), `EvaluateAtCheckpoint` — runs all rules, routes to blocking/advisory, creates attention items for blocking findings, emits `quality.verdict` events, escalates to `quality-gate` when retry ceiling reached.
- `evaluator_test.go`: 10 tests covering all-pass, advisory-only, blocking (with DB attention item verified), mixed blocking+advisory, escalation at limit, no escalation before limit, nil stores safety, default max retries.
- `rules.yaml`: 6 default quality rules (4 block-capable, 2 advisory) with LLM-formatted prompts.

**`core/supervisor.go`** — Added `EventQualityVerdict = "quality.verdict"` and `EventQualityGate = "quality-gate"` constants.

### Design notes

- Type names drop the `Quality` prefix per Go convention (package is `quality`; `quality.Rule` not `quality.QualityRule`).
- `CLIJudge` is the production path; `MockJudge` is the test path. No real API calls in tests.
- `Evaluator` does not import `coding/supervisor` — both are sibling packages. Import graph stays `coding/quality → core, store`.
- `quality-gate` is emitted when `RetryCount >= effectiveMaxRetries()` with blocking findings present. The `effectiveMaxRetries()` falls back to `DefaultMaxQualityRetries = 3` when `MaxRetries == 0`.
- Advisory findings populate `Result.AdvisoryFindings` but never create attention items.

---

## Code Review

*To be filled in during review phase.*
