# Plan 127 — I4 + I9 cleanups (HTTP bind + event constants)

> Two small IMPORTANT items from the 2026-04-27 audit.

## I4 — HTTP daemon binds to loopback by default

**Site:** `cli/daemon.go:157`

**Before:** `Addr: fmt.Sprintf(":%d", daemonHTTPPort)` (binds all interfaces)
**After:** `Addr: fmt.Sprintf("%s:%d", daemonHTTPBind, daemonHTTPPort)`, with `daemonHTTPBind` defaulting to `"127.0.0.1"`.

New flag: `--http-bind` (default `127.0.0.1`). Users who want LAN access pass `--http-bind 0.0.0.0`. Docstring updated.

## I9 — Event constants centralized

5 EventKind constants (`EventSupervisorVerdict`, `EventSupervisorRetry`, `EventComplianceBreach`, `EventQualityVerdict`, `EventQualityGate`) lived in `core/supervisor.go`. Moved to `core/event.go` so all kinds are discoverable in one place. The `core/supervisor.go` block is replaced with a one-line pointer comment.

## I10 deferred

`EventAttentionCreated` / `EventAttentionResolved` are unused but the TUI's case statements depend on them. Removing the constants would silently break the TUI's attention display, and re-wiring AttentionStore to emit events conflicts with Decision 6 (attention is NOT event-based). The right fix is its own plan that decides between (a) emit-as-side-effect (event-bus publish only, no event-log row) or (b) replace TUI's event-based attention sync with HTTP polling. Tracked for follow-up.

## Verification

```
go build ./...                                  → clean
go test -race ./... -count=1 -timeout 180s      → 30 ok, 0 failed, 0 races
golangci-lint run ./...                         → 0 issues
```
