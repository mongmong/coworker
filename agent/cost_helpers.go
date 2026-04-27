package agent

import (
	"sort"

	"github.com/chris/coworker/core"
)

// populateCost inspects msg and updates result.Cost when the message is a
// recognized cost-bearing event. It is called by both CliAgent.Wait and
// ReplayAgent.Wait so the two paths use identical semantics.
//
// Recognized event kinds and provenance:
//
//   - `result` (Claude Code, headless mode) — fires exactly once at end of
//     run with `total_cost_usd`, `usage`, and `modelUsage`. We populate
//     CostSample with USD directly. The model name comes from the
//     `modelUsage` map keys (e.g. "claude-opus-4-7[1m]"). To keep
//     CostSample.Model deterministic across runs (Go map iteration is
//     randomized), we sort the map keys lexicographically and take the
//     first one.
//
//   - `turn.completed` (Codex, exec --json mode) — emitted after every turn
//     with cumulative session `usage` (input_tokens, cached_input_tokens,
//     output_tokens). No USD figure is provided. The latest event wins —
//     since each event reports the cumulative total, overwriting on every
//     event leaves CostSample with the final cumulative usage. CostSample.USD
//     stays at 0; per-model price-table conversion is deferred (Plan 121
//     §Out of Scope).
//
// All other event kinds are no-ops.
func populateCost(msg streamMessage, result *core.JobResult) {
	switch msg.Type {
	case "result":
		if msg.TotalCostUSD <= 0 && msg.Usage == nil && msg.ModelUsage == nil {
			return
		}
		cs := &core.CostSample{
			Provider: "anthropic",
			USD:      msg.TotalCostUSD,
		}
		if msg.Usage != nil {
			cs.TokensIn = msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens
			cs.TokensOut = msg.Usage.OutputTokens
		}
		if len(msg.ModelUsage) > 0 {
			keys := make([]string, 0, len(msg.ModelUsage))
			for k := range msg.ModelUsage {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			cs.Model = keys[0]
		}
		result.Cost = cs
	case "turn.completed":
		if msg.Usage == nil {
			return
		}
		// Claude `result` is authoritative for USD. If we have already
		// captured a Claude result, do not overwrite it with a Codex
		// turn.completed (which would replace USD=$0.05 with USD=$0.00).
		// In practice no single CLI emits both events; this rule defends
		// against hand-constructed transcripts and future combined flows.
		if result.Cost != nil && result.Cost.Provider == "anthropic" {
			return
		}
		result.Cost = &core.CostSample{
			Provider:  "openai",
			TokensIn:  msg.Usage.InputTokens + msg.Usage.CachedInputTokens,
			TokensOut: msg.Usage.OutputTokens,
		}
	}
}
