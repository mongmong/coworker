# Plan 134 — NICE-TO-HAVE batch 1

> Three small audit nice-to-haves: composite index, Makefile release target, README rewrite.

## N1 — Composite index `dispatches(state, role)`

The `ClaimNextDispatch` query (`WHERE state = 'pending' AND role = ? ORDER BY created_at ASC LIMIT 1`) is on the hot path of every `orch_next_dispatch` MCP call. Two separate single-column indexes (`idx_dispatches_state`, `idx_dispatches_role`) forced the SQLite planner to seek by one and filter the other. Migration `009` adds the composite `(state, role)` so the planner seeks directly.

## N3 — `make release` cross-compile target

Adds `release` and `release-clean` targets to the Makefile. `release` cross-compiles the binary for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64` into `dist/`. With `CGO_ENABLED=0` and pure-Go SQLite (`modernc.org/sqlite`), all four targets build cleanly without a C toolchain — verifying the spec's single-binary-distribution claim. `dist/` added to `.gitignore`.

## N4 — README V1 scope refresh

The README still described coworker as a "thin end-to-end ephemeral invoke flow" — accurate for Plan 100 but wildly outdated 33 plans later. Rewrote the status section to enumerate what's actually shipped (event log, schema, agents, dispatcher, workflow, MCP, HTTP, CLI, plugins, test layers, build) plus a clear "deferred to V1.1+" list that mirrors the audit's open items.

## Verification

```
go build ./...                                  → clean
go test -race ./... -count=1 -timeout 180s      → 33 ok, 0 failed, 0 races
golangci-lint run ./...                         → 0 issues
make release                                    → 4 binaries (94 MB total)
```
