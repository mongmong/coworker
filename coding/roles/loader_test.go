package roles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRole_ReviewerArch(t *testing.T) {
	// Use the actual shipped role file.
	// The test is run from the package directory, so we need to find the repo root.
	roleDir := findRoleDir(t)

	role, err := LoadRole(roleDir, "reviewer.arch")
	if err != nil {
		t.Fatalf("LoadRole: %v", err)
	}

	if role.Name != "reviewer.arch" {
		t.Errorf("name = %q, want %q", role.Name, "reviewer.arch")
	}
	if role.CLI != "codex" {
		t.Errorf("cli = %q, want %q", role.CLI, "codex")
	}
	if role.Concurrency != "single" {
		t.Errorf("concurrency = %q, want %q", role.Concurrency, "single")
	}
	if role.PromptTemplate != "prompts/reviewer_arch.md" {
		t.Errorf("prompt_template = %q, want %q", role.PromptTemplate, "prompts/reviewer_arch.md")
	}
	if len(role.Inputs.Required) != 2 {
		t.Errorf("inputs.required length = %d, want 2", len(role.Inputs.Required))
	}
	if role.Sandbox != "read-only" {
		t.Errorf("sandbox = %q, want %q", role.Sandbox, "read-only")
	}
	if role.Budget.MaxCostUSD != 5.00 {
		t.Errorf("budget.max_cost_usd = %f, want 5.00", role.Budget.MaxCostUSD)
	}
	if len(role.Permissions.AllowedTools) != 3 {
		t.Errorf("permissions.allowed_tools length = %d, want 3", len(role.Permissions.AllowedTools))
	}
}

func TestLoadRole_MissingFile(t *testing.T) {
	_, err := LoadRole("/nonexistent", "reviewer.arch")
	if err == nil {
		t.Error("expected error for missing role file, got nil")
	}
}

func TestLoadRole_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "bad_role.yaml"), []byte(":::not yaml"), 0644)
	if err != nil {
		t.Fatalf("write test file: %v", err)
	}

	_, err = LoadRole(dir, "bad.role")
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

func TestLoadRole_MissingRequiredFields(t *testing.T) {
	dir := t.TempDir()

	// Role with missing name.
	err := os.WriteFile(filepath.Join(dir, "empty.yaml"), []byte("cli: codex\n"), 0644)
	if err != nil {
		t.Fatalf("write test file: %v", err)
	}

	_, err = LoadRole(dir, "empty")
	if err == nil {
		t.Error("expected validation error for missing name, got nil")
	}
}

func TestLoadRole_InvalidConcurrency(t *testing.T) {
	dir := t.TempDir()
	yaml := `
name: test.role
cli: codex
concurrency: invalid
prompt_template: prompts/test.md
inputs:
  required: [diff_path]
`
	err := os.WriteFile(filepath.Join(dir, "test_role.yaml"), []byte(yaml), 0644)
	if err != nil {
		t.Fatalf("write test file: %v", err)
	}

	_, err = LoadRole(dir, "test.role")
	if err == nil {
		t.Error("expected validation error for invalid concurrency, got nil")
	}
	if !strings.Contains(err.Error(), "concurrency") {
		t.Errorf("error should mention concurrency: %v", err)
	}
}

func TestLoadPromptTemplate(t *testing.T) {
	promptDir := findPromptDir(t)

	tmpl, err := LoadPromptTemplate(promptDir, "prompts/reviewer_arch.md")
	if err != nil {
		t.Fatalf("LoadPromptTemplate: %v", err)
	}

	rendered, err := RenderPrompt(tmpl, map[string]string{
		"DiffPath": "/tmp/test.diff",
		"SpecPath": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("RenderPrompt: %v", err)
	}

	if !strings.Contains(rendered, "/tmp/test.diff") {
		t.Error("rendered prompt should contain diff path")
	}
	if !strings.Contains(rendered, "/tmp/spec.md") {
		t.Error("rendered prompt should contain spec path")
	}
}

func TestLoadPromptTemplate_MissingFile(t *testing.T) {
	_, err := LoadPromptTemplate("/nonexistent", "missing.md")
	if err == nil {
		t.Error("expected error for missing template, got nil")
	}
}

func TestDotToUnderscore(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"reviewer.arch", "reviewer_arch"},
		{"reviewer.frontend", "reviewer_frontend"},
		{"developer", "developer"},
		{"a.b.c", "a_b_c"},
	}
	for _, tt := range tests {
		got := dotToUnderscore(tt.input)
		if got != tt.want {
			t.Errorf("dotToUnderscore(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// findRoleDir locates the coding/roles directory relative to the test file.
// Since tests run from the package directory (coding/roles/), the YAML file
// is in the same directory.
func findRoleDir(t *testing.T) string {
	t.Helper()
	// We are in coding/roles/ package directory.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Check if the YAML file exists in the current directory.
	if _, err := os.Stat(filepath.Join(wd, "reviewer_arch.yaml")); err == nil {
		return wd
	}
	t.Fatalf("cannot find role dir from %q", wd)
	return ""
}

// findPromptDir locates the coding/ directory (parent of roles/).
func findPromptDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// roles/ is under coding/, so parent is coding/.
	codingDir := filepath.Dir(wd)
	if _, err := os.Stat(filepath.Join(codingDir, "prompts", "reviewer_arch.md")); err == nil {
		return codingDir
	}
	t.Fatalf("cannot find prompt dir from %q", wd)
	return ""
}
