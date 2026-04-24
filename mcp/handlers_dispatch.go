package mcp

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// roleInvokeInput is the typed input for orch_role_invoke.
type roleInvokeInput struct {
	Role   string            `json:"role"`
	Inputs map[string]string `json:"inputs"`
}

// findingOutput is a JSON-serialisable representation of a core.Finding.
type findingOutput struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Severity    string `json:"severity"`
	Body        string `json:"body"`
	Fingerprint string `json:"fingerprint"`
}

// roleInvokeOutput is the typed output for orch_role_invoke.
type roleInvokeOutput struct {
	RunID    string          `json:"run_id"`
	JobID    string          `json:"job_id"`
	Findings []findingOutput `json:"findings"`
	ExitCode int             `json:"exit_code"`
}

// handleRoleInvoke implements orch_role_invoke.
// It calls Dispatcher.Orchestrate and returns the result as JSON.
func handleRoleInvoke(
	d *coding.Dispatcher,
) mcp.ToolHandlerFor[roleInvokeInput, roleInvokeOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in roleInvokeInput,
	) (*mcp.CallToolResult, roleInvokeOutput, error) {
		if in.Role == "" {
			return nil, roleInvokeOutput{}, fmt.Errorf("role is required")
		}
		if in.Inputs == nil {
			return nil, roleInvokeOutput{}, fmt.Errorf("inputs is required")
		}

		result, err := d.Orchestrate(ctx, &coding.DispatchInput{
			RoleName: in.Role,
			Inputs:   in.Inputs,
		})
		if err != nil {
			return nil, roleInvokeOutput{}, fmt.Errorf("orchestrate: %w", err)
		}

		findings := convertFindings(result.Findings)

		out := roleInvokeOutput{
			RunID:    result.RunID,
			JobID:    result.JobID,
			Findings: findings,
			ExitCode: result.ExitCode,
		}
		return nil, out, nil
	}
}

// convertFindings converts a slice of core.Finding to the JSON output type.
func convertFindings(findings []core.Finding) []findingOutput {
	out := make([]findingOutput, 0, len(findings))
	for _, f := range findings {
		out = append(out, findingOutput{
			ID:          f.ID,
			Path:        f.Path,
			Line:        f.Line,
			Severity:    string(f.Severity),
			Body:        f.Body,
			Fingerprint: f.Fingerprint,
		})
	}
	return out
}

// CallRoleInvoke is an exported wrapper around the orch_role_invoke handler
// logic, used by tests to exercise the handler directly without going through
// the MCP protocol transport.
func CallRoleInvoke(ctx context.Context, d *coding.Dispatcher, role string, inputs map[string]string) (map[string]interface{}, error) {
	h := handleRoleInvoke(d)
	_, out, err := h(ctx, nil, roleInvokeInput{Role: role, Inputs: inputs})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}

// --- orch_next_dispatch -------------------------------------------------------

// nextDispatchInput is the typed input for orch_next_dispatch.
type nextDispatchInput struct {
	Role string `json:"role"`
}

// nextDispatchOutput is the typed output for orch_next_dispatch when work is available.
type nextDispatchOutput struct {
	Status     string                 `json:"status"`
	DispatchID string                 `json:"dispatch_id,omitempty"`
	JobID      string                 `json:"job_id,omitempty"`
	Role       string                 `json:"role,omitempty"`
	Prompt     string                 `json:"prompt,omitempty"`
	Inputs     map[string]interface{} `json:"inputs,omitempty"`
}

// handleNextDispatch implements orch_next_dispatch.
// It calls DispatchStore.ClaimNextDispatch for the given role. If no dispatch
// is pending it returns {"status":"idle"}; otherwise it returns the full
// dispatch payload with {"status":"dispatched"}.
func handleNextDispatch(
	ds *store.DispatchStore,
) mcp.ToolHandlerFor[nextDispatchInput, nextDispatchOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in nextDispatchInput,
	) (*mcp.CallToolResult, nextDispatchOutput, error) {
		if in.Role == "" {
			return nil, nextDispatchOutput{}, fmt.Errorf("role is required")
		}

		dispatch, err := ds.ClaimNextDispatch(ctx, in.Role)
		if err != nil {
			return nil, nextDispatchOutput{}, fmt.Errorf("claim dispatch: %w", err)
		}

		if dispatch == nil {
			return nil, nextDispatchOutput{Status: "idle"}, nil
		}

		out := nextDispatchOutput{
			Status:     "dispatched",
			DispatchID: dispatch.ID,
			JobID:      dispatch.JobID,
			Role:       dispatch.Role,
			Prompt:     dispatch.Prompt,
			Inputs:     dispatch.Inputs,
		}
		return nil, out, nil
	}
}

// CallNextDispatch is an exported wrapper around the orch_next_dispatch handler
// logic, used by tests to exercise the handler directly.
func CallNextDispatch(ctx context.Context, ds *store.DispatchStore, role string) (map[string]interface{}, error) {
	h := handleNextDispatch(ds)
	_, out, err := h(ctx, nil, nextDispatchInput{Role: role})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}

// --- orch_job_complete --------------------------------------------------------

// jobCompleteInput is the typed input for orch_job_complete.
type jobCompleteInput struct {
	DispatchID string                 `json:"dispatch_id"`
	JobID      string                 `json:"job_id"`
	Outputs    map[string]interface{} `json:"outputs"`
}

// jobCompleteOutput is the typed output for orch_job_complete.
type jobCompleteOutput struct {
	Status string `json:"status"`
}

// handleJobComplete implements orch_job_complete.
// It calls DispatchStore.CompleteDispatch and optionally updates the job state.
func handleJobComplete(
	ds *store.DispatchStore,
	js *store.JobStore,
) mcp.ToolHandlerFor[jobCompleteInput, jobCompleteOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in jobCompleteInput,
	) (*mcp.CallToolResult, jobCompleteOutput, error) {
		if in.DispatchID == "" {
			return nil, jobCompleteOutput{}, fmt.Errorf("dispatch_id is required")
		}
		if in.JobID == "" {
			return nil, jobCompleteOutput{}, fmt.Errorf("job_id is required")
		}
		if in.Outputs == nil {
			return nil, jobCompleteOutput{}, fmt.Errorf("outputs is required")
		}

		if err := ds.CompleteDispatch(ctx, in.DispatchID, in.Outputs); err != nil {
			return nil, jobCompleteOutput{}, fmt.Errorf("complete dispatch: %w", err)
		}

		// Update the associated job state to complete when a JobStore is wired.
		if js != nil {
			if err := js.UpdateJobState(ctx, in.JobID, core.JobStateComplete); err != nil {
				// Log but do not fail the call — the dispatch is already marked
				// complete, and the job update is a best-effort projection.
				// A future replay/reconcile pass can re-derive state from events.
				slog.Warn("failed to update job state after dispatch complete",
					"job_id", in.JobID, "error", err)
			}
		}

		return nil, jobCompleteOutput{Status: "ok"}, nil
	}
}

// CallJobComplete is an exported wrapper around the orch_job_complete handler
// logic, used by tests to exercise the handler directly.
func CallJobComplete(ctx context.Context, ds *store.DispatchStore, js *store.JobStore, dispatchID, jobID string, outputs map[string]interface{}) (map[string]interface{}, error) {
	h := handleJobComplete(ds, js)
	_, out, err := h(ctx, nil, jobCompleteInput{
		DispatchID: dispatchID,
		JobID:      jobID,
		Outputs:    outputs,
	})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}
