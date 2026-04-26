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

// SkippedMessages returns the messages from rules that were skipped because
// their applies_when predicate evaluated to false.
func (v *SupervisorVerdict) SkippedMessages() []string {
	var msgs []string
	for _, r := range v.Results {
		if r.Skipped {
			msgs = append(msgs, r.RuleName)
		}
	}
	return msgs
}
