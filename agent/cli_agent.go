package agent

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/chris/coworker/core"
)

// CliAgent dispatches jobs by shelling out to a CLI binary.
// It implements the core.Agent interface.
type CliAgent struct {
	// BinaryPath is the path to the CLI executable.
	BinaryPath string
	// Args are additional arguments passed to the CLI.
	Args []string
}

// NewCliAgent creates a CliAgent for the given binary path.
func NewCliAgent(binaryPath string, args ...string) *CliAgent {
	return &CliAgent{
		BinaryPath: binaryPath,
		Args:       args,
	}
}

// Dispatch starts a job by executing the CLI binary with the prompt
// on stdin. Returns a JobHandle to wait for the result.
func (a *CliAgent) Dispatch(ctx context.Context, job *core.Job, prompt string) (core.JobHandle, error) {
	cmd := exec.CommandContext(ctx, a.BinaryPath, a.Args...) //nolint:gosec // G204: CliAgent is designed to execute external CLI binaries
	cmd.Stdin = strings.NewReader(prompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", a.BinaryPath, err)
	}

	handle := &cliJobHandle{
		cmd:    cmd,
		stdout: stdout,
		stderr: stderr,
		job:    job,
	}

	return handle, nil
}

// stderrReader reads stderr fully into a string.
func stderrReader(r io.Reader) string {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Sprintf("<error reading stderr: %v>", err)
	}
	return string(data)
}
