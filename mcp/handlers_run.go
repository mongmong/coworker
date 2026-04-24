package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// runStatusInput is the typed input for orch_run_status.
type runStatusInput struct {
	RunID string `json:"run_id"`
}

// runStatusOutput is the typed output for orch_run_status.
type runStatusOutput struct {
	RunID     string  `json:"run_id"`
	Mode      string  `json:"mode"`
	State     string  `json:"state"`
	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at,omitempty"`
}

// runInspectInput is the typed input for orch_run_inspect.
type runInspectInput struct {
	RunID string `json:"run_id"`
}

// runSummary is the run sub-object embedded in runInspectOutput.
type runSummary struct {
	ID        string  `json:"id"`
	Mode      string  `json:"mode"`
	State     string  `json:"state"`
	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at,omitempty"`
}

// runInspectOutput is the typed output for orch_run_inspect.
type runInspectOutput struct {
	Run        runSummary   `json:"run"`
	Events     []core.Event `json:"events"`
	EventCount int          `json:"event_count"`
}

// handleRunStatus implements orch_run_status.
// It accepts a run_id and returns the run's status fields.
func handleRunStatus(
	rs *store.RunStore,
) mcp.ToolHandlerFor[runStatusInput, runStatusOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in runStatusInput,
	) (*mcp.CallToolResult, runStatusOutput, error) {
		if in.RunID == "" {
			return nil, runStatusOutput{}, fmt.Errorf("run_id is required")
		}

		run, err := rs.GetRun(ctx, in.RunID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, runStatusOutput{}, fmt.Errorf("run %q not found", in.RunID)
			}
			return nil, runStatusOutput{}, fmt.Errorf("get run: %w", err)
		}

		out := runStatusOutput{
			RunID:     run.ID,
			Mode:      run.Mode,
			State:     string(run.State),
			StartedAt: run.StartedAt.Format(time.RFC3339),
		}
		if run.EndedAt != nil {
			s := run.EndedAt.Format(time.RFC3339)
			out.EndedAt = &s
		}
		return nil, out, nil
	}
}

// CallRunStatus is an exported wrapper around the orch_run_status handler
// logic, used by tests to exercise the handler directly without going through
// the MCP protocol transport.
func CallRunStatus(ctx context.Context, rs *store.RunStore, runID string) (map[string]interface{}, error) {
	h := handleRunStatus(rs)
	_, out, err := h(ctx, nil, runStatusInput{RunID: runID})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}

// CallRunInspect is an exported wrapper around the orch_run_inspect handler
// logic, used by tests to exercise the handler directly without going through
// the MCP protocol transport.
func CallRunInspect(ctx context.Context, rs *store.RunStore, es *store.EventStore, runID string) (map[string]interface{}, error) {
	h := handleRunInspect(rs, es)
	_, out, err := h(ctx, nil, runInspectInput{RunID: runID})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}

// toMap round-trips a typed value through JSON to produce a map[string]interface{},
// which is convenient for generic test assertions.
func toMap(v interface{}) (map[string]interface{}, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return m, nil
}

// handleRunInspect implements orch_run_inspect.
// It returns the full run details plus all events for the run.
func handleRunInspect(
	rs *store.RunStore,
	es *store.EventStore,
) mcp.ToolHandlerFor[runInspectInput, runInspectOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in runInspectInput,
	) (*mcp.CallToolResult, runInspectOutput, error) {
		if in.RunID == "" {
			return nil, runInspectOutput{}, fmt.Errorf("run_id is required")
		}

		run, err := rs.GetRun(ctx, in.RunID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, runInspectOutput{}, fmt.Errorf("run %q not found", in.RunID)
			}
			return nil, runInspectOutput{}, fmt.Errorf("get run: %w", err)
		}

		events, err := es.ListEvents(ctx, in.RunID)
		if err != nil {
			return nil, runInspectOutput{}, fmt.Errorf("list events: %w", err)
		}

		summary := runSummary{
			ID:        run.ID,
			Mode:      run.Mode,
			State:     string(run.State),
			StartedAt: run.StartedAt.Format(time.RFC3339),
		}
		if run.EndedAt != nil {
			s := run.EndedAt.Format(time.RFC3339)
			summary.EndedAt = &s
		}

		// Ensure Events is never nil in the JSON output.
		if events == nil {
			events = []core.Event{}
		}

		out := runInspectOutput{
			Run:        summary,
			Events:     events,
			EventCount: len(events),
		}
		return nil, out, nil
	}
}
