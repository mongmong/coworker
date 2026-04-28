# Adding a replay scenario

Replay scenarios drive `coding.Dispatcher.Orchestrate` end-to-end with a `ReplayAgent` that streams a recorded JSONL transcript instead of invoking a real CLI binary. They're fast, deterministic, free, and exercise the real parser + supervisor + persistence pipeline. See [testing.md](testing.md) for the four test layers.

This guide walks through adding a new scenario from scratch.

## When to add a scenario

Replay is the right test layer when you want to exercise:

- A specific stream-json shape (Claude `result` event, Codex `turn.completed`, malformed lines, mixed severities).
- Finding persistence + plan/phase/reviewer attribution.
- Cost capture for a particular CLI's output.

Replay is NOT the right layer for:

- Multi-role workflows (phase loop with developer + reviewer + tester) — needs PhaseExecutor wiring; tracked as a follow-up.
- Worker registry / heartbeat behavior — needs the MCP server.
- HTTP/SSE behavior — needs the daemon HTTP server.

For those, write integration tests under `tests/integration/` instead.

## 1. Create the scenario directory

```bash
mkdir -p tests/replay/<scenario-name>/{transcripts,inputs}
```

Scenario names use lowercase + underscores, matching the package convention: `multi_finding_dedup`, `claude_cost_capture`, etc.

## 2. Write the transcript(s)

Per-role JSONL files in `transcripts/`. Filename matches the role name with dots replaced by underscores: `developer.jsonl`, `reviewer_arch.jsonl`, `reviewer_frontend.jsonl`, etc. (`agent.ReplayAgent.Dispatch` does the replacement.)

Each line is one stream-json event. The shape matches `agent/cli_handle.go::streamMessage`:

```jsonl
{"type":"finding","path":"main.go","line":42,"severity":"important","body":"missing close"}
{"type":"finding","path":"util.go","line":7,"severity":"minor","body":"trailing space"}
{"type":"done","exit_code":0}
```

Optional cost-bearing tail (per CLI):

```jsonl
# Claude:
{"type":"result","total_cost_usd":0.0123,"usage":{"input_tokens":100,"output_tokens":50},"modelUsage":{"claude-opus-4-7":{"inputTokens":100,"outputTokens":50,"costUSD":0.0123}}}

# Codex (no USD; tokens only):
{"type":"turn.completed","usage":{"input_tokens":54000,"cached_input_tokens":12000,"output_tokens":820}}
```

## 3. Provide trivial input fixtures

Most roles require inputs (`plan_path`, `diff_path`, `spec_path`, etc.). Drop placeholder files into `inputs/`:

```bash
echo "# placeholder" > tests/replay/<scenario>/inputs/plan.md
echo "# placeholder" > tests/replay/<scenario>/inputs/spec.md
echo "# placeholder" > tests/replay/<scenario>/inputs/diff.patch
```

The content doesn't matter — the role's prompt template substitutes the path, and the ReplayAgent ignores the prompt entirely (it just streams the transcript).

## 4. Write the test

`tests/replay/<scenario-name>/replay_test.go`:

```go
package <scenario_name>_test

import (
    "context"
    "os"
    "path/filepath"
    "testing"

    "github.com/chris/coworker/agent"
    "github.com/chris/coworker/coding"
    "github.com/chris/coworker/store"
)

func TestReplay_<ScenarioName>(t *testing.T) {
    if os.Getenv("COWORKER_REPLAY") != "1" {
        t.Skip("set COWORKER_REPLAY=1 to enable replay tests")
    }
    fixtureDir, err := filepath.Abs(".")
    if err != nil {
        t.Fatal(err)
    }
    repoRoot, err := filepath.Abs(filepath.Join(fixtureDir, "..", "..", ".."))
    if err != nil {
        t.Fatal(err)
    }

    db, err := store.Open(":memory:")
    if err != nil {
        t.Fatal(err)
    }
    defer db.Close()

    d := &coding.Dispatcher{
        Agent:     &agent.ReplayAgent{TranscriptDir: filepath.Join(fixtureDir, "transcripts")},
        DB:        db,
        RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
        PromptDir: filepath.Join(repoRoot, "coding"),
        // Optional: CostWriter, SupervisorWriter, etc. for richer assertions.
    }

    res, err := d.Orchestrate(context.Background(), &coding.DispatchInput{
        RoleName: "<role-name>",
        Inputs: map[string]string{
            "<input1>": filepath.Join(fixtureDir, "inputs", "<file1>"),
            "<input2>": filepath.Join(fixtureDir, "inputs", "<file2>"),
        },
    })
    if err != nil {
        t.Fatalf("Orchestrate: %v", err)
    }

    // Assert what you care about: findings count, fingerprints, cost
    // rows, etc. Examples in tests/replay/{claude_cost_capture,
    // codex_tokens_no_usd, mixed_severity_findings}/replay_test.go.
    _ = res
}
```

The boilerplate is consistent across scenarios — copy from a similar existing scenario and adjust.

## 5. Run

```bash
COWORKER_REPLAY=1 go test ./tests/replay/<scenario-name>/... -count=1 -v
```

Scenarios skip silently when `COWORKER_REPLAY` is unset. CI runs them on every push (`make test-replay`).

## 6. Reference scenarios

- [`developer_then_reviewer/`](../../tests/replay/developer_then_reviewer/) — two roles, one cost row, finding persistence.
- [`claude_cost_capture/`](../../tests/replay/claude_cost_capture/) — Claude `result` event populates cost_events with USD.
- [`codex_tokens_no_usd/`](../../tests/replay/codex_tokens_no_usd/) — Codex `turn.completed` populates tokens; USD=0.
- [`mixed_severity_findings/`](../../tests/replay/mixed_severity_findings/) — all four severities round-trip with reviewer attribution.

## Tips

- **Determinism**: `created_at` is recorded at second resolution. If your scenario depends on ordering, sleep ≥1.1s between insertions or assert by content rather than recency.
- **Findings dedup**: identical fingerprints (path:line:severity:body) across multiple findings dedupe in the fan-in path. Use distinct bodies if you want N rows.
- **Cost wiring**: only set `Dispatcher.CostWriter` when you're asserting cost — otherwise `cost_events` stays empty.
- **Role attribution**: roles whose name starts with `reviewer.` get their name copied into `Finding.ReviewerHandle`. Plain `developer` doesn't.
