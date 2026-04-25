package workflow

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/chris/coworker/coding/manifest"
	"github.com/chris/coworker/coding/phaseloop"
	"github.com/chris/coworker/core"
)

// BuildFromPRDWorkflow is the autopilot workflow that drives the
// PRD → spec → plans → PRs pipeline.
//
// Plan 106 added manifest scheduling and worktree management.
// Plan 114 adds the PhaseExecutor field and RunPhasesForPlan method,
// implementing the developer → reviewer/tester → dedupe → fix-loop inner loop.
// Plan 115 will add the shipper role and PR creation.
type BuildFromPRDWorkflow struct {
	// ManifestPath is the path to the plan manifest YAML file.
	ManifestPath string

	// Policy controls concurrency limits and checkpoint behavior.
	// May be nil; defaults are used when nil.
	Policy *core.Policy

	// WorktreeManager creates and removes git worktrees for parallel plans.
	// May be nil when max_parallel_plans == 1 (no worktrees needed).
	WorktreeManager *manifest.WorktreeManager

	// BaseBranch is the git branch that feature branches are created from.
	// Defaults to "main" if empty.
	BaseBranch string

	// PhaseExecutor runs the developer → reviewer/tester → dedupe → fix-loop
	// for each phase of a plan. May be nil when only scheduling/worktree
	// management is needed (e.g., in Plan 106 integration tests).
	PhaseExecutor *phaseloop.PhaseExecutor

	// Logger is the structured logger. Uses slog.Default() if nil.
	Logger *slog.Logger
}

// NewBuildFromPRDWorkflow creates a BuildFromPRDWorkflow with the given manifest
// path and policy. The worktree manager should be set separately if parallel
// plan execution is desired.
func NewBuildFromPRDWorkflow(manifestPath string, policy *core.Policy) *BuildFromPRDWorkflow {
	return &BuildFromPRDWorkflow{
		ManifestPath: manifestPath,
		Policy:       policy,
	}
}

// Name returns the canonical name of this workflow.
func (w *BuildFromPRDWorkflow) Name() string {
	return "build-from-prd"
}

// logger returns the configured logger or slog.Default().
func (w *BuildFromPRDWorkflow) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

// baseBranch returns the configured base branch or "main".
func (w *BuildFromPRDWorkflow) baseBranch() string {
	if w.BaseBranch != "" {
		return w.BaseBranch
	}
	return "main"
}

// LoadManifest loads and validates the plan manifest from ManifestPath.
func (w *BuildFromPRDWorkflow) LoadManifest() (*manifest.PlanManifest, error) {
	if w.ManifestPath == "" {
		return nil, fmt.Errorf("build-from-prd: ManifestPath is required")
	}
	m, err := manifest.LoadManifest(w.ManifestPath)
	if err != nil {
		return nil, fmt.Errorf("build-from-prd: load manifest: %w", err)
	}
	return m, nil
}

// Schedule returns the plans that are ready to start given the current
// completed and active sets.
func (w *BuildFromPRDWorkflow) Schedule(
	m *manifest.PlanManifest,
	completed map[int]bool,
	active map[int]bool,
) []manifest.PlanEntry {
	s := manifest.NewDAGScheduler(m, w.Policy)
	return s.ReadyPlans(completed, active)
}

// PrepareWorktrees creates git worktrees for the given plans when parallel
// execution is configured (max_parallel_plans > 1).
//
// For single-plan runs the main checkout is used directly and this is a no-op.
// Returns a map of plan ID → worktree absolute path.
func (w *BuildFromPRDWorkflow) PrepareWorktrees(
	ctx context.Context,
	plans []manifest.PlanEntry,
) (map[int]string, error) {
	log := w.logger()

	worktrees := make(map[int]string, len(plans))

	needsWorktrees := len(plans) > 1 && w.WorktreeManager != nil
	if !needsWorktrees {
		log.Info("worktrees not needed", "plans", len(plans), "has_manager", w.WorktreeManager != nil)
		return worktrees, nil
	}

	base := w.baseBranch()
	for _, p := range plans {
		path, err := w.WorktreeManager.Create(ctx, p.ID, p.Title, base)
		if err != nil {
			return worktrees, fmt.Errorf("create worktree for plan %d (%q): %w", p.ID, p.Title, err)
		}
		worktrees[p.ID] = path
		log.Info("worktree created", "plan_id", p.ID, "path", path)
	}
	return worktrees, nil
}

// Run loads the manifest, schedules ready plans, creates worktrees, and
// returns the set of plans and their worktree paths.
//
// The caller is responsible for iterating over ReadyPlans and calling
// RunPhasesForPlan for each plan. This separation allows the caller to
// manage parallel execution and inter-plan dependencies.
//
// Returns an empty ReadyPlans slice when no plans are ready (all done or
// all blocked).
func (w *BuildFromPRDWorkflow) Run(
	ctx context.Context,
	completed map[int]bool,
	active map[int]bool,
) (*BuildFromPRDResult, error) {
	log := w.logger()

	m, err := w.LoadManifest()
	if err != nil {
		return nil, err
	}
	log.Info("manifest loaded", "spec_path", m.SpecPath, "plans", len(m.Plans))

	ready := w.Schedule(m, completed, active)
	log.Info("plans scheduled", "ready", len(ready))

	if len(ready) == 0 {
		return &BuildFromPRDResult{Manifest: m, ReadyPlans: nil, Worktrees: nil}, nil
	}

	worktrees, err := w.PrepareWorktrees(ctx, ready)
	if err != nil {
		return nil, err
	}

	return &BuildFromPRDResult{
		Manifest:   m,
		ReadyPlans: ready,
		Worktrees:  worktrees,
	}, nil
}

// RunPhasesForPlan executes the phase loop for each phase of the given plan
// in sequence. It requires PhaseExecutor to be set; returns an error if it is nil.
//
// inputs is passed directly to PhaseExecutor.Execute for each phase. Callers
// typically supply at minimum "diff_path" and "spec_path" for reviewer roles.
//
// Returns a slice of PhaseResult, one per phase, in order. If any phase
// returns an error the method stops and returns that error along with the
// results collected so far.
func (w *BuildFromPRDWorkflow) RunPhasesForPlan(
	ctx context.Context,
	runID string,
	plan manifest.PlanEntry,
	inputs map[string]string,
) ([]*phaseloop.PhaseResult, error) {
	if w.PhaseExecutor == nil {
		return nil, fmt.Errorf("build-from-prd: PhaseExecutor is required for RunPhasesForPlan")
	}

	log := w.logger()
	log.Info("running phases for plan",
		"plan_id", plan.ID,
		"plan_title", plan.Title,
		"phases", len(plan.Phases),
	)

	results := make([]*phaseloop.PhaseResult, 0, len(plan.Phases))
	for i, phaseName := range plan.Phases {
		log.Info("executing phase",
			"plan_id", plan.ID,
			"phase_index", i,
			"phase_name", phaseName,
		)

		result, err := w.PhaseExecutor.Execute(ctx, runID, plan.ID, i, phaseName, inputs)
		if err != nil {
			return results, fmt.Errorf("plan %d phase %d (%q): %w", plan.ID, i, phaseName, err)
		}
		results = append(results, result)

		log.Info("phase result",
			"plan_id", plan.ID,
			"phase_index", i,
			"phase_name", phaseName,
			"clean", result.Clean,
			"fix_cycles", result.FixCycles,
			"findings", len(result.Findings),
		)
	}

	return results, nil
}

// BuildFromPRDResult holds the output of one scheduling iteration.
type BuildFromPRDResult struct {
	// Manifest is the loaded plan manifest.
	Manifest *manifest.PlanManifest

	// ReadyPlans are the plans selected for this iteration.
	ReadyPlans []manifest.PlanEntry

	// Worktrees maps plan ID to the absolute path of its worktree.
	// Empty when max_parallel_plans == 1 or no worktree manager is set.
	Worktrees map[int]string
}
