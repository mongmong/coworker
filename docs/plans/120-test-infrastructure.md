# Plan 120 — Test Infrastructure (replay tests + live E2E)

> **For agentic workers:** This plan is implemented inline by Claude Code. Use the executing-plans pattern: implement phase-by-phase, commit each phase, run the full test suite before merging.

**Goal:** Stand up two of the three test layers from the spec that do not yet exist as folders/scaffolding: **replay tests** that re-execute a recorded transcript through a replay-mode CLI wrapper (gated by `COWORKER_REPLAY=1`), and **live E2E tests** gated by `COWORKER_LIVE=1`. Adds the directory structure, fixture format, replay-mode CLI wrapper, and a tiny end-to-end smoke test for each layer.

**Architecture:**
- A *replay test* is just a Go test that points the runtime at a `replayAgent` (a new `core.Agent` implementation) which streams recorded JSON output from disk in place of an actual subprocess. Existing dispatch + supervisor + finding pipelines run unmodified — only the agent boundary is swapped. This matches the spec's pattern of "swap real CLIs with mock binaries" but uses an in-process Go agent instead of a shell script, so we can run it on Windows and on CI without filesystem +x bits.
- A *live test* dispatches a real CLI (Claude Code, Codex, OpenCode) end-to-end. Skipped unless `COWORKER_LIVE=1`. Records cost; warns on >$0.50 per test.
- Both layers reuse existing dispatch infrastructure (`core.Agent` interface, `coding.Dispatcher`). No runtime code changes are required for replay; the only addition is the replay agent + helper to load fixtures.
- Live tests live under build-tag `live` so the default `go test ./...` skips them entirely.

**Tech Stack:** Go test framework, `core.Agent` protocol, build-tag gating (`//go:build live`), env-var gating (`COWORKER_REPLAY`, `COWORKER_LIVE`).

**Reference:** `docs/specs/000-coworker-runtime-design.md` §Testing layers; `CLAUDE.md` §Testing; existing mock binary at `testdata/mocks/codex`.

---

## Scope

In scope:

1. `tests/replay/` directory with at least one replay scenario (a recorded developer-then-reviewer round trip).
2. `agent/replay_agent.go` — a `core.Agent` implementation that reads a JSONL transcript file and streams it line-by-line through the same `JobHandle` protocol the live `CliAgent` uses.
3. `agent/replay_agent_test.go` — unit tests for the replay agent (cancellation, premature EOF, malformed JSON tolerance, idle-detection trailer).
4. `tests/replay/<scenario>/` containing:
   - `transcript.jsonl` — the recorded stream-json output for the developer + reviewer roles.
   - `expected_findings.json` — the supervised output the dispatcher should produce.
   - `replay_test.go` — runs the dispatch pipeline with `replayAgent` and diff-asserts the result.
5. `tests/live/` directory with build-tag `live`. Includes one smoke test per CLI: Claude (`tests/live/claude_smoke_test.go`), Codex (`tests/live/codex_smoke_test.go`), OpenCode (`tests/live/opencode_smoke_test.go`). Each test:
   - Skips when `COWORKER_LIVE != "1"` (defense-in-depth alongside the build tag).
   - Calls `coworker invoke` programmatically with a trivial prompt.
   - Asserts the role finishes, emits at least one event, and the cost is recorded.
   - Honors `COWORKER_LIVE_BUDGET_USD` (default 0.50) — fails fast if any single test exceeds it.
6. `Makefile` targets:
   - `make test-unit` (default; what `go test ./... -count=1` does today)
   - `make test-integration` (already covered by `tests/integration/`)
   - `make test-replay` (`go test ./tests/replay/... -count=1` with `COWORKER_REPLAY=1`)
   - `make test-live` (`go test -tags live ./tests/live/... -count=1` with `COWORKER_LIVE=1`)
7. CI: replay tests added to the default GH Actions workflow (cheap, deterministic). Live tests are NOT added to default CI; instead, a separate workflow file is added with manual `workflow_dispatch` trigger.
8. Documentation: `docs/architecture/testing.md` describing the four test layers, when to use each, and how to record new replay transcripts.

Out of scope:

- Recording new transcripts from real CLI runs (the recording machinery is described, but only the smallest possible synthetic transcript is committed in this plan).
- Coverage gates.
- Performance benchmarks.
- Test sharding or parallelization tuning.

---

## File Structure

**Create:**
- `agent/replay_agent.go` + `agent/replay_agent_test.go`
- `tests/replay/developer_then_reviewer/transcript.jsonl`
- `tests/replay/developer_then_reviewer/expected_findings.json`
- `tests/replay/developer_then_reviewer/replay_test.go`
- `tests/live/doc.go` (build-tag-protected package doc; ensures `go vet` doesn't complain about empty pkg without the tag)
- `tests/live/claude_smoke_test.go`
- `tests/live/codex_smoke_test.go`
- `tests/live/opencode_smoke_test.go`
- `tests/live/helpers.go` (build-tag-protected: env check, cost guard, helpers)
- `docs/architecture/testing.md`
- `.github/workflows/live-tests.yml`

**Modify:**
- `Makefile` (new test targets)
- `.github/workflows/ci.yml` (add `make test-replay` step)
- `docs/architecture/decisions.md` (entry: test layers and replay transcript format)

**Test fixtures:**
- `tests/replay/developer_then_reviewer/transcript.jsonl` — one line per stream-json event (`type:"text" | "finding" | "done"`).

---

## Phase 1 — Replay agent

**Files:**
- Create: `agent/replay_agent.go`, `agent/replay_agent_test.go`

- [ ] **Step 1 — `agent/replay_agent.go`:**

```go
package agent

import (
    "bufio"
    "context"
    "errors"
    "fmt"
    "io"
    "os"
    "sync"
    "time"

    "github.com/chris/coworker/core"
)

// ReplayAgent is a core.Agent implementation that streams recorded
// stream-json output from a transcript file. It is used by replay tests
// to exercise the full dispatch pipeline (supervisor, dedupe, finding
// projection) without invoking a real CLI.
//
// Each Dispatch call selects a transcript by role and streams its lines
// to the consumer at a configurable cadence (default: as fast as the
// reader can consume).
type ReplayAgent struct {
    // TranscriptDir is the directory containing per-role transcript files.
    // The agent looks for "<role>.jsonl" inside this directory for each
    // dispatch.
    TranscriptDir string

    // LineDelay throttles between transcript lines, simulating real CLI
    // output cadence. Zero means stream as fast as possible.
    LineDelay time.Duration
}

// Dispatch loads the transcript for the role and returns a JobHandle
// that streams it. If the transcript does not exist, Dispatch returns an
// error (so missing fixtures are loud).
func (a *ReplayAgent) Dispatch(ctx context.Context, req core.AgentRequest) (core.JobHandle, error) {
    path := fmt.Sprintf("%s/%s.jsonl", a.TranscriptDir, req.Role)
    f, err := os.Open(path) //nolint:gosec // path constructed from controlled inputs in tests only
    if err != nil {
        return nil, fmt.Errorf("replay agent: open transcript %q: %w", path, err)
    }

    sseCtx, cancel := context.WithCancel(ctx)
    h := &replayHandle{
        f:        f,
        cancel:   cancel,
        ctx:      sseCtx,
        delay:    a.LineDelay,
        resultCh: make(chan core.AgentResult, 1),
    }
    go h.run()
    return h, nil
}

type replayHandle struct {
    f        *os.File
    cancel   context.CancelFunc
    ctx      context.Context
    delay    time.Duration
    resultCh chan core.AgentResult
    once     sync.Once
}

func (h *replayHandle) Wait() (core.AgentResult, error) {
    select {
    case res := <-h.resultCh:
        return res, nil
    case <-h.ctx.Done():
        return core.AgentResult{}, h.ctx.Err()
    }
}

func (h *replayHandle) Cancel() error {
    h.once.Do(func() {
        h.cancel()
    })
    return nil
}

func (h *replayHandle) run() {
    defer func() {
        _ = h.f.Close()
        h.cancel()
    }()
    var stdout []byte
    sc := bufio.NewScanner(h.f)
    sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
    for sc.Scan() {
        if err := h.ctx.Err(); err != nil {
            // Cancelled; deliver partial result so Wait does not block.
            select {
            case h.resultCh <- core.AgentResult{Stdout: string(stdout), Stderr: ""}:
            default:
            }
            return
        }
        line := sc.Bytes()
        stdout = append(stdout, line...)
        stdout = append(stdout, '\n')
        if h.delay > 0 {
            select {
            case <-time.After(h.delay):
            case <-h.ctx.Done():
                return
            }
        }
    }
    err := sc.Err()
    if err != nil && !errors.Is(err, io.EOF) {
        select {
        case h.resultCh <- core.AgentResult{
            Stdout: string(stdout),
            Stderr: err.Error(),
        }:
        default:
        }
        return
    }
    select {
    case h.resultCh <- core.AgentResult{Stdout: string(stdout)}:
    default:
    }
}

// Compile-time assertion: ReplayAgent satisfies core.Agent.
var _ core.Agent = (*ReplayAgent)(nil)
```

- [ ] **Step 2 — Tests:**

`agent/replay_agent_test.go` exercises:
1. Happy path: `Dispatch` opens the transcript, `Wait` returns full stdout matching the file (newlines preserved).
2. Missing transcript: `Dispatch` returns an error with the path.
3. Cancellation mid-stream: `Cancel` (or context cancellation) causes `Wait` to return promptly with `ctx.Err()`.
4. `LineDelay` works (rough timing: with 50ms delay and 5 lines, total >= 200ms).
5. Empty transcript: `Wait` returns empty stdout, no error.
6. Very long line: 1MB-ish line scans without overflow.

- [ ] **Step 3 — Run + commit:**

```bash
go test ./agent -count=1 -run TestReplayAgent
git add agent/replay_agent.go agent/replay_agent_test.go
git commit -m "Plan 120: ReplayAgent — stream recorded transcripts as core.Agent"
```

---

## Phase 2 — First replay scenario

**Files:**
- Create:
  - `tests/replay/developer_then_reviewer/transcript_developer.jsonl`
  - `tests/replay/developer_then_reviewer/transcript_reviewer-architect.jsonl`
  - `tests/replay/developer_then_reviewer/expected_findings.json`
  - `tests/replay/developer_then_reviewer/replay_test.go`
  - `tests/replay/doc.go` (package doc)

- [ ] **Step 1 — Transcripts (per-role JSONL):**

Each line is one stream-json event consumed by the existing parser in `agent/cli_agent.go`. Use the `developer` role's expected output kind first, then the reviewer's findings.

`developer.jsonl`:
```jsonl
{"type":"text","content":"Implementing the change..."}
{"type":"text","content":"Done. main.go modified."}
{"type":"done","exit_code":0}
```

`reviewer-architect.jsonl`:
```jsonl
{"type":"finding","path":"main.go","line":42,"severity":"important","body":"Missing error check on Close()"}
{"type":"finding","path":"store.go","line":17,"severity":"minor","body":"Consider prepared statement"}
{"type":"done","exit_code":0}
```

- [ ] **Step 2 — `expected_findings.json`:**

```json
{
    "fingerprints": [
        "main.go:42:important",
        "store.go:17:minor"
    ],
    "count": 2
}
```

- [ ] **Step 3 — `replay_test.go`:**

```go
//go:build !live

package replay_developer_then_reviewer

import (
    "context"
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/chris/coworker/agent"
    "github.com/chris/coworker/core"
    "github.com/chris/coworker/coding"
    "github.com/chris/coworker/store"
)

// TestReplay_DeveloperThenReviewer runs the full dispatch pipeline using
// recorded transcripts as the agent backend. Asserts the supervisor sees
// the expected findings.
func TestReplay_DeveloperThenReviewer(t *testing.T) {
    if os.Getenv("COWORKER_REPLAY") != "1" {
        t.Skip("set COWORKER_REPLAY=1 to enable replay tests")
    }

    fixtureDir, _ := filepath.Abs(".")
    db := setupReplayDB(t)
    defer db.Close()

    es := store.NewEventStore(db)
    rs := store.NewRunStore(db, es)
    js := store.NewJobStore(db, es)

    runID := "replay-1"
    if err := rs.CreateRun(context.Background(), &core.Run{
        ID: runID, Mode: "interactive",
        State: core.RunStateActive,
        StartedAt: time.Now(),
    }); err != nil {
        t.Fatal(err)
    }

    replay := &agent.ReplayAgent{TranscriptDir: fixtureDir}
    d := &coding.Dispatcher{
        Agent:     replay,
        DB:        db,
        // Supervisor and policy intentionally nil so we exercise dispatch alone.
    }

    devJob := &core.Job{
        ID: "j-dev", RunID: runID, Role: "developer",
        State: core.JobStatePending, DispatchedBy: "test",
        StartedAt: time.Now(),
    }
    if err := js.CreateJob(context.Background(), devJob); err != nil {
        t.Fatal(err)
    }
    // ... dispatch developer, then reviewer, collect findings, compare ...
    // (Implementation depends on the actual Dispatcher API; finalize during impl.)

    // Assert resulting findings match expected_findings.json
    var expected struct {
        Fingerprints []string `json:"fingerprints"`
        Count        int      `json:"count"`
    }
    raw, _ := os.ReadFile(filepath.Join(fixtureDir, "expected_findings.json"))
    if err := json.Unmarshal(raw, &expected); err != nil {
        t.Fatal(err)
    }
    // Compare against findings actually inserted in the store.
    fs := store.NewFindingStore(db, es)
    findings, _ := fs.ListFindingsByRun(context.Background(), runID)
    if len(findings) != expected.Count {
        t.Errorf("findings count = %d, want %d", len(findings), expected.Count)
    }
}
```

(The exact API call sequence depends on the `Dispatcher.Orchestrate` signature; finalize during implementation by reading `coding/dispatch.go`. The test asserts the recorded transcripts produce the expected findings.)

- [ ] **Step 4 — Run + commit:**

```bash
COWORKER_REPLAY=1 go test ./tests/replay/... -count=1
git add tests/replay/
git commit -m "Plan 120: first replay scenario — developer + reviewer transcripts"
```

---

## Phase 3 — Live test scaffold

**Files:**
- Create: `tests/live/doc.go`, `tests/live/helpers.go`, `tests/live/claude_smoke_test.go`, `tests/live/codex_smoke_test.go`, `tests/live/opencode_smoke_test.go`

- [ ] **Step 1 — `tests/live/doc.go`:**

```go
//go:build live

// Package live contains end-to-end smoke tests that invoke real CLI
// agents (Claude Code, Codex, OpenCode). Tests skip unless
// COWORKER_LIVE=1 is set in the environment, AND the live build tag is
// enabled. Run with:
//
//     COWORKER_LIVE=1 go test -tags live ./tests/live/...
//
// Each test should consume <1 second of CLI time and well under
// $0.50 of provider cost.
package live
```

- [ ] **Step 2 — `tests/live/helpers.go`:**

```go
//go:build live

package live

import (
    "context"
    "os"
    "os/exec"
    "strconv"
    "testing"
    "time"
)

// requireLiveEnv skips the test unless COWORKER_LIVE=1.
func requireLiveEnv(t *testing.T) {
    t.Helper()
    if os.Getenv("COWORKER_LIVE") != "1" {
        t.Skip("set COWORKER_LIVE=1 to enable live agent tests")
    }
}

// requireBinary skips the test if the named binary is not on PATH.
func requireBinary(t *testing.T, name string) {
    t.Helper()
    if _, err := exec.LookPath(name); err != nil {
        t.Skipf("required binary %q not found on PATH: %v", name, err)
    }
}

// budgetUSD returns the per-test budget in USD, defaulting to 0.50.
func budgetUSD() float64 {
    s := os.Getenv("COWORKER_LIVE_BUDGET_USD")
    if s == "" {
        return 0.50
    }
    v, err := strconv.ParseFloat(s, 64)
    if err != nil || v <= 0 {
        return 0.50
    }
    return v
}

// withTimeout returns a context with the per-test timeout (default 60s).
func withTimeout(t *testing.T) (context.Context, context.CancelFunc) {
    t.Helper()
    return context.WithTimeout(context.Background(), 60*time.Second)
}
```

- [ ] **Step 3 — One smoke test per CLI** (Claude, Codex, OpenCode). Each:

```go
//go:build live

package live

import (
    "testing"
)

func TestLive_Claude_Smoke(t *testing.T) {
    requireLiveEnv(t)
    requireBinary(t, "claude")

    ctx, cancel := withTimeout(t)
    defer cancel()

    // Invoke the developer role with a trivial prompt:
    //   "Say 'ok' and nothing else."
    // Verify the run completes, at least one event is emitted,
    // and recorded cost <= budgetUSD().
    // (Final implementation calls into cli/invoke.go's exported
    // helper or runs the binary directly.)
    _ = ctx
    t.Skip("smoke test scaffold only; full implementation calls cli.Invoke")
}
```

(Codex and OpenCode follow the same pattern; record the actual binary names — `codex` and `opencode`.)

- [ ] **Step 4 — Verify the file builds with the tag and the default `go test` ignores it:**

```bash
go test ./tests/live/... -count=1                     # expected: no test files (tag absent)
go test -tags live ./tests/live/... -count=1          # expected: tests skip without COWORKER_LIVE=1
COWORKER_LIVE=1 go test -tags live ./tests/live/... -count=1  # tests run and skip on missing binary
```

- [ ] **Step 5 — Commit:**

```bash
git add tests/live/
git commit -m "Plan 120: live test scaffold (build-tag + env gated, per-CLI smoke tests)"
```

---

## Phase 4 — Makefile + CI integration

**Files:**
- Modify: `Makefile`, `.github/workflows/ci.yml`
- Create: `.github/workflows/live-tests.yml`

- [ ] **Step 1 — Makefile:**

```makefile
.PHONY: test test-unit test-integration test-replay test-live

test: test-unit

test-unit:
	go test -race ./... -count=1 -timeout 180s

test-integration:
	go test ./tests/integration/... -count=1 -timeout 60s

test-replay:
	COWORKER_REPLAY=1 go test ./tests/replay/... -count=1 -timeout 60s

test-live:
	COWORKER_LIVE=1 go test -tags live ./tests/live/... -count=1 -timeout 300s
```

- [ ] **Step 2 — `.github/workflows/ci.yml`:** add a job step after the existing `go test`:

```yaml
      - name: Replay tests
        run: make test-replay
```

- [ ] **Step 3 — `.github/workflows/live-tests.yml`:**

```yaml
name: Live agent tests
on:
  workflow_dispatch:
    inputs:
      cli:
        description: "Which CLI to test (claude/codex/opencode/all)"
        default: "all"

jobs:
  live:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.25"
      - name: Run live smoke tests
        env:
          COWORKER_LIVE: "1"
          COWORKER_LIVE_BUDGET_USD: "0.50"
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
          OPENAI_API_KEY: ${{ secrets.OPENAI_API_KEY }}
        run: make test-live
```

- [ ] **Step 4 — Verify locally:**

```bash
make test-replay
make test-live  # will skip due to missing binaries; that is OK for local
```

- [ ] **Step 5 — Commit:**

```bash
git add Makefile .github/workflows/
git commit -m "Plan 120: Makefile + CI for replay and live tests"
```

---

## Phase 5 — Documentation

**Files:**
- Create: `docs/architecture/testing.md`
- Modify: `docs/architecture/decisions.md`

- [ ] **Step 1 — `docs/architecture/testing.md`:**

```markdown
# Test layers

Coworker has four discrete test layers. Each addresses a different class of bug.

## 1. Unit (`*_test.go` next to source)

- ~80% of the suite. Fast, deterministic, no agents.
- Run via `make test-unit` or `go test ./... -count=1`.
- Must pass on every commit.

## 2. Integration with mocks (`tests/integration/`)

- Runtime + mock CLI binaries (`testdata/mocks/<name>`).
- Exercises dispatch, registry eviction, supervisor loops, crash recovery.
- Runs in seconds, no API cost.
- Run via `make test-integration`.

## 3. Replay (`tests/replay/<scenario>/`)

- Records real-agent transcripts as JSONL fixtures.
- A `ReplayAgent` (`agent/replay_agent.go`) plays them back as a
  `core.Agent`. Dispatch, supervisor, finding pipelines run unmodified.
- Gated by `COWORKER_REPLAY=1`. Run via `make test-replay`.
- Each scenario is a directory containing per-role `<role>.jsonl`
  transcripts and an `expected_findings.json` baseline.

### Recording a new scenario

1. Run the live runtime end-to-end with `COWORKER_RECORD_TRANSCRIPTS=1`
   (when implemented; see Plan 121).
2. Copy the resulting `<run-id>/transcripts/` directory into
   `tests/replay/<scenario>/`.
3. Run the replay test, capture `findings`, save to
   `expected_findings.json`.

## 4. Live E2E (`tests/live/`)

- Real CLIs against real APIs.
- Build-tag protected (`//go:build live`) — invisible to default
  `go test ./...`.
- Additionally gated by `COWORKER_LIVE=1`.
- Per-test cost guard via `COWORKER_LIVE_BUDGET_USD` (default 0.50).
- Run via `make test-live`.
- Pre-release only. Not part of default CI.

### Adding a new live test

1. Create file under `tests/live/` with `//go:build live`.
2. Use the helpers in `tests/live/helpers.go`.
3. Use a trivial prompt (e.g. "Say 'ok'") — minimum tokens.
4. Assert the role finishes and at least one event is emitted.
5. Verify cost in `cost_events` does not exceed `budgetUSD()`.
```

- [ ] **Step 2 — `docs/architecture/decisions.md`:** append entry summarizing the test-layer architecture and the replay-transcript format choice (JSONL one-event-per-line for grep/diff friendliness; per-role file for deterministic Dispatch routing).

- [ ] **Step 3 — Commit:**

```bash
git add docs/architecture/testing.md docs/architecture/decisions.md
git commit -m "Plan 120: docs/architecture/testing.md describing the four test layers"
```

---

## Phase 6 — Full verification

- [ ] **Step 1 — Default suite (no replay/live):**

```bash
go build ./...
go test -race ./... -count=1 -timeout 180s
golangci-lint run ./...
```

Expected: build clean, all tests pass with `-race`, 0 lint issues. Replay and live tests are skipped.

- [ ] **Step 2 — Replay suite:**

```bash
make test-replay
```

Expected: all replay scenarios pass with `COWORKER_REPLAY=1`.

- [ ] **Step 3 — Live suite (skip OK):**

```bash
make test-live
```

Expected: tests skip when binaries are missing or `ANTHROPIC_API_KEY`/`OPENAI_API_KEY` are not set. No failures from missing dependencies.

---

## Self-Review Checklist

- [ ] `ReplayAgent` satisfies `core.Agent` (compile-time `var _ core.Agent = (*ReplayAgent)(nil)`).
- [ ] Cancellation is observed within 100ms of `Cancel()` in tests.
- [ ] Replay scenarios live under `tests/replay/<scenario>/` with per-role transcripts + `expected_findings.json`.
- [ ] Live tests are protected by build tag `live` AND by env var `COWORKER_LIVE=1`. Default `go test ./...` skips them entirely (no test files reported for `tests/live/`).
- [ ] Budget guard is enforced; tests warn or fail when `COWORKER_LIVE_BUDGET_USD` is exceeded.
- [ ] Makefile targets are idempotent and self-contained.
- [ ] CI workflow for replay tests added; live tests are NOT in default CI (separate manual workflow).
- [ ] `docs/architecture/testing.md` describes the four layers and how to add fixtures.

---

## Code Review

(To be filled in after implementation by Codex review subagent.)

---

## Post-Execution Report

(To be filled in after implementation.)
