package policy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chris/coworker/core"
)

func TestLoadPolicy_Defaults(t *testing.T) {
	t.Parallel()

	loader := &PolicyLoader{}
	policy, err := loader.LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}

	if policy.Checkpoints["spec-approved"] != core.CheckpointActionBlock {
		t.Fatalf("checkpoints[spec-approved] = %q, want %q", policy.Checkpoints["spec-approved"], core.CheckpointActionBlock)
	}
	if policy.SupervisorLimits.MaxRetriesPerJob != 3 {
		t.Fatalf("supervisor_limits.max_retries_per_job = %d, want %d", policy.SupervisorLimits.MaxRetriesPerJob, 3)
	}
	if policy.Source["checkpoints.spec-approved"] != "built-in default" {
		t.Fatalf("source checkpoints.spec-approved = %q, want built-in default", policy.Source["checkpoints.spec-approved"])
	}
	if policy.Source["concurrency.max_parallel_reviewers"] != "built-in default" {
		t.Fatalf("source concurrency.max_parallel_reviewers = %q, want built-in default", policy.Source["concurrency.max_parallel_reviewers"])
	}
}

func TestLoadPolicy_GlobalAndRepoOverride(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global_policy.yaml")
	repoPath := filepath.Join(dir, "repo_policy.yaml")

	globalYAML := `
checkpoints:
  spec-approved: auto
supervisor_limits:
  max_retries_per_job: 9
permissions:
  on_undeclared: warn
workflow_overrides:
  build-from-prd:
    phase-review: [reviewer.arch]
`
	if err := os.WriteFile(globalPath, []byte(globalYAML), 0600); err != nil {
		t.Fatalf("write global policy: %v", err)
	}

	repoYAML := `
checkpoints:
  spec-approved: never
supervisor_limits:
  max_retries_per_job: 1
workflow_overrides:
  build-from-prd:
    phase-review: [reviewer.arch, reviewer.frontend]
`
	if err := os.WriteFile(repoPath, []byte(repoYAML), 0600); err != nil {
		t.Fatalf("write repo policy: %v", err)
	}

	loader := &PolicyLoader{
		GlobalConfigPath: globalPath,
		RepoConfigPath:   repoPath,
	}
	policy, err := loader.LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}

	if policy.Checkpoints["spec-approved"] != core.CheckpointActionNever {
		t.Fatalf("checkpoints[spec-approved] = %q, want %q", policy.Checkpoints["spec-approved"], core.CheckpointActionNever)
	}
	if policy.Source["checkpoints.spec-approved"] != "repo "+repoPath {
		t.Fatalf("source checkpoints.spec-approved = %q, want %q", policy.Source["checkpoints.spec-approved"], "repo "+repoPath)
	}

	if policy.SupervisorLimits.MaxRetriesPerJob != 1 {
		t.Fatalf("supervisor_limits.max_retries_per_job = %d, want %d", policy.SupervisorLimits.MaxRetriesPerJob, 1)
	}
	if policy.Source["supervisor_limits.max_retries_per_job"] != "repo "+repoPath {
		t.Fatalf("source max_retries_per_job = %q, want %q", policy.Source["supervisor_limits.max_retries_per_job"], "repo "+repoPath)
	}

	roles := policy.WorkflowOverrides["build-from-prd"]["phase-review"]
	if got, want := roles, []string{"reviewer.arch", "reviewer.frontend"}; len(got) != len(want) {
		t.Fatalf("workflow override roles = %#v, want %#v", got, want)
	} else {
		for i, role := range want {
			if got[i] != role {
				t.Fatalf("workflow override roles[%d] = %q, want %q", i, got[i], role)
			}
		}
	}
	if policy.Source["workflow_overrides.build-from-prd.phase-review"] != "repo "+repoPath {
		t.Fatalf("source workflow_overrides.build-from-prd.phase-review = %q, want %q", policy.Source["workflow_overrides.build-from-prd.phase-review"], "repo "+repoPath)
	}
}

func TestLoadPolicy_InvalidYAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	globalPath := filepath.Join(dir, "bad_policy.yaml")
	if err := os.WriteFile(globalPath, []byte(":::not yaml"), 0600); err != nil {
		t.Fatalf("write bad policy: %v", err)
	}

	loader := &PolicyLoader{GlobalConfigPath: globalPath}
	if _, err := loader.LoadPolicy(); err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}
