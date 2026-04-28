# Plan 137 — Re-audit fixes

> Re-audit (2026-04-27 second pass) found 1 NEW BLOCKER + 1 NEW IMPORTANT introduced by post-audit work. Both fixed here.

## NEW BLOCKER — Findings immutability trigger missed Plan 125's columns

**File:** `store/migrations/006_findings_immutability.sql`

Migration 006 protected the original 7 columns. When Plan 125 added `plan_id`, `phase_index`, `reviewer_handle` (Migration 008), the trigger silently let those mutate, breaking Decision 2 (findings are immutable).

**Fix:** Migration 010 drops the v1 trigger and recreates it with all 10 immutable columns. The new test cases in `TestFindingsImmutableTrigger_AllImmutableColumns` (3 added) prove each new column is now rejected.

## NEW IMPORTANT — `CostEventStore.RecordCost` had a cumulative race

**File:** `store/cost_event_store.go::RecordCost`

Plan 130 (I11) added a pre-read of `runs.cost_usd` to compute `cumulative_usd` for the event payload — but the read happened OUTSIDE the transaction. Two concurrent calls would each read the same baseline; the transactional bump (`UPDATE runs SET cost_usd = cost_usd + ?`) is atomic so the final stored value is correct, but the event payloads carry stale cumulatives. Live consumers (TUI, HTTP/SSE clients) display wrong totals.

**Fix:** RecordCost now opens its own transaction directly (rather than via `EventStore.WriteEventThenRow`) so the SELECT runs at the same isolation level as the bump. The event-first invariant is preserved: the event INSERT runs before the projection writes within the same tx. The in-memory event bus is published after commit (mirroring the helper's behavior).

New test `TestCostEventStore_ConcurrentRecordCostMatchesFinalCumulative` runs 10 concurrent RecordCost calls (`-race`), then asserts the cumulatives form an arithmetic sequence summing to the final `runs.cost_usd`. Without the fix this test would surface duplicate cumulatives.

## Verification

```text
go build ./...                                      → clean
go test -race ./store -count=1 -timeout 120s        → ok (10/10 immutability subtests, race test passes)
go test -race ./... -count=1 -timeout 180s          → 27 packages PASS, 0 failed, 0 races
golangci-lint run ./...                             → 0 issues
```
