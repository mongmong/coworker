// Package roles loads role definitions from YAML files and their
// associated prompt templates from the filesystem.
package roles

import (
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/chris/coworker/core"
	"gopkg.in/yaml.v3"
)

// LoadRole reads a role YAML file from roleDir and returns the parsed Role.
// roleDir is the directory containing role YAML files (e.g., ".coworker/roles/").
// roleName is the dotted name (e.g., "reviewer.arch") which maps to
// "reviewer_arch.yaml" on disk (dots replaced with underscores).
func LoadRole(roleDir, roleName string) (*core.Role, error) {
	// Convert dotted name to file name: "reviewer.arch" -> "reviewer_arch.yaml"
	fileName := dotToUnderscore(roleName) + ".yaml"
	path := filepath.Join(roleDir, fileName)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read role %q from %q: %w", roleName, path, err)
	}

	var role core.Role
	if err := yaml.Unmarshal(data, &role); err != nil {
		return nil, fmt.Errorf("parse role %q: %w", roleName, err)
	}

	if err := validateRole(&role); err != nil {
		return nil, fmt.Errorf("validate role %q: %w", roleName, err)
	}

	return &role, nil
}

// LoadPromptTemplate reads and parses a prompt template file.
// promptDir is the directory containing prompt .md files.
// templatePath is the relative path from the role's prompt_template field.
func LoadPromptTemplate(promptDir, templatePath string) (*template.Template, error) {
	path := filepath.Join(promptDir, templatePath)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read prompt template %q: %w", path, err)
	}

	tmpl, err := template.New(filepath.Base(templatePath)).Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse prompt template %q: %w", templatePath, err)
	}

	return tmpl, nil
}

// RenderPrompt renders a prompt template with the given data.
func RenderPrompt(tmpl *template.Template, data interface{}) (string, error) {
	var buf []byte
	w := &byteWriter{buf: &buf}
	if err := tmpl.Execute(w, data); err != nil {
		return "", fmt.Errorf("render prompt: %w", err)
	}
	return string(buf), nil
}

// byteWriter is a simple io.Writer that appends to a byte slice.
type byteWriter struct {
	buf *[]byte
}

func (w *byteWriter) Write(p []byte) (int, error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}

// validateRole checks that required fields are present.
func validateRole(role *core.Role) error {
	if role.Name == "" {
		return fmt.Errorf("name is required")
	}
	if role.CLI == "" {
		return fmt.Errorf("cli is required")
	}
	if role.PromptTemplate == "" {
		return fmt.Errorf("prompt_template is required")
	}
	if role.Concurrency == "" {
		return fmt.Errorf("concurrency is required")
	}
	if role.Concurrency != "single" && role.Concurrency != "many" {
		return fmt.Errorf("concurrency must be 'single' or 'many', got %q", role.Concurrency)
	}
	if len(role.Inputs.Required) == 0 {
		return fmt.Errorf("inputs.required must have at least one entry")
	}
	return nil
}

// dotToUnderscore replaces dots with underscores in a string.
func dotToUnderscore(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			result[i] = '_'
		} else {
			result[i] = s[i]
		}
	}
	return string(result)
}
