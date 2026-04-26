package quality

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// LoadRulesFromFile reads and parses a YAML quality rules file.
func LoadRulesFromFile(path string) ([]*Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read quality rules file %q: %w", path, err)
	}
	return LoadRulesFromBytes(data)
}

// LoadRulesFromBytes parses quality rules from YAML bytes.
// Returns rules sorted by name for deterministic ordering.
func LoadRulesFromBytes(data []byte) ([]*Rule, error) {
	var rs RuleSet
	if err := yaml.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("parse quality rules YAML: %w", err)
	}

	if len(rs.Rules) == 0 {
		return nil, fmt.Errorf("quality rules file contains no rules")
	}

	rules := make([]*Rule, 0, len(rs.Rules))
	for name, r := range rs.Rules {
		r := r // capture loop variable
		r.Name = name
		if err := validateRule(&r); err != nil {
			return nil, fmt.Errorf("quality rule %q: %w", name, err)
		}
		rules = append(rules, &r)
	}

	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Name < rules[j].Name
	})

	return rules, nil
}

// validateRule checks that required fields are non-empty and valid.
func validateRule(r *Rule) error {
	if r.Category == "" {
		return fmt.Errorf("category must not be empty")
	}
	if r.Prompt == "" {
		return fmt.Errorf("prompt must not be empty")
	}
	if r.Severity != "block" && r.Severity != "advisory" {
		return fmt.Errorf("severity must be %q or %q, got %q", "block", "advisory", r.Severity)
	}
	return nil
}
