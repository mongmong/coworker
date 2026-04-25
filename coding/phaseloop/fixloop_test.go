package phaseloop

import (
	"strings"
	"testing"

	"github.com/chris/coworker/core"
)

func TestBuildFindingFeedback_Empty(t *testing.T) {
	got := BuildFindingFeedback(nil)
	if got != "" {
		t.Errorf("expected empty string for no findings, got %q", got)
	}
}

func TestBuildFindingFeedback_EmptySlice(t *testing.T) {
	got := BuildFindingFeedback([]core.Finding{})
	if got != "" {
		t.Errorf("expected empty string for empty slice, got %q", got)
	}
}

func TestBuildFindingFeedback_SingleFinding(t *testing.T) {
	f := core.Finding{
		Path:     "pkg/foo.go",
		Line:     42,
		Severity: core.SeverityImportant,
		Body:     "exported function lacks documentation",
	}
	got := BuildFindingFeedback([]core.Finding{f})
	if !strings.Contains(got, "REVIEW FEEDBACK") {
		t.Error("expected REVIEW FEEDBACK header")
	}
	if !strings.Contains(got, "important") {
		t.Error("expected severity 'important' in output")
	}
	if !strings.Contains(got, "pkg/foo.go:42") {
		t.Error("expected path:line in output")
	}
	if !strings.Contains(got, "exported function lacks documentation") {
		t.Error("expected finding body in output")
	}
}

func TestBuildFindingFeedback_MultipleFindings(t *testing.T) {
	findings := []core.Finding{
		{Path: "a.go", Line: 1, Severity: core.SeverityCritical, Body: "nil dereference"},
		{Path: "b.go", Line: 2, Severity: core.SeverityMinor, Body: "unused import"},
		{Path: "c.go", Line: 3, Severity: core.SeverityNit, Body: "trailing newline"},
	}
	got := BuildFindingFeedback(findings)
	// Should contain numbered items.
	if !strings.Contains(got, "1.") {
		t.Error("expected numbered item 1")
	}
	if !strings.Contains(got, "2.") {
		t.Error("expected numbered item 2")
	}
	if !strings.Contains(got, "3.") {
		t.Error("expected numbered item 3")
	}
	if !strings.Contains(got, "nil dereference") {
		t.Error("expected finding body 'nil dereference'")
	}
	if !strings.Contains(got, "Please address all findings") {
		t.Error("expected closing instruction")
	}
}

func TestBuildFindingFeedback_MultipleSourceJobs(t *testing.T) {
	f := core.Finding{
		Path:         "core/run.go",
		Line:         10,
		Severity:     core.SeverityImportant,
		Body:         "missing error check",
		SourceJobIDs: []string{"job-1", "job-2", "job-3"},
	}
	got := BuildFindingFeedback([]core.Finding{f})
	if !strings.Contains(got, "3 reviewers") {
		t.Errorf("expected '3 reviewers' annotation, got: %s", got)
	}
}

func TestBuildFindingFeedback_NoPathOrLine(t *testing.T) {
	f := core.Finding{
		Severity: core.SeverityMinor,
		Body:     "general observation",
	}
	got := BuildFindingFeedback([]core.Finding{f})
	if !strings.Contains(got, "general observation") {
		t.Error("expected finding body in output")
	}
	// Should not crash or produce "at :" when path and line are zero.
	if strings.Contains(got, "at :") {
		t.Errorf("should not emit 'at :' when path is empty: %s", got)
	}
}

func TestMaxFixCycles_Default(t *testing.T) {
	if got := maxFixCycles(nil); got != DefaultMaxFixCycles {
		t.Errorf("expected %d, got %d", DefaultMaxFixCycles, got)
	}
}

func TestMaxFixCycles_FromPolicy(t *testing.T) {
	p := &core.Policy{
		SupervisorLimits: core.SupervisorLimits{MaxFixCyclesPerPhase: 3},
	}
	if got := maxFixCycles(p); got != 3 {
		t.Errorf("expected 3, got %d", got)
	}
}

func TestMaxFixCycles_ZeroFallback(t *testing.T) {
	p := &core.Policy{
		SupervisorLimits: core.SupervisorLimits{MaxFixCyclesPerPhase: 0},
	}
	if got := maxFixCycles(p); got != DefaultMaxFixCycles {
		t.Errorf("expected default %d, got %d", DefaultMaxFixCycles, got)
	}
}
