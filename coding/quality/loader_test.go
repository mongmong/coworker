package quality

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRulesFromBytes_Valid(t *testing.T) {
	yaml := `
quality_rules:
  missing_required_tests:
    category: missing_required_tests
    prompt: "Are there new functions without tests?"
    severity: block
  spec_adherence:
    category: spec_adherence
    prompt: "Does the implementation match the spec?"
    severity: advisory
`
	rules, err := LoadRulesFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}

	// Rules are sorted by name: missing_required_tests < spec_adherence
	if rules[0].Name != "missing_required_tests" {
		t.Errorf("expected first rule 'missing_required_tests', got %q", rules[0].Name)
	}
	if rules[0].Category != CategoryMissingTests {
		t.Errorf("expected category %q, got %q", CategoryMissingTests, rules[0].Category)
	}
	if rules[0].Severity != "block" {
		t.Errorf("expected severity 'block', got %q", rules[0].Severity)
	}
	if rules[1].Name != "spec_adherence" {
		t.Errorf("expected second rule 'spec_adherence', got %q", rules[1].Name)
	}
	if rules[1].Severity != "advisory" {
		t.Errorf("expected severity 'advisory', got %q", rules[1].Severity)
	}
}

func TestLoadRulesFromBytes_Empty(t *testing.T) {
	yaml := `quality_rules: {}`
	_, err := LoadRulesFromBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for empty rules, got nil")
	}
}

func TestLoadRulesFromBytes_MissingCategory(t *testing.T) {
	yaml := `
quality_rules:
  bad_rule:
    prompt: "some prompt"
    severity: advisory
`
	_, err := LoadRulesFromBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for missing category, got nil")
	}
}

func TestLoadRulesFromBytes_MissingPrompt(t *testing.T) {
	yaml := `
quality_rules:
  bad_rule:
    category: spec_adherence
    severity: advisory
`
	_, err := LoadRulesFromBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for missing prompt, got nil")
	}
}

func TestLoadRulesFromBytes_InvalidSeverity(t *testing.T) {
	yaml := `
quality_rules:
  bad_rule:
    category: spec_adherence
    prompt: "some prompt"
    severity: strict
`
	_, err := LoadRulesFromBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for invalid severity, got nil")
	}
}

func TestLoadRulesFromBytes_InvalidYAML(t *testing.T) {
	_, err := LoadRulesFromBytes([]byte(":::"))
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestLoadRulesFromFile_NotFound(t *testing.T) {
	_, err := LoadRulesFromFile("/nonexistent/path/rules.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadRulesFromFile_Valid(t *testing.T) {
	yaml := `
quality_rules:
  test_rule:
    category: missing_required_tests
    prompt: "check tests"
    severity: block
`
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	rules, err := LoadRulesFromFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Name != "test_rule" {
		t.Errorf("expected rule name 'test_rule', got %q", rules[0].Name)
	}
}

func TestBlockCapableCategories(t *testing.T) {
	blockable := []Category{
		CategoryMissingTests,
		CategorySpecContradiction,
		CategorySecurityUnreviewed,
		CategoryShipperReport,
	}
	for _, cat := range blockable {
		if !IsBlockCapable(cat) {
			t.Errorf("expected %q to be block-capable", cat)
		}
	}

	advisory := Category("spec_adherence")
	if IsBlockCapable(advisory) {
		t.Errorf("expected %q to be advisory (not block-capable)", advisory)
	}
}

func TestRuleIsBlockSeverity(t *testing.T) {
	r := &Rule{Severity: "block"}
	if !r.IsBlockSeverity() {
		t.Error("expected block severity to return true")
	}
	r.Severity = "advisory"
	if r.IsBlockSeverity() {
		t.Error("expected advisory severity to return false")
	}
}
