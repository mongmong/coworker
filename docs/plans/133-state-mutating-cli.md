# Plan 133 — I1 (rest, partial): state-mutating CLI

> Closes most of the remaining state-mutating CLI gap from the audit. `edit` ships; `redo` and `resume` deferred with documented rationale.

## edit

`coworker edit <path>` — opens `$EDITOR` (default `vim`) on the given path, inside the active session. After the editor exits, checks `git status --porcelain --` for the path; if dirty, prints next-step instructions to commit + run `coworker record-human-edit --commit <sha>`.

Filesystem watching for auto-detect (per spec line 519) is intentionally deferred — needs an event-bus wiring to publish `human-edit` jobs from fs events. The current shape is a useful workflow shortcut that exposes the existing `record-human-edit` machinery.

5 tests: no-session, missing-artifact, editor runs cleanly, editor failure surfaces, dirty-hint appears in a real git repo.

## redo — deferred

The audit's intent: "re-dispatch a role with the same inputs as a prior job." The runtime today does not persist `DispatchInput.Inputs` on the `jobs` row or in the `job.created` event payload — only Role + CLI + State. To support `redo` properly, inputs need to be persisted (extend `core.Job` + `job.created` event payload). That's a small schema change but it touches a hot path; deferred to a follow-up plan.

Workaround: users re-run with `coworker invoke <role> --diff <...> --spec <...>` — same effect, more typing.

## resume — covered by `--resume-after-attention`

The spec describes `resume` as the inverse of "interactive pause." But the runtime has no separate "paused-interactive" run state; pauses happen via attention checkpoints, and resumption happens via `coworker run --resume-after-attention <id>`. A separate `resume` command would be a thin alias and risks confusion. Decision recorded; the spec command is achievable today via existing flags.

## Verification

```
go build ./...                                  → clean
go test -race ./... -count=1 -timeout 180s      → 33 ok, 0 failed, 0 races
golangci-lint run ./...                         → 0 issues
```
