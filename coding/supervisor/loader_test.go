package supervisor

import (
	"testing"
)

func TestLoadRulesFromBytes_ValidYAML(t *testing.T) {
	yaml := []byte(`
rules:
  reviewer_findings_line_anchored:
    applies_to: [reviewer.*]
    check: all_findings_have(["path", "line"])
    message: "All findings must have path and line references"
  dev_branch_check:
    applies_to: [developer]
    check: git_current_branch_matches("^feature/plan-\\d+-")
    message: "Developer must commit on a feature branch"
`)

	rl, err := LoadRulesFromBytes(yaml)
	if err != nil {
		t.Fatalf("LoadRulesFromBytes: %v", err)
	}
	if len(rl.Rules) != 2 {
		t.Fatalf("rules count = %d, want 2", len(rl.Rules))
	}

	// Verify rule names are populated from map keys.
	names := make(map[string]bool)
	for _, r := range rl.Rules {
		names[r.Name] = true
	}
	if !names["reviewer_findings_line_anchored"] {
		t.Error("missing rule: reviewer_findings_line_anchored")
	}
	if !names["dev_branch_check"] {
		t.Error("missing rule: dev_branch_check")
	}
}

func TestLoadRulesFromBytes_EmptyRules(t *testing.T) {
	yaml := []byte(`rules: {}`)
	_, err := LoadRulesFromBytes(yaml)
	if err == nil {
		t.Error("expected error for empty rules, got nil")
	}
}

func TestLoadRulesFromBytes_InvalidYAML(t *testing.T) {
	_, err := LoadRulesFromBytes([]byte(`{not valid yaml`))
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

func TestLoadRulesFromBytes_MissingCheck(t *testing.T) {
	yaml := []byte(`
rules:
  bad_rule:
    applies_to: [developer]
    message: "some message"
`)
	_, err := LoadRulesFromBytes(yaml)
	if err == nil {
		t.Error("expected error for missing check field, got nil")
	}
}

func TestLoadRulesFromBytes_MissingAppliesTo(t *testing.T) {
	yaml := []byte(`
rules:
  bad_rule:
    check: exit_code_is(0)
    message: "some message"
`)
	_, err := LoadRulesFromBytes(yaml)
	if err == nil {
		t.Error("expected error for missing applies_to, got nil")
	}
}

func TestLoadRulesFromBytes_MissingMessage(t *testing.T) {
	yaml := []byte(`
rules:
  bad_rule:
    applies_to: [developer]
    check: exit_code_is(0)
`)
	_, err := LoadRulesFromBytes(yaml)
	if err == nil {
		t.Error("expected error for missing message, got nil")
	}
}

func TestRulesForRole_ExactMatch(t *testing.T) {
	rl := &RuleList{
		Rules: []Rule{
			{Name: "dev_rule", AppliesTo: []string{"developer"}, Check: "exit_code_is(0)", Message: "m1"},
			{Name: "rev_rule", AppliesTo: []string{"reviewer.arch"}, Check: "exit_code_is(0)", Message: "m2"},
		},
	}

	matched := rl.RulesForRole("developer")
	if len(matched) != 1 {
		t.Fatalf("matched = %d, want 1", len(matched))
	}
	if matched[0].Name != "dev_rule" {
		t.Errorf("matched rule = %q, want %q", matched[0].Name, "dev_rule")
	}
}

func TestRulesForRole_GlobMatch(t *testing.T) {
	rl := &RuleList{
		Rules: []Rule{
			{Name: "rev_rule", AppliesTo: []string{"reviewer.*"}, Check: "exit_code_is(0)", Message: "m1"},
		},
	}

	tests := []struct {
		role  string
		match bool
	}{
		{"reviewer.arch", true},
		{"reviewer.frontend", true},
		{"reviewer", false},
		{"developer", false},
	}
	for _, tt := range tests {
		matched := rl.RulesForRole(tt.role)
		got := len(matched) > 0
		if got != tt.match {
			t.Errorf("RulesForRole(%q): matched=%v, want %v", tt.role, got, tt.match)
		}
	}
}

func TestRulesForRole_WildcardAll(t *testing.T) {
	rl := &RuleList{
		Rules: []Rule{
			{Name: "all_rule", AppliesTo: []string{"*"}, Check: "exit_code_is(0)", Message: "m1"},
		},
	}

	for _, role := range []string{"developer", "reviewer.arch", "shipper"} {
		matched := rl.RulesForRole(role)
		if len(matched) != 1 {
			t.Errorf("RulesForRole(%q): matched=%d, want 1", role, len(matched))
		}
	}
}

func TestRulesForRole_MultipleAppliesTo(t *testing.T) {
	rl := &RuleList{
		Rules: []Rule{
			{Name: "multi_rule", AppliesTo: []string{"developer", "shipper"}, Check: "exit_code_is(0)", Message: "m1"},
		},
	}

	if len(rl.RulesForRole("developer")) != 1 {
		t.Error("should match developer")
	}
	if len(rl.RulesForRole("shipper")) != 1 {
		t.Error("should match shipper")
	}
	if len(rl.RulesForRole("reviewer.arch")) != 0 {
		t.Error("should not match reviewer.arch")
	}
}

func TestRoleGlobMatches(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"developer", "developer", true},
		{"developer", "developer.sub", false},
		{"reviewer.*", "reviewer.arch", true},
		{"reviewer.*", "reviewer.frontend", true},
		{"reviewer.*", "reviewer", false},
		{"reviewer.*", "xreviewer.arch", false},
		{"*", "anything", true},
		{"*.*", "a.b", true},
		{"*.*", "abc", false},
	}
	for _, tt := range tests {
		got := roleGlobMatches(tt.pattern, tt.name)
		if got != tt.want {
			t.Errorf("roleGlobMatches(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
		}
	}
}
