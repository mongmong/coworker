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
	Message  string
}

func (v *SupervisorVerdict) FailedMessages() []string {
	var msgs []string
	for _, r := range v.Results {
		if !r.Passed {
			msgs = append(msgs, r.Message)
		}
	}
	return msgs
}
