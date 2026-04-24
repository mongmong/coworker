package core

// CheckpointAction controls whether a checkpoint blocks progress.
type CheckpointAction string

const (
	CheckpointActionBlock     CheckpointAction = "block"
	CheckpointActionOnFailure CheckpointAction = "on-failure"
	CheckpointActionAuto      CheckpointAction = "auto"
	CheckpointActionNever     CheckpointAction = "never"
)

// SupervisorLimits configures retry and fix-loop behavior for supervisor decisions.
type SupervisorLimits struct {
	MaxRetriesPerJob     int `yaml:"max_retries_per_job" json:"max_retries_per_job"`
	MaxFixCyclesPerPhase int `yaml:"max_fix_cycles_per_phase" json:"max_fix_cycles_per_phase"`
}

// ConcurrencyLimits configures workflow parallelism defaults.
type ConcurrencyLimits struct {
	MaxParallelPlans     int `yaml:"max_parallel_plans" json:"max_parallel_plans"`
	MaxParallelReviewers int `yaml:"max_parallel_reviewers" json:"max_parallel_reviewers"`
}

// PermissionPolicy describes how undeclared permissions are handled.
type PermissionPolicy struct {
	OnUndeclared string `yaml:"on_undeclared" json:"on_undeclared"`
}

// Policy represents the merged policy configuration.
type Policy struct {
	Checkpoints       map[string]CheckpointAction    `yaml:"checkpoints" json:"checkpoints"`
	SupervisorLimits  SupervisorLimits               `yaml:"supervisor_limits" json:"supervisor_limits"`
	ConcurrencyLimits ConcurrencyLimits              `yaml:"concurrency" json:"concurrency"`
	PermissionPolicy  PermissionPolicy               `yaml:"permissions" json:"permissions"`
	WorkflowOverrides map[string]map[string][]string `yaml:"workflow_overrides" json:"workflow_overrides"`
	Source            map[string]string              `json:"-" yaml:"-"`
}
