# Plan 111 — Full Role Catalog

**Flavor:** Runtime
**Blocks on:** 106, 108
**Branch:** feature/plan-111-role-catalog
**Manifest entry:** docs/specs/001-plan-manifest.md §111

---

## Goal

Complete the V1 role roster by adding the missing role YAMLs (`developer`,
`reviewer.frontend`, `tester`), adding per-role supervisor rules, and
implementing the Level 2 `applies_when` predicate DSL so roles can gate
themselves based on git-diff content. After this plan every canonical catalog
role from the design spec has a YAML definition, a prompt template, and at
least one supervisor contract rule.

---

## Architecture

### New role files

- `coding/roles/developer.yaml` + `coding/prompts/developer.md`
- `coding/roles/reviewer_frontend.yaml` + `coding/prompts/reviewer_frontend.md`
- `coding/roles/tester.yaml` + `coding/prompts/tester.md`

### Supervisor rules additions

`coding/supervisor/rules.yaml` gains per-role rules for developer, planner,
reviewer.frontend, tester, and shipper.

### applies_when — Level 2 predicate

The `Rule` struct in `coding/supervisor/loader.go` gains an `AppliesWhen`
field. The engine's `Evaluate` method evaluates the predicate before running
the rule's `check`. If the condition is false, a `RuleResult` with
`Skipped: true` is emitted and a `job.skipped` event kind is defined in
`core/event.go`.

Key design decisions:
- `applies_when` is a map; the only key shipped in Plan 111 is
  `changes_touch: []string` (glob list). Evaluating it calls
  `git diff --name-only HEAD~1..HEAD` in `WorkDir` and matches against the
  globs using `path.Match`.
- `RuleResult` gains a `Skipped bool` field; `SupervisorVerdict.Pass` is
  unaffected by skipped rules.
- `EventJobSkipped` is added to `core/event.go` — the engine itself does not
  write events (that remains the caller's responsibility), but the constant
  is available for callers that do.

### Integration tests

`coding/supervisor/integration_test.go` — tests that each role YAML loads,
that `RulesForRole` returns the expected count, and that `applies_when`
skip logic works end-to-end.

---

## Phases

### Phase 1 — developer role + rules

Files changed:
- `coding/roles/developer.yaml` (new)
- `coding/prompts/developer.md` (new)
- `coding/supervisor/rules.yaml` (add developer rules)

Two developer rules:
- `dev_commits_on_feature_branch`: `git_current_branch_matches("^feature/plan-\\d+-")`
- `dev_phase_tag_in_commit`: `last_commit_msg_contains("Phase \\d+:")`

### Phase 2 — planner rules

Files changed:
- `coding/supervisor/rules.yaml` (add planner rule)

One planner rule:
- `planner_plan_file_written`: `exit_code_is(0)`

### Phase 3 — reviewer.frontend role + rules

Files changed:
- `coding/roles/reviewer_frontend.yaml` (new)
- `coding/prompts/reviewer_frontend.md` (new)
- `coding/supervisor/rules.yaml` (no new rules needed — reviewer.* wildcard covers it)

### Phase 4 — tester role + rules

Files changed:
- `coding/roles/tester.yaml` (new)
- `coding/prompts/tester.md` (new)
- `coding/supervisor/rules.yaml` (add tester rule)

One tester rule:
- `tester_exit_zero`: `exit_code_is(0)`

### Phase 5 — shipper rules

Files changed:
- `coding/supervisor/rules.yaml` (add shipper rule)

One shipper rule:
- `shipper_exit_zero`: `exit_code_is(0)`

### Phase 6 — Level 2 applies_when predicate DSL

Files changed:
- `core/event.go` (add `EventJobSkipped`)
- `core/supervisor.go` (add `Skipped bool` to `RuleResult`)
- `coding/supervisor/loader.go` (add `AppliesWhen` field to `Rule`)
- `coding/supervisor/predicates.go` (add `changes_touch` predicate + `EvalAppliesWhen`)
- `coding/supervisor/engine.go` (evaluate `applies_when` before check)

`EvalAppliesWhen(ctx *EvalContext, rule Rule) (bool, error)` — returns true if
the rule should be evaluated. Looks at `rule.AppliesWhen["changes_touch"]` and
runs `git diff --name-only HEAD~1..HEAD` to get changed files, then matches
against each glob using `path.Match`. Returns true (evaluate) if any changed
file matches any glob. Returns true if `AppliesWhen` is nil/empty (no guard).

`SkippedMessages() []string` on `SupervisorVerdict` — mirror of
`FailedMessages()` for tracing.

### Phase 7 — Integration tests

Files changed:
- `coding/supervisor/integration_test.go` (new)

Tests:
1. Each role YAML (developer, reviewer_frontend, tester) loads without error
   via `LoadRulesFromFile` applied to rules.yaml and `RulesForRole`.
2. `applies_when.changes_touch` skips rule when no files match.
3. `applies_when.changes_touch` does NOT skip rule when a file matches.
4. `SkippedMessages()` returns correct skipped rule names.
5. Verdict.Pass is not affected by skipped rules.

---

## Testing checklist

- [ ] `go build ./...` passes
- [ ] `go test ./... -count=1 -timeout 60s` passes
- [ ] `golangci-lint run ./...` passes
- [ ] All new role YAMLs parse correctly
- [ ] `applies_when` skip emits `Skipped: true` in `RuleResult`
- [ ] `applies_when` skip does NOT affect `verdict.Pass`
- [ ] `changes_touch` glob matching works for `web/**` and `*.tsx` patterns

---

## Code Review

### External code review — 2026-04-20

**[FIXED] Critical: `path.Match("web/**", ...)` silently fails for nested paths**
`predicates.go:263-280`. `path.Match` does not treat `**` as a multi-segment
wildcard — it only matches a literal `*` within a single path segment. As a
result, `path.Match("web/**", "web/components/Button.tsx")` returns `false`
while `path.Match("web/**", "web/app.tsx")` returns `true` (only one level
deep). The `reviewer_frontend.yaml` and tests both advertise `web/**` support,
but the advertised semantics are silently broken for real-world frontend trees
where components live in subdirectories. The basename fallback (`!strings.Contains(g, "/")`)
does not help here because `web/**` contains a slash.
Fix: replace `path.Match` with `doublestar.Match` from `bmatcuk/doublestar`
(zero-dependency, MIT) or implement a recursive split: try `path.Match` against
the full path, and if the glob ends in `/**` also try `strings.HasPrefix(file, prefix+"/")`.
File: `coding/supervisor/predicates.go:263`.
→ Response: Replaced `path.Match` with `filepath.Match` and introduced a `globMatch`
helper that handles `**` patterns: `"prefix/**"` checks `strings.HasPrefix`, `"**/<rest>"`
matches the base name, all others delegate to `filepath.Match`. The `path` import removed.
[FIXED]

**[FIXED] Important: `web/**` glob is neither tested at the nested-path level nor
guarded by a validator warning**
`integration_test.go:199` — `TestAppliesWhen_FiresWhenFileMatches` uses
`Button.tsx` at root (matched by `*.tsx`) as the trigger file when the
`applies_when` block also contains `"web/**"`. No test exercises
`web/components/Button.tsx` against `"web/**"` and verifies it matches.
Given the `path.Match` limitation above, a test would also fail. Add a
`TestChangesTouch_NestedWebPath` case that commits `web/components/Button.tsx`
and asserts `changes_touch(["web/**"])` returns true.
→ Response: Added `TestChangesTouch_NestedWebPath` to `integration_test.go`.
Commits `web/components/Button.tsx`, asserts `"web/**"` matches and `"api/**"` does not.
Passes with the `globMatch` fix in C1. [FIXED]

**[FIXED] Important: `SkippedMessages()` returns `RuleName`, not `Message` —
asymmetric with `FailedMessages()`**
`core/supervisor.go:40`. `FailedMessages()` returns `r.Message` (the
human-readable rule failure string). `SkippedMessages()` returns `r.RuleName`
(the machine identifier). The naming and docstring say "messages", but the
contents are names. This asymmetry will confuse callers that log or display
both lists. Either (a) rename to `SkippedRuleNames()` and update all call
sites and tests, or (b) store a skip message in `RuleResult.Message` for
skipped rules (currently it copies `rule.Message`, the original contract
message, which is also misleading). The plan spec says "mirror of
`FailedMessages()` for tracing" (`docs/plans/111-full-role-catalog.md:122`)
which implies it should return message strings, not names.
→ Response: Renamed `SkippedMessages()` to `SkippedRuleNames()` in `core/supervisor.go`
and updated all call sites and test references in `coding/supervisor/integration_test.go`.
Docstring updated to make the asymmetry explicit. [FIXED]

**[FIXED] Important: `applies_when` evaluation error is not tested**
`engine.go:63-71`. When `EvalAppliesWhen` returns an error (e.g., invalid glob,
git not available), the engine sets `verdict.Pass = false` and appends a
`RuleResult` with `Passed: false` and `Skipped: false`. This path is
load-bearing — it prevents silent pass on evaluation failures — but has no
test coverage. Add a test that injects a rule with an invalid glob pattern in
`applies_when.changes_touch` (e.g., `"[invalid"`) and asserts `verdict.Pass`
is false and the result message contains "applies_when eval error".
→ Response: Added `TestAppliesWhen_InvalidGlobReturnsError` to `integration_test.go`.
Commits a real file so git diff works, then evaluates a rule whose `applies_when.changes_touch`
contains `"[invalid"`. Asserts `verdict.Pass=false`, `Skipped=false`, and message contains
`"applies_when eval error"`. [FIXED]

**[OPEN] Suggestion: `reviewer_frontend.yaml` has `applies_when` that core.Role
cannot deserialize — silent field drop**
`coding/roles/reviewer_frontend.yaml:16-24`. The `core.Role` struct has no
`AppliesWhen` field, so the `applies_when` block in the role YAML is silently
dropped by the YAML parser when loaded via `coding/roles` package. The self-
review notes this is intentional (Plan 114 will wire it), but there is no
warning at load time and no compile-time guard. This risks a future reader
assuming the field is already wired. Consider adding a `AppliesWhen *struct{...}`
placeholder with a `// not yet evaluated; owned by Plan 114` comment in
`core/role.go`, or add a YAML strict-decoder check in the roles loader that
at minimum logs a warning for unknown keys.

**[OPEN] Suggestion: `gitDiffChangedFiles` falls back to empty-tree on ANY
error, not just "no parent" errors**
`predicates.go:336-345`. The fallback from `HEAD~1..HEAD` to
`emptyTree..HEAD` is triggered on any `cmd.Output()` error, not specifically
`exit status 128` (unknown revision). A transient git error (disk full,
permissions issue, corrupted repo) would silently produce an empty-tree diff
instead of propagating the error. Tighten the fallback: check
`strings.Contains(err.Error(), "unknown revision")` or parse the exit code
before falling back.

**[FIXED — self-review] `core/event.go` gofmt alignment**
**[WONTFIX — self-review] `EvalAppliesWhen` exported**
**[WONTFIX — self-review] empty-tree fallback on error**
**[WONTFIX — self-review] role YAML `applies_when` not yet evaluated**

### Self-review findings

**[FIXED] `core/event.go` gofmt alignment** — Inserting the `EventJobSkipped`
constant with a comment block before `EventPhaseStarted` broke `gofmt`
alignment. Fixed by running `gofmt -w` before commit.

**[WONTFIX] `EvalAppliesWhen` is exported but only called by `engine.go`** —
It is exported intentionally so callers outside the package (e.g. a future
phase-loop executor that wants to pre-check roles before dispatch) can evaluate
the clause without instantiating a full engine.

**[WONTFIX] `gitDiffChangedFiles` falls back to empty-tree diff only on
error** — The initial commit case (no `HEAD~1`) is handled by catching the
command error and falling back to the empty-tree SHA. This is deterministic and
correctly returns the list of files introduced by the initial commit.

**[WONTFIX] `applies_when` on the role YAML (`reviewer_frontend.yaml`) vs on
supervisor rules** — The spec allows both: the role YAML gates whether the
role fires at all (dispatch-time), while the rules YAML gates whether a
contract rule runs (verdict-time). Plan 111 implements the rules-time version.
The role-YAML dispatch-time version (used by the phase-loop executor) is
deferred to Plan 114 which owns the phase-loop. The `reviewer_frontend.yaml`
file includes `applies_when` for documentation/future use but it is not yet
evaluated by the engine.

---

## Post-Execution Report

**Date:** 2026-04-20
**Status:** Complete

### What was delivered

All seven phases of Plan 111 were implemented in a single commit:

1. **developer.yaml + developer.md** — full role definition + prompt template
   with explicit branch, commit-tag, and test-coverage rules.
2. **Planner rules** — `planner_plan_file_written: exit_code_is(0)` added to
   `rules.yaml`.
3. **reviewer_frontend.yaml + reviewer_frontend.md** — frontend reviewer role
   targeting design-system, CSS, and accessibility concerns.
4. **tester.yaml + tester.md** — tester role with workspace-write sandbox.
5. **Shipper rules** — `shipper_exit_zero: exit_code_is(0)` added.
6. **Level 2 `applies_when` DSL** — `AppliesWhenClause` struct in loader.go,
   `EvalAppliesWhen` + `evalChangesTouch` + `changesTouch` predicate, engine
   evaluates clause before check, skipped rules emit `Skipped: true` in
   `RuleResult`, `EventJobSkipped` constant in `core/event.go`,
   `SkippedMessages()` on `SupervisorVerdict`, `FailedMessages()` updated to
   ignore skipped results.
7. **Integration tests** — `coding/supervisor/integration_test.go` with 14
   new test functions covering all canonical roles, applies_when skip logic,
   glob matching, and the SkippedMessages/FailedMessages invariants.

### Test results

- `go build ./...` — clean
- `go test ./... -count=1 -timeout 60s -race` — all packages pass
- `golangci-lint run ./...` — 0 issues (after gofmt fix on event.go)

### Scope notes

The `applies_when` block in `reviewer_frontend.yaml` is present for
documentation purposes; dispatch-time evaluation is owned by the phase-loop
(Plan 114). The rules-time `applies_when` in `rules.yaml` is fully functional.
