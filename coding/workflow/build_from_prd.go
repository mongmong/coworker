package workflow

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/chris/coworker/coding/manifest"
	"github.com/chris/coworker/coding/phaseloop"
	"github.com/chris/coworker/coding/shipper"
	"github.com/chris/coworker/coding/stages"
	"github.com/chris/coworker/core"
)

// BuildFromPRDWorkflow is the autopilot workflow that drives the
// PRD → spec → plans → PRs pipeline.
//
// Plan 106 added manifest scheduling and worktree management.
// Plan 114 adds the PhaseExecutor field and RunPhasesForPlan method,
// implementing the developer → reviewer/tester → dedupe → fix-loop inner loop.
// Plan 115 adds the Shipper field (PR creation) and StageRegistry (Level 1
// named-stage customization via policy.yaml workflow_overrides).
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

	// Shipper creates the GitHub PR after all phases of a plan complete.
	// When nil, the ship step is skipped (useful in tests that focus on
	// scheduling or phase execution only).
	Shipper *shipper.Shipper

	// StageRegistry provides the role list for each named stage.
	// When nil, defaults from coding/stages are used.
	StageRegistry *stages.StageRegistry

	// WorkDir is the working directory for git-based applies_when predicates.
	// When set, it is propagated to PhaseExecutor.WorkDir before each
	// RunPhasesForPlan call. Empty means the current working directory is used
	// by predicates (adequate for single-worktree runs).
	WorkDir string

	// RoleDir is the directory containing role YAML files. When set, it is
	// propagated to PhaseExecutor.RoleDir before each RunPhasesForPlan call,
	// enabling applies_when evaluation (role YAMLs are loaded to check
	// predicates before dispatching). When empty, PhaseExecutor.RoleDir is
	// not modified (applies_when evaluation is skipped if it was not already
	// configured on PhaseExecutor).
	RoleDir string

	// Logger is the structured logger. Uses slog.Default() if nil.
	Logger *slog.Logger

	// PlanWriter persists plan projection rows. Optional; when nil the workflow
	// still schedules and runs plans without projection writes.
	PlanWriter core.PlanWriter

	// CheckpointWriter persists checkpoint projection rows. Optional.
	CheckpointWriter core.CheckpointWriter
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
	w.recordManifestPlans(ctx, "", m)

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
// in sequence, then ships the plan as a PR when a Shipper is configured.
//
// It requires PhaseExecutor to be set; returns an error if it is nil.
//
// inputs is passed directly to PhaseExecutor.Execute for each phase. Callers
// typically supply at minimum "diff_path" and "spec_path" for reviewer roles.
// "branch" is used by the shipper to open the PR; defaults to "main" if absent.
//
// Returns a RunPhasesResult holding phase results and, when shipping succeeded,
// the ShipResult. If any phase returns an error the method stops and returns
// that error along with the results collected so far.
func (w *BuildFromPRDWorkflow) RunPhasesForPlan(
	ctx context.Context,
	runID string,
	plan manifest.PlanEntry,
	inputs map[string]string,
) (*RunPhasesResult, error) {
	if w.PhaseExecutor == nil {
		return nil, fmt.Errorf("build-from-prd: PhaseExecutor is required for RunPhasesForPlan")
	}

	// Populate ReviewerRoles and TesterRoles from the StageRegistry so
	// policy.yaml overrides take effect. Fall back to PhaseExecutor defaults
	// when no registry is set or when the registry returns nil (stage not
	// registered).
	if w.StageRegistry != nil {
		// phase-dev: first entry wins; multi-developer per phase is out of
		// scope. Empty → use PhaseExecutor's default ("developer"). Plan 131.
		if devRoles := w.StageRegistry.RolesForStage("phase-dev"); len(devRoles) > 0 {
			w.PhaseExecutor.DeveloperRole = devRoles[0]
		}
		if roles := w.StageRegistry.RolesForStage("phase-review"); roles != nil {
			w.PhaseExecutor.ReviewerRoles = roles
		}
		// Assign directly — preserves nil-vs-empty distinction.
		// RolesForStage returns nil when the stage is not registered at all
		// (no override; default applies) and an empty non-nil slice when
		// registered with no roles (disabled).
		w.PhaseExecutor.TesterRoles = w.StageRegistry.RolesForStage("phase-test")
		// phase-ship is structurally different: Shipper does git+gh, not
		// role dispatch. Customizing the ship stage is deferred — see
		// docs/architecture/decisions.md Decision 15 for the
		// authoritative rationale and follow-up shape options.
		// `stages.DefaultStages["phase-ship"]` is kept for spec
		// completeness; `policy.workflow_overrides.phase-ship` has no
		// effect today.
	}

	// Propagate WorkDir to PhaseExecutor for applies_when evaluation.
	if w.WorkDir != "" {
		w.PhaseExecutor.WorkDir = w.WorkDir
	}

	// Propagate RoleDir to PhaseExecutor so applies_when can load role YAMLs.
	if w.RoleDir != "" {
		w.PhaseExecutor.RoleDir = w.RoleDir
	}

	// Propagate CheckpointWriter so phase-clean attention items are paired
	// with checkpoints rows. Same pattern for the shipper.
	if w.CheckpointWriter != nil {
		w.PhaseExecutor.CheckpointWriter = w.CheckpointWriter
		if w.Shipper != nil {
			w.Shipper.CheckpointWriter = w.CheckpointWriter
		}
	}

	log := w.logger()
	log.Info("running phases for plan",
		"plan_id", plan.ID,
		"plan_title", plan.Title,
		"phases", len(plan.Phases),
	)
	w.createPlan(ctx, runID, plan)
	w.updatePlanState(ctx, runID, plan.ID, "running")

	phaseResults := make([]*phaseloop.PhaseResult, 0, len(plan.Phases))
	for i, phaseName := range plan.Phases {
		log.Info("executing phase",
			"plan_id", plan.ID,
			"phase_index", i,
			"phase_name", phaseName,
		)

		result, err := w.PhaseExecutor.Execute(ctx, runID, plan.ID, i, phaseName, inputs)
		if err != nil {
			w.updatePlanState(ctx, runID, plan.ID, "failed")
			return &RunPhasesResult{PhaseResults: phaseResults}, fmt.Errorf("plan %d phase %d (%q): %w", plan.ID, i, phaseName, err)
		}
		phaseResults = append(phaseResults, result)

		log.Info("phase result",
			"plan_id", plan.ID,
			"phase_index", i,
			"phase_name", phaseName,
			"clean", result.Clean,
			"fix_cycles", result.FixCycles,
			"findings", len(result.Findings),
		)

		// B-4: if the phase did not converge, stop the workflow and surface the
		// attention item created by the fix-loop exhaustion path.
		if !result.Clean {
			out := &RunPhasesResult{
				PhaseResults:        phaseResults,
				StoppedAtPhaseClean: true,
				DirtyPhaseIndex:     i,
				DirtyPhaseName:      phaseName,
			}
			if w.PhaseExecutor.AttentionStore != nil {
				item, getErr := w.PhaseExecutor.AttentionStore.GetUnansweredCheckpointForRun(ctx, runID, "phase-loop")
				if getErr == nil && item != nil {
					out.AttentionItemID = item.ID
				}
			}
			log.Warn("phase-clean block: stopping workflow",
				"plan_id", plan.ID,
				"phase_index", i,
				"phase_name", phaseName,
				"attention_id", out.AttentionItemID,
			)
			w.updatePlanState(ctx, runID, plan.ID, "failed")
			return out, nil
		}
	}

	out := &RunPhasesResult{PhaseResults: phaseResults}

	// Ship the plan as a PR after all phases complete.
	if w.Shipper != nil {
		branch := inputs["branch"]
		if branch == "" {
			branch = w.baseBranch()
		}
		shipResult, err := w.Shipper.Ship(ctx, runID, &plan, branch)
		if err != nil {
			w.updatePlanState(ctx, runID, plan.ID, "failed")
			return out, fmt.Errorf("plan %d ship: %w", plan.ID, err)
		}
		out.ShipResult = shipResult
		w.updatePlanBranchAndPR(ctx, runID, plan.ID, branch, shipResult.PRURL)
		log.Info("plan shipped",
			"plan_id", plan.ID,
			"pr_url", shipResult.PRURL,
		)
	}

	w.updatePlanState(ctx, runID, plan.ID, "done")
	return out, nil
}

func (w *BuildFromPRDWorkflow) recordManifestPlans(ctx context.Context, runID string, m *manifest.PlanManifest) {
	if w.PlanWriter == nil || m == nil {
		return
	}
	for _, p := range m.Plans {
		_ = w.PlanWriter.CreatePlan(ctx, core.PlanRecord{
			ID:       workflowPlanID(runID, p.ID),
			RunID:    runID,
			Number:   p.ID,
			Title:    p.Title,
			BlocksOn: p.BlocksOn,
			State:    "pending",
		})
	}
}

func (w *BuildFromPRDWorkflow) createPlan(ctx context.Context, runID string, p manifest.PlanEntry) {
	if w.PlanWriter == nil {
		return
	}
	_ = w.PlanWriter.CreatePlan(ctx, core.PlanRecord{
		ID:       workflowPlanID(runID, p.ID),
		RunID:    runID,
		Number:   p.ID,
		Title:    p.Title,
		BlocksOn: p.BlocksOn,
		State:    "pending",
	})
}

func (w *BuildFromPRDWorkflow) updatePlanState(ctx context.Context, runID string, planNumber int, state string) {
	if w.PlanWriter == nil {
		return
	}
	_ = w.PlanWriter.UpdatePlanState(ctx, workflowPlanID(runID, planNumber), state)
}

func (w *BuildFromPRDWorkflow) updatePlanBranchAndPR(ctx context.Context, runID string, planNumber int, branch, prURL string) {
	if w.PlanWriter == nil {
		return
	}
	_ = w.PlanWriter.UpdatePlanBranchAndPR(ctx, workflowPlanID(runID, planNumber), branch, prURL)
}

func workflowPlanID(runID string, planNumber int) string {
	if runID == "" {
		return fmt.Sprintf("plan-%d", planNumber)
	}
	return fmt.Sprintf("%s-plan-%d", runID, planNumber)
}

// RunPhasesResult holds the aggregated output of RunPhasesForPlan.
type RunPhasesResult struct {
	// PhaseResults holds one PhaseResult per phase, in order.
	PhaseResults []*phaseloop.PhaseResult

	// ShipResult is the output of the ship step.
	// Nil when no Shipper is configured or shipping was not attempted.
	ShipResult *shipper.ShipResult

	// StoppedAtPhaseClean is true when workflow was halted because a phase
	// returned Clean==false (fix-loop exhausted). When true, the Shipper was
	// NOT called.
	StoppedAtPhaseClean bool

	// DirtyPhaseIndex is the zero-based index of the phase that returned
	// Clean==false. Meaningful only when StoppedAtPhaseClean is true.
	DirtyPhaseIndex int

	// DirtyPhaseName is the human-readable name of the dirty phase.
	// Meaningful only when StoppedAtPhaseClean is true.
	DirtyPhaseName string

	// AttentionItemID is the ID of the phase-clean attention item created by
	// PhaseExecutor for the dirty phase. Empty when no item was found or
	// AttentionStore is nil.
	AttentionItemID string
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
