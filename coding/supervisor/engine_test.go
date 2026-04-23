package supervisor

import (
	"testing"

	"github.com/chris/coworker/core"
)

func makeEngine(t *testing.T, yaml string) *RuleEngine {
	t.Helper()
	engine, err := NewRuleEngineFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("NewRuleEngineFromBytes: %v", err)
	}
	return engine
}

func TestEngine_AllRulesPass(t *testing.T) {
	engine := makeEngine(t, `
rules:
  findings_have_path_line:
    applies_to: [reviewer.*]
    check: all_findings_have(["path", "line"])
    message: "All findings must have path and line"
  exit_zero:
    applies_to: [reviewer.*]
    check: exit_code_is(0)
    message: "Job must exit with code 0"
`)

	ctx := &EvalContext{
		Role: &core.Role{Name: "reviewer.arch"},
		Result: &core.JobResult{
			ExitCode: 0,
			Findings: []core.Finding{
				{Path: "main.go", Line: 10, Severity: "important", Body: "fix"},
			},
		},
		Job:   &core.Job{ID: "j1", RunID: "r1", Role: "reviewer.arch"},
		RunID: "r1",
	}

	verdict, err := engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !verdict.Pass {
		t.Error("expected pass")
		for _, r := range verdict.Results {
			t.Logf("  %s: passed=%v msg=%s", r.RuleName, r.Passed, r.Message)
		}
	}
	if len(verdict.Results) != 2 {
		t.Errorf("results count = %d, want 2", len(verdict.Results))
	}
}

func TestEngine_OneRuleFails(t *testing.T) {
	engine := makeEngine(t, `
rules:
  findings_have_path_line:
    applies_to: [reviewer.*]
    check: all_findings_have(["path", "line"])
    message: "All findings must have path and line"
  exit_zero:
    applies_to: [reviewer.*]
    check: exit_code_is(0)
    message: "Job must exit with code 0"
`)

	ctx := &EvalContext{
		Role: &core.Role{Name: "reviewer.arch"},
		Result: &core.JobResult{
			ExitCode: 0,
			Findings: []core.Finding{
				{Path: "", Line: 10}, // missing path -> rule fails
			},
		},
		Job:   &core.Job{ID: "j1", RunID: "r1", Role: "reviewer.arch"},
		RunID: "r1",
	}

	verdict, err := engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if verdict.Pass {
		t.Error("expected fail (missing path in finding)")
	}

	// exit_code_is(0) should still pass.
	var failCount, passCount int
	for _, r := range verdict.Results {
		if r.Passed {
			passCount++
		} else {
			failCount++
		}
	}
	if failCount != 1 {
		t.Errorf("failCount = %d, want 1", failCount)
	}
	if passCount != 1 {
		t.Errorf("passCount = %d, want 1", passCount)
	}
}

func TestEngine_NoApplicableRules(t *testing.T) {
	engine := makeEngine(t, `
rules:
  dev_rule:
    applies_to: [developer]
    check: exit_code_is(0)
    message: "exit 0"
`)

	ctx := &EvalContext{
		Role:   &core.Role{Name: "reviewer.arch"},
		Result: &core.JobResult{ExitCode: 1},
		Job:    &core.Job{ID: "j1", RunID: "r1", Role: "reviewer.arch"},
		RunID:  "r1",
	}

	verdict, err := engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !verdict.Pass {
		t.Error("expected pass when no rules apply")
	}
	if len(verdict.Results) != 0 {
		t.Errorf("results count = %d, want 0", len(verdict.Results))
	}
}

func TestEngine_UnknownPredicate(t *testing.T) {
	engine := makeEngine(t, `
rules:
  bad_check:
    applies_to: [reviewer.*]
    check: nonexistent_predicate("arg")
    message: "should fail"
`)

	ctx := &EvalContext{
		Role:   &core.Role{Name: "reviewer.arch"},
		Result: &core.JobResult{},
		Job:    &core.Job{ID: "j1", RunID: "r1", Role: "reviewer.arch"},
		RunID:  "r1",
	}

	verdict, err := engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// Unknown predicate -> rule fails, verdict fails.
	if verdict.Pass {
		t.Error("expected fail for unknown predicate")
	}
	if len(verdict.Results) != 1 {
		t.Fatalf("results count = %d, want 1", len(verdict.Results))
	}
	if verdict.Results[0].Passed {
		t.Error("expected rule result to be failed")
	}
}

func TestEngine_NilRoleErrors(t *testing.T) {
	engine := makeEngine(t, `
rules:
  r1:
    applies_to: [developer]
    check: exit_code_is(0)
    message: "m"
`)

	_, err := engine.Evaluate(&EvalContext{Role: nil})
	if err == nil {
		t.Error("expected error for nil Role, got nil")
	}
}

func TestEngine_FailedMessages(t *testing.T) {
	verdict := &core.SupervisorVerdict{
		Pass: false,
		Results: []core.RuleResult{
			{RuleName: "r1", Passed: true, Message: "ok"},
			{RuleName: "r2", Passed: false, Message: "findings need path"},
			{RuleName: "r3", Passed: false, Message: "wrong exit code"},
		},
	}

	msgs := verdict.FailedMessages()
	if len(msgs) != 2 {
		t.Fatalf("FailedMessages() len = %d, want 2", len(msgs))
	}
	if msgs[0] != "findings need path" {
		t.Errorf("msgs[0] = %q, want %q", msgs[0], "findings need path")
	}
	if msgs[1] != "wrong exit code" {
		t.Errorf("msgs[1] = %q, want %q", msgs[1], "wrong exit code")
	}
}

func TestEngine_GlobMatchMultipleRoles(t *testing.T) {
	engine := makeEngine(t, `
rules:
  rev_findings:
    applies_to: [reviewer.*]
    check: all_findings_have(["path", "line"])
    message: "findings must have path and line"
  all_exit:
    applies_to: ["*"]
    check: exit_code_is(0)
    message: "must exit 0"
`)

	// reviewer.arch should get both rules.
	ctx := &EvalContext{
		Role: &core.Role{Name: "reviewer.arch"},
		Result: &core.JobResult{
			ExitCode: 0,
			Findings: []core.Finding{
				{Path: "a.go", Line: 1},
			},
		},
		Job:   &core.Job{ID: "j1", RunID: "r1", Role: "reviewer.arch"},
		RunID: "r1",
	}

	verdict, err := engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(verdict.Results) != 2 {
		t.Errorf("results count = %d, want 2", len(verdict.Results))
	}

	// developer should only get the wildcard rule.
	ctx.Role = &core.Role{Name: "developer"}
	ctx.Job.Role = "developer"
	verdict, err = engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(verdict.Results) != 1 {
		t.Errorf("results count = %d, want 1 (wildcard only)", len(verdict.Results))
	}
}

func TestEngine_RuleCount(t *testing.T) {
	engine := makeEngine(t, `
rules:
  r1:
    applies_to: [developer]
    check: exit_code_is(0)
    message: "m1"
  r2:
    applies_to: [reviewer.*]
    check: all_findings_have(["path"])
    message: "m2"
`)
	if engine.RuleCount() != 2 {
		t.Errorf("RuleCount() = %d, want 2", engine.RuleCount())
	}
}
