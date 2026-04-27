//go:build live

package live

import (
	"bytes"
	"os/exec"
	"testing"
	"time"
)

// TestLive_Claude_Smoke verifies that a fresh claude (Claude Code)
// invocation completes within the budget timeout and emits at least
// one stream-json line on stdout. Skipped unless COWORKER_LIVE=1 and
// the claude binary is on PATH.
func TestLive_Claude_Smoke(t *testing.T) {
	requireLiveEnv(t)
	bin := requireBinary(t, "claude")

	ctx, cancel := withTimeout(t, 60*time.Second)
	defer cancel()

	// Use Claude Code's headless mode with stream-json output. Pass a
	// trivial prompt on the command line so we can keep stdin closed
	// (Claude reads --print prompts from argv).
	cmd := exec.CommandContext(ctx, bin, "-p",
		`Print one stream-json line: {"type":"done","exit_code":0}`,
		"--output-format", "stream-json",
		"--verbose")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("claude run: %v\nstderr: %s", err, stderr.String())
	}
	if !hasJSONLine(stdout.String(), "type") {
		t.Errorf("claude emitted no JSON line with 'type' key.\nstdout: %s\nstderr: %s",
			stdout.String(), stderr.String())
	}
}
