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

Reviewer: claude-sonnet-4-6  
Date: 2026-04-20  
Range: 25c32c7..cc5384f (10 files, 1707 lines)  
Test run: `go test -race ./coding/quality/... -count=1` → **PASS** (1.483s, no races)

### What was done well

- Routing authority correctly assigned to `IsBlockCapable(rule.Category)`, not `rule.Severity`. The category is the hard gate; severity is metadata only. This matches the spec invariant.
- `effectiveMaxRetries()` fallback is clean; the `RetryCount >= effectiveMaxRetries()` boundary is correct (zero-indexed retries, fires on the 4th call when default=3).
- `exec.CommandContext` propagates context cancellation to the subprocess — no hung child processes when the parent context is cancelled.
- All four block-capable categories are hard-coded in `BlockCapableCategories`; the map is unexported-value-safe and policy cannot extend it.
- `MockJudge.Calls` recorder enables deterministic call-order assertions in tests.
- Nil-store safety in both `createAttentionItem` and `writeVerdictEvent` / `writeQualityGateEvent` allows nil-store unit testing without DB setup.
- `rules.yaml` advisory categories (`spec_adherence`, `review_coverage_depth`) are non-block-capable and carry `severity: advisory`, consistent with the allowlist.

---

### Issues

**[FIXED] Important — `IsBlockSeverity()` is dead code; severity field does not affect routing**

`coding/quality/schema.go:61-64`

`Rule.IsBlockSeverity()` is defined and tested but is never called in the evaluator. The evaluator routes entirely via `IsBlockCapable(rule.Category)`. This means a rule can declare `severity: block` on a non-block-capable category (or `severity: advisory` on a block-capable one) and the loader will accept it without warning. The severity field is currently misleading documentation rather than an enforced constraint.

Two options:

1. Remove `IsBlockSeverity()` and add a loader validation that warns (or errors) when `severity` and category membership are inconsistent. This would catch misconfigurations in `rules.yaml` early.
2. If `severity` is intentionally reserved for future use, add a comment in `validateRule` explaining that it is not yet load-bearing in routing decisions.

Either way, the inconsistency between the field's name/test and its actual impact on behaviour needs to be resolved to avoid future confusion.

→ Response: Removed `IsBlockSeverity()` and its test (`TestRuleIsBlockSeverity`). Added `validateRule` warnings via `slog.Warn` when severity contradicts category block-capability. Rules are still accepted — the warning is a best-effort misconfiguration signal. Added `TestLoadRulesFromBytes_SeverityCategoryMismatch` to verify both warning paths are hit without errors. [FIXED]

---

**[FIXED] Important — `stdout.String()` in JSON parse error message is always empty**

`coding/quality/judge.go:70-71`

```go
dec := json.NewDecoder(&stdout)
if err := dec.Decode(&verdict); err != nil {
    return nil, fmt.Errorf("... (stdout: %s)", rule.Name, err, stdout.String())
}
```

`json.Decoder` reads from the `bytes.Buffer` as a stream. By the time `Decode` returns an error, it has already drained the buffer's contents into its own internal read buffer. `stdout.String()` on the remaining buffer will be empty (or show only the unconsumed suffix if the JSON was partially valid). The error message will lose the actual output that caused the parse failure, making debugging hard.

Fix: capture the raw bytes before handing them to the decoder.

```go
raw := stdout.Bytes()
dec := json.NewDecoder(bytes.NewReader(raw))
if err := dec.Decode(&verdict); err != nil {
    return nil, fmt.Errorf("quality judge: parse verdict JSON for rule %q: %w (stdout: %s)",
        rule.Name, err, raw)
}
```

→ Response: Fixed in `judge.go` — `raw := stdout.Bytes()` captured before `json.NewDecoder(bytes.NewReader(raw))`. Error message now uses `string(raw)`. `TestCLIJudge_InvalidJSON` extended to assert the raw output (`"not json"`) appears in the error string. [FIXED]

---

**[OPEN] Suggestion — Judge errors silently drop the rule with no verdict event; evaluator test has no coverage for this path**

`coding/quality/evaluator.go:88-94`

When `e.Judge.Evaluate` returns an error, the rule is silently skipped (`continue`) and no `quality.verdict` event is written. This means a systematic judge failure (e.g. `codex` binary missing) produces a clean `result.Pass = true` with no audit trail in the event log. The comment says "treat as advisory pass", but advisory findings still write events; judge errors do not.

Recommendation: write a `quality.verdict` event with `pass: true, confidence: 0.0` and a `findings: ["judge error: <err>"]` field so failures are observable in the event log. Also add an evaluator test with a `MockJudge` that returns errors to verify the pass-through behaviour.

---

**[OPEN] Suggestion — No test verifies that `quality.verdict` events are actually written to the event store**

`coding/quality/evaluator_test.go`

All evaluator tests assert on the `Result` struct (findings counts, attention item IDs, escalation flag) but none query the event store to confirm that `writeVerdictEvent` or `writeQualityGateEvent` persisted rows. Given that both functions silently swallow store errors (log only), a broken `EventStore` would cause all tests to pass while emitting nothing. Adding one assertion along the lines of `evtStore.ListEvents(runID)` and checking the count would close this gap.

---

**[OPEN] Suggestion — `CLIJudge` has no `WaitDelay` set; subprocess may linger after context cancellation**

`coding/quality/judge.go:55`

`exec.CommandContext` sends `SIGKILL` when the context is cancelled, but the Go runtime's `cmd.Wait()` may still block waiting for the process's I/O pipes to drain. Setting `cmd.WaitDelay` (available since Go 1.20, confirmed present in Go 1.25) gives the subprocess a grace period before the pipe is force-closed:

```go
cmd.WaitDelay = 5 * time.Second
```

Without this, a subprocess that ignores SIGKILL or holds the pipe open will cause `cmd.Run()` to block indefinitely even after the context deadline fires. This is low severity for a batch quality judge but worth addressing before production use.
