//go:build live

package live

import (
	"bytes"
	"os/exec"
	"testing"
	"time"
)

// TestLive_Codex_Smoke verifies that a fresh codex invocation completes
// within the budget timeout and emits at least one stream-json line on
// stdout. Skipped unless COWORKER_LIVE=1 and the codex binary is on
// PATH.
func TestLive_Codex_Smoke(t *testing.T) {
	requireLiveEnv(t)
	bin := requireBinary(t, "codex")

	ctx, cancel := withTimeout(t, 60*time.Second)
	defer cancel()

	// Trivial prompt; codex's `exec --json` mode emits JSONL on stdout.
	cmd := exec.CommandContext(ctx, bin, "exec", "--json")
	cmd.Stdin = bytes.NewReader([]byte(`Print one stream-json line: {"type":"done","exit_code":0}`))
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
