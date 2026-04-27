package core

// Supervisor and quality event kinds previously lived here. They have moved
// to core/event.go alongside the rest of the EventKind catalog (Plan 127, I9).

type SupervisorVerdict struct {
	Pass    bool
	Results []RuleResult
}

type RuleResult struct {
	RuleName string
	Passed   bool
	// Skipped is true when the rule's applies_when predicate evaluated to false.
	// A skipped rule does not contribute to verdict.Pass.
	Skipped bool
	Message string
}

func (v *SupervisorVerdict) FailedMessages() []string {
	var msgs []string
	for _, r := range v.Results {
		if !r.Passed && !r.Skipped {
			msgs = append(msgs, r.Message)
		}
	}
	return msgs
}

// SkippedRuleNames returns the names of rules that were skipped because
// their applies_when predicate evaluated to false.
// Named "RuleNames" (not "Messages") to make the asymmetry with
// FailedMessages explicit — FailedMessages returns human-readable strings
// while this returns machine identifiers.
func (v *SupervisorVerdict) SkippedRuleNames() []string {
	var names []string
	for _, r := range v.Results {
		if r.Skipped {
			names = append(names, r.RuleName)
		}
	}
	return names
}
