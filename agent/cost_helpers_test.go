package agent

import (
	"testing"

	"github.com/chris/coworker/core"
)

func TestPopulateCost_ClaudeResult(t *testing.T) {
	msg := streamMessage{
		Type:         "result",
		TotalCostUSD: 0.46878525,
		Usage: &streamUsage{
			InputTokens:          15,
			OutputTokens:         2324,
			CacheReadInputTokens: 121758,
		},
		ModelUsage: map[string]modelUsageRow{
			"claude-opus-4-7[1m]": {InputTokens: 15, OutputTokens: 2324, CostUSD: 0.46878525},
		},
	}
	res := &core.JobResult{}
	populateCost(msg, res)
	if res.Cost == nil {
		t.Fatal("Cost was not populated")
	}
	if res.Cost.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic", res.Cost.Provider)
	}
	if res.Cost.USD != 0.46878525 {
		t.Errorf("USD = %v, want 0.46878525", res.Cost.USD)
	}
	if res.Cost.TokensIn != 15+121758 {
		t.Errorf("TokensIn = %d, want %d", res.Cost.TokensIn, 15+121758)
	}
	if res.Cost.TokensOut != 2324 {
		t.Errorf("TokensOut = %d, want 2324", res.Cost.TokensOut)
	}
	if res.Cost.Model != "claude-opus-4-7[1m]" {
		t.Errorf("Model = %q, want claude-opus-4-7[1m]", res.Cost.Model)
	}
}

func TestPopulateCost_ClaudeResult_DeterministicModelSelection(t *testing.T) {
	// Two model keys; sorted lexicographically, "a..." wins.
	msg := streamMessage{
		Type:         "result",
		TotalCostUSD: 1.0,
		ModelUsage: map[string]modelUsageRow{
			"z-model": {CostUSD: 0.5},
			"a-model": {CostUSD: 0.5},
		},
	}
	for i := 0; i < 10; i++ {
		res := &core.JobResult{}
		populateCost(msg, res)
		if res.Cost == nil || res.Cost.Model != "a-model" {
			t.Fatalf("iter %d: Model = %v, want a-model", i, res.Cost)
		}
	}
}

func TestPopulateCost_ClaudeResult_EmptyDoesNothing(t *testing.T) {
	msg := streamMessage{Type: "result"}
	res := &core.JobResult{}
	populateCost(msg, res)
	if res.Cost != nil {
		t.Errorf("Cost should be nil when result has no fields; got %+v", res.Cost)
	}
}

func TestPopulateCost_CodexTurnCompleted(t *testing.T) {
	msg := streamMessage{
		Type: "turn.completed",
		Usage: &streamUsage{
			InputTokens:       154968,
			CachedInputTokens: 127744,
			OutputTokens:      1734,
		},
	}
	res := &core.JobResult{}
	populateCost(msg, res)
	if res.Cost == nil {
		t.Fatal("Cost was not populated")
	}
	if res.Cost.Provider != "openai" {
		t.Errorf("Provider = %q, want openai", res.Cost.Provider)
	}
	if res.Cost.USD != 0 {
		t.Errorf("USD = %v, want 0 (Codex has no USD)", res.Cost.USD)
	}
	if res.Cost.TokensIn != 154968+127744 {
		t.Errorf("TokensIn = %d, want %d", res.Cost.TokensIn, 154968+127744)
	}
	if res.Cost.TokensOut != 1734 {
		t.Errorf("TokensOut = %d, want 1734", res.Cost.TokensOut)
	}
}

func TestPopulateCost_CodexTurnCompletedLastWins(t *testing.T) {
	// Codex emits turn.completed cumulatively per session.
	// Latest-event-wins semantics: second event overwrites first.
	res := &core.JobResult{}
	populateCost(streamMessage{
		Type:  "turn.completed",
		Usage: &streamUsage{InputTokens: 100, OutputTokens: 50},
	}, res)
	populateCost(streamMessage{
		Type:  "turn.completed",
		Usage: &streamUsage{InputTokens: 250, OutputTokens: 120},
	}, res)
	if res.Cost == nil || res.Cost.TokensIn != 250 || res.Cost.TokensOut != 120 {
		t.Errorf("expected last-event-wins (250, 120); got %+v", res.Cost)
	}
}

func TestPopulateCost_CodexEmptyUsageNoOp(t *testing.T) {
	msg := streamMessage{Type: "turn.completed"}
	res := &core.JobResult{}
	populateCost(msg, res)
	if res.Cost != nil {
		t.Errorf("Cost should be nil when turn.completed has no usage; got %+v", res.Cost)
	}
}

func TestPopulateCost_UnknownTypeIsNoOp(t *testing.T) {
	msg := streamMessage{Type: "finding", TotalCostUSD: 1.0, Usage: &streamUsage{InputTokens: 1}}
	res := &core.JobResult{}
	populateCost(msg, res)
	if res.Cost != nil {
		t.Errorf("Cost should be nil for non-cost event types; got %+v", res.Cost)
	}
}

func TestPopulateCost_ClaudeResultWinsOverLaterTurnCompleted(t *testing.T) {
	// Defensive rule: if a Claude `result` has already populated Cost,
	// a later Codex `turn.completed` must not overwrite it (would zero out
	// the USD figure). In practice no single CLI emits both events; this
	// guards hand-constructed or combined-flow transcripts.
	res := &core.JobResult{}
	populateCost(streamMessage{
		Type:         "result",
		TotalCostUSD: 0.05,
		ModelUsage:   map[string]modelUsageRow{"claude": {CostUSD: 0.05}},
	}, res)
	populateCost(streamMessage{
		Type:  "turn.completed",
		Usage: &streamUsage{InputTokens: 999, OutputTokens: 999},
	}, res)
	if res.Cost == nil || res.Cost.Provider != "anthropic" || res.Cost.USD != 0.05 {
		t.Errorf("Claude result should win; got %+v", res.Cost)
	}
}

func TestPopulateCost_DoneIsNoOp(t *testing.T) {
	msg := streamMessage{Type: "done", ExitCode: 0}
	res := &core.JobResult{}
	populateCost(msg, res)
	if res.Cost != nil {
		t.Errorf("Cost should be nil on `done`; got %+v", res.Cost)
	}
}
