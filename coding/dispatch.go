package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chris/coworker/coding/roles"
	"github.com/chris/coworker/coding/supervisor"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/internal/executil"
	"github.com/chris/coworker/store"
)

// DefaultMaxRetries is the default maximum number of supervisor retries
// before escalating to a compliance-breach event.
const DefaultMaxRetries = 3

// SupervisorEvaluator is the interface for evaluating supervisor rules against
// job outputs. Implemented by *supervisor.RuleEngine; can be stubbed in tests.
type SupervisorEvaluator interface {
	Evaluate(ctx *supervisor.EvalContext) (*core.SupervisorVerdict, error)
}

// Dispatcher orchestrates the end-to-end flow: load role -> create run/job
// -> render prompt -> dispatch agent -> capture result -> supervisor check
// -> persist findings (with retry loop on contract failure).
type Dispatcher struct {
	RoleDir   string // path to directory containing role YAML files
	PromptDir string // path to directory containing prompt template files

	// Agent is the default agent used when a role's CLI field does not match
	// any entry in CLIAgents, or when CLIAgents is empty. For backwards
	// compatibility, a Dispatcher with only Agent set routes all roles through
	// that single agent.
	Agent core.Agent

	// CLIAgents maps CLI name (e.g. "codex", "claude-code", "opencode") to an
	// Agent implementation. When non-empty, Orchestrate selects the agent by
	// matching role.CLI against this map. Falls back to Agent when the role's
	// CLI is not in the map or the map is nil.
	CLIAgents map[string]core.Agent

	DB     *store.DB
	Logger *slog.Logger

	// Supervisor is the optional rule engine. If nil, no contract
	// checks are performed (equivalent to all-pass).
	// The interface is satisfied by *supervisor.RuleEngine; tests may
	// supply a stub.
	Supervisor SupervisorEvaluator

	// SupervisorWriter records each supervisor rule result as an event-first
	// projection row. Optional; when nil, supervisor persistence is skipped.
	SupervisorWriter core.SupervisorWriter

	// CostWriter records cost samples after each completed attempt.
	// Optional; when nil, cost persistence is skipped. Failure to persist is
	// logged but does not fail dispatch (Plan 121).
	CostWriter core.CostWriter

	// MaxRetries is the maximum number of supervisor retries per job.
	// Zero means use DefaultMaxRetries. Negative means no retries.
	MaxRetries int

	// WorkDir is the working directory for git-based predicates.
	// If empty, git predicates use the current working directory.
	WorkDir string

	// Policy is the effective merged policy. When non-nil it is consulted for
	// permission enforcement (e.g. on_undeclared). When nil, all undeclared
	// actions default to hard-fail (most restrictive).
	Policy *core.Policy

	// AttentionStore is used to create attention.permission items when an
	// action matches the requires_human permission list. Optional; no item is
	// created when nil.
	AttentionStore *store.AttentionStore
}

// agentFor returns the Agent to use for the given role. It prefers CLIAgents
// keyed by role.CLI; falls back to the default Agent field.
func (d *Dispatcher) agentFor(role *core.Role) core.Agent {
	if len(d.CLIAgents) > 0 && role.CLI != "" {
		if a, ok := d.CLIAgents[role.CLI]; ok {
			return a
		}
	}
	return d.Agent
}

// DispatchInput contains the inputs for a dispatch operation.
type DispatchInput struct {
	RoleName string
	Inputs   map[string]string // required inputs (e.g., "diff_path", "spec_path")
}

// DispatchResult contains the output of a dispatch operation.
type DispatchResult struct {
	RunID     string
	JobID     string
	Findings  []core.Finding
	Artifacts []core.Artifact
	ExitCode  int

	// SupervisorVerdict is the final verdict from the last evaluation.
	// Nil if no supervisor engine was configured.
	SupervisorVerdict *core.SupervisorVerdict

	// RetryCount is the number of supervisor retries that occurred.
	RetryCount int
}

type dispatchAttemptResult struct {
	jobID       string
	result      *core.JobResult
	verdict     *core.SupervisorVerdict
	nextPrompt  string
	shouldRetry bool
}

// Orchestrate runs the full dispatch pipeline for an ephemeral job.
//
//nolint:gocyclo // The orchestration flow is linear but still exceeds the threshold after extracting attempt execution.
func (d *Dispatcher) Orchestrate(ctx context.Context, input *DispatchInput) (*DispatchResult, error) {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// 1. Load the role.
	role, err := roles.LoadRole(d.RoleDir, input.RoleName)
	if err != nil {
		return nil, fmt.Errorf("load role: %w", err)
	}
	logger.Info("loaded role", "name", role.Name, "cli", role.CLI)

	// 2. Validate required inputs.
	for _, req := range role.Inputs.Required {
		if _, ok := input.Inputs[req]; !ok {
			return nil, fmt.Errorf("missing required input %q for role %q", req, role.Name)
		}
	}

	// 3. Create the stores.
	eventStore := store.NewEventStore(d.DB)
	runStore := store.NewRunStore(d.DB, eventStore)
	jobStore := store.NewJobStore(d.DB, eventStore)
	findingStore := store.NewFindingStore(d.DB, eventStore)

	// 4. Create a run.
	runID := core.NewID()
	run := &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := runStore.CreateRun(ctx, run); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	logger.Info("created run", "id", runID)

	// 6. Render the prompt template.
	tmpl, err := roles.LoadPromptTemplate(d.PromptDir, role.PromptTemplate)
	if err != nil {
		return nil, fmt.Errorf("load prompt template: %w", err)
	}

	// Build template data from inputs.
	tmplData := make(map[string]string)
	for k, v := range input.Inputs {
		// Convert snake_case to PascalCase for template fields.
		tmplData[snakeToPascal(k)] = v
	}

	originalPrompt, err := roles.RenderPrompt(tmpl, tmplData)
	if err != nil {
		return nil, fmt.Errorf("render prompt: %w", err)
	}

	// Select the agent for this role (routes by role.CLI when CLIAgents is set).
	selectedAgent := d.agentFor(role)

	maxRetries := d.maxRetries()
	var lastVerdict *core.SupervisorVerdict
	var lastJobID string
	var lastResult *core.JobResult
	retryCount := 0
	prompt := originalPrompt

	for attempt := 0; attempt <= maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		attemptResult, err := d.executeAttempt(ctx, logger, eventStore, jobStore, runID, role, selectedAgent, prompt, originalPrompt, attempt, maxRetries)
		if err != nil {
			// Mark the run as failed so state is consistent even though the
			// job was already marked failed inside executeAttempt.
			if completeErr := runStore.CompleteRun(ctx, runID, core.RunStateFailed); completeErr != nil {
				logger.Error("failed to mark run as failed after attempt error",
					"run_id", runID, "error", completeErr)
			}
			return nil, err
		}
		lastJobID = attemptResult.jobID
		lastResult = attemptResult.result
		lastVerdict = attemptResult.verdict

		if attemptResult.shouldRetry {
			prompt = attemptResult.nextPrompt
			retryCount++
			continue
		}

		break
	}

	// 11. Persist findings from the final attempt.
	// Populate plan/phase/reviewer attribution from the dispatch context
	// so audit queries can filter findings by plan or phase. Plan 125 (B3).
	planID := input.Inputs["plan_id"]
	var phaseIdxPtr *int
	if v, ok := input.Inputs["phase_index"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			phaseIdxPtr = &n
		}
	}
	for i := range lastResult.Findings {
		f := &lastResult.Findings[i]
		f.RunID = runID
		f.JobID = lastJobID
		if f.ID == "" {
			f.ID = core.NewID()
		}
		if f.PlanID == "" {
			f.PlanID = planID
		}
		if f.PhaseIndex == nil && phaseIdxPtr != nil {
			pi := *phaseIdxPtr
			f.PhaseIndex = &pi
		}
		if f.ReviewerHandle == "" && strings.HasPrefix(role.Name, "reviewer.") {
			f.ReviewerHandle = role.Name
		}
		if err := findingStore.InsertFinding(ctx, f); err != nil {
			logger.Error("failed to persist finding", "error", err, "path", f.Path, "line", f.Line)
		}
	}

	// 12. Update job state to complete (or failed).
	finalState := core.JobStateComplete
	if lastResult.ExitCode != 0 {
		finalState = core.JobStateFailed
	}
	// If supervisor verdict failed after max retries, mark as failed.
	if lastVerdict != nil && !lastVerdict.Pass {
		finalState = core.JobStateFailed
	}
	if err := jobStore.UpdateJobState(ctx, lastJobID, finalState); err != nil {
		return nil, fmt.Errorf("update job to %s: %w", finalState, err)
	}

	// 13. Complete the run.
	runState := core.RunStateCompleted
	if finalState == core.JobStateFailed {
		runState = core.RunStateFailed
	}
	if err := runStore.CompleteRun(ctx, runID, runState); err != nil {
		return nil, fmt.Errorf("complete run: %w", err)
	}

	return &DispatchResult{
		RunID:             runID,
		JobID:             lastJobID,
		Findings:          lastResult.Findings,
		Artifacts:         lastResult.Artifacts,
		ExitCode:          lastResult.ExitCode,
		SupervisorVerdict: lastVerdict,
		RetryCount:        retryCount,
	}, nil
}

// checkPermission evaluates whether the dispatcher is allowed to launch the
// given CLI binary for the given role. binaryName must be a binary basename
// (e.g. "codex"), not a full path. It:
//   - Returns nil if the action is explicitly allowed.
//   - Returns an error (hard-fail) if the action is in the never list.
//   - Creates an attention.permission item and returns an error if the action
//     requires human approval.
//   - Returns an error (hard-fail) on undeclared actions when policy is
//     "deny" (default). Returns nil with a warning log when policy is "warn".
func (d *Dispatcher) checkPermission(ctx context.Context, logger *slog.Logger, runID string, role *core.Role, binaryName string) error {
	// Ensure we have just the basename in case a full path is supplied.
	binaryName = filepath.Base(binaryName)
	action := core.Permission{
		Kind:    core.PermKindBash,
		Subject: binaryName,
		Raw:     "bash:" + binaryName,
	}

	decision := core.EvaluateAction(role.Permissions, action)
	switch decision {
	case core.PermDecisionAllow:
		return nil

	case core.PermDecisionHardDeny:
		return fmt.Errorf("role %q is not permitted to execute %s (matched 'never')", role.Name, action.Raw)

	case core.PermDecisionRequiresHuman:
		// Create an attention.permission item so a human can approve.
		if d.AttentionStore != nil && d.DB != nil {
			item := &core.AttentionItem{
				ID:       core.NewID(),
				RunID:    runID,
				Kind:     core.AttentionPermission,
				Source:   "dispatch",
				Question: fmt.Sprintf("Role %q requests permission to execute %s", role.Name, action.Raw),
			}
			if insertErr := d.AttentionStore.InsertAttention(ctx, item); insertErr != nil {
				logger.Error("failed to insert attention.permission item", "error", insertErr, "role", role.Name, "action", action.Raw)
				// Proceed to hard-fail even if insert fails.
				return fmt.Errorf("role %q requires human approval to execute %s (attention insert failed: %w)", role.Name, action.Raw, insertErr)
			}
			return fmt.Errorf("role %q requires human approval to execute %s (attention ID: %s)", role.Name, action.Raw, item.ID)
		}
		return fmt.Errorf("role %q requires human approval to execute %s", role.Name, action.Raw)

	default: // PermDecisionUndeclared
		onUndeclared := "deny"
		if d.Policy != nil {
			onUndeclared = d.Policy.PermissionPolicy.OnUndeclared
		}
		if onUndeclared == "warn" {
			logger.Warn("undeclared permission: proceeding (on_undeclared=warn)",
				"role", role.Name,
				"action", action.Raw,
			)
			return nil
		}
		// Default: hard-fail.
		return fmt.Errorf("role %q is not permitted to execute %s (undeclared action; set policy.permissions.on_undeclared=warn to allow in development)", role.Name, action.Raw)
	}
}

func (d *Dispatcher) executeAttempt(
	ctx context.Context,
	logger *slog.Logger,
	eventStore *store.EventStore,
	jobStore *store.JobStore,
	runID string,
	role *core.Role,
	roleAgent core.Agent,
	prompt string,
	originalPrompt string,
	attempt int,
	maxRetries int,
) (*dispatchAttemptResult, error) {
	jobID := core.NewID()
	dispatchedBy := "user"
	if attempt > 0 {
		dispatchedBy = "supervisor-retry"
	}

	job := &core.Job{
		ID:           jobID,
		RunID:        runID,
		Role:         role.Name,
		State:        core.JobStatePending,
		DispatchedBy: dispatchedBy,
		CLI:          role.CLI,
		StartedAt:    time.Now(),
	}
	if err := jobStore.CreateJob(ctx, job); err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}
	logger.Info("created job", "id", jobID, "role", role.Name, "attempt", attempt)

	if err := jobStore.UpdateJobState(ctx, jobID, core.JobStateDispatched); err != nil {
		return nil, fmt.Errorf("update job to dispatched: %w", err)
	}

	// Permission check: verify that the role is allowed to invoke the agent binary.
	// We use a BinaryBasename() interface assertion so the check is cleanly
	// opt-in for non-CLI agent implementations (e.g., test stubs).
	if binaryAgent, ok := roleAgent.(interface{ BinaryBasename() string }); ok {
		binaryName := binaryAgent.BinaryBasename()
		if binaryName != "" {
			if permErr := d.checkPermission(ctx, logger, runID, role, binaryName); permErr != nil {
				jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed) //nolint:errcheck
				return nil, permErr
			}
		}
	}

	// Apply the role's wall-clock budget as a subprocess deadline.
	subCtx, subCancel := executil.BudgetTimeout(ctx, role.Budget.MaxWallclockMinutes)
	defer subCancel()

	handle, err := roleAgent.Dispatch(subCtx, job, prompt)
	if err != nil {
		jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed) //nolint:errcheck
		return nil, fmt.Errorf("dispatch agent: %w", err)
	}
	logger.Info("dispatched to agent", "cli", role.CLI, "attempt", attempt)

	result, err := handle.Wait(ctx)
	if err != nil {
		jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed) //nolint:errcheck
		return nil, fmt.Errorf("wait for agent: %w", err)
	}
	logger.Info("agent completed", "findings", len(result.Findings), "exit_code", result.ExitCode, "attempt", attempt)

	// Plan 121: persist cost per-attempt. Each retry has a distinct jobID,
	// so retries produce N rows for N attempts (matching real API spend).
	// Best-effort: failure logs and continues.
	if d.CostWriter != nil && result.Cost != nil {
		if err := d.CostWriter.RecordCost(ctx, runID, jobID, *result.Cost); err != nil {
			logger.Error("failed to persist cost sample",
				"run_id", runID, "job_id", jobID, "attempt", attempt, "error", err)
		}
	}

	attemptResult := &dispatchAttemptResult{
		jobID:   jobID,
		result:  result,
		verdict: nil,
	}
	if d.Supervisor == nil {
		return attemptResult, nil
	}

	evalCtx := &supervisor.EvalContext{
		Job:     job,
		Result:  result,
		Role:    role,
		RunID:   runID,
		WorkDir: d.WorkDir,
	}
	verdict, evalErr := d.Supervisor.Evaluate(evalCtx)
	if evalErr != nil {
		logger.Error("supervisor evaluation error", "error", evalErr)
		// Evaluation error means something is wrong with the rule engine or
		// its inputs. Silently passing would hide the bug. Return the error
		// so the caller marks the job and run as failed.
		jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed) //nolint:errcheck
		return nil, fmt.Errorf("supervisor.Evaluate: %w", evalErr)
	}
	attemptResult.verdict = verdict

	d.recordSupervisorResults(ctx, logger, runID, jobID, verdict)

	if verdict.Pass {
		return attemptResult, nil
	}

	if attempt < maxRetries {
		jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed) //nolint:errcheck

		feedback := d.buildRetryFeedback(verdict)
		retryPayload, _ := json.Marshal(map[string]interface{}{
			"job_id":       jobID,
			"attempt":      attempt + 1,
			"feedback":     feedback,
			"failed_rules": verdict.FailedMessages(),
		})
		retryEvent := &core.Event{
			ID:            core.NewID(),
			RunID:         runID,
			Kind:          core.EventSupervisorRetry,
			SchemaVersion: 1,
			CorrelationID: jobID,
			Payload:       string(retryPayload),
			CreatedAt:     time.Now(),
		}
		if writeErr := eventStore.WriteEventThenRow(ctx, retryEvent, nil); writeErr != nil {
			logger.Error("failed to write supervisor.retry event", "error", writeErr)
		}

		logger.Info("supervisor retry", "attempt", attempt+1, "failed_rules", len(verdict.FailedMessages()))
		attemptResult.nextPrompt = feedback + "\n\n" + originalPrompt
		attemptResult.shouldRetry = true
		return attemptResult, nil
	}

	logger.Warn("supervisor max retries exhausted, emitting compliance-breach",
		"job_id", jobID, "attempts", attempt+1)

	breachPayload, _ := json.Marshal(map[string]interface{}{
		"job_id":       jobID,
		"role":         role.Name,
		"failed_rules": verdict.FailedMessages(),
		"attempts":     attempt + 1,
	})
	breachEvent := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventComplianceBreach,
		SchemaVersion: 1,
		CorrelationID: jobID,
		Payload:       string(breachPayload),
		CreatedAt:     time.Now(),
	}
	if writeErr := eventStore.WriteEventThenRow(ctx, breachEvent, nil); writeErr != nil {
		logger.Error("failed to write compliance-breach event", "error", writeErr)
	}

	return attemptResult, nil
}

func (d *Dispatcher) recordSupervisorResults(ctx context.Context, logger *slog.Logger, runID, jobID string, verdict *core.SupervisorVerdict) {
	if d.SupervisorWriter == nil || verdict == nil {
		return
	}
	for _, result := range verdict.Results {
		if result.RuleName == "" && result.Message == "" {
			continue
		}
		if err := d.SupervisorWriter.RecordVerdict(ctx, runID, jobID, result); err != nil {
			logger.Error("failed to record supervisor verdict", "error", err, "job_id", jobID, "rule", result.RuleName)
		}
	}
}

// maxRetries returns the effective max retry count.
func (d *Dispatcher) maxRetries() int {
	if d.MaxRetries < 0 {
		return 0
	}
	if d.MaxRetries == 0 {
		return DefaultMaxRetries
	}
	return d.MaxRetries
}

// buildRetryFeedback constructs the supervisor feedback string
// prepended to the prompt on retry.
func (d *Dispatcher) buildRetryFeedback(verdict *core.SupervisorVerdict) string {
	msgs := verdict.FailedMessages()
	var sb strings.Builder
	sb.WriteString("SUPERVISOR FEEDBACK: The following contract rules were violated:\n")
	for i, msg := range msgs {
		fmt.Fprintf(&sb, "  %d. %s\n", i+1, msg)
	}
	sb.WriteString("Please fix these issues and try again.")
	return sb.String()
}

// snakeToPascal converts "diff_path" to "DiffPath".
func snakeToPascal(s string) string {
	parts := splitOn(s, '_')
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = string(upper(p[0])) + p[1:]
		}
	}
	result := ""
	for _, p := range parts {
		result += p
	}
	return result
}

func splitOn(s string, sep byte) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func upper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 32
	}
	return b
}
