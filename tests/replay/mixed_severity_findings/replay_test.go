package mixed_severity_findings_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// TestReplay_MixedSeverityFindings verifies that findings of different
// severities (critical / important / minor / nit) all round-trip through
// the parser and persistence layer with their severity preserved.
// Plan 132 (B7).
func TestReplay_MixedSeverityFindings(t *testing.T) {
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
	fs := store.NewFindingStore(db, es)

	d := &coding.Dispatcher{
		Agent:     &agent.ReplayAgent{TranscriptDir: filepath.Join(fixtureDir, "transcripts")},
		DB:        db,
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
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

	findings, err := fs.ListFindings(context.Background(), res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 4 {
		t.Fatalf("findings = %d, want 4", len(findings))
	}

	// Build a severity histogram and verify all four severities are present.
	got := map[core.Severity]int{}
	for _, f := range findings {
		got[f.Severity]++
	}
	want := map[core.Severity]int{
		core.SeverityCritical:  1,
		core.SeverityImportant: 1,
		core.SeverityMinor:     1,
		core.SeverityNit:       1,
	}
	for sev, expected := range want {
		if got[sev] != expected {
			t.Errorf("severity %q count = %d, want %d (full: %v)", sev, got[sev], expected, got)
		}
	}

	// reviewer.arch should attribute reviewer_handle on every finding.
	for _, f := range findings {
		if f.ReviewerHandle != "reviewer.arch" {
			t.Errorf("ReviewerHandle = %q, want reviewer.arch (finding: %s:%d)",
				f.ReviewerHandle, f.Path, f.Line)
		}
	}
}
