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
)

// Event is a single entry in the append-only event log.
// The events table is the authoritative history of a run.
type Event struct {
	ID             string
	RunID          string
	Sequence       int
	Kind           EventKind
	SchemaVersion  int
	IdempotencyKey string
	CausationID    string
	CorrelationID  string
	Payload        string // JSON
	CreatedAt      time.Time
}

// EventWriter is the interface for writing events to the event log.
// Implemented by store.EventStore.
type EventWriter interface {
	// WriteEventThenRow writes the event first, then calls applyFn
	// within the same transaction to update projection tables.
	// This enforces the event-log-before-state invariant.
	WriteEventThenRow(ctx context.Context, event *Event, applyFn func(tx interface{}) error) error
}
