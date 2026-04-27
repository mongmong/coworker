# Test Layers

Coworker has four discrete test layers. Each addresses a different class of bug. Together they cover correctness, integration, regression, and provider compatibility.

## 1. Unit (`*_test.go` next to source)

- ~80% of the suite. Fast, deterministic, no external processes.
- Run via `make test` (default) or `go test -race ./... -count=1`.
- Must pass on every commit.
- Includes the cross-cutting `tests/architecture/` package, which enforces import-discipline invariants (e.g. `core/` does not import `coding/`).

## 2. Integration with mocks (`tests/integration/`)

- Runtime + mock CLI binaries (`testdata/mocks/<name>` — typically shell scripts or compiled Go binaries that emit canned stream-json).
- Exercises dispatch, registry eviction, supervisor loops, crash recovery.
- Runs in seconds, no API cost.
- Run via `make test-integration`.

## 3. Replay (`tests/replay/<scenario>/`)

- Records real-agent transcripts as JSONL fixtures.
- A `ReplayAgent` (`agent/replay_agent.go`) plays them back as a `core.Agent`. Dispatch, supervisor, finding pipelines run unmodified.
- Gated by `COWORKER_REPLAY=1`. Run via `make test-replay`.
- Each scenario directory contains:
  - `transcripts/<role>.jsonl` — one JSONL per role. Role names use the on-disk role-file convention (dots replaced by underscores: `reviewer.arch` → `reviewer_arch.jsonl`).
  - `inputs/` — placeholder files referenced by the role's required inputs (`plan_path`, `diff_path`, `spec_path`, etc.).
  - `expected.json` — assertions for each role: `exit_code`, `findings_count`, optional `fingerprints` (`<path>:<line>:<severity>`).
  - `replay_test.go` — drives `Dispatcher.Orchestrate` for each role and compares against `expected.json`.

### Adding a new replay scenario

1. Run the runtime end-to-end with `COWORKER_RECORD_TRANSCRIPTS=1` (when implemented; planned for Plan 121).
2. Copy the resulting `<run-id>/transcripts/` directory into `tests/replay/<scenario>/transcripts/`.
3. Run the dispatcher in dry mode with the recorded transcripts; capture findings into `expected.json`.
4. Add a `replay_test.go` that calls `Dispatcher.Orchestrate` for each role and asserts the result.

Until the recording machinery exists, transcripts can be hand-written to verify specific finding/exit-code shapes.

## 4. Live E2E (`tests/live/`)

- Real CLIs against real APIs.
- Build-tag protected (`//go:build live`) — invisible to default `go test ./...`.
- Additionally gated by `COWORKER_LIVE=1`.
- Run via `make test-live`.
- Pre-release only. **Not** part of default CI; a separate `live-tests.yml` workflow with manual `workflow_dispatch` trigger handles this.
- Per-test cost guard: `COWORKER_LIVE_BUDGET_USD` (default `0.50`). **Currently documentation-only**: `Dispatcher` does not yet wire `core.CostWriter`, so live tests cannot read `cost_events` rows. Cost wiring lands in Plan 121, after which the budget will be enforced.

### Adding a new live test

1. Create file under `tests/live/` with `//go:build live`.
2. Use the helpers in `tests/live/helpers.go` (`requireLiveEnv`, `requireBinary`, `withTimeout`, `hasJSONLine`).
3. Use a trivial prompt (e.g., "Print one line: …") to minimize tokens.
4. Assert the binary exits 0 and emits at least one stream-json line on stdout.
5. Keep the test under 60 seconds wall-clock.

### Local execution

Live tests run only when both the build tag and the env var are set:

```bash
# Default `go test ./...` from the repo root excludes tests/live/ entirely.
go test ./...

# With the tag but no env var: tests register and SKIP cleanly.
go test -tags live ./tests/live/... -count=1

# With the env var but missing binaries: tests SKIP (Skipf).
COWORKER_LIVE=1 go test -tags live ./tests/live/... -count=1

# Full live run (real API calls, costs money):
COWORKER_LIVE=1 make test-live
```

## Stream-JSON line shape

All four layers (unit mocks, integration mocks, replay transcripts, live CLIs) share a single output format: newline-delimited JSON, one event per line. The parser at `agent/cli_handle.go::streamMessage` is authoritative:

```json
{"type":"finding","path":"main.go","line":42,"severity":"important","body":"…"}
{"type":"done","exit_code":0}
```

Any new event kind added to the parser must be backfilled into the relevant test fixtures.

## Event log as fixture

Many runtime tests snapshot the resulting `events` log and diff against a golden file. This catches ordering regressions that return-value or DB-state assertions miss. Replay transcripts are the agent-facing equivalent: they pin the *agent output*, not the *event log*. Use both when validating multi-step workflows.
