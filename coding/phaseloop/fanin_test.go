package phaseloop

import (
	"testing"

	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/core"
)

// --- DedupeFindings tests ---

func TestDedupeFindings_Empty(t *testing.T) {
	got := DedupeFindings(nil)
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d findings", len(got))
	}
}

func TestDedupeFindings_SingleFinding(t *testing.T) {
	f := core.Finding{
		ID:       "1",
		JobID:    "job-1",
		Path:     "foo.go",
		Line:     10,
		Severity: core.SeverityMinor,
		Body:     "unused variable",
	}
	got := DedupeFindings([]core.Finding{f})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(got))
	}
	if len(got[0].SourceJobIDs) != 1 || got[0].SourceJobIDs[0] != "job-1" {
		t.Errorf("unexpected SourceJobIDs: %v", got[0].SourceJobIDs)
	}
}

func TestDedupeFindings_DifferentFingerprints(t *testing.T) {
	findings := []core.Finding{
		{ID: "1", JobID: "job-1", Path: "a.go", Line: 1, Severity: core.SeverityMinor, Body: "issue A"},
		{ID: "2", JobID: "job-2", Path: "b.go", Line: 2, Severity: core.SeverityImportant, Body: "issue B"},
		{ID: "3", JobID: "job-3", Path: "c.go", Line: 3, Severity: core.SeverityCritical, Body: "issue C"},
	}
	got := DedupeFindings(findings)
	if len(got) != 3 {
		t.Fatalf("expected 3 findings, got %d", len(got))
	}
}

func TestDedupeFindings_SameFingerprint(t *testing.T) {
	// Two findings with the same path/line/severity/body → same fingerprint.
	f1 := core.Finding{ID: "1", JobID: "job-1", Path: "foo.go", Line: 5, Severity: core.SeverityImportant, Body: "same issue"}
	f2 := core.Finding{ID: "2", JobID: "job-2", Path: "foo.go", Line: 5, Severity: core.SeverityImportant, Body: "same issue"}

	got := DedupeFindings([]core.Finding{f1, f2})
	if len(got) != 1 {
		t.Fatalf("expected 1 deduplicated finding, got %d", len(got))
	}
	if got[0].ID != "1" {
		t.Errorf("expected canonical finding ID=1, got %q", got[0].ID)
	}
	// Both job IDs should be preserved.
	if len(got[0].SourceJobIDs) != 2 {
		t.Errorf("expected 2 source job IDs, got %v", got[0].SourceJobIDs)
	}
}

func TestDedupeFindings_MixedDuplicates(t *testing.T) {
	// 3 findings: A is unique, B appears twice, C is unique.
	a := core.Finding{ID: "a", JobID: "job-1", Path: "a.go", Line: 1, Severity: core.SeverityMinor, Body: "a"}
	b1 := core.Finding{ID: "b1", JobID: "job-2", Path: "b.go", Line: 2, Severity: core.SeverityMinor, Body: "b"}
	b2 := core.Finding{ID: "b2", JobID: "job-3", Path: "b.go", Line: 2, Severity: core.SeverityMinor, Body: "b"}
	c := core.Finding{ID: "c", JobID: "job-4", Path: "c.go", Line: 3, Severity: core.SeverityMinor, Body: "c"}

	got := DedupeFindings([]core.Finding{a, b1, b2, c})
	if len(got) != 3 {
		t.Fatalf("expected 3 findings, got %d", len(got))
	}
	// Verify B has 2 source job IDs.
	var bFound bool
	for _, f := range got {
		if f.ID == "b1" {
			bFound = true
			if len(f.SourceJobIDs) != 2 {
				t.Errorf("expected 2 source job IDs for B, got %v", f.SourceJobIDs)
			}
		}
	}
	if !bFound {
		t.Error("canonical finding b1 not found in output")
	}
}

func TestDedupeFindings_PrecomputedFingerprint(t *testing.T) {
	// If Fingerprint is already set, DedupeFindings should use it directly.
	fp := core.ComputeFingerprint("x.go", 7, core.SeverityNit, "nit")
	f1 := core.Finding{ID: "1", JobID: "j1", Fingerprint: fp}
	f2 := core.Finding{ID: "2", JobID: "j2", Fingerprint: fp}

	got := DedupeFindings([]core.Finding{f1, f2})
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
}

func TestDedupeFindings_PreservesOrder(t *testing.T) {
	// First occurrence of each fingerprint should be the canonical one.
	findings := make([]core.Finding, 0)
	for i := 0; i < 5; i++ {
		findings = append(findings, core.Finding{
			ID:       string(rune('A' + i)),
			JobID:    "j",
			Path:     string(rune('a'+i)) + ".go",
			Line:     i,
			Severity: core.SeverityMinor,
			Body:     "unique",
		})
	}
	// Add duplicates of first and third.
	findings = append(findings, core.Finding{
		ID: "dup-0", JobID: "j2",
		Path: "a.go", Line: 0, Severity: core.SeverityMinor, Body: "unique",
	})
	findings = append(findings, core.Finding{
		ID: "dup-2", JobID: "j3",
		Path: "c.go", Line: 2, Severity: core.SeverityMinor, Body: "unique",
	})

	got := DedupeFindings(findings)
	if len(got) != 5 {
		t.Fatalf("expected 5, got %d", len(got))
	}
	if got[0].ID != "A" {
		t.Errorf("expected first finding to be A, got %q", got[0].ID)
	}
}

// --- AggregateResults tests ---

func TestAggregateResults_Empty(t *testing.T) {
	got := AggregateResults(nil)
	if !got.TestsPassed {
		t.Error("empty aggregate should have TestsPassed=true (vacuous pass)")
	}
	if len(got.Findings) != 0 {
		t.Error("expected no findings")
	}
}

func TestAggregateResults_AllPass(t *testing.T) {
	results := []*coding.DispatchResult{
		{ExitCode: 0, Findings: []core.Finding{{ID: "f1"}}},
		{ExitCode: 0, Findings: []core.Finding{{ID: "f2"}}},
	}
	got := AggregateResults(results)
	if !got.TestsPassed {
		t.Error("expected TestsPassed=true when all exit codes are 0")
	}
	if len(got.Findings) != 2 {
		t.Errorf("expected 2 findings, got %d", len(got.Findings))
	}
}

func TestAggregateResults_OneFails(t *testing.T) {
	results := []*coding.DispatchResult{
		{ExitCode: 0},
		{ExitCode: 1},
		{ExitCode: 0},
	}
	got := AggregateResults(results)
	if got.TestsPassed {
		t.Error("expected TestsPassed=false when any exit code is non-zero")
	}
}

func TestAggregateResults_ArtifactsMerged(t *testing.T) {
	results := []*coding.DispatchResult{
		{ExitCode: 0, Artifacts: []core.Artifact{{ID: "a1", Path: "p1"}}},
		{ExitCode: 0, Artifacts: []core.Artifact{{ID: "a2", Path: "p2"}, {ID: "a3", Path: "p3"}}},
	}
	got := AggregateResults(results)
	if len(got.Artifacts) != 3 {
		t.Errorf("expected 3 artifacts, got %d", len(got.Artifacts))
	}
}
