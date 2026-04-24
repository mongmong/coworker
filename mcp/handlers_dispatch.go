package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/core"
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
