package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// --- orch_findings_list -------------------------------------------------------

// findingsListInput is the typed input for orch_findings_list.
type findingsListInput struct {
	RunID string `json:"run_id"`
}

// findingListItem is a JSON-serialisable representation of a core.Finding for
// list output. It includes the resolved flag derived from ResolvedByJobID.
type findingListItem struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Severity    string `json:"severity"`
	Body        string `json:"body"`
	Fingerprint string `json:"fingerprint"`
	Resolved    bool   `json:"resolved"`
}

// findingsListOutput is the typed output for orch_findings_list.
type findingsListOutput struct {
	Findings []findingListItem `json:"findings"`
}

// handleFindingsList implements orch_findings_list.
// It calls FindingStore.ListFindings and maps findings to the output format.
func handleFindingsList(
	fs *store.FindingStore,
) mcp.ToolHandlerFor[findingsListInput, findingsListOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in findingsListInput,
	) (*mcp.CallToolResult, findingsListOutput, error) {
		if in.RunID == "" {
			return nil, findingsListOutput{}, fmt.Errorf("run_id is required")
		}

		findings, err := fs.ListFindings(ctx, in.RunID)
		if err != nil {
			return nil, findingsListOutput{}, fmt.Errorf("list findings: %w", err)
		}

		items := convertFindingListItems(findings)
		return nil, findingsListOutput{Findings: items}, nil
	}
}

// convertFindingListItems converts a slice of core.Finding to the list output
// type. Never returns nil — always returns an empty slice so the JSON output is
// [] not null.
func convertFindingListItems(findings []core.Finding) []findingListItem {
	out := make([]findingListItem, 0, len(findings))
	for _, f := range findings {
		out = append(out, findingListItem{
			ID:          f.ID,
			Path:        f.Path,
			Line:        f.Line,
			Severity:    string(f.Severity),
			Body:        f.Body,
			Fingerprint: f.Fingerprint,
			Resolved:    f.ResolvedByJobID != "",
		})
	}
	return out
}

// CallFindingsList is an exported wrapper around the orch_findings_list handler
// logic, used by tests to exercise the handler directly without going through
// the MCP protocol transport.
func CallFindingsList(ctx context.Context, fs *store.FindingStore, runID string) (map[string]interface{}, error) {
	h := handleFindingsList(fs)
	_, out, err := h(ctx, nil, findingsListInput{RunID: runID})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}

// --- orch_artifact_read -------------------------------------------------------

// artifactReadInput is the typed input for orch_artifact_read.
type artifactReadInput struct {
	JobID string `json:"job_id"`
}

// artifactItem is a JSON-serialisable representation of a core.Artifact.
type artifactItem struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// artifactReadOutput is the typed output for orch_artifact_read.
type artifactReadOutput struct {
	Artifacts []artifactItem `json:"artifacts"`
}

// handleArtifactRead implements orch_artifact_read.
// It calls ArtifactStore.ListArtifacts for the given job_id.
func handleArtifactRead(
	as *store.ArtifactStore,
) mcp.ToolHandlerFor[artifactReadInput, artifactReadOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in artifactReadInput,
	) (*mcp.CallToolResult, artifactReadOutput, error) {
		if in.JobID == "" {
			return nil, artifactReadOutput{}, fmt.Errorf("job_id is required")
		}

		artifacts, err := as.ListArtifacts(ctx, in.JobID)
		if err != nil {
			return nil, artifactReadOutput{}, fmt.Errorf("list artifacts: %w", err)
		}

		items := convertArtifactItems(artifacts)
		return nil, artifactReadOutput{Artifacts: items}, nil
	}
}

// convertArtifactItems converts a slice of core.Artifact to the output type.
// Never returns nil — always returns an empty slice so the JSON output is
// [] not null.
func convertArtifactItems(artifacts []core.Artifact) []artifactItem {
	out := make([]artifactItem, 0, len(artifacts))
	for _, a := range artifacts {
		out = append(out, artifactItem{
			ID:   a.ID,
			Kind: a.Kind,
			Path: a.Path,
		})
	}
	return out
}

// CallArtifactRead is an exported wrapper around the orch_artifact_read handler
// logic, used by tests to exercise the handler directly without going through
// the MCP protocol transport.
func CallArtifactRead(ctx context.Context, as *store.ArtifactStore, jobID string) (map[string]interface{}, error) {
	h := handleArtifactRead(as)
	_, out, err := h(ctx, nil, artifactReadInput{JobID: jobID})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}

// --- orch_artifact_write ------------------------------------------------------

// artifactWriteInput is the typed input for orch_artifact_write.
type artifactWriteInput struct {
	JobID string `json:"job_id"`
	Kind  string `json:"kind"`
	Path  string `json:"path"`
	RunID string `json:"run_id"`
}

// artifactWriteOutput is the typed output for orch_artifact_write.
type artifactWriteOutput struct {
	ArtifactID string `json:"artifact_id"`
	Status     string `json:"status"`
}

// handleArtifactWrite implements orch_artifact_write.
// It creates a new Artifact and calls ArtifactStore.InsertArtifact.
func handleArtifactWrite(
	as *store.ArtifactStore,
) mcp.ToolHandlerFor[artifactWriteInput, artifactWriteOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in artifactWriteInput,
	) (*mcp.CallToolResult, artifactWriteOutput, error) {
		if in.JobID == "" {
			return nil, artifactWriteOutput{}, fmt.Errorf("job_id is required")
		}
		if in.Kind == "" {
			return nil, artifactWriteOutput{}, fmt.Errorf("kind is required")
		}
		if in.Path == "" {
			return nil, artifactWriteOutput{}, fmt.Errorf("path is required")
		}
		if in.RunID == "" {
			return nil, artifactWriteOutput{}, fmt.Errorf("run_id is required")
		}

		artifact := &core.Artifact{
			ID:    core.NewID(),
			JobID: in.JobID,
			Kind:  in.Kind,
			Path:  in.Path,
		}

		if err := as.InsertArtifact(ctx, artifact, in.RunID); err != nil {
			return nil, artifactWriteOutput{}, fmt.Errorf("insert artifact: %w", err)
		}

		return nil, artifactWriteOutput{
			ArtifactID: artifact.ID,
			Status:     "ok",
		}, nil
	}
}

// CallArtifactWrite is an exported wrapper around the orch_artifact_write
// handler logic, used by tests to exercise the handler directly without going
// through the MCP protocol transport.
func CallArtifactWrite(ctx context.Context, as *store.ArtifactStore, jobID, kind, path, runID string) (map[string]interface{}, error) {
	h := handleArtifactWrite(as)
	_, out, err := h(ctx, nil, artifactWriteInput{
		JobID: jobID,
		Kind:  kind,
		Path:  path,
		RunID: runID,
	})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}
