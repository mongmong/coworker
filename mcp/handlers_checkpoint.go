package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// --- orch_checkpoint_list ----------------------------------------------------

// checkpointListInput is the typed input for orch_checkpoint_list.
type checkpointListInput struct {
	RunID string `json:"run_id"`
}

// checkpointListOutput is the typed output for orch_checkpoint_list.
type checkpointListOutput struct {
	Items []attentionItemOutput `json:"items"`
}

// handleCheckpointList implements orch_checkpoint_list.
// run_id is required. Returns all checkpoint-kind attention items for the run.
func handleCheckpointList(
	as *store.AttentionStore,
) mcp.ToolHandlerFor[checkpointListInput, checkpointListOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in checkpointListInput,
	) (*mcp.CallToolResult, checkpointListOutput, error) {
		if in.RunID == "" {
			return nil, checkpointListOutput{}, fmt.Errorf("run_id is required")
		}

		kind := core.AttentionCheckpoint
		items, err := as.ListAttentionByRun(ctx, in.RunID, &kind)
		if err != nil {
			return nil, checkpointListOutput{}, fmt.Errorf("list checkpoints: %w", err)
		}

		out := convertAttentionItems(items)
		return nil, checkpointListOutput{Items: out}, nil
	}
}

// CallCheckpointList is an exported wrapper around the orch_checkpoint_list
// handler logic, used by tests to exercise the handler directly.
func CallCheckpointList(ctx context.Context, as *store.AttentionStore, runID string) (map[string]interface{}, error) {
	h := handleCheckpointList(as)
	_, out, err := h(ctx, nil, checkpointListInput{RunID: runID})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}

// --- orch_checkpoint_advance -------------------------------------------------

// checkpointAdvanceInput is the typed input for orch_checkpoint_advance.
type checkpointAdvanceInput struct {
	AttentionID string `json:"attention_id"`
	AnsweredBy  string `json:"answered_by,omitempty"`
	Notes       string `json:"notes,omitempty"`
}

// checkpointActionOutput is the shared typed output for advance and rollback.
type checkpointActionOutput struct {
	Status      string `json:"status"`
	AttentionID string `json:"attention_id"`
}

// handleCheckpointAdvance implements orch_checkpoint_advance.
// It writes AttentionAnswerApprove ("approve") for the checkpoint item and resolves it.
func handleCheckpointAdvance(
	as *store.AttentionStore,
	cw core.CheckpointWriter,
) mcp.ToolHandlerFor[checkpointAdvanceInput, checkpointActionOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in checkpointAdvanceInput,
	) (*mcp.CallToolResult, checkpointActionOutput, error) {
		if in.AttentionID == "" {
			return nil, checkpointActionOutput{}, fmt.Errorf("attention_id is required")
		}

		item, err := as.GetAttentionByID(ctx, in.AttentionID)
		if err != nil {
			return nil, checkpointActionOutput{}, fmt.Errorf("get checkpoint: %w", err)
		}
		if item == nil {
			return nil, checkpointActionOutput{}, fmt.Errorf("checkpoint not found: %s", in.AttentionID)
		}
		if item.Kind != core.AttentionCheckpoint {
			return nil, checkpointActionOutput{}, fmt.Errorf("not a checkpoint: item %s has kind %q", in.AttentionID, item.Kind)
		}

		answeredBy := in.AnsweredBy
		if answeredBy == "" {
			answeredBy = "user"
		}

		if err := as.AnswerAttention(ctx, in.AttentionID, core.AttentionAnswerApprove, answeredBy); err != nil {
			return nil, checkpointActionOutput{}, fmt.Errorf("answer checkpoint: %w", err)
		}
		if err := as.ResolveAttention(ctx, in.AttentionID); err != nil {
			// Best-effort: answer is recorded; resolution can be retried.
			_ = err
		}
		if cw != nil {
			if err := cw.ResolveCheckpoint(ctx, in.AttentionID, core.AttentionAnswerApprove, answeredBy, in.Notes); err != nil {
				return nil, checkpointActionOutput{}, fmt.Errorf("resolve checkpoint: %w", err)
			}
		}

		return nil, checkpointActionOutput{
			Status:      "approved",
			AttentionID: in.AttentionID,
		}, nil
	}
}

// CallCheckpointAdvance is an exported wrapper around the orch_checkpoint_advance
// handler logic, used by tests to exercise the handler directly.
func CallCheckpointAdvance(ctx context.Context, as *store.AttentionStore, attentionID, answeredBy string, writers ...core.CheckpointWriter) (map[string]interface{}, error) {
	var cw core.CheckpointWriter
	if len(writers) > 0 {
		cw = writers[0]
	}
	h := handleCheckpointAdvance(as, cw)
	_, out, err := h(ctx, nil, checkpointAdvanceInput{
		AttentionID: attentionID,
		AnsweredBy:  answeredBy,
	})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}

// --- orch_checkpoint_rollback ------------------------------------------------

// checkpointRollbackInput is the typed input for orch_checkpoint_rollback.
type checkpointRollbackInput struct {
	AttentionID string `json:"attention_id"`
	AnsweredBy  string `json:"answered_by,omitempty"`
	Notes       string `json:"notes,omitempty"`
}

// handleCheckpointRollback implements orch_checkpoint_rollback.
// It writes AttentionAnswerReject ("reject") for the checkpoint item and resolves it.
func handleCheckpointRollback(
	as *store.AttentionStore,
	cw core.CheckpointWriter,
) mcp.ToolHandlerFor[checkpointRollbackInput, checkpointActionOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in checkpointRollbackInput,
	) (*mcp.CallToolResult, checkpointActionOutput, error) {
		if in.AttentionID == "" {
			return nil, checkpointActionOutput{}, fmt.Errorf("attention_id is required")
		}

		item, err := as.GetAttentionByID(ctx, in.AttentionID)
		if err != nil {
			return nil, checkpointActionOutput{}, fmt.Errorf("get checkpoint: %w", err)
		}
		if item == nil {
			return nil, checkpointActionOutput{}, fmt.Errorf("checkpoint not found: %s", in.AttentionID)
		}
		if item.Kind != core.AttentionCheckpoint {
			return nil, checkpointActionOutput{}, fmt.Errorf("not a checkpoint: item %s has kind %q", in.AttentionID, item.Kind)
		}

		answeredBy := in.AnsweredBy
		if answeredBy == "" {
			answeredBy = "user"
		}

		if err := as.AnswerAttention(ctx, in.AttentionID, core.AttentionAnswerReject, answeredBy); err != nil {
			return nil, checkpointActionOutput{}, fmt.Errorf("answer checkpoint: %w", err)
		}
		if err := as.ResolveAttention(ctx, in.AttentionID); err != nil {
			// Best-effort: answer is recorded; resolution can be retried.
			_ = err
		}
		if cw != nil {
			if err := cw.ResolveCheckpoint(ctx, in.AttentionID, core.AttentionAnswerReject, answeredBy, in.Notes); err != nil {
				return nil, checkpointActionOutput{}, fmt.Errorf("resolve checkpoint: %w", err)
			}
		}

		return nil, checkpointActionOutput{
			Status:      "rejected",
			AttentionID: in.AttentionID,
		}, nil
	}
}

// CallCheckpointRollback is an exported wrapper around the orch_checkpoint_rollback
// handler logic, used by tests to exercise the handler directly.
func CallCheckpointRollback(ctx context.Context, as *store.AttentionStore, attentionID, answeredBy string, writers ...core.CheckpointWriter) (map[string]interface{}, error) {
	var cw core.CheckpointWriter
	if len(writers) > 0 {
		cw = writers[0]
	}
	h := handleCheckpointRollback(as, cw)
	_, out, err := h(ctx, nil, checkpointRollbackInput{
		AttentionID: attentionID,
		AnsweredBy:  answeredBy,
	})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}
