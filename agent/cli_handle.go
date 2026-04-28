package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"

	"github.com/chris/coworker/core"
)

// cliJobHandle wraps an exec.Cmd to implement core.JobHandle.
type cliJobHandle struct {
	cmd         *exec.Cmd
	stdout      io.ReadCloser
	stderr      io.ReadCloser
	job         *core.Job
	coworkerDir string
}

// streamMessage represents one line of the stream-JSON output from a CLI agent.
//
// Finding/done fields are present for any agent that emits them. Cost-bearing
// fields populate from the optional Claude `result` event and Codex
// `turn.completed` event; populateCost (cost_helpers.go) extracts the data.
type streamMessage struct {
	Type     string `json:"type"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity,omitempty"`
	Body     string `json:"body,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`

	// Artifact fields (Plan 139, Codex CRITICAL #1). When type=="artifact",
	// Kind and Path are required. Used by architect (kind=spec / kind=manifest)
	// and any future role that emits durable file artifacts.
	Kind string `json:"kind,omitempty"`

	// Cost-bearing fields. Plan 121.
	TotalCostUSD float64                  `json:"total_cost_usd,omitempty"`
	Usage        *streamUsage             `json:"usage,omitempty"`
	ModelUsage   map[string]modelUsageRow `json:"modelUsage,omitempty"`
}

// streamUsage is the union of token-count fields emitted by Claude and Codex.
// Only the relevant subset is non-zero for any one CLI:
//
//   - Claude: InputTokens, OutputTokens, CacheReadInputTokens.
//   - Codex:  InputTokens, OutputTokens, CachedInputTokens.
type streamUsage struct {
	InputTokens          int `json:"input_tokens,omitempty"`
	OutputTokens         int `json:"output_tokens,omitempty"`
	CacheReadInputTokens int `json:"cache_read_input_tokens,omitempty"`
	CachedInputTokens    int `json:"cached_input_tokens,omitempty"`
}

// modelUsageRow is one entry in Claude's `modelUsage` map. Used only to
// extract the model identifier (the key) and verify there is data present.
type modelUsageRow struct {
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CostUSD      float64 `json:"costUSD"`
}

// Wait blocks until the CLI process completes, parsing stream-JSON stdout
// into findings. Implements core.JobHandle.
func (h *cliJobHandle) Wait(ctx context.Context) (*core.JobResult, error) {
	result := &core.JobResult{}

	// Open the per-job JSONL log file and tee every byte read from stdout into it.
	logFile, logErr := OpenJobLog(h.coworkerDir, h.job.RunID, h.job.ID)
	if logErr != nil {
		// Non-fatal: log the error and proceed without persistence.
		_ = logErr
		logFile = nopCloser{io.Discard}
	}
	defer logFile.Close()

	// Parse stream-JSON from stdout using json.Decoder.
	// io.TeeReader ensures raw bytes are also written to the log file.
	decoder := json.NewDecoder(io.TeeReader(h.stdout, logFile))
	for decoder.More() {
		var msg streamMessage
		if err := decoder.Decode(&msg); err != nil {
			// If we hit a decode error, read the rest as raw stdout.
			remaining, _ := io.ReadAll(decoder.Buffered())
			rest, _ := io.ReadAll(h.stdout)
			result.Stdout = string(remaining) + string(rest)
			break
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
		case "artifact":
			// Plan 139 (Codex CRITICAL #1): roles that produce durable
			// file artifacts (architect → spec + manifest, planner →
			// plan files) emit `{"type":"artifact","kind":"<kind>","path":"<path>"}`
			// events. The dispatcher persists these via ArtifactStore.
			if msg.Kind != "" && msg.Path != "" {
				result.Artifacts = append(result.Artifacts, core.Artifact{
					ID:   core.NewID(),
					Kind: msg.Kind,
					Path: msg.Path,
				})
			}
		case "done":
			result.ExitCode = msg.ExitCode
		}
		// Plan 121: extract cost from result/turn.completed events.
		populateCost(msg, result)
	}

	// Read any remaining stdout.
	if remaining, err := io.ReadAll(h.stdout); err == nil && len(remaining) > 0 {
		result.Stdout += string(remaining)
	}

	// Read stderr.
	result.Stderr = stderrReader(h.stderr)

	// Wait for the process to exit.
	if err := h.cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return result, fmt.Errorf("wait for CLI: %w", err)
		}
	}

	return result, nil
}

// Cancel kills the running CLI process.
func (h *cliJobHandle) Cancel() error {
	if h.cmd.Process == nil {
		return nil
	}
	return h.cmd.Process.Kill()
}
