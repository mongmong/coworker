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
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr io.ReadCloser
	job    *core.Job
}

// streamMessage represents one line of the stream-JSON output from a CLI agent.
type streamMessage struct {
	Type     string `json:"type"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity,omitempty"`
	Body     string `json:"body,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// Wait blocks until the CLI process completes, parsing stream-JSON stdout
// into findings. Implements core.JobHandle.
func (h *cliJobHandle) Wait(ctx context.Context) (*core.JobResult, error) {
	result := &core.JobResult{}

	// Parse stream-JSON from stdout using json.Decoder.
	decoder := json.NewDecoder(h.stdout)
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
		case "done":
			result.ExitCode = msg.ExitCode
		}
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
