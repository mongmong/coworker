package stages_test

import (
	"slices"
	"testing"

	"github.com/chris/coworker/coding/stages"
	"github.com/chris/coworker/core"
)

func TestStageRegistry_DefaultRoles(t *testing.T) {
	reg := stages.NewStageRegistry(stages.WorkflowBuildFromPRD, stages.DefaultStages, nil)

	cases := []struct {
		stage string
		want  []string
	}{
		{"phase-dev", []string{"developer"}},
		{"phase-review", []string{"reviewer.arch", "reviewer.frontend"}},
		{"phase-test", []string{"tester"}},
		{"phase-ship", []string{"shipper"}},
	}

	for _, tc := range cases {
		got := reg.RolesForStage(tc.stage)
		if !slices.Equal(got, tc.want) {
			t.Errorf("RolesForStage(%q) = %v, want %v", tc.stage, got, tc.want)
		}
	}
}

func TestStageRegistry_UnknownStage(t *testing.T) {
	reg := stages.NewStageRegistry(stages.WorkflowBuildFromPRD, stages.DefaultStages, nil)

	got := reg.RolesForStage("phase-nonexistent")
	if got != nil {
		t.Errorf("RolesForStage(unknown) = %v, want nil", got)
	}
}

func TestStageRegistry_PolicyOverride_ReplacesList(t *testing.T) {
	policy := &core.Policy{
		WorkflowOverrides: map[string]map[string][]string{
			"build-from-prd": {
				"phase-review": {"reviewer.arch", "security-auditor"},
			},
		},
	}

	reg := stages.NewStageRegistry(stages.WorkflowBuildFromPRD, stages.DefaultStages, policy)

	got := reg.RolesForStage("phase-review")
	want := []string{"reviewer.arch", "security-auditor"}
	if !slices.Equal(got, want) {
		t.Errorf("overridden phase-review = %v, want %v", got, want)
	}

	// Non-overridden stage should still use default.
	devRoles := reg.RolesForStage("phase-dev")
	if !slices.Equal(devRoles, []string{"developer"}) {
		t.Errorf("phase-dev after override = %v, want [developer]", devRoles)
	}
}

func TestStageRegistry_PolicyOverride_EmptyListDisablesStage(t *testing.T) {
	policy := &core.Policy{
		WorkflowOverrides: map[string]map[string][]string{
			"build-from-prd": {
				"phase-test": {},
			},
		},
	}

	reg := stages.NewStageRegistry(stages.WorkflowBuildFromPRD, stages.DefaultStages, policy)

	got := reg.RolesForStage("phase-test")
	// Empty slice, not nil — the stage exists but is disabled.
	if got == nil {
		t.Fatal("expected empty slice (stage disabled), got nil (stage unknown)")
	}
	if len(got) != 0 {
		t.Errorf("disabled stage should have 0 roles, got %v", got)
	}
}

func TestStageRegistry_PolicyOverride_AddsNewStage(t *testing.T) {
	policy := &core.Policy{
		WorkflowOverrides: map[string]map[string][]string{
			"build-from-prd": {
				"phase-changelog": {"changelog-writer"},
			},
		},
	}

	reg := stages.NewStageRegistry(stages.WorkflowBuildFromPRD, stages.DefaultStages, policy)

	got := reg.RolesForStage("phase-changelog")
	want := []string{"changelog-writer"}
	if !slices.Equal(got, want) {
		t.Errorf("new stage = %v, want %v", got, want)
	}
}

func TestStageRegistry_PolicyOverride_DifferentWorkflow_NoEffect(t *testing.T) {
	// Override for a different workflow should not affect this registry.
	policy := &core.Policy{
		WorkflowOverrides: map[string]map[string][]string{
			"freeform": {
				"phase-review": {"some-other-reviewer"},
			},
		},
	}

	reg := stages.NewStageRegistry(stages.WorkflowBuildFromPRD, stages.DefaultStages, policy)

	got := reg.RolesForStage("phase-review")
	want := []string{"reviewer.arch", "reviewer.frontend"}
	if !slices.Equal(got, want) {
		t.Errorf("phase-review with unrelated override = %v, want %v", got, want)
	}
}

func TestStageRegistry_Mutation_IsolatedFromCaller(t *testing.T) {
	// Callers mutating the returned slice should not affect the registry.
	reg := stages.NewStageRegistry(stages.WorkflowBuildFromPRD, stages.DefaultStages, nil)

	first := reg.RolesForStage("phase-review")
	first[0] = "MUTATED"

	second := reg.RolesForStage("phase-review")
	if second[0] != "reviewer.arch" {
		t.Errorf("registry mutated by caller: got %q, want %q", second[0], "reviewer.arch")
	}
}

func TestStageRegistry_Workflow(t *testing.T) {
	reg := stages.NewStageRegistry(stages.WorkflowBuildFromPRD, stages.DefaultStages, nil)
	if reg.Workflow() != stages.WorkflowBuildFromPRD {
		t.Errorf("Workflow() = %q, want %q", reg.Workflow(), stages.WorkflowBuildFromPRD)
	}
}

func TestStageRegistry_AllStages_IncludesDefaults(t *testing.T) {
	reg := stages.NewStageRegistry(stages.WorkflowBuildFromPRD, stages.DefaultStages, nil)
	all := reg.AllStages()
	wantStages := []string{"phase-dev", "phase-review", "phase-test", "phase-ship"}
	for _, want := range wantStages {
		found := false
		for _, got := range all {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AllStages() missing %q; got %v", want, all)
		}
	}
}
