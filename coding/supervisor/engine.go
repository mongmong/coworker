package supervisor

import (
	"fmt"

	"github.com/chris/coworker/core"
)

// RuleEngine evaluates contract rules against job outputs.
// It is pure logic — event writing is the caller's responsibility.
type RuleEngine struct {
	rules *RuleList
}

// NewRuleEngine creates an engine from a loaded rule list.
func NewRuleEngine(rules *RuleList) *RuleEngine {
	return &RuleEngine{rules: rules}
}

// NewRuleEngineFromFile loads rules from a YAML file and creates an engine.
func NewRuleEngineFromFile(path string) (*RuleEngine, error) {
	rules, err := LoadRulesFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("load rules: %w", err)
	}
	return NewRuleEngine(rules), nil
}

// NewRuleEngineFromBytes loads rules from YAML bytes and creates an engine.
func NewRuleEngineFromBytes(data []byte) (*RuleEngine, error) {
	rules, err := LoadRulesFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("load rules: %w", err)
	}
	return NewRuleEngine(rules), nil
}

// Evaluate runs all applicable rules for the given role against the
// job result and returns a SupervisorVerdict. All rules are evaluated
// even if some fail (no short-circuit) so the feedback includes all
// violations.
func (e *RuleEngine) Evaluate(ctx *EvalContext) (*core.SupervisorVerdict, error) {
	if ctx.Role == nil {
		return nil, fmt.Errorf("EvalContext.Role must not be nil")
	}

	applicable := e.rules.RulesForRole(ctx.Role.Name)

	// If no rules apply, verdict is pass.
	if len(applicable) == 0 {
		return &core.SupervisorVerdict{Pass: true}, nil
	}

	verdict := &core.SupervisorVerdict{Pass: true}

	for _, rule := range applicable {
		funcName, args, err := ParseCheck(rule.Check)
		if err != nil {
			verdict.Pass = false
			verdict.Results = append(verdict.Results, core.RuleResult{
				RuleName: rule.Name,
				Passed:   false,
				Message:  fmt.Sprintf("failed to parse check %q: %v", rule.Check, err),
			})
			continue
		}

		predFn, err := LookupPredicate(funcName)
		if err != nil {
			verdict.Pass = false
			verdict.Results = append(verdict.Results, core.RuleResult{
				RuleName: rule.Name,
				Passed:   false,
				Message:  fmt.Sprintf("unknown predicate %q in rule %q", funcName, rule.Name),
			})
			continue
		}

		passed, err := predFn(ctx, args)
		if err != nil {
			verdict.Pass = false
			verdict.Results = append(verdict.Results, core.RuleResult{
				RuleName: rule.Name,
				Passed:   false,
				Message:  fmt.Sprintf("predicate %q error: %v", funcName, err),
			})
			continue
		}

		result := core.RuleResult{
			RuleName: rule.Name,
			Passed:   passed,
			Message:  rule.Message,
		}
		if !passed {
			verdict.Pass = false
		}
		verdict.Results = append(verdict.Results, result)
	}

	return verdict, nil
}

// RuleCount returns the total number of loaded rules.
func (e *RuleEngine) RuleCount() int {
	return len(e.rules.Rules)
}

// RulesForRole returns the rules that apply to the given role name.
func (e *RuleEngine) RulesForRole(roleName string) []Rule {
	return e.rules.RulesForRole(roleName)
}
