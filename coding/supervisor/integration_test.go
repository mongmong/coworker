package supervisor

// integration_test.go verifies that:
// 1. Each canonical role YAML loads without error and RulesForRole returns the
//    expected count.
// 2. The applies_when.changes_touch predicate correctly skips rules when no
//    files match and fires rules when they do.
// 3. Skipped rules do not affect verdict.Pass.
// 4. SkippedRuleNames returns the right rule names.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chris/coworker/core"
)

// rulesYAMLPath is the canonical rules file.
const rulesYAMLPath = "rules.yaml"

// TestLoadCanonicalRulesYAML verifies that the shipped rules.yaml is valid.
func TestLoadCanonicalRulesYAML(t *testing.T) {
	rl, err := LoadRulesFromFile(rulesYAMLPath)
	if err != nil {
		t.Fatalf("LoadRulesFromFile(%q): %v", rulesYAMLPath, err)
	}
	if len(rl.Rules) == 0 {
		t.Fatal("no rules loaded from canonical file")
	}
	t.Logf("loaded %d rules", len(rl.Rules))
}

// TestRulesForRole_Developer checks that developer gets its two rules.
func TestRulesForRole_Developer(t *testing.T) {
	rl, err := LoadRulesFromFile(rulesYAMLPath)
	if err != nil {
		t.Fatalf("LoadRulesFromFile: %v", err)
	}

	rules := rl.RulesForRole("developer")
	if len(rules) != 2 {
		t.Errorf("RulesForRole(developer) = %d rules, want 2", len(rules))
		for _, r := range rules {
			t.Logf("  rule: %s", r.Name)
		}
	}
}

// TestRulesForRole_Planner checks that planner gets exactly one rule.
func TestRulesForRole_Planner(t *testing.T) {
	rl, err := LoadRulesFromFile(rulesYAMLPath)
	if err != nil {
		t.Fatalf("LoadRulesFromFile: %v", err)
	}

	rules := rl.RulesForRole("planner")
	if len(rules) != 1 {
		t.Errorf("RulesForRole(planner) = %d rules, want 1", len(rules))
		for _, r := range rules {
			t.Logf("  rule: %s", r.Name)
		}
	}
}

// TestRulesForRole_ReviewerArch checks that reviewer.arch gets the three
// wildcard reviewer rules.
func TestRulesForRole_ReviewerArch(t *testing.T) {
	rl, err := LoadRulesFromFile(rulesYAMLPath)
	if err != nil {
		t.Fatalf("LoadRulesFromFile: %v", err)
	}

	rules := rl.RulesForRole("reviewer.arch")
	if len(rules) != 3 {
		t.Errorf("RulesForRole(reviewer.arch) = %d rules, want 3", len(rules))
		for _, r := range rules {
			t.Logf("  rule: %s", r.Name)
		}
	}
}

// TestRulesForRole_ReviewerFrontend checks that reviewer.frontend gets the
// three wildcard reviewer rules.
func TestRulesForRole_ReviewerFrontend(t *testing.T) {
	rl, err := LoadRulesFromFile(rulesYAMLPath)
	if err != nil {
		t.Fatalf("LoadRulesFromFile: %v", err)
	}

	rules := rl.RulesForRole("reviewer.frontend")
	if len(rules) != 3 {
		t.Errorf("RulesForRole(reviewer.frontend) = %d rules, want 3", len(rules))
		for _, r := range rules {
			t.Logf("  rule: %s", r.Name)
		}
	}
}

// TestRulesForRole_Tester checks that tester gets exactly one rule.
func TestRulesForRole_Tester(t *testing.T) {
	rl, err := LoadRulesFromFile(rulesYAMLPath)
	if err != nil {
		t.Fatalf("LoadRulesFromFile: %v", err)
	}

	rules := rl.RulesForRole("tester")
	if len(rules) != 1 {
		t.Errorf("RulesForRole(tester) = %d rules, want 1", len(rules))
		for _, r := range rules {
			t.Logf("  rule: %s", r.Name)
		}
	}
}

// TestRulesForRole_Shipper checks that shipper gets exactly one rule.
func TestRulesForRole_Shipper(t *testing.T) {
	rl, err := LoadRulesFromFile(rulesYAMLPath)
	if err != nil {
		t.Fatalf("LoadRulesFromFile: %v", err)
	}

	rules := rl.RulesForRole("shipper")
	if len(rules) != 1 {
		t.Errorf("RulesForRole(shipper) = %d rules, want 1", len(rules))
		for _, r := range rules {
			t.Logf("  rule: %s", r.Name)
		}
	}
}

// TestAppliesWhen_SkipsWhenNoFilesMatch verifies that a rule with
// applies_when.changes_touch is skipped when the git diff does not touch any
// matching file.
func TestAppliesWhen_SkipsWhenNoFilesMatch(t *testing.T) {
	// Set up a git repo with a non-frontend commit.
	tmpDir := setupGitRepo(t)
	writeFile(t, filepath.Join(tmpDir, "main.go"), "package main")
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "Phase 1: add main.go")

	engine := makeEngine(t, `
rules:
  frontend_rule:
    applies_to: [reviewer.frontend]
    applies_when:
      changes_touch: ["*.tsx", "web/**"]
    check: exit_code_is(0)
    message: "Frontend rule"
`)

	ctx := &EvalContext{
		Role:    &core.Role{Name: "reviewer.frontend"},
		Result:  &core.JobResult{ExitCode: 0},
		Job:     &core.Job{ID: "j1", RunID: "r1", Role: "reviewer.frontend"},
		RunID:   "r1",
		WorkDir: tmpDir,
	}

	verdict, err := engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	// Verdict must pass (no failed rules).
	if !verdict.Pass {
		t.Error("expected verdict.Pass=true when rule is skipped")
	}

	// One result emitted, marked as skipped.
	if len(verdict.Results) != 1 {
		t.Fatalf("results count = %d, want 1", len(verdict.Results))
	}
	if !verdict.Results[0].Skipped {
		t.Error("expected result.Skipped=true")
	}

	skipped := verdict.SkippedRuleNames()
	if len(skipped) != 1 || skipped[0] != "frontend_rule" {
		t.Errorf("SkippedRuleNames() = %v, want [frontend_rule]", skipped)
	}
}

// TestAppliesWhen_FiresWhenFileMatches verifies that a rule with
// applies_when.changes_touch is NOT skipped when the diff touches a matching
// file.
func TestAppliesWhen_FiresWhenFileMatches(t *testing.T) {
	tmpDir := setupGitRepo(t)
	writeFile(t, filepath.Join(tmpDir, "Button.tsx"), "export const Button = () => null")
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "Phase 1: add Button.tsx")

	engine := makeEngine(t, `
rules:
  frontend_rule:
    applies_to: [reviewer.frontend]
    applies_when:
      changes_touch: ["*.tsx", "web/**"]
    check: exit_code_is(0)
    message: "Frontend rule"
`)

	ctx := &EvalContext{
		Role:    &core.Role{Name: "reviewer.frontend"},
		Result:  &core.JobResult{ExitCode: 0},
		Job:     &core.Job{ID: "j1", RunID: "r1", Role: "reviewer.frontend"},
		RunID:   "r1",
		WorkDir: tmpDir,
	}

	verdict, err := engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected verdict.Pass=true")
	}

	if len(verdict.Results) != 1 {
		t.Fatalf("results count = %d, want 1", len(verdict.Results))
	}
	if verdict.Results[0].Skipped {
		t.Error("expected result.Skipped=false when file matches")
	}
	if !verdict.Results[0].Passed {
		t.Error("expected result.Passed=true")
	}

	if len(verdict.SkippedRuleNames()) != 0 {
		t.Errorf("SkippedRuleNames() = %v, want empty", verdict.SkippedRuleNames())
	}
}

// TestAppliesWhen_SkipDoesNotAffectPass verifies that a skipped rule does NOT
// pull verdict.Pass to false, even when a simultaneously evaluated rule fails.
func TestAppliesWhen_SkipDoesNotAffectPass(t *testing.T) {
	// Commit a Go file — not a frontend file.
	tmpDir := setupGitRepo(t)
	writeFile(t, filepath.Join(tmpDir, "store.go"), "package store")
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "Phase 2: add store.go")

	// One rule always applies and passes; one rule applies_when a tsx file is
	// touched (it won't be) — that rule would fail if it ran.
	engine := makeEngine(t, `
rules:
  always_pass:
    applies_to: [reviewer.frontend]
    check: exit_code_is(0)
    message: "Must exit 0"
  frontend_only_fail:
    applies_to: [reviewer.frontend]
    applies_when:
      changes_touch: ["*.tsx"]
    check: exit_code_is(99)
    message: "This would fail if it ran"
`)

	ctx := &EvalContext{
		Role:    &core.Role{Name: "reviewer.frontend"},
		Result:  &core.JobResult{ExitCode: 0},
		Job:     &core.Job{ID: "j1", RunID: "r1", Role: "reviewer.frontend"},
		RunID:   "r1",
		WorkDir: tmpDir,
	}

	verdict, err := engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected verdict.Pass=true — skipped rule must not cause failure")
		for _, r := range verdict.Results {
			t.Logf("  %s: passed=%v skipped=%v", r.RuleName, r.Passed, r.Skipped)
		}
	}

	// Both results should be present.
	if len(verdict.Results) != 2 {
		t.Fatalf("results count = %d, want 2", len(verdict.Results))
	}

	skipped := verdict.SkippedRuleNames()
	if len(skipped) != 1 {
		t.Errorf("SkippedRuleNames() = %v, want exactly 1", skipped)
	}

	failed := verdict.FailedMessages()
	if len(failed) != 0 {
		t.Errorf("FailedMessages() = %v, want empty", failed)
	}
}

// TestAppliesWhen_NoClause verifies that a rule without applies_when always runs.
func TestAppliesWhen_NoClause(t *testing.T) {
	engine := makeEngine(t, `
rules:
  unconditional:
    applies_to: [tester]
    check: exit_code_is(0)
    message: "Always runs"
`)

	ctx := &EvalContext{
		Role:   &core.Role{Name: "tester"},
		Result: &core.JobResult{ExitCode: 0},
		Job:    &core.Job{ID: "j1", RunID: "r1", Role: "tester"},
		RunID:  "r1",
	}

	verdict, err := engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass")
	}
	if len(verdict.Results) != 1 {
		t.Fatalf("results count = %d, want 1", len(verdict.Results))
	}
	if verdict.Results[0].Skipped {
		t.Error("expected Skipped=false for unconditional rule")
	}
}

// TestChangesTouch_GlobMatching exercises the glob matching in evalChangesTouch
// directly via the predicate registry.
func TestChangesTouch_GlobMatching(t *testing.T) {
	tmpDir := setupGitRepo(t)
	writeFile(t, filepath.Join(tmpDir, "app.css"), "body{}")
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "Phase 1: add app.css")

	ctx := &EvalContext{
		WorkDir: tmpDir,
		Result:  &core.JobResult{},
	}

	// *.css should match app.css.
	passed, err := changesTouch(ctx, []string{"*.css"})
	if err != nil {
		t.Fatalf("changesTouch: %v", err)
	}
	if !passed {
		t.Error("expected *.css to match app.css")
	}

	// *.tsx should not match app.css.
	passed, err = changesTouch(ctx, []string{"*.tsx"})
	if err != nil {
		t.Fatalf("changesTouch: %v", err)
	}
	if passed {
		t.Error("expected *.tsx NOT to match app.css")
	}
}

// TestChangesTouch_NestedWebPath verifies that "web/**" matches a deeply
// nested file such as web/components/Button.tsx (I1).
func TestChangesTouch_NestedWebPath(t *testing.T) {
	tmpDir := setupGitRepo(t)

	// Create the nested directory and file.
	componentsDir := filepath.Join(tmpDir, "web", "components")
	if err := os.MkdirAll(componentsDir, 0755); err != nil {
		t.Fatalf("mkdir web/components: %v", err)
	}
	writeFile(t, filepath.Join(componentsDir, "Button.tsx"), "export const Button = () => null")
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "Phase 1: add web/components/Button.tsx")

	ctx := &EvalContext{
		WorkDir: tmpDir,
		Result:  &core.JobResult{},
	}

	// "web/**" must match web/components/Button.tsx.
	passed, err := changesTouch(ctx, []string{"web/**"})
	if err != nil {
		t.Fatalf("changesTouch: %v", err)
	}
	if !passed {
		t.Error("expected web/** to match web/components/Button.tsx")
	}

	// "api/**" must NOT match web/components/Button.tsx.
	passed, err = changesTouch(ctx, []string{"api/**"})
	if err != nil {
		t.Fatalf("changesTouch: %v", err)
	}
	if passed {
		t.Error("expected api/** NOT to match web/components/Button.tsx")
	}
}

// TestAppliesWhen_InvalidGlobReturnsError verifies that an unparseable glob
// in applies_when.changes_touch causes rule evaluation to fail (I3).
// This exercises the error propagation path in the engine when
// EvalAppliesWhen returns an error.
func TestAppliesWhen_InvalidGlobReturnsError(t *testing.T) {
	tmpDir := setupGitRepo(t)
	writeFile(t, filepath.Join(tmpDir, "main.go"), "package main")
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "Phase 1: add main.go")

	engine := makeEngine(t, `
rules:
  bad_glob_rule:
    applies_to: [developer]
    applies_when:
      changes_touch: ["[invalid"]
    check: exit_code_is(0)
    message: "Bad glob rule"
`)

	ctx := &EvalContext{
		Role:    &core.Role{Name: "developer"},
		Result:  &core.JobResult{ExitCode: 0},
		Job:     &core.Job{ID: "j1", RunID: "r1", Role: "developer"},
		RunID:   "r1",
		WorkDir: tmpDir,
	}

	verdict, err := engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate returned unexpected error: %v", err)
	}

	// The verdict must fail because the applies_when evaluation errored.
	if verdict.Pass {
		t.Error("expected verdict.Pass=false when applies_when eval errors")
	}

	// There must be exactly one result, not skipped (it errored, not skipped).
	if len(verdict.Results) != 1 {
		t.Fatalf("results count = %d, want 1", len(verdict.Results))
	}
	r := verdict.Results[0]
	if r.Skipped {
		t.Error("expected result.Skipped=false for eval error (error ≠ skip)")
	}
	if r.Passed {
		t.Error("expected result.Passed=false for eval error")
	}
	if !strings.Contains(r.Message, "applies_when eval error") {
		t.Errorf("result.Message = %q, want it to contain \"applies_when eval error\"", r.Message)
	}
}

// TestChangesTouch_NoArgs verifies that calling changesTouch with no args
// returns an error.
func TestChangesTouch_NoArgs(t *testing.T) {
	ctx := &EvalContext{Result: &core.JobResult{}}
	_, err := changesTouch(ctx, nil)
	if err == nil {
		t.Error("expected error for no args, got nil")
	}
}

// TestLookupPredicate_ChangesTouchRegistered verifies that changes_touch
// is in the registry.
func TestLookupPredicate_ChangesTouchRegistered(t *testing.T) {
	fn, err := LookupPredicate("changes_touch")
	if err != nil {
		t.Fatalf("LookupPredicate(changes_touch): %v", err)
	}
	if fn == nil {
		t.Error("LookupPredicate(changes_touch) returned nil")
	}
}

// TestSkippedRuleNames_Empty verifies SkippedRuleNames on an all-pass verdict.
func TestSkippedRuleNames_Empty(t *testing.T) {
	verdict := &core.SupervisorVerdict{
		Pass: true,
		Results: []core.RuleResult{
			{RuleName: "r1", Passed: true, Message: "ok"},
		},
	}
	msgs := verdict.SkippedRuleNames()
	if len(msgs) != 0 {
		t.Errorf("SkippedRuleNames() = %v, want empty", msgs)
	}
}

// TestFailedMessages_IgnoresSkipped verifies FailedMessages ignores skipped results.
func TestFailedMessages_IgnoresSkipped(t *testing.T) {
	verdict := &core.SupervisorVerdict{
		Pass: false,
		Results: []core.RuleResult{
			{RuleName: "r1", Passed: false, Skipped: true, Message: "skipped rule"},
			{RuleName: "r2", Passed: false, Skipped: false, Message: "real failure"},
		},
	}
	msgs := verdict.FailedMessages()
	if len(msgs) != 1 || msgs[0] != "real failure" {
		t.Errorf("FailedMessages() = %v, want [real failure]", msgs)
	}
}

// --- helpers ---

// setupGitRepo creates a temporary git repository with an initial empty commit.
func setupGitRepo(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "checkout", "-b", "feature/plan-111-test")

	// Create an initial empty commit so HEAD~1 always exists for subsequent commits.
	// We use --allow-empty to avoid needing a real file.
	cmd := gitCmdInDir(tmpDir, "commit", "--allow-empty", "-m", "init")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git commit --allow-empty: %v\n%s", err, out)
	}
	return tmpDir
}

func gitCmdInDir(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd
}
