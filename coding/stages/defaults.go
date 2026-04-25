package stages

// DefaultStages defines the default role lists for each named stage of the
// build-from-prd workflow.
//
// These match the spec catalog (§Workflow customization):
//   - phase-dev:    developer writes code
//   - phase-review: arch + frontend reviewers run in parallel
//   - phase-test:   tester validates correctness
//   - phase-ship:   shipper creates the PR
//
// All four can be overridden via policy.yaml workflow_overrides.
var DefaultStages = map[string][]string{
	"phase-dev":    {"developer"},
	"phase-review": {"reviewer.arch", "reviewer.frontend"},
	"phase-test":   {"tester"},
	"phase-ship":   {"shipper"},
}

// WorkflowBuildFromPRD is the canonical name for the autopilot workflow.
const WorkflowBuildFromPRD = "build-from-prd"
