package phaseloop

import (
	"fmt"
	"strings"

	"github.com/chris/coworker/core"
)

// DefaultMaxFixCycles is the default maximum number of developer fix cycles
// per phase before a phase-clean checkpoint is raised.
const DefaultMaxFixCycles = 5

// BuildFindingFeedback constructs a human-readable feedback string from a
// deduplicated finding list. The string is prepended to the developer's next
// prompt so the agent knows which issues to address.
//
// Returns an empty string when findings is empty.
func BuildFindingFeedback(findings []core.Finding) string {
	if len(findings) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("REVIEW FEEDBACK: The following issues were found and must be fixed:\n")

	for i, f := range findings {
		severity := string(f.Severity)
		if severity == "" {
			severity = "unspecified"
		}

		// Format: "  N. [severity] path:line — body"
		fmt.Fprintf(&sb, "  %d. [%s] %s", i+1, severity, f.Body)
		if f.Path != "" {
			location := f.Path
			if f.Line > 0 {
				location = fmt.Sprintf("%s:%d", f.Path, f.Line)
			}
			fmt.Fprintf(&sb, " (at %s)", location)
		}
		if len(f.SourceJobIDs) > 1 {
			fmt.Fprintf(&sb, " [reported by %d reviewers]", len(f.SourceJobIDs))
		}
		sb.WriteByte('\n')
	}

	sb.WriteString("\nPlease address all findings above before completing the phase.")
	return sb.String()
}

// maxFixCycles returns the effective max fix-cycle count from the policy.
// Falls back to DefaultMaxFixCycles when the policy value is zero or negative.
func maxFixCycles(policy *core.Policy) int {
	if policy == nil {
		return DefaultMaxFixCycles
	}
	if policy.SupervisorLimits.MaxFixCyclesPerPhase <= 0 {
		return DefaultMaxFixCycles
	}
	return policy.SupervisorLimits.MaxFixCyclesPerPhase
}
