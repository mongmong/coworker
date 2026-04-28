package phaseloop

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/coding/roles"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/internal/predicates"
	"github.com/chris/coworker/store"
)

// Orchestrator is the interface the PhaseExecutor uses to dispatch individual
// role jobs. The concrete implementation is *coding.Dispatcher; tests may
// supply a stub.
type Orchestrator interface {
	Orchestrate(ctx context.Context, input *coding.DispatchInput) (*coding.DispatchResult, error)
}

// defaultDeveloperRole is the role dispatched at the start of each fix-loop
// iteration when no override is provided. Plan 131.
const defaultDeveloperRole = "developer"

// defaultReviewerRoles are the roles dispatched in parallel after each developer
// job when no override is provided.
var defaultReviewerRoles = []string{"reviewer.arch", "reviewer.frontend"}

// defaultTesterRoles are the tester roles dispatched in parallel alongside
// reviewer roles when no override is provided.
var defaultTesterRoles = []string{"tester"}

// PhaseExecutor runs one phase of a plan through the full cycle:
// developer → fan-out reviewers/tester → dedupe → fix-loop → checkpoint.
//
// It does NOT manage branches or worktrees (Plan 106) and does NOT ship PRs
// (Plan 115). It is purely the inner loop of the phase state machine.
type PhaseExecutor struct {
	// Dispatcher executes individual role jobs. Must implement Orchestrator.
	// In production this is *coding.Dispatcher; in tests a stub may be used.
	Dispatcher Orchestrator

	// EventStore writes phase lifecycle events.
	EventStore *store.EventStore

	// AttentionStore, when non-nil, is used to create attention items when
	// the fix-loop exhausts its budget without converging. The TUI/CLI can
	// then surface these items to the operator. Nil means no attention item
	// is created (true blocking is deferred to Plan 103).
	AttentionStore *store.AttentionStore

	// CheckpointWriter, when non-nil, is paired with AttentionStore: every
	// phase-clean attention item also gets a matching checkpoints row so the
	// durable record exists for later resolution. Optional; when nil, only
	// the attention row is created (legacy behavior).
	CheckpointWriter core.CheckpointWriter

	// Policy controls fix-cycle limits and checkpoint behavior.
	// May be nil; defaults are used when nil.
	Policy *core.Policy

	// DeveloperRole is the role dispatched at the start of each fix-loop
	// iteration. Empty means use defaultDeveloperRole ("developer"). Set by
	// BuildFromPRDWorkflow from StageRegistry.RolesForStage("phase-dev")[0]
	// so policy.yaml workflow_overrides can swap the developer role.
	// Plan 131. Multi-developer phase support is intentionally out of scope:
	// the registry's first entry wins.
	DeveloperRole string

	// ReviewerRoles is the list of roles dispatched in parallel after each
	// developer job. When nil, defaultReviewerRoles is used.
	// Set by BuildFromPRDWorkflow from StageRegistry.RolesForStage("phase-review")
	// so policy.yaml workflow_overrides take effect.
	ReviewerRoles []string

	// TesterRoles controls which tester roles are dispatched in parallel
	// alongside ReviewerRoles after each developer job.
	//
	//   nil              → use defaultTesterRoles (["tester"])
	//   non-nil, empty   → tester stage disabled; no tester is dispatched
	//   non-nil, non-empty → dispatch the listed roles
	//
	// This mirrors the nil-vs-empty semantics of ReviewerRoles.
	TesterRoles []string

	// WorkDir is the working directory used for git-based applies_when
	// predicates. When empty, predicates use the current working directory
	// (safe for single-worktree runs; callers should set this explicitly
	// when using worktrees or for reproducibility).
	WorkDir string

	// RoleDir is the directory containing role YAML files. When non-empty,
	// fanOut loads each role to evaluate its AppliesWhen clause before
	// dispatching. When empty, AppliesWhen evaluation is skipped (all roles
	// are dispatched unconditionally).
	RoleDir string

	// Logger is the structured logger. Uses slog.Default() if nil.
	Logger *slog.Logger
}

// PhaseResult holds the aggregated output of a complete phase execution,
// including all fix cycles.
type PhaseResult struct {
	// Findings is the deduplicated set of review findings after the last cycle.
	Findings []core.Finding

	// Artifacts is the merged set of artifacts from all jobs in the phase.
	Artifacts []core.Artifact

	// TestsPassed is true iff all tester jobs exited with code 0 in the final
	// fix cycle.
	TestsPassed bool

	// FixCycles is the number of developer fix iterations that occurred.
	// Zero means the phase was clean on the first reviewer pass.
	FixCycles int

	// Clean is true when the phase completed with no outstanding findings.
	// When false, the fix-loop exhausted max_fix_cycles_per_phase and a
	// phase.clean checkpoint event was emitted.
	Clean bool
}

// Execute runs the phase loop for a single plan phase.
//
//   - runID: the parent run identifier (used for event correlation).
//   - planID: the numeric plan ID (used in event payloads for traceability).
//   - phaseIndex: zero-based index of this phase within the plan.
//   - phaseName: human-readable name (from PlanEntry.Phases[phaseIndex]).
//   - inputs: role inputs passed directly to Dispatcher.Orchestrate (e.g.,
//     "diff_path", "spec_path"). The executor does not interpret them.
//
// Execute emits phase.started, phase.completed/phase.failed, and (on
// fix-loop exhaustion) phase.clean events.
func (e *PhaseExecutor) Execute(
	ctx context.Context,
	runID string,
	planID int,
	phaseIndex int,
	phaseName string,
	inputs map[string]string,
) (*PhaseResult, error) {
	log := e.logger()

	log.Info("phase started",
		"run_id", runID,
		"plan_id", planID,
		"phase_index", phaseIndex,
		"phase_name", phaseName,
	)

	// Inject plan/phase context into inputs so finding rows can be
	// attributed to the (plan, phase) that produced them. Plan 125 (B3).
	if inputs == nil {
		inputs = map[string]string{}
	}
	inputs["plan_id"] = strconv.Itoa(planID)
	inputs["phase_index"] = strconv.Itoa(phaseIndex)

	e.emitPhaseEvent(ctx, runID, planID, phaseIndex, phaseName, core.EventPhaseStarted, nil)

	result, err := e.runLoop(ctx, runID, planID, phaseIndex, phaseName, inputs, log)
	if err != nil {
		e.emitPhaseEvent(ctx, runID, planID, phaseIndex, phaseName, core.EventPhaseFailed, map[string]interface{}{
			"error": err.Error(),
		})
		return nil, err
	}

	evtKind := core.EventPhaseCompleted
	e.emitPhaseEvent(ctx, runID, planID, phaseIndex, phaseName, evtKind, map[string]interface{}{
		"fix_cycles":   result.FixCycles,
		"clean":        result.Clean,
		"findings":     len(result.Findings),
		"tests_passed": result.TestsPassed,
	})

	log.Info("phase completed",
		"run_id", runID,
		"plan_id", planID,
		"phase_index", phaseIndex,
		"phase_name", phaseName,
		"clean", result.Clean,
		"fix_cycles", result.FixCycles,
		"findings", len(result.Findings),
	)

	return result, nil
}

// runLoop contains the core developer → reviewer/tester → dedupe → fix-loop.
func (e *PhaseExecutor) runLoop(
	ctx context.Context,
	runID string,
	planID int,
	phaseIndex int,
	phaseName string,
	inputs map[string]string,
	log *slog.Logger,
) (*PhaseResult, error) {
	maxCycles := maxFixCycles(e.Policy)
	devInputs := copyInputs(inputs)
	fixCycles := 0

	for {
		// Step 1: dispatch developer.
		log.Info("dispatching developer",
			"plan_id", planID, "phase_index", phaseIndex, "fix_cycle", fixCycles)

		devRole := e.DeveloperRole
		if devRole == "" {
			devRole = defaultDeveloperRole
		}
		devResult, err := e.Dispatcher.Orchestrate(ctx, &coding.DispatchInput{
			RoleName: devRole,
			RunID:    runID, // Plan 139: attach to workflow run
			Inputs:   devInputs,
		})
		if err != nil {
			return nil, fmt.Errorf("phase %d/%d developer dispatch: %w", planID, phaseIndex, err)
		}

		// Step 2: fan-out reviewers + tester in parallel.
		reviewerResults, err := e.fanOut(ctx, runID, phaseIndex, inputs, log)
		if err != nil {
			return nil, fmt.Errorf("phase %d/%d fan-out: %w", planID, phaseIndex, err)
		}

		// Include developer findings in aggregation (developer may self-report).
		allResults := append([]*coding.DispatchResult{devResult}, reviewerResults...)
		agg := AggregateResults(allResults)
		deduped := DedupeFindings(agg.Findings)

		log.Info("fan-in complete",
			"plan_id", planID,
			"phase_index", phaseIndex,
			"fix_cycle", fixCycles,
			"raw_findings", len(agg.Findings),
			"deduped_findings", len(deduped),
			"tests_passed", agg.TestsPassed,
		)

		// Step 3: clean check — no findings and tests pass.
		if len(deduped) == 0 && agg.TestsPassed {
			return &PhaseResult{
				Findings:    deduped,
				Artifacts:   agg.Artifacts,
				TestsPassed: agg.TestsPassed,
				FixCycles:   fixCycles,
				Clean:       true,
			}, nil
		}

		// Step 4: check fix-cycle budget.
		if fixCycles >= maxCycles {
			// Exhausted — emit phase-clean checkpoint and return dirty.
			log.Warn("fix-loop exhausted, raising phase-clean checkpoint",
				"plan_id", planID,
				"phase_index", phaseIndex,
				"fix_cycles", fixCycles,
				"remaining_findings", len(deduped),
			)
			e.emitPhaseEvent(ctx, runID, planID, phaseIndex, phaseName, core.EventPhaseClean, map[string]interface{}{
				"fix_cycles":   fixCycles,
				"findings":     len(deduped),
				"tests_passed": agg.TestsPassed,
				"checkpoint":   "phase-clean",
			})
			// Create an attention item so the TUI/CLI can surface this
			// checkpoint to the operator. True blocking (waiting for an
			// answer) is deferred to Plan 103; here we only record the item.
			if e.AttentionStore != nil {
				item := &core.AttentionItem{
					ID:       core.NewID(),
					RunID:    runID,
					Kind:     core.AttentionCheckpoint,
					Source:   "phase-loop",
					Question: fmt.Sprintf("Phase %d (%s) did not converge after %d fix cycles. %d findings remain.", phaseIndex, phaseName, maxCycles, len(deduped)),
				}
				if insertErr := e.AttentionStore.InsertAttention(ctx, item); insertErr != nil {
					e.logger().Error("failed to insert phase-clean attention item",
						"phase_index", phaseIndex,
						"phase_name", phaseName,
						"error", insertErr,
					)
				} else if e.CheckpointWriter != nil {
					if cpErr := e.CheckpointWriter.CreateCheckpoint(ctx, core.CheckpointRecord{
						ID:    item.ID,
						RunID: runID,
						Kind:  core.CheckpointKindPhaseClean,
					}); cpErr != nil {
						e.logger().Error("failed to insert phase-clean checkpoint row",
							"phase_index", phaseIndex,
							"phase_name", phaseName,
							"error", cpErr,
						)
					}
				}
			}
			return &PhaseResult{
				Findings:    deduped,
				Artifacts:   agg.Artifacts,
				TestsPassed: agg.TestsPassed,
				FixCycles:   fixCycles,
				Clean:       false,
			}, nil
		}

		// Step 5: build feedback and update developer inputs for next cycle.
		feedback := BuildFindingFeedback(deduped)
		devInputs = copyInputs(inputs)
		devInputs["fix_feedback"] = feedback
		fixCycles++

		log.Info("starting fix cycle",
			"plan_id", planID, "phase_index", phaseIndex, "fix_cycle", fixCycles)
	}
}

// reviewerRoles returns the effective reviewer role list.
// nil → defaultReviewerRoles; non-nil (even empty) → as-is.
func (e *PhaseExecutor) reviewerRoles() []string {
	if e.ReviewerRoles == nil {
		return defaultReviewerRoles
	}
	return e.ReviewerRoles
}

// testerRoles returns the effective tester role list.
// nil → defaultTesterRoles; non-nil (even empty) → as-is (empty = disabled).
func (e *PhaseExecutor) testerRoles() []string {
	if e.TesterRoles == nil {
		return defaultTesterRoles
	}
	return e.TesterRoles
}

// fanOut dispatches all reviewer roles and tester roles in parallel and collects
// results. An error from any goroutine cancels the rest via errgroup.
// Roles whose AppliesWhen clause evaluates to false are skipped; a
// job.skipped event is emitted for each skipped role.
func (e *PhaseExecutor) fanOut(
	ctx context.Context,
	runID string,
	phaseIndex int,
	inputs map[string]string,
	log *slog.Logger,
) ([]*coding.DispatchResult, error) {
	roleNames := append(e.reviewerRoles(), e.testerRoles()...)

	// Preallocate results slice — each goroutine writes to its own index,
	// so no mutex is needed. Skipped roles leave a nil entry.
	results := make([]*coding.DispatchResult, len(roleNames))

	g, gCtx := errgroup.WithContext(ctx)

	for i, roleName := range roleNames {
		i, roleName := i, roleName // capture loop vars
		g.Go(func() error {
			// Evaluate applies_when if RoleDir is set.
			if e.RoleDir != "" {
				role, err := roles.LoadRole(e.RoleDir, roleName)
				if err != nil {
					// Role file not found or invalid: log a warning and dispatch
					// anyway (graceful degradation — applies_when is optional).
					log.Warn("could not load role for applies_when check; dispatching unconditionally",
						"role", roleName, "error", err)
				} else if shouldSkip, skipErr := e.roleShouldSkip(gCtx, role, phaseIndex); skipErr != nil {
					// Predicate error: log warning and dispatch anyway (don't crash the phase).
					if e.WorkDir == "" {
						log.Warn("WorkDir not set for applies_when evaluation; dispatching unconditionally",
							"role", roleName)
					} else {
						log.Warn("applies_when evaluation error; dispatching unconditionally",
							"role", roleName, "error", skipErr)
					}
				} else if shouldSkip {
					log.Info("role skipped by applies_when", "role", roleName)
					e.emitJobSkippedEvent(gCtx, runID, roleName)
					results[i] = &coding.DispatchResult{} // sentinel: empty result, not nil
					return nil
				}
			}

			log.Info("dispatching reviewer/tester", "role", roleName)
			result, err := e.Dispatcher.Orchestrate(gCtx, &coding.DispatchInput{
				RoleName: roleName,
				RunID:    runID, // Plan 139: attach to workflow run
				Inputs:   copyInputs(inputs),
			})
			if err != nil {
				return fmt.Errorf("dispatch %s: %w", roleName, err)
			}
			results[i] = result
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Filter out nil entries (roles that were somehow not dispatched and not skipped).
	filtered := results[:0]
	for _, r := range results {
		if r != nil {
			filtered = append(filtered, r)
		}
	}

	return filtered, nil
}

// roleShouldSkip returns true if the role's AppliesWhen clause evaluates to
// false (meaning the role should be skipped, not dispatched).
// Returns (false, nil) when AppliesWhen is nil (no filter → always dispatch).
//
// Multiple predicates AND together: every populated predicate must hold for
// the role to fire. Plan 131 (I3) extended this from changes_touch to also
// support commit_msg_contains and phase_index_in.
func (e *PhaseExecutor) roleShouldSkip(ctx context.Context, role *core.Role, phaseIndex int) (bool, error) {
	_ = ctx // reserved for future async predicate evaluation
	if role.AppliesWhen == nil {
		return false, nil // no filter → dispatch
	}

	if len(role.AppliesWhen.ChangesTouch) > 0 {
		if e.WorkDir == "" {
			return false, fmt.Errorf("WorkDir not set; cannot evaluate changes_touch for role %q", role.Name)
		}
		touched, err := predicates.ChangesTouchInDir(e.WorkDir, role.AppliesWhen.ChangesTouch)
		if err != nil {
			return false, err
		}
		if !touched {
			return true, nil // skip
		}
	}

	if role.AppliesWhen.CommitMsgContains != "" {
		if e.WorkDir == "" {
			return false, fmt.Errorf("WorkDir not set; cannot evaluate commit_msg_contains for role %q", role.Name)
		}
		matched, err := predicates.CommitMsgContains(e.WorkDir, role.AppliesWhen.CommitMsgContains)
		if err != nil {
			return false, err
		}
		if !matched {
			return true, nil // skip
		}
	}

	if role.AppliesWhen.PhaseIndexIn != "" {
		inRange, err := predicates.PhaseIndexIn(phaseIndex, role.AppliesWhen.PhaseIndexIn)
		if err != nil {
			return false, err
		}
		if !inRange {
			return true, nil // skip
		}
	}

	return false, nil // all predicates hold → dispatch
}

// emitJobSkippedEvent writes a job.skipped event to the EventStore.
// Errors are logged but not returned — skipped events are best-effort.
func (e *PhaseExecutor) emitJobSkippedEvent(ctx context.Context, runID, roleName string) {
	payload, err := json.Marshal(map[string]string{
		"role":   roleName,
		"reason": "applies_when=false",
	})
	if err != nil {
		e.logger().Error("failed to marshal job.skipped payload", "role", roleName, "error", err)
		return
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventJobSkipped,
		SchemaVersion: 1,
		Payload:       string(payload),
		CreatedAt:     time.Now(),
	}

	if writeErr := e.EventStore.WriteEventThenRow(ctx, event, nil); writeErr != nil {
		e.logger().Error("failed to write job.skipped event", "role", roleName, "error", writeErr)
	}
}

// emitPhaseEvent writes a phase lifecycle event to the event store.
// Errors are logged but not returned — phase lifecycle events are best-effort.
func (e *PhaseExecutor) emitPhaseEvent(
	ctx context.Context,
	runID string,
	planID int,
	phaseIndex int,
	phaseName string,
	kind core.EventKind,
	extra map[string]interface{},
) {
	payload := map[string]interface{}{
		"plan_id":     planID,
		"phase_index": phaseIndex,
		"phase_name":  phaseName,
	}
	for k, v := range extra {
		payload[k] = v
	}

	data, err := json.Marshal(payload)
	if err != nil {
		e.logger().Error("failed to marshal phase event payload", "kind", kind, "error", err)
		return
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          kind,
		SchemaVersion: 1,
		CorrelationID: fmt.Sprintf("plan-%d-phase-%d", planID, phaseIndex),
		Payload:       string(data),
		CreatedAt:     time.Now(),
	}

	if writeErr := e.EventStore.WriteEventThenRow(ctx, event, nil); writeErr != nil {
		e.logger().Error("failed to write phase event", "kind", kind, "error", writeErr)
	}
}

// logger returns the configured logger or slog.Default().
func (e *PhaseExecutor) logger() *slog.Logger {
	if e.Logger != nil {
		return e.Logger
	}
	return slog.Default()
}

// copyInputs returns a shallow copy of the inputs map so each dispatch gets
// its own map without sharing mutations.
func copyInputs(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
