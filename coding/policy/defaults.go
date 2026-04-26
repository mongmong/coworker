package policy

import "github.com/chris/coworker/core"

const builtinSource = "built-in default"

// DefaultPolicy returns the built-in policy defaults.
func DefaultPolicy() *core.Policy {
	return &core.Policy{
		Checkpoints: map[string]core.CheckpointAction{
			"spec-approved":     core.CheckpointActionBlock,
			"plan-approved":     core.CheckpointActionBlock,
			"phase-clean":       core.CheckpointActionOnFailure,
			"ready-to-ship":     core.CheckpointActionBlock,
			"compliance-breach": core.CheckpointActionBlock,
			"quality-gate":      core.CheckpointActionBlock,
		},
		SupervisorLimits: core.SupervisorLimits{
			MaxRetriesPerJob:     3,
			MaxFixCyclesPerPhase: 5,
		},
		ConcurrencyLimits: core.ConcurrencyLimits{
			MaxParallelPlans:     2,
			MaxParallelReviewers: 3,
		},
		PermissionPolicy: core.PermissionPolicy{
			OnUndeclared: "deny",
		},
		WorkflowOverrides: make(map[string]map[string][]string),
		Source:            map[string]string{},
	}
}

func defaultSourceMap(policy *core.Policy) {
	for checkpoint := range policy.Checkpoints {
		policy.Source["checkpoints."+checkpoint] = builtinSource
	}

	policy.Source["supervisor_limits.max_retries_per_job"] = builtinSource
	policy.Source["supervisor_limits.max_fix_cycles_per_phase"] = builtinSource
	policy.Source["concurrency.max_parallel_plans"] = builtinSource
	policy.Source["concurrency.max_parallel_reviewers"] = builtinSource
	policy.Source["permissions.on_undeclared"] = builtinSource
}
