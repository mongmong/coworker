# Plan 123 — B5 + B6: Goroutine Leak Fix + Stub Commands

> Implemented inline. Two independent fixes bundled because they're both small and both touch user-visible reliability.

**Goal:** Close two BLOCKERs from the 2026-04-27 V1 audit:
1. **B5** — `agent/opencode_http_agent.go:181-182` fires `sendMessage` as fire-and-forget. The goroutine respects `sseCtx` but no completion signal exists. Under adverse network conditions (DNS hang, slow-handshake) it leaks indefinitely.
2. **B6** — `cli/advance.go:61` and `cli/rollback.go:62` print `"not yet implemented"` and exit. Spec §Modes line 506-507 requires both as the CLI verbs for checkpoint advance/rollback. Equivalent MCP tools (`orch_checkpoint_advance`, `orch_checkpoint_rollback`) already exist with exported wrappers (`CallCheckpointAdvance`, `CallCheckpointRollback`) the CLI can reuse.

**Architecture:**
- **B5:** Track the message goroutine via a `sync.WaitGroup` field on `openCodeJobHandle`. `Cancel()` cancels the SSE context (which `sendMessage` respects) and then `Wait`s on the WaitGroup with a tight timeout (e.g., 5s) before returning. If the goroutine is still hung after the timeout, Cancel returns nil anyway — we can't block forever — but the timeout is logged so operators see the leak.
- **B6:** Both stubs already open the DB and read the current session. The new logic finds the relevant attention/checkpoint and calls the existing `mcp.CallCheckpointAdvance` / `mcp.CallCheckpointRollback`. For `advance` (no args), find the most recent unanswered checkpoint for the session's run and approve it. For `rollback <checkpoint-id>`, look up the named checkpoint and reject it.

**Tech Stack:** No new dependencies. Reuses existing `mcp` exported wrappers.

**Reference:** `docs/reviews/2026-04-27-comprehensive-audit.md` §B5 + §B6; existing `mcp/handlers_checkpoint.go::CallCheckpointAdvance` and `CallCheckpointRollback`.

---

## Required-API audit

| Surface | Reality |
| --- | --- |
| `mcp.CallCheckpointAdvance(ctx, as *AttentionStore, attentionID, answeredBy string, writers ...core.CheckpointWriter)` | `mcp/handlers_checkpoint.go:134`. Returns `(map[string]interface{}, error)`. |
| `mcp.CallCheckpointRollback(ctx, as *AttentionStore, attentionID, answeredBy string, writers ...core.CheckpointWriter)` | `mcp/handlers_checkpoint.go:214`. Same shape. |
| `*store.AttentionStore.GetUnansweredCheckpointForRun(ctx, runID, source string)` | `store/attention_store.go:227`. Returns `(*core.AttentionItem, error)`. Source is filter; pass empty for "any". |
| `*store.AttentionStore.GetAttentionByID(ctx, id)` | Returns `(*core.AttentionItem, error)`. |
| `session.Manager.CurrentSession()` | Returns the active session including `RunID`. |
| `openCodeJobHandle` struct | At `agent/opencode_http_agent.go:135-143`. Add a `messageWG sync.WaitGroup` field. |

---

## Scope

In scope:

1. **B5:** `agent/opencode_http_agent.go`:
   - Add `messageWG sync.WaitGroup` field to `openCodeJobHandle`.
   - In `Dispatch`, `messageWG.Add(1)` before launching the message goroutine; `defer messageWG.Done()` inside the goroutine.
   - In `Cancel`, after the abort POST and `h.cancel()`, wait on `messageWG` with a 5-second timeout. On timeout, log a warning that the message goroutine did not return cleanly. Return nil regardless — `Cancel()` is best-effort.
   - One unit test: `TestOpenCodeHTTPAgent_CancelWaitsForMessageGoroutine` — using `httptest.NewServer` that hangs on the message POST, assert `Cancel()` returns within ~5s and the WaitGroup state is observable.
2. **B6:** `cli/advance.go` and `cli/rollback.go`:
   - `advance` (no args): find the unanswered checkpoint for the session's current run via `AttentionStore.GetUnansweredCheckpointForRun(ctx, runID, "")`. If none, print "no checkpoint waiting" and exit 0. Otherwise call `mcp.CallCheckpointAdvance`. Print the resolved attention ID.
   - `rollback <checkpoint-id>`: validate the ID via `AttentionStore.GetAttentionByID`. If kind != checkpoint, error. Call `mcp.CallCheckpointRollback`. Print the resolved ID.
   - Both gain `--answered-by <user>` flag (default `"cli"`) so audit trails distinguish advance/rollback origin.
   - Tests: 4 cases per command (success / no-pending / wrong-kind / DB error).
3. `decisions.md` Decision 10 documenting:
   - Why CLI commands reuse MCP exported wrappers (one source of truth for checkpoint resolution semantics).
   - The 5-second cancel timeout for the message goroutine; documented as "best-effort, may leak under hung-network paths beyond this window."

Out of scope:

- The `--source` filter on `advance` (apply a specific checkpoint kind only) — defer; users can use `rollback <id>` for surgical operations.
- B5 fix for `cli_handle.go` pipe cleanup (audit IMPORTANT-I8) — separate plan.
- Restructuring `openCodeJobHandle` as part of broader cleanup — minimum-viable change here.

---

## File Structure

**Modify:**
- `agent/opencode_http_agent.go` — add WaitGroup, await on Cancel.
- `agent/opencode_http_agent_test.go` — new cancel-waits test.
- `cli/advance.go` — implement advance logic.
- `cli/rollback.go` — implement rollback logic.
- `cli/advance_test.go` — new test file (or extend existing if present).
- `cli/rollback_test.go` — new test file.
- `docs/architecture/decisions.md` — Decision 10.

**Create:** `cli/advance_test.go`, `cli/rollback_test.go` if absent.

---

## Phase 1 — B5: opencode message goroutine WaitGroup

**Files:** `agent/opencode_http_agent.go`, `agent/opencode_http_agent_test.go`

- [ ] **Step 1 — Extend `openCodeJobHandle`:**

```go
type openCodeJobHandle struct {
    sessionID string
    agent     *OpenCodeHTTPAgent
    resultCh  <-chan *core.JobResult
    cancel    context.CancelFunc

    // messageWG tracks the fire-and-forget sendMessage goroutine.
    // Cancel waits on it (with a timeout) so callers don't leak when
    // the network hangs. Plan 123 (B5).
    messageWG sync.WaitGroup
}
```

- [ ] **Step 2 — In `Dispatch`, instrument the message goroutine:**

```go
// Before:  go func() { _ = a.sendMessage(...) }()
handle := &openCodeJobHandle{
    sessionID: sessionID,
    agent:     a,
    resultCh:  resultCh,
    cancel:    sseCancel,
}
handle.messageWG.Add(1)
go func() {
    defer handle.messageWG.Done()
    _ = a.sendMessage(sseCtx, client, base, sessionID, prompt)
}()
return handle, nil
```

- [ ] **Step 3 — In `Cancel`, wait on the message goroutine after cancelling the SSE context:**

```go
func (h *openCodeJobHandle) Cancel() error {
    abortCtx, abortCancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer abortCancel()
    _ = h.agent.abortSession(abortCtx, h.agent.httpClient(), h.agent.serverURL(), h.sessionID)

    h.cancel()

    // Wait briefly for the message goroutine to observe sseCtx cancellation.
    // If it doesn't return within 5s (hung network), log and return anyway.
    done := make(chan struct{})
    go func() {
        h.messageWG.Wait()
        close(done)
    }()
    select {
    case <-done:
    case <-time.After(5 * time.Second):
        slog.Warn("opencode message goroutine did not return within timeout",
            "session_id", h.sessionID,
            "timeout", "5s")
    }
    return nil
}
```

(Or use a deferred `slog` import if not already present in the file — verify before writing.)

- [ ] **Step 4 — Test: `TestOpenCodeHTTPAgent_CancelWaitsForMessageGoroutine`:**

Use `httptest.NewServer` that hangs the message POST until the test signals via a channel. Assert:
- `Cancel()` returns within 6s (5s timeout + a small slack).
- The hang signal eventually unblocks; verify no goroutine count diff via `runtime.NumGoroutine()` snapshots taken before Dispatch and after Cancel + a brief drain.

- [ ] **Step 5 — Run + commit:**

```bash
go test -race ./agent -count=1 -timeout 30s
git add agent/opencode_http_agent.go agent/opencode_http_agent_test.go
git commit -m "Plan 123 Phase 1: B5 fix — opencode message goroutine tracked via WaitGroup"
```

---

## Phase 2 — B6: implement `advance`

**Files:** `cli/advance.go`, `cli/advance_test.go`

- [ ] **Step 1 — Replace the stub body of `runAdvance`:**

```go
func runAdvance(cmd *cobra.Command, args []string) error {
    _ = args
    dbPath := advanceDBPath
    if dbPath == "" {
        dbPath = filepath.Join(".coworker", "state.db")
    }
    if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
        return fmt.Errorf("create db directory %q: %w", filepath.Dir(dbPath), err)
    }
    db, err := store.Open(dbPath)
    if err != nil {
        return fmt.Errorf("open database: %w", err)
    }
    defer db.Close()

    eventStore := store.NewEventStore(db)
    sm := &session.Manager{
        RunStore: store.NewRunStore(db, eventStore),
        LockPath: sessionLockPath(dbPath),
    }
    sess, err := sm.CurrentSession()
    if err != nil {
        return fmt.Errorf("read session: %w", err)
    }
    if sess == nil {
        return fmt.Errorf("no active session — start one with `coworker session`")
    }

    as := store.NewAttentionStore(db)
    cs := store.NewCheckpointStore(db, eventStore)

    item, err := as.GetUnansweredCheckpointForRun(cmd.Context(), sess.RunID, "")
    if err != nil {
        return fmt.Errorf("find unanswered checkpoint: %w", err)
    }
    if item == nil {
        fmt.Fprintln(cmd.OutOrStdout(), "no checkpoint waiting on the active run")
        return nil
    }

    out, err := mcpserver.CallCheckpointAdvance(cmd.Context(), as, item.ID, advanceAnsweredBy, cs)
    if err != nil {
        return fmt.Errorf("advance checkpoint %s: %w", item.ID, err)
    }
    fmt.Fprintf(cmd.OutOrStdout(), "advanced checkpoint %s (status=%v)\n", item.ID, out["status"])
    return nil
}
```

- [ ] **Step 2 — Add the `--answered-by` flag:**

```go
var advanceAnsweredBy string
// In init():
advanceCmd.Flags().StringVar(&advanceAnsweredBy, "answered-by", "cli", "Identity recorded as the checkpoint answerer")
```

- [ ] **Step 3 — Add `mcpserver` import:**

```go
mcpserver "github.com/chris/coworker/mcp"
```

- [ ] **Step 4 — Tests in `cli/advance_test.go`:**

```go
package cli

import (
    "bytes"
    "context"
    "testing"
    "time"

    "github.com/chris/coworker/core"
    "github.com/chris/coworker/store"
)

// TestAdvance_NoSession returns an error when no session is active.
// TestAdvance_NoCheckpoint prints "no checkpoint waiting" cleanly.
// TestAdvance_ResolvesCheckpoint advances an attention+checkpoint pair and
//   verifies both are resolved.
// TestAdvance_AnsweredByFlag verifies the flag controls the recorded answerer.
```

(Implementation: use the existing `openTestDB` helper, manually insert a checkpoint attention + checkpoint row, verify via `GetAttentionByID` and `CheckpointStore.GetCheckpoint` after advance.)

- [ ] **Step 5 — Commit:**

```bash
go test -race ./cli -count=1 -timeout 60s -run TestAdvance
git add cli/advance.go cli/advance_test.go
git commit -m "Plan 123 Phase 2: B6 — implement advance command"
```

---

## Phase 3 — B6: implement `rollback`

**Files:** `cli/rollback.go`, `cli/rollback_test.go`

- [ ] **Step 1 — Replace stub body of `runRollback`:**

```go
func runRollback(cmd *cobra.Command, args []string) error {
    checkpointID := args[0]
    dbPath := rollbackDBPath
    if dbPath == "" {
        dbPath = filepath.Join(".coworker", "state.db")
    }
    if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
        return fmt.Errorf("create db directory %q: %w", filepath.Dir(dbPath), err)
    }
    db, err := store.Open(dbPath)
    if err != nil {
        return fmt.Errorf("open database: %w", err)
    }
    defer db.Close()

    eventStore := store.NewEventStore(db)
    sm := &session.Manager{
        RunStore: store.NewRunStore(db, eventStore),
        LockPath: sessionLockPath(dbPath),
    }
    if _, err := sm.CurrentSession(); err != nil {
        return fmt.Errorf("read session: %w", err)
    }

    as := store.NewAttentionStore(db)
    cs := store.NewCheckpointStore(db, eventStore)

    out, err := mcpserver.CallCheckpointRollback(cmd.Context(), as, checkpointID, rollbackAnsweredBy, cs)
    if err != nil {
        return fmt.Errorf("rollback checkpoint %s: %w", checkpointID, err)
    }
    fmt.Fprintf(cmd.OutOrStdout(), "rolled back checkpoint %s (status=%v)\n", checkpointID, out["status"])
    return nil
}
```

- [ ] **Step 2 — Add `--answered-by` flag:**

```go
var rollbackAnsweredBy string
rollbackCmd.Flags().StringVar(&rollbackAnsweredBy, "answered-by", "cli", "Identity recorded as the checkpoint rejecter")
```

- [ ] **Step 3 — Tests in `cli/rollback_test.go`:**

```
- TestRollback_ResolvesCheckpoint — success path; both attention and checkpoint flip to resolved with decision="reject".
- TestRollback_UnknownID — bubble up not-found error.
- TestRollback_NotACheckpoint — reject when the attention item is kind=permission etc.
- TestRollback_NoSession — error when no active session.
```

- [ ] **Step 4 — Commit:**

```bash
go test -race ./cli -count=1 -timeout 60s -run TestRollback
git add cli/rollback.go cli/rollback_test.go
git commit -m "Plan 123 Phase 3: B6 — implement rollback command"
```

---

## Phase 4 — Decisions + verification

- [ ] **Step 1 — Append Decision 10 to `docs/architecture/decisions.md`.**

```markdown
## Decision 10: CLI Checkpoint Commands Reuse MCP Wrappers (Plan 123)

**Context:** The `advance` and `rollback` CLI commands shipped as stubs since their introduction. The MCP server already exposed `orch_checkpoint_advance` and `orch_checkpoint_rollback` with exported wrappers (`mcp.CallCheckpointAdvance`, `mcp.CallCheckpointRollback`).

**Decision:** The CLI commands directly invoke the MCP wrappers rather than re-implementing the AnswerAttention + ResolveAttention + ResolveCheckpoint flow. This keeps one source of truth for checkpoint resolution semantics: any future invariant change (e.g., a new event type for advance) propagates to both surfaces automatically.

**Decision:** `cli/advance` (no args) finds the most recent unanswered checkpoint for the active session's run and advances it. `cli/rollback <id>` is explicit. Both expose `--answered-by <user>` (default "cli") so audit trails distinguish CLI advances from HTTP / MCP advances.

**Status:** Introduced in Plan 123.
```

- [ ] **Step 2 — Append Decision 11 (B5 cancel timeout):**

```markdown
## Decision 11: OpenCode Cancel Best-Effort with 5s Goroutine Drain (Plan 123)

**Context:** Plan 118 launched the `sendMessage` POST in a fire-and-forget goroutine so `Dispatch` could return immediately. The audit (BLOCKER B5) flagged that under hung-network conditions the goroutine could leak indefinitely.

**Decision:** `openCodeJobHandle.Cancel()` waits on a `sync.WaitGroup` for the message goroutine with a 5-second timeout. On timeout we log a warning and return nil — a hung message POST cannot block Cancel forever. The 5s window is generous enough that healthy networks always drain in time, narrow enough that operators see leaks promptly.

**Decision:** This is best-effort cancellation. Operators with persistent leaks should investigate network configuration (DNS resolution, OpenCode server health) rather than tune the timeout.

**Status:** Introduced in Plan 123.
```

- [ ] **Step 3 — Full verification:**

```bash
go build ./...
go test -race ./... -count=1 -timeout 180s
golangci-lint run ./...
```

Expected: build clean, all tests PASS, 0 lint issues.

- [ ] **Step 4 — Commit + merge.**

---

## Self-Review Checklist

- [ ] `openCodeJobHandle.messageWG` is incremented before launching the message goroutine (no race with Done).
- [ ] `Cancel()` returns within 6s under all conditions (5s WaitGroup timeout + small slack).
- [ ] `advance` no-pending case prints a clear message; doesn't error.
- [ ] `rollback <id>` propagates not-found / not-a-checkpoint errors with the same messages as the MCP handler.
- [ ] Both commands honor `--answered-by`.
- [ ] Decisions 10 + 11 added.

---

## Code Review

(To be filled in after implementation by Codex review subagent.)

---

## Post-Execution Report

(To be filled in after implementation.)
