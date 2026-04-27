//go:build live

package live

import (
	"bytes"
	"os/exec"
	"testing"
	"time"
)

// TestLive_OpenCode_Smoke verifies that a fresh opencode invocation
// completes within the budget timeout and emits at least one stream-json
// line on stdout. Skipped unless COWORKER_LIVE=1 and the opencode binary
// is on PATH.
//
// FUTURE: budget enforcement via cost_events is not active for opencode —
// the SSE stream this codebase consumes does not surface token or cost
// data. See Plan 121 §Out of Scope.
func TestLive_OpenCode_Smoke(t *testing.T) {
	requireLiveEnv(t)
	bin := requireBinary(t, "opencode")

	ctx, cancel := withTimeout(t, 60*time.Second)
	defer cancel()

	// Use opencode's run mode with stream-json output. Pipe the prompt
	// on stdin (opencode run accepts stdin like other CLIs).
	cmd := exec.CommandContext(ctx, bin, "run", "--format", "json")
	cmd.Stdin = bytes.NewReader([]byte(`Print one stream-json line: {"type":"done","exit_code":0}`))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("opencode run: %v\nstderr: %s", err, stderr.String())
	}
	if !hasJSONLine(stdout.String(), "type") {
		t.Errorf("opencode emitted no JSON line with 'type' key.\nstdout: %s\nstderr: %s",
			stdout.String(), stderr.String())
	}
}
