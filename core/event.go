package core

import (
	"context"
	"time"
)

// EventKind identifies the type of event in the event log.
type EventKind string

const (
	EventRunCreated      EventKind = "run.created"
	EventRunCompleted    EventKind = "run.completed"
	EventJobCreated      EventKind = "job.created"
	EventJobLeased       EventKind = "job.leased"
	EventJobCompleted    EventKind = "job.completed"
	EventJobFailed       EventKind = "job.failed"
	EventFindingCreated  EventKind = "finding.created"
	EventArtifactCreated EventKind = "artifact.created"

	// Dispatch lifecycle events — emitted by the MCP pull-dispatch protocol.
	EventDispatchQueued    EventKind = "dispatch.queued"
	EventDispatchLeased    EventKind = "dispatch.leased"
	EventDispatchCompleted EventKind = "dispatch.completed"
	EventDispatchExpired   EventKind = "dispatch.expired"

	// Worker registry events — emitted when persistent CLI workers connect
	// and disconnect via the MCP server.
	EventWorkerRegistered   EventKind = "worker.registered"
	EventWorkerHeartbeat    EventKind = "worker.heartbeat"
	EventWorkerDeregistered EventKind = "worker.deregistered"

	// Attention events — emitted when human-input items are created/resolved.
	EventAttentionCreated  EventKind = "attention.created"
	EventAttentionResolved EventKind = "attention.resolved"

	// Cost events — emitted by the scheduler when token usage is recorded.
	EventCostDelta EventKind = "cost.delta"

	// Phase lifecycle events — emitted by the phase loop executor.
	EventPhaseStarted   EventKind = "phase.started"
	EventPhaseCompleted EventKind = "phase.completed"
	EventPhaseFailed    EventKind = "phase.failed"
	// EventPhaseClean is a checkpoint event emitted when the fix-loop
	// exhausts max_fix_cycles_per_phase without converging to zero findings.
	EventPhaseClean EventKind = "phase.clean"

	// Plan shipping events — emitted by the shipper after PR creation.
	EventPlanShipped EventKind = "plan.shipped"
)

// Event is a single entry in the append-only event log.
// The events table is the authoritative history of a run.
type Event struct {
	ID             string    `json:"id"`
	RunID          string    `json:"run_id"`
	Sequence       int       `json:"sequence"`
	Kind           EventKind `json:"kind"`
	SchemaVersion  int       `json:"schema_version"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	CausationID    string    `json:"causation_id,omitempty"`
	CorrelationID  string    `json:"correlation_id,omitempty"`
	Payload        string    `json:"payload"`
	CreatedAt      time.Time `json:"created_at"`
}

// EventWriter is the interface for writing events to the event log.
// Implemented by store.EventStore.
//
// applyFn receives the live *sql.Tx as an any to avoid importing database/sql
// from the core package. Callers that need the concrete type should use
// store.EventStore directly; this interface exists only for dependency
// injection in tests and cross-package wiring.
type EventWriter interface {
	// WriteEventThenRow writes the event first, then calls applyFn
	// within the same transaction to update projection tables.
	// This enforces the event-log-before-state invariant.
	WriteEventThenRow(ctx context.Context, event *Event, applyFn func(tx any) error) error
}
