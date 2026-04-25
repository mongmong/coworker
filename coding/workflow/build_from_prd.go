package workflow

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/chris/coworker/coding/manifest"
	"github.com/chris/coworker/core"
)

// BuildFromPRDWorkflow is the autopilot workflow that drives the
// PRD → spec → plans → PRs pipeline.
//
// In Plan 106 this is a scaffold: it loads the manifest, schedules the first
// batch of ready plans, and creates worktrees for parallel plans. The full
// phase loop (developer → reviewer → tester → shipper) is implemented in
// Plan 114.
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
// TODO(Plan 114): dispatch architect → planner → phase loop for each plan.
//
// Returns errNoReadyPlans when no plans are ready (all done or all blocked).
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
