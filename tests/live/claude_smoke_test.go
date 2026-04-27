//go:build live

package live

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/store"
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

// TestLive_Claude_BudgetGuard exercises the full dispatcher path with
// cost capture: invokes a real claude binary through CliAgent + a tiny
// smoke role, then asserts cost_events row count and budget compliance.
func TestLive_Claude_BudgetGuard(t *testing.T) {
	requireLiveEnv(t)
	bin := requireBinary(t, "claude")

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	es := store.NewEventStore(db)

	repoRoot := repoRootFromTest(t)
	smokeDir := filepath.Join(repoRoot, "tests", "live", "testdata")

	// CliAgent sends prompt on stdin; --print + --output-format stream-json
	// + --verbose is the headless mode that emits the result event.
	a := agent.NewCliAgent(bin,
		"--print",
		"--output-format", "stream-json",
		"--verbose")

	d := &coding.Dispatcher{
		Agent:      a,
		DB:         db,
		RoleDir:    filepath.Join(smokeDir, "roles"),
		PromptDir:  smokeDir,
		CostWriter: store.NewCostEventStore(db, es),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := d.Orchestrate(ctx, &coding.DispatchInput{
		RoleName: "smoke",
		Inputs: map[string]string{
			"prompt_text": `Print exactly this single line and stop: {"type":"done","exit_code":0}`,
		},
	})
	if err != nil {
		t.Fatalf("smoke dispatch: %v", err)
	}

	verifyCostUnderBudget(t, db, res.RunID, 1)
}
