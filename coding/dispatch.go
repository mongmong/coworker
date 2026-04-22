package coding

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/chris/coworker/coding/roles"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// Dispatcher orchestrates the end-to-end flow: load role -> create run/job
// -> render prompt -> dispatch agent -> capture result -> persist findings.
type Dispatcher struct {
	RoleDir   string // path to directory containing role YAML files
	PromptDir string // path to directory containing prompt template files
	Agent     core.Agent
	DB        *store.DB
	Logger    *slog.Logger
}

// DispatchInput contains the inputs for a dispatch operation.
type DispatchInput struct {
	RoleName string
	Inputs   map[string]string // required inputs (e.g., "diff_path", "spec_path")
}

// DispatchResult contains the output of a dispatch operation.
type DispatchResult struct {
	RunID    string
	JobID    string
	Findings []core.Finding
	ExitCode int
}

// Orchestrate runs the full dispatch pipeline for an ephemeral job.
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

	// 5. Create a job.
	jobID := core.NewID()
	job := &core.Job{
		ID:           jobID,
		RunID:        runID,
		Role:         role.Name,
		State:        core.JobStatePending,
		DispatchedBy: "user",
		CLI:          role.CLI,
		StartedAt:    time.Now(),
	}
	if err := jobStore.CreateJob(ctx, job); err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}
	logger.Info("created job", "id", jobID, "role", role.Name)

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

	prompt, err := roles.RenderPrompt(tmpl, tmplData)
	if err != nil {
		return nil, fmt.Errorf("render prompt: %w", err)
	}

	// 7. Update job state to dispatched.
	if err := jobStore.UpdateJobState(ctx, jobID, core.JobStateDispatched); err != nil {
		return nil, fmt.Errorf("update job to dispatched: %w", err)
	}

	// 8. Dispatch to the agent.
	handle, err := d.Agent.Dispatch(ctx, job, prompt)
	if err != nil {
		jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed) //nolint:errcheck
		return nil, fmt.Errorf("dispatch agent: %w", err)
	}
	logger.Info("dispatched to agent", "cli", role.CLI)

	// 9. Wait for result.
	result, err := handle.Wait(ctx)
	if err != nil {
		jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed) //nolint:errcheck
		return nil, fmt.Errorf("wait for agent: %w", err)
	}
	logger.Info("agent completed", "findings", len(result.Findings), "exit_code", result.ExitCode)

	// 10. Persist findings.
	for i := range result.Findings {
		f := &result.Findings[i]
		f.RunID = runID
		f.JobID = jobID
		if f.ID == "" {
			f.ID = core.NewID()
		}
		if err := findingStore.InsertFinding(ctx, f); err != nil {
			logger.Error("failed to persist finding", "error", err, "path", f.Path, "line", f.Line)
			// Continue persisting other findings.
		}
	}

	// 11. Update job state to complete (or failed).
	finalState := core.JobStateComplete
	if result.ExitCode != 0 {
		finalState = core.JobStateFailed
	}
	if err := jobStore.UpdateJobState(ctx, jobID, finalState); err != nil {
		return nil, fmt.Errorf("update job to %s: %w", finalState, err)
	}

	// 12. Complete the run.
	runState := core.RunStateCompleted
	if finalState == core.JobStateFailed {
		runState = core.RunStateFailed
	}
	if err := runStore.CompleteRun(ctx, runID, runState); err != nil {
		return nil, fmt.Errorf("complete run: %w", err)
	}

	return &DispatchResult{
		RunID:    runID,
		JobID:    jobID,
		Findings: result.Findings,
		ExitCode: result.ExitCode,
	}, nil
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
