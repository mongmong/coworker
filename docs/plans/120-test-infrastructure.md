# Plan 120 — Test Infrastructure (replay tests + live E2E)

> **For agentic workers:** This plan is implemented inline by Claude Code. Use the executing-plans pattern: implement phase-by-phase, commit each phase, run the full test suite before merging.

**Goal:** Stand up two of the three test layers from the spec that do not yet have scaffolding: **replay tests** that re-execute recorded transcripts through a `ReplayAgent` (gated by `COWORKER_REPLAY=1`), and **live E2E tests** gated by build tag `live` plus env var `COWORKER_LIVE=1`. Adds the directory structure, fixture format, replay agent, and one smoke test per CLI.

**Architecture:**
- A *replay test* is a Go test that runs `coding.Dispatcher.Orchestrate` with a `replayAgent` that implements `core.Agent`. The replay agent's `JobHandle.Wait` parses the recorded transcript JSONL using the **same stream-json schema and parsing rules** that `agent/cli_handle.go` uses for live `CliAgent` output. This means findings flow through the existing pipeline (Dispatcher persists findings via `FindingStore.InsertFinding`).
- A *live test* shells out to a real CLI (`claude` / `codex` / `opencode`) with a trivial prompt and asserts the binary exits cleanly and emits at least one stream-json line on stdout (any JSON object with a top-level `"type"` field — `done`, `finding`, or vendor-specific kinds all qualify). Live tests are protected by build tag `live` AND `COWORKER_LIVE=1`. They are NOT in default CI.
- No production code changes are required for replay; the only addition is `ReplayAgent` itself.

**Tech Stack:** Go test framework, `core.Agent` protocol (`Dispatch(ctx, *Job, prompt) (JobHandle, error)` and `Wait(ctx) (*JobResult, error)`), build-tag gating (`//go:build live`), env-var gating.

**Reference:** `docs/specs/000-coworker-runtime-design.md` §Testing layers; `CLAUDE.md` §Testing; existing `agent/cli_agent.go` and `agent/cli_handle.go` for the parser the replay agent must match; existing `testdata/mocks/codex` for the JSONL shape that the parser already consumes.

---

## Required-API audit (do this before writing code)

| Surface | Reality (verified in repo) |
| --- | --- |
| `core.Agent` | `Dispatch(ctx context.Context, job *core.Job, prompt string) (core.JobHandle, error)` (`core/agent.go:9-13`). |
| `core.JobHandle` | `Wait(ctx context.Context) (*core.JobResult, error)` and `Cancel() error` (`core/agent.go:15-22`). |
| `core.JobResult` | `Findings []Finding`, `Artifacts []Artifact`, `ExitCode int`, `Stdout string`, `Stderr string` (`core/job.go:32-39`). |
| Stream-json line shape | `{"type":"finding","path":...,"line":...,"severity":...,"body":...}` and `{"type":"done","exit_code":N}` (`agent/cli_handle.go:22-30,59-70`; `testdata/mocks/codex:9-11`). |
| Role names | Files in `coding/roles/`. The reviewer role is named `reviewer.arch` (file `reviewer_arch.yaml`). Other names: `architect`, `developer`, `planner`, `reviewer.frontend`, `shipper`, `tester`. |
| Required inputs | `developer`: `plan_path`, `phase_index`, `run_context_ref`. `reviewer.arch`: `diff_path`, `spec_path`. |
| Dispatcher entrypoint | `Orchestrate(ctx, *DispatchInput) (*DispatchResult, error)` creates its own run, job, prompt rendering — caller does **not** pre-create. Caller must set `RoleDir` and `PromptDir`. (`coding/dispatch.go:121-265`) |
| Findings persistence | After Orchestrate, findings are in `findings` table — query with `store.FindingStore.ListFindings(ctx, runID)` (the actual method name; not `ListFindingsByRun`). |
| `cli/invoke` | Accepts a single `--cli-binary` flag (defaults to `codex`); only supports `diff_path`/`spec_path` as inputs. Cannot directly drive Claude Code or OpenCode at the CLI level (`cli/invoke.go:41-47`). |

---

## Scope

In scope:

1. `agent/replay_agent.go` — a `core.Agent` implementation whose `JobHandle.Wait` reads a JSONL transcript file and parses stream-json the same way `cli_handle.go` does, producing a real `core.JobResult` with `Findings`, `ExitCode`, `Stdout`, `Stderr`.
2. `agent/replay_agent_test.go` — unit tests for the replay agent (parsing, cancellation, missing transcript, malformed line tolerance).
3. `tests/replay/developer_then_reviewer/` — first replay scenario:
   - `transcripts/developer.jsonl` and `transcripts/reviewer_arch.jsonl` (matching the on-disk role-file naming convention with underscore).
   - `expected.json` — expected dispatch result (findings count, fingerprints).
   - `replay_test.go` — runs `Dispatcher.Orchestrate` for each role with `replayAgent`, asserts findings match `expected.json`.
4. `tests/live/` directory with build-tag `live`. One smoke test per CLI:
   - `tests/live/claude_smoke_test.go`
   - `tests/live/codex_smoke_test.go`
   - `tests/live/opencode_smoke_test.go`
   - Each test invokes the CLI binary directly with a trivial prompt (e.g., `Print one line: '{"type":"done","exit_code":0}'`) and asserts exit code 0 + at least one parseable JSON line on stdout. **No** assertions on `cost_events` (Dispatcher has no CostWriter wiring yet — Plan 121).
5. `tests/live/helpers.go` — env/binary check helpers; budget guard is documentation-only in this plan because cost capture is not yet wired (also called out as out-of-scope below).
6. `Makefile` targets: `test` (default unit), `test-replay`, `test-live`.
7. CI: `make test-replay` added to default GH Actions workflow. Live tests get a separate `workflow_dispatch`-triggered workflow file.
8. `docs/architecture/testing.md` describing the four layers and how to record a new replay transcript.

Out of scope:

- Recording new transcripts from real CLI runs. Recording machinery is described in `docs/architecture/testing.md` but only the smallest synthetic transcript is committed.
- Cost assertions in live tests. `Dispatcher` has no `CostWriter` field, and CLI agents do not yet emit `core.CostSample`. Plan 121 wires this; live tests document a `COWORKER_LIVE_BUDGET_USD` env var for *future* enforcement only.
- Coverage gates, performance benchmarks, sharding.

---

## File Structure

**Create:**
- `agent/replay_agent.go` + `agent/replay_agent_test.go`
- `tests/replay/developer_then_reviewer/transcripts/developer.jsonl`
- `tests/replay/developer_then_reviewer/transcripts/reviewer_arch.jsonl`
- `tests/replay/developer_then_reviewer/expected.json`
- `tests/replay/developer_then_reviewer/replay_test.go`
- `tests/live/doc.go` (build-tag-protected package doc)
- `tests/live/helpers.go` (build-tag-protected)
- `tests/live/claude_smoke_test.go`
- `tests/live/codex_smoke_test.go`
- `tests/live/opencode_smoke_test.go`
- `docs/architecture/testing.md`
- `.github/workflows/live-tests.yml`

**Modify:**
- `Makefile`
- `.github/workflows/ci.yml` (add replay step)
- `docs/architecture/decisions.md` (entry: test layers + transcript format)

---

## Phase 1 — ReplayAgent

**Files:**
- Create: `agent/replay_agent.go`, `agent/replay_agent_test.go`

- [ ] **Step 1 — `agent/replay_agent.go`:**

```go
package agent

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "strings"
    "sync"
    "time"

    "github.com/chris/coworker/core"
)

// ReplayAgent is a core.Agent implementation that streams recorded
// stream-json output from a transcript file. It mirrors CliAgent's
// behavior exactly: Dispatch returns a JobHandle whose Wait parses the
// transcript using the same streamMessage schema and produces a real
// core.JobResult with parsed Findings, ExitCode, Stdout, Stderr.
//
// Used by replay tests to exercise the full dispatch pipeline
// (supervisor, dedupe, finding persistence) without running a real CLI.
//
// Per-role transcript routing: ReplayAgent looks for
// "<TranscriptDir>/<job.Role-with-dots-replaced-by-underscores>.jsonl"
// (matching the role-file naming convention). Missing transcripts are a
// loud error.
type ReplayAgent struct {
    TranscriptDir string

    // LineDelay throttles between transcript lines. Zero == as fast as
    // the parser consumes.
    LineDelay time.Duration
}

func (a *ReplayAgent) Dispatch(ctx context.Context, job *core.Job, _ string) (core.JobHandle, error) {
    role := strings.ReplaceAll(job.Role, ".", "_")
    path := filepath.Join(a.TranscriptDir, role+".jsonl")
    f, err := os.Open(path) //nolint:gosec // G304: path constructed from controlled inputs in tests only
    if err != nil {
        return nil, fmt.Errorf("replay agent: open transcript %q: %w", path, err)
    }
    return &replayHandle{
        f:     f,
        delay: a.LineDelay,
    }, nil
}

type replayHandle struct {
    f     *os.File
    delay time.Duration

    cancelMu sync.Mutex
    cancelled bool
}

func (h *replayHandle) markCancelled() bool {
    h.cancelMu.Lock()
    defer h.cancelMu.Unlock()
    if h.cancelled {
        return false
    }
    h.cancelled = true
    return true
}

func (h *replayHandle) isCancelled() bool {
    h.cancelMu.Lock()
    defer h.cancelMu.Unlock()
    return h.cancelled
}

// Wait reads the transcript line-by-line, parses each line as a stream-json
// event, and assembles a core.JobResult. Returns ctx.Err() if the context
// is cancelled or Cancel() is called before the transcript completes.
func (h *replayHandle) Wait(ctx context.Context) (*core.JobResult, error) {
    defer h.f.Close()

    result := &core.JobResult{}
    decoder := json.NewDecoder(h.f)

    for decoder.More() {
        if h.isCancelled() {
            return result, context.Canceled
        }
        select {
        case <-ctx.Done():
            return result, ctx.Err()
        default:
        }

        var msg streamMessage
        if err := decoder.Decode(&msg); err != nil {
            // On decode error, surface as Stderr; drain remainder.
            rest, _ := io.ReadAll(decoder.Buffered())
            extra, _ := io.ReadAll(h.f)
            result.Stdout = string(rest) + string(extra)
            if !errors.Is(err, io.EOF) {
                result.Stderr = err.Error()
            }
            return result, nil
        }

        switch msg.Type {
        case "finding":
            result.Findings = append(result.Findings, core.Finding{
                ID:       core.NewID(),
                Path:     msg.Path,
                Line:     msg.Line,
                Severity: core.Severity(msg.Severity),
                Body:     msg.Body,
            })
        case "done":
            result.ExitCode = msg.ExitCode
        }

        if h.delay > 0 {
            select {
            case <-time.After(h.delay):
            case <-ctx.Done():
                return result, ctx.Err()
            }
        }
    }
    return result, nil
}

func (h *replayHandle) Cancel() error {
    h.markCancelled()
    return nil
}

// Compile-time assertion that ReplayAgent satisfies core.Agent.
var _ core.Agent = (*ReplayAgent)(nil)
```

- [ ] **Step 2 — `agent/replay_agent_test.go`:**

Tests:
1. **Happy path**: write a temp transcript with 2 findings + 1 done; `Dispatch` + `Wait` returns `JobResult{Findings: 2, ExitCode: 0}`.
2. **Missing transcript**: `Dispatch` returns an error containing the constructed path.
3. **Role with dots**: `job.Role = "reviewer.arch"` looks up `reviewer_arch.jsonl`.
4. **Cancellation mid-stream**: cancel context after first line; `Wait` returns the partial result with `ctx.Err()`.
5. **`Cancel()` mid-stream**: same outcome.
6. **Malformed JSON line**: third line is invalid JSON; `Wait` returns the parsed first two findings + non-empty `Stderr`, `nil` error.
7. **`LineDelay` honored**: 50ms × 5 lines >= 200ms total.
8. **Empty transcript**: zero findings, no error.

- [ ] **Step 3 — Run tests + commit:**

```bash
go test ./agent -count=1 -run TestReplayAgent
git add agent/replay_agent.go agent/replay_agent_test.go
git commit -m "Plan 120: ReplayAgent — replay recorded stream-json transcripts as core.Agent"
```

---

## Phase 2 — First replay scenario (developer → reviewer.arch)

**Files:**
- Create:
  - `tests/replay/developer_then_reviewer/transcripts/developer.jsonl`
  - `tests/replay/developer_then_reviewer/transcripts/reviewer_arch.jsonl`
  - `tests/replay/developer_then_reviewer/expected.json`
  - `tests/replay/developer_then_reviewer/replay_test.go`
  - `tests/replay/doc.go` (top-level package marker)
  - `tests/replay/developer_then_reviewer/inputs/plan.md` (used as `plan_path` template input)
  - `tests/replay/developer_then_reviewer/inputs/diff.patch` (used as `diff_path`)
  - `tests/replay/developer_then_reviewer/inputs/spec.md` (used as `spec_path`)

- [ ] **Step 1 — Transcripts (per-role):**

`transcripts/developer.jsonl`:
```jsonl
{"type":"done","exit_code":0}
```
(Developer outputs no findings — it makes commits — so the simplest happy-path transcript is just "done".)

`transcripts/reviewer_arch.jsonl`:
```jsonl
{"type":"finding","path":"main.go","line":42,"severity":"important","body":"Missing error check on Close()"}
{"type":"finding","path":"store.go","line":17,"severity":"minor","body":"Consider prepared statement"}
{"type":"done","exit_code":0}
```

- [ ] **Step 2 — `expected.json`:**

```json
{
    "developer": {
        "exit_code": 0,
        "findings_count": 0
    },
    "reviewer.arch": {
        "exit_code": 0,
        "findings_count": 2,
        "fingerprints": [
            "main.go:42:important",
            "store.go:17:minor"
        ]
    }
}
```

- [ ] **Step 3 — `inputs/`:** trivial files, just so the prompts render. `inputs/plan.md` = `"plan content"`, `inputs/diff.patch` = `"--- a\n+++ b\n@@ ...\n"`, `inputs/spec.md` = `"spec content"`.

- [ ] **Step 4 — `replay_test.go`:**

```go
package developer_then_reviewer_test

import (
    "context"
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/chris/coworker/agent"
    "github.com/chris/coworker/coding"
    "github.com/chris/coworker/store"
)

// TestReplay_DeveloperThenReviewer runs the full dispatch pipeline using
// recorded transcripts. Asserts exit codes and findings match expected.json.
func TestReplay_DeveloperThenReviewer(t *testing.T) {
    if os.Getenv("COWORKER_REPLAY") != "1" {
        t.Skip("set COWORKER_REPLAY=1 to enable replay tests")
    }

    fixtureDir, err := filepath.Abs(".")
    if err != nil {
        t.Fatal(err)
    }
    transcriptsDir := filepath.Join(fixtureDir, "transcripts")
    inputsDir := filepath.Join(fixtureDir, "inputs")

    // Locate the repo root (two levels up from tests/replay/<scenario>/).
    repoRoot, err := filepath.Abs(filepath.Join(fixtureDir, "..", "..", ".."))
    if err != nil {
        t.Fatal(err)
    }
    roleDir := filepath.Join(repoRoot, "coding", "roles")
    // Role YAML's prompt_template is "prompts/<file>.md" relative to a
    // PromptDir that contains a "prompts/" subdirectory. So PromptDir is
    // "<repo>/coding" (not "<repo>/coding/prompts").
    promptDir := filepath.Join(repoRoot, "coding")

    db, dbCleanup := newReplayDB(t)
    defer dbCleanup()

    expected := loadExpected(t, fixtureDir)

    replay := &agent.ReplayAgent{TranscriptDir: transcriptsDir}

    // --- Developer: dispatch + assert ---
    devDisp := &coding.Dispatcher{
        Agent:     replay,
        DB:        db,
        RoleDir:   roleDir,
        PromptDir: promptDir,
    }
    devOut, err := devDisp.Orchestrate(context.Background(), &coding.DispatchInput{
        RoleName: "developer",
        Inputs: map[string]string{
            "plan_path":       filepath.Join(inputsDir, "plan.md"),
            "phase_index":     "1",
            "run_context_ref": "rep-1",
        },
    })
    if err != nil {
        t.Fatalf("developer dispatch: %v", err)
    }
    assertDispatch(t, "developer", devOut, expected["developer"])

    // --- Reviewer.arch: dispatch + assert ---
    revDisp := &coding.Dispatcher{
        Agent:     replay,
        DB:        db,
        RoleDir:   roleDir,
        PromptDir: promptDir,
    }
    revOut, err := revDisp.Orchestrate(context.Background(), &coding.DispatchInput{
        RoleName: "reviewer.arch",
        Inputs: map[string]string{
            "diff_path": filepath.Join(inputsDir, "diff.patch"),
            "spec_path": filepath.Join(inputsDir, "spec.md"),
        },
    })
    if err != nil {
        t.Fatalf("reviewer.arch dispatch: %v", err)
    }
    assertDispatch(t, "reviewer.arch", revOut, expected["reviewer.arch"])

    // Verify findings persisted to the store for the reviewer run.
    es := store.NewEventStore(db)
    fs := store.NewFindingStore(db, es)
    persisted, err := fs.ListFindings(context.Background(), revOut.RunID)
    if err != nil {
        t.Fatalf("ListFindings: %v", err)
    }
    if got, want := len(persisted), expected["reviewer.arch"].FindingsCount; got != want {
        t.Errorf("persisted findings = %d, want %d", got, want)
    }
}

type roleExpected struct {
    ExitCode      int      `json:"exit_code"`
    FindingsCount int      `json:"findings_count"`
    Fingerprints  []string `json:"fingerprints,omitempty"`
}

func loadExpected(t *testing.T, dir string) map[string]roleExpected {
    t.Helper()
    raw, err := os.ReadFile(filepath.Join(dir, "expected.json"))
    if err != nil {
        t.Fatal(err)
    }
    out := map[string]roleExpected{}
    if err := json.Unmarshal(raw, &out); err != nil {
        t.Fatal(err)
    }
    return out
}

func assertDispatch(t *testing.T, role string, got *coding.DispatchResult, want roleExpected) {
    t.Helper()
    if got.ExitCode != want.ExitCode {
        t.Errorf("%s: exit_code = %d, want %d", role, got.ExitCode, want.ExitCode)
    }
    if len(got.Findings) != want.FindingsCount {
        t.Errorf("%s: findings count = %d, want %d", role, len(got.Findings), want.FindingsCount)
    }
    if want.Fingerprints != nil {
        gotFps := make([]string, 0, len(got.Findings))
        for _, f := range got.Findings {
            gotFps = append(gotFps, fmt.Sprintf("%s:%d:%s", f.Path, f.Line, f.Severity))
        }
        if got, want := strings.Join(gotFps, ","), strings.Join(want.Fingerprints, ","); got != want {
            t.Errorf("%s: fingerprints = %q, want %q", role, got, want)
        }
    }
}

// newReplayDB opens a fresh in-memory SQLite DB with all migrations
// applied. The store package exposes Open(path string) (*DB, error) which
// auto-runs migrations.
func newReplayDB(t *testing.T) (*store.DB, func()) {
    t.Helper()
    db, err := store.Open(":memory:")
    if err != nil {
        t.Fatal(err)
    }
    return db, func() { _ = db.Close() }
}
```

(Adjust import names to match the actual `store.NewDB` signature; use the existing test-DB helper in `store/db_test.go` if it is exported, or duplicate its logic.)

- [ ] **Step 5 — Verify the test passes:**

```bash
COWORKER_REPLAY=1 go test ./tests/replay/... -count=1
```

Expected: PASS. Without `COWORKER_REPLAY=1`, the test is skipped.

- [ ] **Step 6 — Commit:**

```bash
git add tests/replay/
git commit -m "Plan 120: replay scenario — developer + reviewer.arch transcripts"
```

---

## Phase 3 — Live test scaffold (build-tag + env gated)

**Files:**
- Create: `tests/live/doc.go`, `tests/live/helpers.go`, `tests/live/claude_smoke_test.go`, `tests/live/codex_smoke_test.go`, `tests/live/opencode_smoke_test.go`

- [ ] **Step 1 — `tests/live/doc.go`:**

```go
//go:build live

// Package live contains end-to-end smoke tests that invoke real CLI
// agents (Claude Code, Codex, OpenCode). Tests skip unless
// COWORKER_LIVE=1 is set in the environment AND the live build tag is
// enabled. Run with:
//
//     COWORKER_LIVE=1 go test -tags live ./tests/live/...
//
// Each test should consume <1 second of CLI time and well under
// $0.50 of provider cost. Cost is not currently asserted (Dispatcher
// has no CostWriter wiring yet — see Plan 121); instead, tests use
// trivial prompts and short timeouts.
package live
```

- [ ] **Step 2 — `tests/live/helpers.go`:**

```go
//go:build live

package live

import (
    "context"
    "encoding/json"
    "os"
    "os/exec"
    "strconv"
    "strings"
    "testing"
    "time"
)

func requireLiveEnv(t *testing.T) {
    t.Helper()
    if os.Getenv("COWORKER_LIVE") != "1" {
        t.Skip("set COWORKER_LIVE=1 to enable live agent tests")
    }
}

func requireBinary(t *testing.T, name string) string {
    t.Helper()
    path, err := exec.LookPath(name)
    if err != nil {
        t.Skipf("required binary %q not found on PATH: %v", name, err)
    }
    return path
}

// budgetUSD returns the per-test budget in USD, defaulting to 0.50.
// Currently documented but not enforced (cost wiring is Plan 121).
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

func withTimeout(t *testing.T, d time.Duration) (context.Context, context.CancelFunc) {
    t.Helper()
    return context.WithTimeout(context.Background(), d)
}

// hasJSONLine returns true if any line in s parses as a JSON object
// containing the given top-level key. Useful for asserting CLIs emitted
// at least one stream-json event of the expected shape.
func hasJSONLine(s, requireKey string) bool {
    for _, line := range strings.Split(s, "\n") {
        line = strings.TrimSpace(line)
        if line == "" {
            continue
        }
        var m map[string]any
        if err := json.Unmarshal([]byte(line), &m); err != nil {
            continue
        }
        if _, ok := m[requireKey]; ok {
            return true
        }
    }
    return false
}
```

- [ ] **Step 3 — `tests/live/codex_smoke_test.go`:**

```go
//go:build live

package live

import (
    "bytes"
    "os/exec"
    "testing"
    "time"
)

// TestLive_Codex_Smoke verifies a fresh codex invocation completes within
// the budget timeout and emits stream-json on stdout.
func TestLive_Codex_Smoke(t *testing.T) {
    requireLiveEnv(t)
    bin := requireBinary(t, "codex")

    ctx, cancel := withTimeout(t, 60*time.Second)
    defer cancel()

    // Trivial prompt; codex's stream-json mode emits JSONL on stdout.
    cmd := exec.CommandContext(ctx, bin, "exec", "--json")
    cmd.Stdin = bytesNewReader(`Print one line: {"type":"done","exit_code":0}`)
    var stdout, stderr bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Stderr = &stderr

    if err := cmd.Run(); err != nil {
        t.Fatalf("codex run: %v\nstderr: %s", err, stderr.String())
    }
    if !hasJSONLine(stdout.String(), "type") {
        t.Errorf("codex emitted no JSON line with 'type' key.\nstdout: %s\nstderr: %s",
            stdout.String(), stderr.String())
    }
}

func bytesNewReader(s string) *bytes.Reader { return bytes.NewReader([]byte(s)) }
```

(Claude Code and OpenCode follow the same pattern with their respective binary names: `claude` and `opencode`. Each smoke test is one file. Adjust the invocation flags to match each CLI's `--output-format stream-json` equivalent. Find the right invocation in existing code: `agent/cli_agent.go` has the args, or read the CLI plugin docs.)

- [ ] **Step 4 — Sanity-check the build tag wiring:**

```bash
# Default `go test ./...` from the repo root must NOT execute these tests
# and must exit 0 (the package is excluded by the build tag).
go test ./... -count=1 -timeout 60s
# Expected: PASS for all matched packages; tests/live/ is silently excluded.

# Targeting the tests/live/ package directly without the tag prints
# "[no test files]" or "build constraints exclude all Go files" and
# exits 0 — this is informational only, not an error.
go test ./tests/live/... -count=1

# With the tag but without COWORKER_LIVE, tests SKIP cleanly.
go test -tags live ./tests/live/... -count=1
# Expected: PASS (all SKIPped).

# With the env var but missing binaries, tests still SKIP.
COWORKER_LIVE=1 go test -tags live ./tests/live/... -count=1
# Expected: PASS (Skipf on missing binary).
```

- [ ] **Step 5 — Commit:**

```bash
git add tests/live/
git commit -m "Plan 120: live test scaffold — build-tag + env-var gated, per-CLI smoke tests"
```

---

## Phase 4 — Makefile + CI

**Files:**
- Modify: `Makefile`, `.github/workflows/ci.yml`
- Create: `.github/workflows/live-tests.yml`

- [ ] **Step 1 — `Makefile`:** add the four targets. Read the existing Makefile first to match its style.

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

- [ ] **Step 2 — `.github/workflows/ci.yml`:** add a step `Replay tests: run: make test-replay` after the existing `go test`.

- [ ] **Step 3 — `.github/workflows/live-tests.yml`:**

```yaml
name: Live agent tests
on:
  workflow_dispatch:

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

(`inputs.cli` was removed because `make test-live` runs the entire suite; reintroduce only when per-CLI selection is implemented.)

- [ ] **Step 4 — Verify locally:**

```bash
make test-replay   # passes
make test-live     # skips locally; passes
```

- [ ] **Step 5 — Commit:**

```bash
git add Makefile .github/workflows/
git commit -m "Plan 120: Makefile + CI workflows for replay and live tests"
```

---

## Phase 5 — Documentation

**Files:**
- Create: `docs/architecture/testing.md`
- Modify: `docs/architecture/decisions.md`

- [ ] **Step 1 — `docs/architecture/testing.md`:**

Describe the four test layers, when to use each, how to run each, and the replay-transcript format (per-role JSONL named after the role file with dots replaced by underscores).

Include a clear note: **live tests do not assert cost yet** (cost capture wiring is Plan 121). The `COWORKER_LIVE_BUDGET_USD` variable is reserved for the future; its current effect is the documented per-test cap *intent*.

Include the recipe for adding a new replay scenario:
1. Run the runtime end-to-end with `COWORKER_RECORD_TRANSCRIPTS=1` (when implemented; Plan 121).
2. Copy `<run-id>/transcripts/` into `tests/replay/<scenario>/transcripts/`.
3. Capture findings into `expected.json`.
4. Add a `replay_test.go` calling `Dispatcher.Orchestrate` for each role.

- [ ] **Step 2 — `docs/architecture/decisions.md`:** append entry summarizing:
- Four test layers (unit / integration-with-mocks / replay / live).
- Build-tag `live` excludes live files from default `go test ./...`.
- Replay transcripts are per-role JSONL keyed by `<role>.jsonl` with dots → underscores (matching role-file naming).
- Live tests do NOT assert cost yet; cost wiring is Plan 121.

- [ ] **Step 3 — Commit:**

```bash
git add docs/architecture/testing.md docs/architecture/decisions.md
git commit -m "Plan 120: docs/architecture/testing.md describing the four test layers"
```

---

## Phase 6 — Full verification

- [ ] **Step 1 — Default suite:**

```bash
go build ./...
go test -race ./... -count=1 -timeout 180s
golangci-lint run ./...
```

Expected: build clean, all tests pass with `-race`, 0 lint issues. Replay tests skip (no env var set).

- [ ] **Step 2 — Replay suite:**

```bash
make test-replay
```

Expected: replay scenarios PASS.

- [ ] **Step 3 — Live suite (skip OK):**

```bash
make test-live
```

Expected: tests skip when binaries are missing; no failures.

---

## Self-Review Checklist

- [ ] `ReplayAgent` has compile-time `var _ core.Agent = (*ReplayAgent)(nil)`.
- [ ] `ReplayAgent.Dispatch` matches `core.Agent.Dispatch(ctx, *core.Job, prompt)` signature.
- [ ] `replayHandle.Wait` matches `core.JobHandle.Wait(ctx) (*core.JobResult, error)`.
- [ ] Replay agent uses the SAME `streamMessage` schema as `cli_handle.go` (kept in lockstep — if `cli_handle.go` adds new fields, replay must be updated).
- [ ] Cancellation is observed: ctx-cancel and `Cancel()` both cause `Wait` to return promptly with the partial result.
- [ ] Replay scenarios use real role names: `developer`, `reviewer.arch`. Transcripts are in `transcripts/<role-with-underscores>.jsonl`.
- [ ] Replay test calls `Dispatcher.Orchestrate` correctly: with `RoleDir`, `PromptDir`, all required role inputs.
- [ ] Replay test checks both the returned `DispatchResult.Findings` AND `FindingStore.ListFindings` (persistence path).
- [ ] Live tests are protected by build tag `live` AND env var `COWORKER_LIVE=1`. Default `go test ./...` does not list them.
- [ ] No live test asserts `cost_events` rows (out of scope; documented).
- [ ] Makefile targets are self-contained.
- [ ] CI: replay added to default workflow; live tests in a separate manual workflow.
- [ ] `docs/architecture/testing.md` documents recording flow.

---

## Code Review

(To be filled in after implementation by Codex review subagent.)

---

## Post-Execution Report

(To be filled in after implementation.)
