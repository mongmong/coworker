# Plan 130 — I11: TUI cost field reconciliation

> Reconciles producer + consumer field names + adds cumulative/budget to the event payload so live consumers can render totals without re-querying the runs row.

## Mismatch (before)

- **Producer:** `store.CostEventStore.RecordCost` writes payload `{tokens_in, tokens_out, usd}`.
- **Consumer:** `tui.CostPayload` decoded `{input_tok, output_tok, cost_usd}`.

TUI silently dropped events because the JSON keys never matched. `cumulative_usd` and `budget_usd` weren't emitted at all.

## Resolution

1. Producer pre-reads `runs.cost_usd` + `runs.budget_usd`, computes cumulative = pre + sample.USD, marshals all of `tokens_in`, `tokens_out`, `usd`, `provider`, `model`, `cumulative_usd`, `budget_usd` into the payload.
2. TUI's `CostPayload` struct renamed to match producer (`TokensIn`, `TokensOut`, `USD`), keeps existing `Cumulative` / `BudgetUSD` fields. The TUI's consumer code only reads Cumulative and BudgetUSD so behavior is unchanged — but now the data actually arrives.

New test `TestCostEventStore_PayloadIncludesCumulativeAndBudget` asserts:
- First sample: `cumulative_usd == sample.USD`, `budget_usd == run.budget_usd`.
- Second sample: `cumulative_usd` accumulates correctly.

## Verification

```
go build ./...                                  → clean
go test -race ./... -count=1 -timeout 180s      → 30 ok, 0 failed, 0 races
golangci-lint run ./...                         → 0 issues
```
