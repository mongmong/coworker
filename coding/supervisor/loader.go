// Package supervisor implements the deterministic contract rule engine
// that evaluates job outputs against YAML-configured rules.
package supervisor

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// AppliesWhenClause is the structured form of the applies_when YAML block.
// In V1 the only supported key is changes_touch (a list of file globs).
// When the clause is nil or empty, the rule applies unconditionally.
type AppliesWhenClause struct {
	// ChangesTouch is a list of glob patterns (using path.Match semantics).
	// The rule fires only if the current git diff touches at least one file
	// that matches any of the listed patterns.
	ChangesTouch []string `yaml:"changes_touch"`
}

// Rule is a single contract rule parsed from YAML.
type Rule struct {
	Name        string             `yaml:"-"` // populated from the map key
	AppliesTo   []string           `yaml:"applies_to"`
	AppliesWhen *AppliesWhenClause `yaml:"applies_when,omitempty"`
	Check       string             `yaml:"check"`
	Message     string             `yaml:"message"`
	compiled    []*regexp.Regexp   `yaml:"-"`
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
		compiled, err := compileRoleGlobs(r.AppliesTo)
		if err != nil {
			return nil, fmt.Errorf("rule %q: compile applies_to: %w", name, err)
		}
		r.compiled = compiled
		rules = append(rules, r)
	}

	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Name < rules[j].Name
	})

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
		if roleGlobMatches(r.roleRegexps(), roleName) {
			matched = append(matched, r)
		}
	}
	return matched
}

func (r Rule) roleRegexps() []*regexp.Regexp {
	if len(r.compiled) == len(r.AppliesTo) {
		return r.compiled
	}

	compiled, err := compileRoleGlobs(r.AppliesTo)
	if err != nil {
		return nil
	}
	return compiled
}

// roleGlobMatches tests the role name against precompiled glob regexes.
func roleGlobMatches(patterns []*regexp.Regexp, roleName string) bool {
	for _, re := range patterns {
		if re.MatchString(roleName) {
			return true
		}
	}
	return false
}

func compileRoleGlobs(patterns []string) ([]*regexp.Regexp, error) {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		escaped := regexp.QuoteMeta(pattern)
		escaped = strings.ReplaceAll(escaped, `\*`, ".*")
		re, err := regexp.Compile("^" + escaped + "$")
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}
