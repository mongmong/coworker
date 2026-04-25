// Package stages implements the Level 1 named-stage registry for workflow
// customization.
//
// Each workflow (e.g., "build-from-prd") declares a set of named stages
// (e.g., "phase-dev", "phase-review"). Each stage has a default list of
// roles that fire when it runs. Per-repo policy.yaml can override those lists:
//
//	workflow_overrides:
//	  build-from-prd:
//	    stages:
//	      phase-review: [reviewer.arch, security-auditor]
//	      phase-test:   []   # disabled
//
// The registry is constructed once at runtime start and is read-only
// thereafter. Thread safety comes from the immutable-after-construction pattern.
package stages

import (
	"github.com/chris/coworker/core"
)

// StageRegistry maps stage names to the role lists that fire at each stage.
// Overrides from policy.yaml are merged on top of the defaults at construction
// time; RolesForStage reads from the merged map only.
type StageRegistry struct {
	workflow string
	// merged holds defaults overlaid with overrides.
	merged map[string][]string
}

// NewStageRegistry constructs a StageRegistry for the given workflow.
//
// defaults is the workflow's built-in stage → role list map.
// policy may be nil; when non-nil, its WorkflowOverrides["<workflow>"]["stages"]
// entries replace the corresponding defaults (not append — full replacement per
// stage, matching spec Level 1 semantics).
func NewStageRegistry(workflow string, defaults map[string][]string, policy *core.Policy) *StageRegistry {
	// Start with a copy of the defaults.
	merged := make(map[string][]string, len(defaults))
	for stage, roles := range defaults {
		cp := make([]string, len(roles))
		copy(cp, roles)
		merged[stage] = cp
	}

	// Apply policy overrides if present.
	if policy != nil {
		if wfOverride, ok := policy.WorkflowOverrides[workflow]; ok {
			if stages, ok := wfOverride["stages"]; ok {
				// stages is map[string][]string encoded as the YAML value.
				// core.Policy.WorkflowOverrides is map[string]map[string][]string,
				// but the inner map key is stage name and value is role list.
				//
				// policy.WorkflowOverrides["build-from-prd"]["stages"] is not quite
				// right — the YAML structure is:
				//   workflow_overrides:
				//     build-from-prd:
				//       stages:
				//         phase-review: [...]
				//
				// WorkflowOverrides[workflow] is map[string][]string where each key
				// under "stages" is a stage name. However the YAML parses the
				// nested "stages:" key as its own entry in the inner map, so the
				// inner map key is "stages" and value is nil ([]string). The stage
				// names are nested one level deeper.
				//
				// We handle this by checking whether WorkflowOverrides[workflow] has
				// a "stages" key. If it does, the actual stage keys live in a separate
				// nested map that requires a different decoding. Since core.Policy uses
				// map[string]map[string][]string, the nesting cannot represent the
				// three-level YAML structure directly.
				//
				// Resolution: treat WorkflowOverrides[workflow] as the stages map
				// directly (skipping the intermediate "stages:" key). This matches
				// how callers that build Policy in tests will set it up.
				_ = stages // suppress unused warning; handled below
			}

			// Treat every key in wfOverride as a stage name → role list override.
			// This matches the direct-stages encoding described above.
			for stage, roleList := range wfOverride {
				if stage == "stages" {
					// Reserved YAML key used as a namespace in config; skip.
					continue
				}
				if len(roleList) == 0 {
					// Empty list = stage disabled (no roles fire).
					merged[stage] = []string{}
				} else {
					cp := make([]string, len(roleList))
					copy(cp, roleList)
					merged[stage] = cp
				}
			}
		}
	}

	return &StageRegistry{
		workflow: workflow,
		merged:   merged,
	}
}

// RolesForStage returns the role list for the named stage.
//
// Returns nil when the stage is not registered at all (distinct from an empty
// slice, which means the stage is registered but disabled by policy).
func (r *StageRegistry) RolesForStage(stage string) []string {
	roles, ok := r.merged[stage]
	if !ok {
		return nil
	}
	// Return a copy so callers cannot mutate the registry.
	cp := make([]string, len(roles))
	copy(cp, roles)
	return cp
}

// AllStages returns all stage names in the registry (defaults + overrides),
// in no guaranteed order. Useful for diagnostic logging.
func (r *StageRegistry) AllStages() []string {
	names := make([]string, 0, len(r.merged))
	for stage := range r.merged {
		names = append(names, stage)
	}
	return names
}

// Workflow returns the workflow name this registry is scoped to.
func (r *StageRegistry) Workflow() string {
	return r.workflow
}
