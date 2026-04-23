package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/chris/coworker/core"
)

// --- ParseCheck tests ---

func TestParseCheck_ArrayArgs(t *testing.T) {
	fn, args, err := ParseCheck(`all_findings_have(["path", "line"])`)
	if err != nil {
		t.Fatalf("ParseCheck: %v", err)
	}
	if fn != "all_findings_have" {
		t.Errorf("funcName = %q, want %q", fn, "all_findings_have")
	}
	if len(args) != 2 || args[0] != "path" || args[1] != "line" {
		t.Errorf("args = %v, want [path, line]", args)
	}
}

func TestParseCheck_QuotedString(t *testing.T) {
	fn, args, err := ParseCheck(`git_current_branch_matches("^feature/plan-\\d+-")`)
	if err != nil {
		t.Fatalf("ParseCheck: %v", err)
	}
	if fn != "git_current_branch_matches" {
		t.Errorf("funcName = %q, want %q", fn, "git_current_branch_matches")
	}
	if len(args) != 1 || args[0] != `^feature/plan-\d+-` {
		t.Errorf("args = %v, want [^feature/plan-\\d+-]", args)
	}
}

func TestParseCheck_BareInteger(t *testing.T) {
	fn, args, err := ParseCheck(`exit_code_is(0)`)
	if err != nil {
		t.Fatalf("ParseCheck: %v", err)
	}
	if fn != "exit_code_is" {
		t.Errorf("funcName = %q, want %q", fn, "exit_code_is")
	}
	if len(args) != 1 || args[0] != "0" {
		t.Errorf("args = %v, want [0]", args)
	}
}

func TestParseCheck_InvalidSyntax(t *testing.T) {
	tests := []string{
		"no_parens",
		"",
		"bad(",
		"bad)",
	}
	for _, check := range tests {
		_, _, err := ParseCheck(check)
		if err == nil {
			t.Errorf("ParseCheck(%q): expected error, got nil", check)
		}
	}
}

// --- allFindingsHave tests ---

func TestAllFindingsHave_AllPresent(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{
			Findings: []core.Finding{
				{Path: "main.go", Line: 10, Severity: "important", Body: "fix this"},
				{Path: "store.go", Line: 20, Severity: "minor", Body: "nit"},
			},
		},
	}

	passed, err := allFindingsHave(ctx, []string{"path", "line"})
	if err != nil {
		t.Fatalf("allFindingsHave: %v", err)
	}
	if !passed {
		t.Error("expected pass, got fail")
	}
}

func TestAllFindingsHave_MissingPath(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{
			Findings: []core.Finding{
				{Path: "main.go", Line: 10},
				{Path: "", Line: 20}, // missing path
			},
		},
	}

	passed, err := allFindingsHave(ctx, []string{"path"})
	if err != nil {
		t.Fatalf("allFindingsHave: %v", err)
	}
	if passed {
		t.Error("expected fail, got pass")
	}
}

func TestAllFindingsHave_MissingLine(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{
			Findings: []core.Finding{
				{Path: "main.go", Line: 0}, // line 0 = missing
			},
		},
	}

	passed, err := allFindingsHave(ctx, []string{"line"})
	if err != nil {
		t.Fatalf("allFindingsHave: %v", err)
	}
	if passed {
		t.Error("expected fail for line=0, got pass")
	}
}

func TestAllFindingsHave_NoFindings(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{
			Findings: nil,
		},
	}

	passed, err := allFindingsHave(ctx, []string{"path", "line"})
	if err != nil {
		t.Fatalf("allFindingsHave: %v", err)
	}
	if !passed {
		t.Error("expected vacuous pass for empty findings, got fail")
	}
}

func TestAllFindingsHave_UnknownField(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{
			Findings: []core.Finding{
				{Path: "main.go", Line: 10},
			},
		},
	}

	_, err := allFindingsHave(ctx, []string{"nonexistent"})
	if err == nil {
		t.Error("expected error for unknown field, got nil")
	}
}

func TestAllFindingsHave_NoArgs(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{},
	}

	_, err := allFindingsHave(ctx, nil)
	if err == nil {
		t.Error("expected error for no args, got nil")
	}
}

func TestAllFindingsHave_SeverityAndBody(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{
			Findings: []core.Finding{
				{Path: "a.go", Line: 1, Severity: "critical", Body: "bad"},
				{Path: "b.go", Line: 2, Severity: "", Body: "ok"}, // missing severity
			},
		},
	}

	passed, err := allFindingsHave(ctx, []string{"severity"})
	if err != nil {
		t.Fatalf("allFindingsHave: %v", err)
	}
	if passed {
		t.Error("expected fail for missing severity, got pass")
	}
}

// --- exitCodeIs tests ---

func TestExitCodeIs_Match(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{ExitCode: 0},
	}
	passed, err := exitCodeIs(ctx, []string{"0"})
	if err != nil {
		t.Fatalf("exitCodeIs: %v", err)
	}
	if !passed {
		t.Error("expected pass")
	}
}

func TestExitCodeIs_NoMatch(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{ExitCode: 1},
	}
	passed, err := exitCodeIs(ctx, []string{"0"})
	if err != nil {
		t.Fatalf("exitCodeIs: %v", err)
	}
	if passed {
		t.Error("expected fail")
	}
}

func TestExitCodeIs_InvalidArg(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{ExitCode: 0},
	}
	_, err := exitCodeIs(ctx, []string{"not_a_number"})
	if err == nil {
		t.Error("expected error for non-integer arg, got nil")
	}
}

func TestExitCodeIs_WrongArgCount(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{ExitCode: 0},
	}
	_, err := exitCodeIs(ctx, []string{"0", "1"})
	if err == nil {
		t.Error("expected error for wrong arg count, got nil")
	}
}

// --- gitCurrentBranchMatches tests ---

func TestGitCurrentBranchMatches(t *testing.T) {
	// Create a temporary git repo.
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "checkout", "-b", "feature/plan-101-supervisor")
	// Need at least one commit for HEAD to exist.
	writeFile(t, filepath.Join(tmpDir, "dummy.txt"), "x")
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "init")

	ctx := &EvalContext{
		WorkDir: tmpDir,
		Result:  &core.JobResult{},
	}

	// Should match.
	passed, err := gitCurrentBranchMatches(ctx, []string{`^feature/plan-\d+-`})
	if err != nil {
		t.Fatalf("gitCurrentBranchMatches: %v", err)
	}
	if !passed {
		t.Error("expected match for feature/plan-101-supervisor")
	}

	// Should not match.
	passed, err = gitCurrentBranchMatches(ctx, []string{`^main$`})
	if err != nil {
		t.Fatalf("gitCurrentBranchMatches: %v", err)
	}
	if passed {
		t.Error("expected no match for ^main$")
	}
}

func TestGitCurrentBranchMatches_InvalidRegex(t *testing.T) {
	ctx := &EvalContext{
		WorkDir: t.TempDir(),
		Result:  &core.JobResult{},
	}
	_, err := gitCurrentBranchMatches(ctx, []string{"[invalid"})
	if err == nil {
		t.Error("expected error for invalid regex, got nil")
	}
}

// --- lastCommitMsgContains tests ---

func TestLastCommitMsgContains(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	writeFile(t, filepath.Join(tmpDir, "dummy.txt"), "x")
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "Phase 2: implement supervisor")

	ctx := &EvalContext{
		WorkDir: tmpDir,
		Result:  &core.JobResult{},
	}

	passed, err := lastCommitMsgContains(ctx, []string{`Phase \d+:`})
	if err != nil {
		t.Fatalf("lastCommitMsgContains: %v", err)
	}
	if !passed {
		t.Error("expected match for Phase 2:")
	}

	passed, err = lastCommitMsgContains(ctx, []string{`^BREAKING:`})
	if err != nil {
		t.Fatalf("lastCommitMsgContains: %v", err)
	}
	if passed {
		t.Error("expected no match for ^BREAKING:")
	}
}

// --- LookupPredicate tests ---

func TestLookupPredicate_Known(t *testing.T) {
	known := []string{"all_findings_have", "exit_code_is", "git_current_branch_matches", "last_commit_msg_contains"}
	for _, name := range known {
		fn, err := LookupPredicate(name)
		if err != nil {
			t.Errorf("LookupPredicate(%q): %v", name, err)
		}
		if fn == nil {
			t.Errorf("LookupPredicate(%q) returned nil", name)
		}
	}
}

func TestLookupPredicate_Unknown(t *testing.T) {
	_, err := LookupPredicate("nonexistent_predicate")
	if err == nil {
		t.Error("expected error for unknown predicate, got nil")
	}
}

// --- test helpers ---

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Set git author for commits.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}
