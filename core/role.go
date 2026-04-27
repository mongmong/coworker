package core

// Role is a named job description. Binds an agent to a prompt template,
// inputs/outputs contract, sandbox override, concurrency rule, and skill set.
// Parsed from YAML files under .coworker/roles/.
type Role struct {
	Name           string           `yaml:"name"`
	Concurrency    string           `yaml:"concurrency"`     // "single" | "many"
	CLI            string           `yaml:"cli"`             // "codex" | "claude-code" | "opencode"
	PromptTemplate string           `yaml:"prompt_template"` // relative path to .md file
	Inputs         RoleInputs       `yaml:"inputs"`
	Outputs        RoleOutputs      `yaml:"outputs"`
	Sandbox        string           `yaml:"sandbox"` // "read-only" | "workspace-write" | etc.
	Permissions    RolePermissions  `yaml:"permissions"`
	Budget         RoleBudget       `yaml:"budget"`
	RetryPolicy    RetryPolicy      `yaml:"retry_policy"`
	AppliesWhen    *RoleAppliesWhen `yaml:"applies_when,omitempty"`
}

// RoleAppliesWhen is an optional dispatch-time filter on a role.
// When present, the phase loop evaluates the condition before dispatching.
// If the condition evaluates to false, the role is skipped and a
// job.skipped event is emitted instead.
type RoleAppliesWhen struct {
	// ChangesTouch is a list of glob patterns. The role fires only if the
	// current git diff touches at least one file matching any pattern.
	ChangesTouch []string `yaml:"changes_touch,omitempty"`
}

// RoleInputs declares the required and optional inputs for a role.
type RoleInputs struct {
	Required []string `yaml:"required"`
	Optional []string `yaml:"optional,omitempty"`
}

// RoleOutputs declares the output contract for a role.
type RoleOutputs struct {
	Contract map[string]interface{} `yaml:"contract"`
	Emits    map[string]interface{} `yaml:"emits"`
}

// RolePermissions declares the expected permission surface of a role.
type RolePermissions struct {
	AllowedTools  []string `yaml:"allowed_tools"`
	Never         []string `yaml:"never"`
	RequiresHuman []string `yaml:"requires_human"`
}

// RoleBudget sets resource limits for jobs of this role.
type RoleBudget struct {
	MaxTokensPerJob     int     `yaml:"max_tokens_per_job"`
	MaxWallclockMinutes int     `yaml:"max_wallclock_minutes"`
	MaxCostUSD          float64 `yaml:"max_cost_usd"`
}

// RetryPolicy controls how failed jobs are retried.
type RetryPolicy struct {
	OnContractFail string `yaml:"on_contract_fail"` // "retry_with_feedback" | "fail"
	OnJobError     string `yaml:"on_job_error"`     // "retry_once" | "fail"
}
