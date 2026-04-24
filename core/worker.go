package core

import "time"

// WorkerState represents the lifecycle state of a registered persistent worker.
type WorkerState string

const (
	WorkerStateLive    WorkerState = "live"
	WorkerStateStale   WorkerState = "stale"
	WorkerStateEvicted WorkerState = "evicted"
)

// Worker represents a registered persistent CLI worker session.
// Written to the workers table; updated on every heartbeat and eviction.
type Worker struct {
	Handle        string      // unique opaque ID returned by orch_register
	Role          string      // role name (matches roles/*.yaml)
	PID           int         // OS PID of the CLI process (0 if unknown)
	SessionID     string      // tmux session or equivalent; may be empty
	CLI           string      // "claude-code" | "codex" | "opencode"
	RegisteredAt  time.Time
	LastHeartbeat time.Time
	State         WorkerState // live | stale | evicted
}
