package claude_cost_capture_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/store"
)

// TestReplay_ClaudeCostCapture verifies that a Claude `result` event in a
// recorded transcript populates cost_events with total_cost_usd. Plan 132 (B7).
func TestReplay_ClaudeCostCapture(t *testing.T) {
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
	es := store.NewEventStore(db)
	ce := store.NewCostEventStore(db, es)

	d := &coding.Dispatcher{
		Agent:      &agent.ReplayAgent{TranscriptDir: filepath.Join(fixtureDir, "transcripts")},
		DB:         db,
		RoleDir:    filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:  filepath.Join(repoRoot, "coding"),
		CostWriter: ce,
	}

	res, err := d.Orchestrate(context.Background(), &coding.DispatchInput{
		RoleName: "developer",
		Inputs: map[string]string{
			"plan_path":       filepath.Join(fixtureDir, "inputs", "plan.md"),
			"phase_index":     "0",
			"run_context_ref": "claude-cost-1",
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	rows, err := ce.ListByJob(context.Background(), res.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("cost rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", r.Provider)
	}
	if r.USD != 0.087 {
		t.Errorf("usd = %v, want 0.087", r.USD)
	}
	// Tokens should accumulate input + cache read.
	if r.TokensIn != 120+4500 {
		t.Errorf("tokens_in = %d, want %d", r.TokensIn, 120+4500)
	}
	if r.TokensOut != 350 {
		t.Errorf("tokens_out = %d, want 350", r.TokensOut)
	}
	if r.Model != "claude-opus-4-7" {
		t.Errorf("model = %q, want claude-opus-4-7", r.Model)
	}
}
