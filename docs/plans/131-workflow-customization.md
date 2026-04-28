# Plan 131 — I3 (partial): workflow customization

> Expands StageRegistry to consult phase-dev (in addition to phase-review and phase-test) and adds two more applies_when predicates. plan_tagged + logical operators + phase-ship customization deferred to a follow-up.

## phase-dev customization

`PhaseExecutor.DeveloperRole string` — overrides the hardcoded `"developer"` role at the start of each fix-loop iteration. Empty → default. `BuildFromPRDWorkflow` reads `StageRegistry.RolesForStage("phase-dev")[0]` to populate it; multi-developer-per-phase is intentionally out of scope (the second dev's commits would overwrite the first's).

`phase-ship` is structurally different — Shipper does git+gh, not role dispatch. Customizing it requires Shipper to grow a role-based shape; deferred.

## applies_when extensions

`RoleAppliesWhen` gains two fields:

- `commit_msg_contains: <regex>` — matches the latest commit message in WorkDir via `git log -1 --format=%B`. New helper at `internal/predicates/commit_msg.go`.
- `phase_index_in: "0-3,7,9-11"` — supports single integers, closed ranges, and comma-separated lists. New helper at `internal/predicates/phase_index.go`.

`PhaseExecutor.roleShouldSkip` now ANDs the predicates: every populated predicate must hold for the role to fire. Existing behavior (only `changes_touch` set) is unchanged.

`plan_tagged` and logical operators (`any_of`, `all_of`) are deferred — `plan_tagged` requires a Tags field on PlanEntry (touches the manifest), and logical ops are speculative until users ask.

## Tests

- `internal/predicates/phase_index_test.go` — 13 cases (single, range, list, whitespace, errors).
- `internal/predicates/commit_msg_test.go` — 5 cases (match, no-match, invalid regex, empty pattern, not-a-repo).
- `coding/phaseloop/executor_test.go::TestPhaseExecutor_RoleShouldSkip_PhaseIndexIn` — verifies the new predicate gates dispatch by phase index.

## Verification

```
go build ./...                                  → clean
go test -race ./... -count=1 -timeout 180s      → 30 ok, 0 failed, 0 races
golangci-lint run ./...                         → 0 issues
```
