package mcp

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// --- orch_ask_user -----------------------------------------------------------

// askUserInput is the typed input for orch_ask_user.
type askUserInput struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	JobID    string   `json:"job_id,omitempty"`
	RunID    string   `json:"run_id,omitempty"`
}

// askUserOutput is the typed output for orch_ask_user.
// V1: non-blocking — creates the attention item and returns its ID.
// The caller must poll orch_attention_list or wait for the operator to answer
// via the TUI/CLI.
type askUserOutput struct {
	AttentionID string `json:"attention_id"`
	Status      string `json:"status"`
}

// handleAskUser implements orch_ask_user.
// It creates an AttentionItem with Kind="question" and returns its ID.
func handleAskUser(
	as *store.AttentionStore,
) mcp.ToolHandlerFor[askUserInput, askUserOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in askUserInput,
	) (*mcp.CallToolResult, askUserOutput, error) {
		if in.Question == "" {
			return nil, askUserOutput{}, fmt.Errorf("question is required")
		}

		item := &core.AttentionItem{
			Kind:     core.AttentionQuestion,
			Source:   "mcp",
			Question: in.Question,
			Options:  in.Options,
			JobID:    in.JobID,
			RunID:    in.RunID,
		}

		if err := as.InsertAttention(ctx, item); err != nil {
			return nil, askUserOutput{}, fmt.Errorf("insert attention: %w", err)
		}

		return nil, askUserOutput{
			AttentionID: item.ID,
			Status:      "pending",
		}, nil
	}
}

// CallAskUser is an exported wrapper around the orch_ask_user handler logic,
// used by tests to exercise the handler directly without going through the MCP
// protocol transport.
func CallAskUser(ctx context.Context, as *store.AttentionStore, question string, options []string, jobID, runID string) (map[string]interface{}, error) {
	h := handleAskUser(as)
	_, out, err := h(ctx, nil, askUserInput{
		Question: question,
		Options:  options,
		JobID:    jobID,
		RunID:    runID,
	})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}

// --- orch_attention_list -----------------------------------------------------

// attentionListInput is the typed input for orch_attention_list.
type attentionListInput struct {
	RunID string `json:"run_id,omitempty"`
}

// attentionItemOutput is a JSON-serialisable representation of a core.AttentionItem.
type attentionItemOutput struct {
	ID         string   `json:"id"`
	RunID      string   `json:"run_id"`
	Kind       string   `json:"kind"`
	Source     string   `json:"source"`
	JobID      string   `json:"job_id,omitempty"`
	Question   string   `json:"question,omitempty"`
	Options    []string `json:"options,omitempty"`
	Answer     string   `json:"answer,omitempty"`
	AnsweredBy string   `json:"answered_by,omitempty"`
	CreatedAt  string   `json:"created_at"`
}

// attentionListOutput is the typed output for orch_attention_list.
type attentionListOutput struct {
	Items []attentionItemOutput `json:"items"`
}

// handleAttentionList implements orch_attention_list.
// If run_id is provided it lists all pending items for that run; otherwise it
// lists all pending items across all runs.
func handleAttentionList(
	as *store.AttentionStore,
) mcp.ToolHandlerFor[attentionListInput, attentionListOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in attentionListInput,
	) (*mcp.CallToolResult, attentionListOutput, error) {
		var items []*core.AttentionItem
		var err error

		if in.RunID != "" {
			items, err = as.ListUnansweredByRun(ctx, in.RunID)
		} else {
			items, err = as.ListAllPending(ctx)
		}
		if err != nil {
			return nil, attentionListOutput{}, fmt.Errorf("list attention: %w", err)
		}

		out := convertAttentionItems(items)
		return nil, attentionListOutput{Items: out}, nil
	}
}

// convertAttentionItems converts a slice of *core.AttentionItem to the JSON
// output type. Never returns nil — always returns an empty slice when input is
// empty so the JSON output is [] not null.
func convertAttentionItems(items []*core.AttentionItem) []attentionItemOutput {
	out := make([]attentionItemOutput, 0, len(items))
	for _, item := range items {
		out = append(out, attentionItemOutput{
			ID:         item.ID,
			RunID:      item.RunID,
			Kind:       string(item.Kind),
			Source:     item.Source,
			JobID:      item.JobID,
			Question:   item.Question,
			Options:    item.Options,
			Answer:     item.Answer,
			AnsweredBy: item.AnsweredBy,
			CreatedAt:  item.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return out
}

// CallAttentionList is an exported wrapper around the orch_attention_list
// handler logic, used by tests to exercise the handler directly.
func CallAttentionList(ctx context.Context, as *store.AttentionStore, runID string) (map[string]interface{}, error) {
	h := handleAttentionList(as)
	_, out, err := h(ctx, nil, attentionListInput{RunID: runID})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}

// --- orch_attention_answer ---------------------------------------------------

// attentionAnswerInput is the typed input for orch_attention_answer.
type attentionAnswerInput struct {
	AttentionID string `json:"attention_id"`
	Answer      string `json:"answer"`
	AnsweredBy  string `json:"answered_by"`
}

// attentionAnswerOutput is the typed output for orch_attention_answer.
type attentionAnswerOutput struct {
	Status string `json:"status"`
}

// handleAttentionAnswer implements orch_attention_answer.
// It calls AttentionStore.AnswerAttention then ResolveAttention.
func handleAttentionAnswer(
	as *store.AttentionStore,
) mcp.ToolHandlerFor[attentionAnswerInput, attentionAnswerOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in attentionAnswerInput,
	) (*mcp.CallToolResult, attentionAnswerOutput, error) {
		if in.AttentionID == "" {
			return nil, attentionAnswerOutput{}, fmt.Errorf("attention_id is required")
		}
		if in.Answer == "" {
			return nil, attentionAnswerOutput{}, fmt.Errorf("answer is required")
		}
		if in.AnsweredBy == "" {
			return nil, attentionAnswerOutput{}, fmt.Errorf("answered_by is required")
		}

		if err := as.AnswerAttention(ctx, in.AttentionID, in.Answer, in.AnsweredBy); err != nil {
			return nil, attentionAnswerOutput{}, fmt.Errorf("answer attention: %w", err)
		}

		if err := as.ResolveAttention(ctx, in.AttentionID); err != nil {
			// Best-effort: the answer is recorded; resolution failure is non-fatal.
			// A future reconcile pass can re-derive resolved_at from the answer fields.
			slog.Warn("failed to resolve attention after answer",
				"attention_id", in.AttentionID, "error", err)
		}

		return nil, attentionAnswerOutput{Status: "ok"}, nil
	}
}

// CallAttentionAnswer is an exported wrapper around the orch_attention_answer
// handler logic, used by tests to exercise the handler directly.
func CallAttentionAnswer(ctx context.Context, as *store.AttentionStore, attentionID, answer, answeredBy string) (map[string]interface{}, error) {
	h := handleAttentionAnswer(as)
	_, out, err := h(ctx, nil, attentionAnswerInput{
		AttentionID: attentionID,
		Answer:      answer,
		AnsweredBy:  answeredBy,
	})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}
