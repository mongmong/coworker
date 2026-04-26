package core

// Supervisor EventKinds.
const (
	EventSupervisorVerdict EventKind = "supervisor.verdict"
	EventSupervisorRetry   EventKind = "supervisor.retry"
	EventComplianceBreach  EventKind = "compliance-breach"
)

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
