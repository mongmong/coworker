package policy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chris/coworker/core"
	"gopkg.in/yaml.v3"
)

// Loader loads and merges policy from multiple layers.
type Loader struct {
	GlobalConfigPath string // e.g., ~/.config/coworker/policy.yaml
	RepoConfigPath   string // e.g., .coworker/policy.yaml
}

type policyDocument struct {
	Checkpoints       map[string]core.CheckpointAction `yaml:"checkpoints"`
	SupervisorLimits  *supervisorLimitsDocument        `yaml:"supervisor_limits"`
	ConcurrencyLimits *concurrencyLimitsDocument       `yaml:"concurrency"`
	PermissionPolicy  *permissionPolicyDocument        `yaml:"permissions"`
	WorkflowOverrides map[string]map[string][]string   `yaml:"workflow_overrides"`
}

type supervisorLimitsDocument struct {
	MaxRetriesPerJob     *int `yaml:"max_retries_per_job"`
	MaxFixCyclesPerPhase *int `yaml:"max_fix_cycles_per_phase"`
}

type concurrencyLimitsDocument struct {
	MaxParallelPlans     *int `yaml:"max_parallel_plans"`
	MaxParallelReviewers *int `yaml:"max_parallel_reviewers"`
}

type permissionPolicyDocument struct {
	OnUndeclared *string `yaml:"on_undeclared"`
}

// LoadPolicy merges policy layers in order: built-in -> global -> repo.
func (l *Loader) LoadPolicy() (*core.Policy, error) {
	policy := DefaultPolicy()
	defaultSourceMap(policy)

	if l.GlobalConfigPath != "" {
		if err := l.mergeFromPath(policy, l.GlobalConfigPath, "global "+l.GlobalConfigPath); err != nil {
			return nil, err
		}
	}
	if l.RepoConfigPath != "" {
		if err := l.mergeFromPath(policy, l.RepoConfigPath, "repo "+l.RepoConfigPath); err != nil {
			return nil, err
		}
	}

	return policy, nil
}

func (l *Loader) mergeFromPath(policy *core.Policy, path string, source string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read policy %q: %w", path, err)
	}

	var doc policyDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse policy %q: %w", filepath.Base(path), err)
	}

	l.mergePolicy(policy, &doc, source)
	return nil
}

func (l *Loader) mergePolicy(policy *core.Policy, doc *policyDocument, source string) {
	if doc.Checkpoints != nil {
		if policy.Checkpoints == nil {
			policy.Checkpoints = make(map[string]core.CheckpointAction)
		}
		for checkpoint, action := range doc.Checkpoints {
			policy.Checkpoints[checkpoint] = action
			policy.Source["checkpoints."+checkpoint] = source
		}
	}

	if doc.SupervisorLimits != nil {
		if doc.SupervisorLimits.MaxRetriesPerJob != nil {
			policy.SupervisorLimits.MaxRetriesPerJob = *doc.SupervisorLimits.MaxRetriesPerJob
			policy.Source["supervisor_limits.max_retries_per_job"] = source
		}
		if doc.SupervisorLimits.MaxFixCyclesPerPhase != nil {
			policy.SupervisorLimits.MaxFixCyclesPerPhase = *doc.SupervisorLimits.MaxFixCyclesPerPhase
			policy.Source["supervisor_limits.max_fix_cycles_per_phase"] = source
		}
	}

	if doc.ConcurrencyLimits != nil {
		if doc.ConcurrencyLimits.MaxParallelPlans != nil {
			policy.ConcurrencyLimits.MaxParallelPlans = *doc.ConcurrencyLimits.MaxParallelPlans
			policy.Source["concurrency.max_parallel_plans"] = source
		}
		if doc.ConcurrencyLimits.MaxParallelReviewers != nil {
			policy.ConcurrencyLimits.MaxParallelReviewers = *doc.ConcurrencyLimits.MaxParallelReviewers
			policy.Source["concurrency.max_parallel_reviewers"] = source
		}
	}

	if doc.PermissionPolicy != nil && doc.PermissionPolicy.OnUndeclared != nil {
		policy.PermissionPolicy.OnUndeclared = *doc.PermissionPolicy.OnUndeclared
		policy.Source["permissions.on_undeclared"] = source
	}

	if doc.WorkflowOverrides != nil {
		if policy.WorkflowOverrides == nil {
			policy.WorkflowOverrides = make(map[string]map[string][]string)
		}
		for workflow, stages := range doc.WorkflowOverrides {
			if _, ok := policy.WorkflowOverrides[workflow]; !ok {
				policy.WorkflowOverrides[workflow] = make(map[string][]string)
			}
			for stage, roles := range stages {
				overrides := append([]string(nil), roles...)
				policy.WorkflowOverrides[workflow][stage] = overrides
				policy.Source[fmt.Sprintf("workflow_overrides.%s.%s", workflow, stage)] = source
			}
		}
	}
}

// InspectString returns a human-readable policy dump with source annotations.
func InspectString(policy *core.Policy) string {
	var sb strings.Builder
	if policy == nil {
		return "# Effective Policy\n<nil>\n"
	}

	sb.WriteString("# Effective Policy\n\n")

	sb.WriteString("## Checkpoints\n")
	checkpointNames := make([]string, 0, len(policy.Checkpoints))
	for checkpoint := range policy.Checkpoints {
		checkpointNames = append(checkpointNames, checkpoint)
	}
	sort.Strings(checkpointNames)
	for _, checkpoint := range checkpointNames {
		action := policy.Checkpoints[checkpoint]
		src := policy.Source["checkpoints."+checkpoint]
		if src == "" {
			src = builtinSource
		}
		fmt.Fprintf(&sb, "  %s: %s (from %s)\n", checkpoint, action, src)
	}

	sb.WriteString("\n## Supervisor Limits\n")
	fmt.Fprintf(&sb,
		"  max_retries_per_job: %d (from %s)\n",
		policy.SupervisorLimits.MaxRetriesPerJob,
		sourceOrDefault(policy.Source["supervisor_limits.max_retries_per_job"]),
	)
	fmt.Fprintf(&sb,
		"  max_fix_cycles_per_phase: %d (from %s)\n",
		policy.SupervisorLimits.MaxFixCyclesPerPhase,
		sourceOrDefault(policy.Source["supervisor_limits.max_fix_cycles_per_phase"]),
	)

	sb.WriteString("\n## Concurrency\n")
	fmt.Fprintf(&sb,
		"  max_parallel_plans: %d (from %s)\n",
		policy.ConcurrencyLimits.MaxParallelPlans,
		sourceOrDefault(policy.Source["concurrency.max_parallel_plans"]),
	)
	fmt.Fprintf(&sb,
		"  max_parallel_reviewers: %d (from %s)\n",
		policy.ConcurrencyLimits.MaxParallelReviewers,
		sourceOrDefault(policy.Source["concurrency.max_parallel_reviewers"]),
	)

	sb.WriteString("\n## Permissions\n")
	fmt.Fprintf(&sb,
		"  on_undeclared: %s (from %s)\n",
		policy.PermissionPolicy.OnUndeclared,
		sourceOrDefault(policy.Source["permissions.on_undeclared"]),
	)

	sb.WriteString("\n## Workflow Overrides\n")
	if len(policy.WorkflowOverrides) == 0 {
		sb.WriteString("  <none>\n")
		return sb.String()
	}

	workflowNames := make([]string, 0, len(policy.WorkflowOverrides))
	for workflow := range policy.WorkflowOverrides {
		workflowNames = append(workflowNames, workflow)
	}
	sort.Strings(workflowNames)

	for _, workflow := range workflowNames {
		stages := policy.WorkflowOverrides[workflow]
		sb.WriteString("  " + workflow + "\n")
		stageNames := make([]string, 0, len(stages))
		for stage := range stages {
			stageNames = append(stageNames, stage)
		}
		sort.Strings(stageNames)
		for _, stage := range stageNames {
			key := fmt.Sprintf("workflow_overrides.%s.%s", workflow, stage)
			fmt.Fprintf(&sb, "    %s: %v (from %s)\n", stage, stages[stage], sourceOrDefault(policy.Source[key]))
		}
	}

	return sb.String()
}

func sourceOrDefault(source string) string {
	if source == "" {
		return builtinSource
	}
	return source
}
