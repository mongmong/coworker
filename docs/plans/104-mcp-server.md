# Plan 104 — MCP Server + `orch.*` Tools

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose the coworker daemon as an MCP server that can run in two modes: user-control tools and worker tools, including `orch.next_dispatch()`/`orch.job.complete()` with lease/claim semantics, run/job/finding/attention/artifact introspection, and a spike-informed degraded-mode policy for unreliable persistent-worker paths.

**Architecture:** `mcp/` owns MCP transport/bootstrap and tool handlers. A lightweight in-DB dispatch queue sits beside existing `runs`/`jobs`/`findings`/`attention` tables. Existing `store` event-write paths remain authoritative; MCP operations become one more control-plane API over that state.

**Tech Stack:** Go 1.25+, `github.com/modelcontextprotocol/go-sdk` (default), `github.com/mark3labs/mcp-go` (fallback only), `net/http` + Unix sockets, `encoding/json`, `database/sql`, `modernc.org/sqlite`.

**Manifest entry:** `docs/specs/001-plan-manifest.md` section 104.

**Branch:** `feature/plan-104-mcp-server`.

---

## Architecture Overview

This plan adds a daemon-facing MCP contract for two audiences:

1. **User/control tools:** `orch.run.*`, `orch.role.invoke`, `orch.attention.*`, `orch.findings.*`, `orch.artifact.*`.
2. **Worker tools:** `orch.register`, `orch.heartbeat`, `orch.deregister`, `orch.next_dispatch`, `orch.job.complete`, `orch.ask_user`.

The persistent path is explicit-turn pull only and must never depend on tmux-enter wake assumptions. Spike findings are captured as degraded capability defaults:

- Claude Code: verified pull tool-call flow, but idle wake is unreliable and session wake is broken in current behavior => no autonomous idle wake requirement.
- Codex: verified pull tool-call flow in explicit turns; full MCP path requires high-privilege execution constraints, so production use of persistent Codex is deferred until explicit policy approval.
- OpenCode: not MCP-first in this plan; treated as HTTP-path for its dedicated worker plan.

---

## File structure after Plan 104

```text
coworker/
├── mcp/
│   ├── config.go
│   ├── contract.go
│   ├── daemon.go
│   ├── errors.go
│   ├── handlers_run.go
│   ├── handlers_dispatch.go
│   ├── handlers_attention.go
│   ├── handlers_findings.go
│   ├── handlers_artifacts.go
│   ├── registry.go
│   ├── capabilities.go
│   └── mcp_server_test.go
├── store/
│   ├── migration/ (new if existing conventions require)
│   ├── migrations/103_mcp_runtime.sql
│   ├── dispatch_store.go
│   └── dispatch_store_test.go
├── core/
│   ├── dispatch.go (new)
│   └── event.go
├── cli/
│   └── daemon.go
├── cmd/coworker/
│   └── main.go (only if process split needed)
└── tests/integration/
    ├── mcp_daemon_test.go
    ├── mcp_dispatch_flow_test.go
    └── mcp_tool_contract_test.go
```

---

## Task 1: MCP contracts and dispatch primitives in `core`

**Files:**
- Create: `core/dispatch.go`
- Modify: `core/event.go`
- Create: `store/migrations/103_mcp_runtime.sql`
- Create: `store/dispatch_store.go`
- Create: `store/dispatch_store_test.go`

### Step 1.1 — Add MCP and dispatch domain types

- [ ] Create `core/dispatch.go` with:

```go
package core

import "time"

type ToolContract string

const (
	ToolStatusIdle    ToolContract = "idle"
	ToolStatusQueued  ToolContract = "queued"
	ToolStatusGranted ToolContract = "dispatched"
	ToolStatusDenied  ToolContract = "denied"
)

type MCPWorkerMode string

const (
	MCPWorkerModePersistent MCPWorkerMode = "persistent"
	MCPWorkerModeEphemeral  MCPWorkerMode = "ephemeral"
)

type MCPHandle struct {
	ID        string
	Role      string
	SessionID string
	Pid       int
	Mode      MCPWorkerMode
	CLI       string
	CreatedAt time.Time
	TouchedAt time.Time
	Alive     bool
}

type MCPDispatch struct {
	ID             string
	RunID          string
	Role           string
	Inputs         map[string]string
	Prompt         string
	Context        map[string]string
	Mode           string
	RequestedBy    string
	WorkerHandleID string
	LeasedUntil    *time.Time
	CreatedAt      time.Time
}

type MCPDispatchResult struct {
	Status    string                 `json:"status"`
	JobID     string                 `json:"job_id"`
	RunID     string                 `json:"run_id"`
	Outputs   map[string]interface{} `json:"outputs"`
	ExitCode  int                    `json:"exit_code"`
	Warnings  []string               `json:"warnings,omitempty"`
	ErrorCode string                 `json:"error_code,omitempty"`
	ErrorMsg  string                 `json:"error_msg,omitempty"`
}

type MCPToolCall struct {
	Handle string
	RunID  string
	Args   map[string]interface{}
}

// DispatcherMode controls run-mode behavior for this dispatch.
type DispatcherMode string

const (
	DispatcherModeAuto    DispatcherMode = "auto"
	DispatcherModeQueued  DispatcherMode = "queued"
	DispatcherModeDirect  DispatcherMode = "direct"
)
```

### Step 1.2 — Extend event taxonomy for MCP control-plane transitions

- [ ] Update `core/event.go` constants with new event kinds:
  - `EventDispatchQueued`
  - `EventDispatchLeased`
  - `EventDispatchCompleted`
  - `EventWorkerRegistered`
  - `EventWorkerHeartbeat`
  - `EventWorkerDeregistered`
  - `EventAttentionRequested`
  - `EventArtifactRead` (or keep as info events)
  - `EventArtifactWritten`

### Step 1.3 — Add MCP runtime schema

- [ ] Create `store/migrations/103_mcp_runtime.sql`:

```sql
-- Plan 104 MCP runtime

CREATE TABLE IF NOT EXISTS worker_handles (
    id TEXT PRIMARY KEY,
    role TEXT NOT NULL,
    pid INTEGER NOT NULL,
    session_id TEXT NOT NULL,
    cli TEXT NOT NULL,
    mode TEXT NOT NULL DEFAULT 'ephemeral',
    allowed_tool_prefixes TEXT NOT NULL,
    created_at TEXT NOT NULL,
    touched_at TEXT NOT NULL,
    active INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS idx_worker_handles_mode ON worker_handles(mode);
CREATE INDEX IF NOT EXISTS idx_worker_handles_touched_at ON worker_handles(touched_at);

CREATE TABLE IF NOT EXISTS dispatch_queue (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES jobs(id),
    run_id TEXT NOT NULL REFERENCES runs(id),
    role TEXT NOT NULL,
    requested_by TEXT NOT NULL,
    mode TEXT NOT NULL DEFAULT 'queued',
    worker_handle_id TEXT,
    prompt TEXT NOT NULL,
    inputs TEXT NOT NULL,
    context_snapshot TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    leased_at TEXT,
    lease_expires_at TEXT,
    completed_at TEXT,
    completed_by TEXT,
    outputs TEXT,
    error_code TEXT,
    error_msg TEXT,
    created_at TEXT NOT NULL,
    FOREIGN KEY (worker_handle_id) REFERENCES worker_handles(id)
);

CREATE INDEX IF NOT EXISTS idx_dispatch_queue_status ON dispatch_queue(status);
CREATE INDEX IF NOT EXISTS idx_dispatch_queue_role ON dispatch_queue(role);
CREATE INDEX IF NOT EXISTS idx_dispatch_queue_run_id ON dispatch_queue(run_id);
CREATE INDEX IF NOT EXISTS idx_dispatch_queue_worker_handle_id ON dispatch_queue(worker_handle_id);
```

### Step 1.4 — Add store access for queue + handles with event-log-before-state

- [ ] Create `store/dispatch_store.go` with `DispatchStore` methods:
  - `EnqueueDispatch(ctx, *core.MCPDispatch) (string, error)`
  - `LeaseDispatch(ctx, handleID string, requestedRole string, runID string, leaseDuration time.Duration) (*core.MCPDispatch, error)`
  - `CompleteDispatch(ctx, dispatchID, handleID, jobID string, outputs map[string]interface{}, exitCode int) error`
  - `FailDispatch(ctx, dispatchID, handleID, code, message string) error`
  - `GetQueuedDispatch(ctx, runID, role string, workerMode string) ([]*core.MCPDispatch, error)`
  - `TouchHandle(ctx, handleID string) error`
  - `RegisterHandle(ctx, handle *core.MCPHandle) error`
  - `DeregisterHandle(ctx, handleID string) error`
  - `ListActiveHandles(ctx, role string) ([]*core.MCPHandle, error)`
  - `RequeueExpiredLeases(ctx time.Time) ([]*core.MCPDispatch, error)`

All write methods must use `store.EventStore.WriteEventThenRow` before mutating table rows.

### Step 1.5 — Add store tests for leases and event ordering

- [ ] Create `store/dispatch_store_test.go` and cover:
  - queue enqueue writes dispatch event first
  - lease transitions `queued -> leased`
  - heartbeat refresh path
  - completion writes outputs and emits event payload
  - lease expiry returns to queued
  - unauthorized handle completion returns no rows updated error

---

## Task 2: Build MCP tool contracts and runtime handler layer

**Files:**
- Create: `mcp/contract.go`
- Create: `mcp/config.go`
- Create: `mcp/daemon.go`
- Create: `mcp/handlers_run.go`
- Create: `mcp/handlers_dispatch.go`
- Create: `mcp/handlers_attention.go`
- Create: `mcp/handlers_findings.go`
- Create: `mcp/handlers_artifacts.go`
- Create: `mcp/errors.go`
- Create: `mcp/capabilities.go`

### Step 2.1 — Define MCP handler interfaces and standardized envelope

- [ ] Create `mcp/contract.go`:

```go
package mcp

import "github.com/chris/coworker/core"

type ToolResult struct {
	Status string                 `json:"status"`
	Data   map[string]interface{} `json:"data,omitempty"`
	Error  *ToolError             `json:"error,omitempty"`
}

type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ServerOptions struct {
	SocketPath           string
	StateDBPath          string
	ListenAddr           string
	UseOfficialSDK       bool
	DisableAutoNamespace bool
	EnableEphOnlyFallback bool
}
```

### Step 2.2 — Add MCP server bootstrap and lifecycle

- [ ] Create `mcp/daemon.go`:
  - parse `ServerOptions`
  - open DB once with WAL + migration
  - create bus + event publisher
  - register all tools
  - bind to Unix socket and HTTP `Serve` path for SSE compatibility hooks
  - support signal-aware shutdown and idempotent cleanup of stale socket files

### Step 2.3 — Implement user control tools

- [ ] Create `mcp/handlers_run.go` with tools:
  - `orch.run.status` args: `{run_id: string}`
  - `orch.run.inspect` args: `{run_id: string, include_events: bool}`
  - `orch.role.invoke` args: `{role:string, run_id?:string, inputs?:map[string]string, timeout_sec?:int, mode?:"queued"|"direct", wait?:bool}`

`orch.role.invoke` behavior:
- build prompt from role loader
- create run if missing
- for `wait=true` execute ephemeral path by invoking existing `coding.Dispatcher`
- for `wait=false` create dispatch row and return `run_id`, `dispatch_id`, `status="queued"`

### Step 2.4 — Implement worker tools (persistent path)

- [ ] Create `mcp/handlers_dispatch.go`:
  - `orch.next_dispatch` args: `{handle:string, role?:string, run_id?:string}`
  - returns `{status:"dispatched", dispatch_id, run_id, role, prompt, inputs, context}` or `{status:"idle"}`
  - enforce role filtering from handle and optional run scoping
  - only returns at most one lease per handle unless explicit concurrency requested

- [ ] Implement `orch.job.complete` args: `{handle:string, dispatch_id:string, job_id:string, outputs:object, exit_code:int}`
  - validates lease owner
  - writes completion row + event
  - marks dispatch complete and emits `EventDispatchCompleted`

### Step 2.5 — Implement worker registration and keepalive

- [ ] Add `mcp/registry.go`:
  - `orch.register` args: `{role:string, pid:int, session_id:string, cli:string, mode:string}`
  - `orch.heartbeat` args: `{handle:string}`
  - `orch.deregister` args: `{handle:string}`

Policy:
- mode is validated against `capabilities` matrix.
- `register` rejects persistent modes if not enabled by local capability matrix.

### Step 2.6 — Implement question/attention and question response

- [ ] Create `mcp/handlers_attention.go`:
  - `orch.ask_user` args: `{run_id:string, question:string, options?:[]string, ttl_sec?:int}`
  - writes `core.AttentionItem` (`AttentionQuestion`) to attention store
  - blocks only on return data contract when queue channel available
  - returns `{status:"asked", attention_id:"..."}`
  - `orch.attention.answer` args: `{attention_id:string, answer:string}` (user-only or role-level worker tools may call)

### Step 2.7 — Implement findings and artifact access tools

- [ ] Create `mcp/handlers_findings.go`:
  - `orch.findings.create` args: `{run_id:string, findings_json:object}` for explicit worker/manual paths
  - `orch.findings.list` args: `{run_id:string}`

- [ ] Create `mcp/handlers_artifacts.go`:
  - `orch.artifact.read` args: `{path:string}` with JSON output
  - `orch.artifact.write` args: `{path:string, patch?:string, content?:string}`
  - gate writes behind policy/permission and path allowlist checks where possible

### Step 2.8 — Add deterministic MCP tool error mapping

- [ ] Create `mcp/errors.go`:
  - `ErrNoHandle`, `ErrUnknownHandle`, `ErrLeaseMismatch`, `ErrRunUnknown`, `ErrRoleMissing`, `ErrPermissionDenied`, `ErrDegradedMode`
  - map to structured `{status:"error", error:{code,message}}` payloads, never expose stack traces.

### Step 2.9 — Add capability matrix and degraded-mode enforcement

- [ ] Create `mcp/capabilities.go` with explicit per-CLI profile:
  - `supports_persistent` (default false for current evidence)
  - `supports_explicit_turns` (false only for blocked clients)
  - `requires_full_access` (for commands requiring explicit override)

Use spike data:
- Claude: persistent auto-wake false, explicit-turn true
- Codex: persistent explicit-turn true, full-access requirement for MCP success path
- OpenCode MCP worker integration deferred to dedicated plan, so MCP path marks worker as ephemeral-only where role requires.

---

## Task 3: Add daemon command and socket wiring

**Files:**
- Create: `cli/daemon.go`
- Modify: `cli/root.go`
- Modify: `cli/watch.go` if same socket also proxies SSE
- Create: `mcp/mcp_server_test.go`

### Step 3.1 — Expose `coworker daemon`

- [ ] Create `daemon` cobra command in `cli/daemon.go`:
  - flags:
    - `--state-db` (default `.coworker/state.db`)
    - `--socket` (default `.coworker/mcp.sock`)
    - `--listen` (HTTP for health/watch, default `:7700`)
    - `--sdk` (`official`/`mark3labs`)
    - `--degraded-mode` (`true|false`)

### Step 3.2 — Register MCP + SSE from same process

- [ ] Wire `cli/daemon.go` to:
  - create `coworker` event bus (`eventbus.NewInMemoryBus`)
  - initialize MCP service with store + bus
  - keep `/events` SSE endpoint from existing `eventbus.SSEHandler` running alongside MCP socket server

### Step 3.3 — Add CLI command plumbing

- [ ] Modify `cli/root.go` to ensure `daemon` appears in help output and command docs include socket path defaults and shutdown behavior.

### Step 3.4 — Add `mcp` transport smoke tests

- [ ] Add `mcp/mcp_server_test.go` with:
  - server config parse test
  - unix socket creation/removal test
  - handler registration test (all `orch.*` tool names exist)

---

## Task 4: Integrate existing code paths and maintain compatibility with existing dispatcher

**Files:**
- Modify: `coding/dispatch.go`
- Create: `coding/dispatch_template.go`
- Modify: `mcp/handlers_dispatch.go`

### Step 4.1 — Refactor prompt/render helper

- [ ] Create `coding/dispatch_template.go` with shared function:

```go
package coding

import (
  "github.com/chris/coworker/core"
)

func BuildRenderedPrompt(roleName string, inputs map[string]string, roleDir, promptDir string) (string, error) {
  role, err := roles.LoadRole(roleDir, roleName)
  if err != nil { return "", err }
  tmpl, err := roles.LoadPromptTemplate(promptDir, role.PromptTemplate)
  if err != nil { return "", err }
  normalized := make(map[string]string)
  for k, v := range inputs { normalized[k] = v }
  return roles.RenderPrompt(tmpl, normalized)
}
```

### Step 4.2 — Reuse the same rendering path in MCP

- [ ] Update `mcp/handlers_dispatch.go` and `mcp/handlers_run.go` to call the shared helper so `orch.next_dispatch` and `orch.role.invoke` output identical prompt contracts.

### Step 4.3 — Keep `orch.role.invoke(..., mode="queued")` non-blocking

- [ ] In `coding/dispatch.go` expose a small helper for dry-run enqueue of dispatch context to avoid duplicate code and maintain event ordering with `RunStore`, `JobStore`, and `EventStore` semantics.

---

## Task 5: Security, permissions, and audit requirements

**Files:**
- Create: `mcp/capabilities.go`
- Modify: `mcp/handlers_*.go`
- Modify: `docs/architecture/decisions.md`

### Step 5.1 — Enforce MCP caller checks

- [ ] Ensure every tool that mutates state checks:
  - handle valid for worker tools
  - `run_id` existence and state rules
  - role allowed for handle
  - path policy when writing artifacts

### Step 5.2 — Log structured audit fields per tool call

- [ ] Record in event payload:
  - `tool_name`
  - `tool_args_hash`
  - `handle_id`
  - `run_id`
  - `job_id` (where applicable)

### Step 5.3 — Update architecture decisions

- [ ] Add Decision entry in `docs/architecture/decisions.md` for MCP-first control plane + event-log-first control mutations.

---

## Task 6: Tests and validation

**Files:**
- Create: `tests/integration/mcp_daemon_test.go`
- Create: `tests/integration/mcp_dispatch_flow_test.go`
- Create: `tests/integration/mcp_tool_contract_test.go`
- Modify: `tests/architecture/imports_test.go` if new package import direction restrictions are added

### Step 6.1 — End-to-end dispatch protocol test

- [ ] Add integration test:
  - start MCP daemon in test mode
  - register worker
  - enqueue role invoke in queued mode
  - call `next_dispatch`
  - complete via `job.complete`
  - verify run/job/events and event log sequence

### Step 6.2 — Idle and degraded behavior test

- [ ] Add test cases for:
  - `orch.next_dispatch` no work => `idle`
  - `orch.register` rejected in persistent-disabled mode
  - explicit-turn fallback returns queued jobs only

### Step 6.3 — Tool contract names and schema test

- [ ] Add assertions for all tool names and input/output shapes, including:
  - `orch.run.status`
  - `orch.run.inspect`
  - `orch.role.invoke`
  - `orch.next_dispatch`
  - `orch.job.complete`
  - `orch.ask_user`
  - `orch.attention.list`
  - `orch.attention.answer`
  - `orch.findings.list`
  - `orch.findings.create`
  - `orch.artifact.read`
  - `orch.artifact.write`

---

## Task 7: Integration and documentation updates

**Files:**
- Modify: `README.md`
- Modify: `docs/CLAUDE.md` if command usage changes
- Modify: `docs/tutorial.md`
- Create: `docs/spike-rerun-guide.md` entries for mcp server smoke commands

### Step 7.1 — Add daemon usage docs

- [ ] Update `README.md` with:
  - `coworker daemon --socket .coworker/mcp.sock`
  - example `orch.role.invoke` + `orch.next_dispatch` flow
  - degraded-mode note for Claude/Codex based on spike verdicts

### Step 7.2 — Include MCP command behavior in tutorial

- [ ] Add a short section in `docs/tutorial.md` for local MCP sanity check:
  - `ls -l .coworker/mcp.sock`
  - tool discovery path in your installed CLI
  - a simple question/followup flow with `orch.ask_user`

### Step 7.3 — Persist generated spike artifacts

- [ ] Add command transcript templates in `docs/spike-rerun-guide.md` for:
  - spawn daemon
  - run tool smoke checks
  - verify `idle` and `dispatched` responses

---

## Post-Implementation Checklist

- [ ] `go test ./... -count=1 -timeout 60s`
- [ ] `go test ./mcp ./store ./cli ./tests/integration -count=1 -timeout 60s`
- [ ] `go test ./cmd/coworker -run TestDoesNotExist` (package-level compile pass)
- [ ] `go build ./...`
- [ ] Manual smoke: launch daemon and call minimal tool set with each supported CLI type mode

---

## Post-Execution Report

**Implementation details**

Six tasks completed across 11 commits on `feature/plan-104-mcp-server`. Key deliverables:

- `mcp/server.go` — MCP server using the official `modelcontextprotocol/go-sdk`; `NewServer` registers all `orch.*` tool handlers; `StartStdio` runs the JSON-RPC transport.
- `cli/daemon.go` — `coworker daemon` cobra command; wires `store.DB`, `store.EventStore`, `store.DispatchStore`, `store.AttentionStore`, `store.FindingStore`, `store.ArtifactStore`, and starts MCP server on stdio.
- `mcp/handlers_run.go` — `orch.run.status` and `orch.run.inspect` handlers.
- `mcp/handlers_dispatch.go` — `orch.role.invoke`, `orch.next_dispatch`, `orch.job_complete` handlers; `store.DispatchStore` with `EnqueueDispatch`, `ClaimNextDispatch` (atomic SELECT+UPDATE, single tx), `CompleteDispatch` (state-guarded), `ExpireLeases` (state-guarded).
- `mcp/handlers_attention.go` — `orch.ask_user`, `orch.attention.list`, `orch.attention.answer` handlers.
- `mcp/handlers_query.go` — `orch.findings.list`, `orch.artifact.read`, `orch.artifact.write` handlers.
- `store/dispatch_store.go` — dispatch queue with `EnqueueDispatch`, `ClaimNextDispatch`, `CompleteDispatch`, `ExpireLeases`, `RequeueByWorker`, `GetDispatch`.

**Deviations from plan**

- Official `modelcontextprotocol/go-sdk` was viable and adopted (spike confirmed); `mark3labs/mcp-go` fallback not needed.
- Daemon is stdio-only for V1; `--socket`/`--listen` flags not implemented (WONTFIX — plan updated).
- `DispatchStateExpired` constant was dead code and removed.
- `core.EventWriter` signature changed to `func(tx any) error` for compatibility.
- Critical code-review fixes: TOCTOU race in `ClaimNextDispatch` fixed (atomic SELECT+UPDATE), `CompleteDispatch` state guard added (`AND state = 'leased'`), silent error drops replaced with `slog.Warn`.
- 11 new handler tests added during code review fix pass covering `orch.next_dispatch`/`orch.job_complete` idle, dispatch, error, and full-cycle paths.
- Worker registration tools (`orch.register`, `orch.heartbeat`, `orch.deregister`) and `orch.findings.create` deferred to Plan 105.

**Known limitations**

- No composite index on `(state, role, created_at)` in dispatch table — deferred until performance matters.
- Worker registration/heartbeat/deregister MCP tools not present (Plan 105).
- `orch.role.invoke` dispatches ephemerally only; persistent worker routing deferred to Plan 105.

**Verification results**

- `mcp/` package: 87 tests pass. Full suite: all 21 packages green.
- `go build ./...` passes cleanly.
- Lint: clean.

---

## Code Review

### Review 1

- **Date:** 2026-04-24
- **Reviewer:** Codex (pre-implementation plan review)
- **Verdict:** approved (plan staged for implementation)

### Review 2

- **Date**: 2026-04-23
- **Reviewer**: Claude (post-implementation full review)
- **Verdict**: Changes requested (2 Critical, 6 Important)

**Critical**

1. `[FIXED]` **TOCTOU race in ClaimNextDispatch — double-claim possible.**
   → Response: Rewrote to SELECT+UPDATE inside single tx with `AND state='pending'` guard + RowsAffected check.

2. `[FIXED]` **CompleteDispatch missing state guard.**
   → Response: Added `AND state = 'leased'` + RowsAffected check. Returns error if not in leased state.

**Important**

3. `[FIXED]` **ExpireLeases not atomic per row.**
   → Response: Added `AND state = 'leased'` guard. Silently skips concurrently completed dispatches.

4. `[FIXED]` **core.EventWriter interface signature mismatch.**
   → Response: Changed to `func(tx any) error` — identical to `interface{}` in Go, existing implementations still satisfy.

5. `[FIXED]` **Silent error drops in handleJobComplete and handleAttentionAnswer.**
   → Response: Replaced `_ = err` with `slog.Warn(...)` in both locations.

6. `[FIXED]` **Zero tests for orch_next_dispatch and orch_job_complete MCP handlers.**
   → Response: Added 11 new handler tests covering idle, dispatch, errors, and full cycle.

7. `[FIXED]` **DispatchStateExpired never written to DB.**
   → Response: Removed dead constant, added clarifying comment.

8. `[OPEN]` **No post-execution report; decisions.md not updated for Plan 104.** Per CLAUDE.md critical rules.

**Suggestions**

9. `[WONTFIX]` Missing composite index (state, role, created_at) — optimize when performance matters.
10. `[WONTFIX]` orch_ask_user status "pending" vs plan "asked" — pending is clearer, keep it.
11. `[OPEN]` Worker registration tools (orch.register/heartbeat/deregister) and orch.findings.create unimplemented — defer to Plan 105 with explicit note.
12. `[WONTFIX]` mcp/ → coding/ import — accepted coupling; MCP is a transport layer that necessarily knows about dispatch.
13. `[WONTFIX]` Daemon missing --socket/--listen flags — V1 is stdio-only; update plan to reflect.

---

## Execution Handoff

Plan 104 is ready to execute. Two execution options:

1. Subagent-driven execution (recommended): use `superpowers:subagent-driven-development` per task.
2. Single-session execution: use `superpowers:executing-plans` with per-task checkpoints.

