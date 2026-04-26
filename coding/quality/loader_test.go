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

// TestLoadRulesFromBytes_SeverityCategoryMismatch verifies that rules with
// contradictory severity/category combinations are accepted (not rejected) and
// that the loader does not return an error. The slog warning is a best-effort
// signal; the rule is still usable.
func TestLoadRulesFromBytes_SeverityCategoryMismatch(t *testing.T) {
	// block-capable category with advisory severity — accepted with warning.
	yaml1 := `
quality_rules:
  mismatch_advisory:
    category: missing_required_tests
    prompt: "check tests"
    severity: advisory
`
	rules, err := LoadRulesFromBytes([]byte(yaml1))
	if err != nil {
		t.Fatalf("unexpected error for advisory severity on block-capable category: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}

	// non-block-capable category with block severity — accepted with warning.
	yaml2 := `
quality_rules:
  mismatch_block:
    category: spec_adherence
    prompt: "check spec"
    severity: block
`
	rules, err = LoadRulesFromBytes([]byte(yaml2))
	if err != nil {
		t.Fatalf("unexpected error for block severity on non-block-capable category: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
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
