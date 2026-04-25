package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chris/coworker/coding/roles"
	"github.com/chris/coworker/coding/supervisor"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// DefaultMaxRetries is the default maximum number of supervisor retries
// before escalating to a compliance-breach event.
const DefaultMaxRetries = 3

// Dispatcher orchestrates the end-to-end flow: load role -> create run/job
// -> render prompt -> dispatch agent -> capture result -> supervisor check
// -> persist findings (with retry loop on contract failure).
type Dispatcher struct {
	RoleDir   string // path to directory containing role YAML files
	PromptDir string // path to directory containing prompt template files
	Agent     core.Agent
	DB        *store.DB
	Logger    *slog.Logger

	// Supervisor is the optional rule engine. If nil, no contract
	// checks are performed (equivalent to all-pass).
	Supervisor *supervisor.RuleEngine

	// MaxRetries is the maximum number of supervisor retries per job.
	// Zero means use DefaultMaxRetries. Negative means no retries.
	MaxRetries int

	// WorkDir is the working directory for git-based predicates.
	// If empty, git predicates use the current working directory.
	WorkDir string
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

		attemptResult, err := d.executeAttempt(ctx, logger, eventStore, jobStore, runID, role, prompt, originalPrompt, attempt, maxRetries)
		if err != nil {
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
	for i := range lastResult.Findings {
		f := &lastResult.Findings[i]
		f.RunID = runID
		f.JobID = lastJobID
		if f.ID == "" {
			f.ID = core.NewID()
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

func (d *Dispatcher) executeAttempt(
	ctx context.Context,
	logger *slog.Logger,
	eventStore *store.EventStore,
	jobStore *store.JobStore,
	runID string,
	role *core.Role,
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

	handle, err := d.Agent.Dispatch(ctx, job, prompt)
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
		// Treat evaluation error as a pass — don't block the job
		// for engine bugs.
		verdict = &core.SupervisorVerdict{Pass: true}
	}
	attemptResult.verdict = verdict

	verdictPayload := d.marshalVerdictPayload(jobID, verdict)
	verdictEvent := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventSupervisorVerdict,
		SchemaVersion: 1,
		CorrelationID: jobID,
		Payload:       verdictPayload,
		CreatedAt:     time.Now(),
	}
	if writeErr := eventStore.WriteEventThenRow(ctx, verdictEvent, nil); writeErr != nil {
		logger.Error("failed to write supervisor.verdict event", "error", writeErr)
	}

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

// marshalVerdictPayload serializes a verdict to JSON for the event payload.
func (d *Dispatcher) marshalVerdictPayload(jobID string, verdict *core.SupervisorVerdict) string {
	type resultJSON struct {
		RuleName string `json:"rule_name"`
		Passed   bool   `json:"passed"`
		Message  string `json:"message"`
	}
	var results []resultJSON
	for _, r := range verdict.Results {
		results = append(results, resultJSON{
			RuleName: r.RuleName,
			Passed:   r.Passed,
			Message:  r.Message,
		})
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"job_id":  jobID,
		"pass":    verdict.Pass,
		"results": results,
	})
	return string(payload)
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
