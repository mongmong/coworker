package quality

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMockJudge_ReturnsConfiguredVerdict(t *testing.T) {
	judge := &MockJudge{
		Verdicts: map[string]*Verdict{
			"missing_required_tests": {
				Pass:       false,
				Category:   "missing_required_tests",
				Findings:   []string{"Function Foo has no test"},
				Confidence: 0.9,
			},
		},
	}

	rule := &Rule{
		Name:     "missing_required_tests",
		Category: CategoryMissingTests,
		Prompt:   "Check for missing tests",
		Severity: "block",
	}

	verdict, err := judge.Evaluate(context.Background(), rule, "diff content", "context")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Pass {
		t.Error("expected verdict to be failing")
	}
	if len(verdict.Findings) != 1 {
		t.Errorf("expected 1 finding, got %d", len(verdict.Findings))
	}
	if verdict.Findings[0] != "Function Foo has no test" {
		t.Errorf("unexpected finding: %q", verdict.Findings[0])
	}
	if verdict.Confidence != 0.9 {
		t.Errorf("expected confidence 0.9, got %f", verdict.Confidence)
	}

	// Verify call was recorded.
	if len(judge.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(judge.Calls))
	}
	if judge.Calls[0].RuleName != "missing_required_tests" {
		t.Errorf("expected rule name 'missing_required_tests', got %q", judge.Calls[0].RuleName)
	}
}

func TestMockJudge_DefaultPassForUnknownRule(t *testing.T) {
	judge := &MockJudge{
		Verdicts: map[string]*Verdict{},
	}

	rule := &Rule{
		Name:     "unknown_rule",
		Category: "some_category",
		Prompt:   "some prompt",
		Severity: "advisory",
	}

	verdict, err := judge.Evaluate(context.Background(), rule, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Pass {
		t.Error("expected default verdict to pass")
	}
	if verdict.Confidence != 1.0 {
		t.Errorf("expected confidence 1.0, got %f", verdict.Confidence)
	}
}

func TestMockJudge_RecordsAllCalls(t *testing.T) {
	judge := &MockJudge{}

	rules := []*Rule{
		{Name: "rule_a", Category: CategoryMissingTests, Prompt: "p", Severity: "block"},
		{Name: "rule_b", Category: "advisory_cat", Prompt: "p", Severity: "advisory"},
	}

	for _, r := range rules {
		if _, err := judge.Evaluate(context.Background(), r, "diff", "ctx"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if len(judge.Calls) != 2 {
		t.Errorf("expected 2 calls, got %d", len(judge.Calls))
	}
}

func TestCLIJudge_FakeBinary(t *testing.T) {
	// Create a fake binary that writes a JSON verdict to stdout.
	verdict := Verdict{
		Pass:       false,
		Category:   "missing_required_tests",
		Findings:   []string{"TestFoo is missing"},
		Confidence: 0.8,
	}
	verdictJSON, _ := json.Marshal(verdict)

	// Write a shell script that emits the verdict JSON.
	dir := t.TempDir()
	binPath := filepath.Join(dir, "fakejudge")
	script := "#!/bin/sh\necho '" + string(verdictJSON) + "'\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil { //nolint:gosec // test binary needs +x
		t.Fatalf("write fake binary: %v", err)
	}

	judge := &CLIJudge{BinaryPath: binPath}
	rule := &Rule{
		Name:     "missing_required_tests",
		Category: CategoryMissingTests,
		Prompt:   "check tests",
		Severity: "block",
	}

	got, err := judge.Evaluate(context.Background(), rule, "diff content", "job context")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Pass {
		t.Error("expected verdict to be failing")
	}
	if len(got.Findings) != 1 || got.Findings[0] != "TestFoo is missing" {
		t.Errorf("unexpected findings: %v", got.Findings)
	}
	if got.Confidence != 0.8 {
		t.Errorf("expected confidence 0.8, got %f", got.Confidence)
	}
}

func TestCLIJudge_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "fakejudge_bad")
	script := "#!/bin/sh\necho 'not json'\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil { //nolint:gosec // test binary needs +x
		t.Fatalf("write fake binary: %v", err)
	}

	judge := &CLIJudge{BinaryPath: binPath}
	rule := &Rule{
		Name:     "test_rule",
		Category: CategoryMissingTests,
		Prompt:   "check",
		Severity: "block",
	}

	_, err := judge.Evaluate(context.Background(), rule, "", "")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	// The raw stdout content must be preserved in the error message so that
	// callers can diagnose what the judge actually emitted.
	if !contains(err.Error(), "not json") {
		t.Errorf("expected error to contain raw stdout output, got: %v", err)
	}
}

func TestCLIJudge_NonZeroExit(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "fakejudge_fail")
	script := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil { //nolint:gosec // test binary needs +x
		t.Fatalf("write fake binary: %v", err)
	}

	judge := &CLIJudge{BinaryPath: binPath}
	rule := &Rule{
		Name:     "test_rule",
		Category: CategoryMissingTests,
		Prompt:   "check",
		Severity: "block",
	}

	_, err := judge.Evaluate(context.Background(), rule, "", "")
	if err == nil {
		t.Fatal("expected error for non-zero exit, got nil")
	}
}

func TestRenderJudgePrompt(t *testing.T) {
	rule := &Rule{
		Name:     "missing_required_tests",
		Category: CategoryMissingTests,
		Prompt:   "Are there new functions without tests?",
		Severity: "block",
	}

	prompt := renderJudgePrompt(rule, "diff content", "job context")

	// Must contain the rule prompt.
	if !contains(prompt, "Are there new functions without tests?") {
		t.Error("expected prompt to contain the rule prompt text")
	}
	// Must contain the diff.
	if !contains(prompt, "diff content") {
		t.Error("expected prompt to contain the diff")
	}
	// Must contain the context.
	if !contains(prompt, "job context") {
		t.Error("expected prompt to contain the job context")
	}
	// Must mention the category.
	if !contains(prompt, string(CategoryMissingTests)) {
		t.Error("expected prompt to mention the category")
	}
}

func TestRenderJudgePrompt_NoDiff(t *testing.T) {
	rule := &Rule{
		Name:     "spec_check",
		Category: CategorySpecContradiction,
		Prompt:   "Check spec adherence",
		Severity: "block",
	}

	prompt := renderJudgePrompt(rule, "", "some context")
	// Should not have an empty diff section.
	if contains(prompt, "```diff") {
		t.Error("expected prompt without diff section when diff is empty")
	}
	if !contains(prompt, "some context") {
		t.Error("expected prompt to contain the context")
	}
}

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
