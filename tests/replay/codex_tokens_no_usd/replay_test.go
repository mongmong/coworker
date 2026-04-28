package codex_tokens_no_usd_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/store"
)

// TestReplay_CodexTokensNoUSD verifies that a Codex `turn.completed` event
// populates token counts but leaves usd at 0 (Codex doesn't emit USD;
// per-model price-table conversion is deferred). Plan 132 (B7).
func TestReplay_CodexTokensNoUSD(t *testing.T) {
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
	fs := store.NewFindingStore(db, es)

	d := &coding.Dispatcher{
		Agent:      &agent.ReplayAgent{TranscriptDir: filepath.Join(fixtureDir, "transcripts")},
		DB:         db,
		RoleDir:    filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:  filepath.Join(repoRoot, "coding"),
		CostWriter: ce,
	}

	res, err := d.Orchestrate(context.Background(), &coding.DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": filepath.Join(fixtureDir, "inputs", "diff.patch"),
			"spec_path": filepath.Join(fixtureDir, "inputs", "spec.md"),
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	// Two findings persisted.
	findings, err := fs.ListFindings(context.Background(), res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Errorf("findings = %d, want 2", len(findings))
	}

	// Cost row: tokens populated, USD=0.
	rows, err := ce.ListByJob(context.Background(), res.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("cost rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Provider != "openai" {
		t.Errorf("provider = %q, want openai", r.Provider)
	}
	if r.USD != 0 {
		t.Errorf("usd = %v, want 0 (Codex does not emit USD)", r.USD)
	}
	if r.TokensIn != 54000+12000 {
		t.Errorf("tokens_in = %d, want %d (input + cached_input)", r.TokensIn, 54000+12000)
	}
	if r.TokensOut != 820 {
		t.Errorf("tokens_out = %d, want 820", r.TokensOut)
	}
}
