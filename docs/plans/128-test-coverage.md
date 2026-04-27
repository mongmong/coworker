# Plan 128 — I5 + I6 test coverage

> Two test gaps from the 2026-04-27 audit.

## I5 — `agent/cli_handle.go` parser tests

Created `agent/cli_handle_test.go` with 9 tests covering the stream-json parser:

- `ParsesFinding` — single finding round-trip.
- `ParsesMultipleFindings` — three findings with different severities.
- `DoneNonZeroExitCode` — `{"type":"done","exit_code":2}` populates ExitCode.
- `MalformedJSONFallsBackToStdout` — invalid JSON line accumulates into Stdout (no Stderr) per `cli_handle.go:51-57` contract.
- `EmptyOutput` — no-op subprocess produces zero findings.
- `StderrCaptured` — stderr text reaches `JobResult.Stderr`.
- `NonZeroExitFromShell` — exec.ExitError propagates to ExitCode.
- `MissingFieldsTreatedAsZero` — JSON with missing fields decodes to zero values.
- `UnknownTypeIgnored` — unknown event types are silently dropped.

Tests use `sh -c <script>` to drive controlled stream-json input through the real `cliJobHandle` so the test exercises the production parser without spawning the actual CLI binaries.

## I6 — Shipper PR creation tests

Refactored `coding/shipper/shipper.go::Shipper` to expose `GhRunner func(ctx, branch, title, body) (string, error)` (nil → uses default `ghCreatePR`). The Ship method now consults the field instead of calling `ghCreatePR` directly. Backward-compatible: existing tests with `DryRun: true` still skip gh entirely.

Three new tests in `coding/shipper/shipper_test.go`:

- `GhRunner_Success` — stub returns a URL; assert ShipResult + captured args (branch, title contains plan ID).
- `GhRunner_Failure` — stub returns an error; assert Ship returns a wrapped error containing the stub's message.
- `GhRunner_EmptyURL` — stub returns the "no PR URL" error; assert Ship surfaces it (no silent empty-URL ship).

## Verification

```
go build ./...                                  → clean
go test -race ./... -count=1 -timeout 180s      → 30 ok, 0 failed, 0 races
golangci-lint run ./...                         → 0 issues
```
