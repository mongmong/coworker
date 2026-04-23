// Package supervisor implements the deterministic contract rule engine
// that evaluates job outputs against YAML-configured rules.
package supervisor

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Rule is a single contract rule parsed from YAML.
type Rule struct {
	Name      string   `yaml:"-"` // populated from the map key
	AppliesTo []string `yaml:"applies_to"`
	Check     string   `yaml:"check"`
	Message   string   `yaml:"message"`
}

// RuleSet is the top-level structure of a rules YAML file.
type RuleSet struct {
	Rules map[string]Rule `yaml:"rules"`
}

// RuleList is a flattened, validated list of rules ready for evaluation.
type RuleList struct {
	Rules []Rule
}

// LoadRulesFromFile reads and parses a YAML rule file.
func LoadRulesFromFile(path string) (*RuleList, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rules file %q: %w", path, err)
	}
	return LoadRulesFromBytes(data)
}

// LoadRulesFromBytes parses YAML rule bytes into a RuleList.
func LoadRulesFromBytes(data []byte) (*RuleList, error) {
	var rs RuleSet
	if err := yaml.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("parse rules YAML: %w", err)
	}

	if len(rs.Rules) == 0 {
		return nil, fmt.Errorf("rules file contains no rules")
	}

	var rules []Rule
	for name, r := range rs.Rules {
		r.Name = name
		if err := validateRule(&r); err != nil {
			return nil, fmt.Errorf("rule %q: %w", name, err)
		}
		rules = append(rules, r)
	}

	return &RuleList{Rules: rules}, nil
}

// validateRule checks that required fields are present.
func validateRule(r *Rule) error {
	if len(r.AppliesTo) == 0 {
		return fmt.Errorf("applies_to must not be empty")
	}
	if r.Check == "" {
		return fmt.Errorf("check must not be empty")
	}
	if r.Message == "" {
		return fmt.Errorf("message must not be empty")
	}
	return nil
}

// RulesForRole returns the subset of rules that apply to the given role name.
// Matching uses glob semantics: "reviewer.*" matches "reviewer.arch",
// "developer" matches exactly "developer".
func (rl *RuleList) RulesForRole(roleName string) []Rule {
	var matched []Rule
	for _, r := range rl.Rules {
		for _, pattern := range r.AppliesTo {
			if roleGlobMatches(pattern, roleName) {
				matched = append(matched, r)
				break
			}
		}
	}
	return matched
}

// roleGlobMatches converts a role glob pattern to a regex and tests the name.
// "reviewer.*" -> matches "reviewer.arch", "reviewer.frontend"
// "developer" -> matches only "developer" exactly
// "*" -> matches everything
func roleGlobMatches(pattern, roleName string) bool {
	// Escape regex meta-characters except *.
	// First escape dots (they are literal in role names).
	escaped := strings.ReplaceAll(pattern, ".", "\\.")
	// Then convert glob * to regex .*
	escaped = strings.ReplaceAll(escaped, "*", ".*")
	// Anchor the pattern.
	re, err := regexp.Compile("^" + escaped + "$")
	if err != nil {
		return false
	}
	return re.MatchString(roleName)
}
