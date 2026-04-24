package core

import "time"

// DispatchState represents the lifecycle state of a pull-model dispatch.
type DispatchState string

const (
	DispatchStatePending   DispatchState = "pending"
	DispatchStateLeased    DispatchState = "leased"
	DispatchStateCompleted DispatchState = "completed"
	DispatchStateExpired   DispatchState = "expired"
)

// Dispatch is a unit of work queued for a persistent CLI worker to pull.
// Workers claim dispatches via orch_next_dispatch and report completion via
// orch_job_complete. The pull model avoids push-to-terminal fragility.
type Dispatch struct {
	ID           string
	RunID        string
	Role         string
	JobID        string
	Prompt       string
	Inputs       map[string]interface{}
	State        DispatchState
	WorkerHandle string
	LeasedAt     *time.Time
	CompletedAt  *time.Time
	CreatedAt    time.Time
}
