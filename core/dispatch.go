package core

import "time"

// DispatchState represents the lifecycle state of a pull-model dispatch.
type DispatchState string

const (
	DispatchStatePending   DispatchState = "pending"
	DispatchStateLeased    DispatchState = "leased"
	DispatchStateCompleted DispatchState = "completed"
	// Note: expired leases are reset to pending by ExpireLeases, not to a
	// separate "expired" state. There is no DispatchStateExpired constant.
)

// Dispatch mode constants. See spec §Data Model line 770-772. Plan 125.
const (
	DispatchModePersistent string = "persistent"
	DispatchModeEphemeral  string = "ephemeral"
	DispatchModeInProcess  string = "in-process"
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

	// Mode declares how this dispatch is to be executed.
	//   "persistent" — pulled by a long-lived CLI worker via MCP.
	//   "ephemeral"  — spawned synchronously as a subprocess.
	//   "in-process" — handled by an in-process agent (rare; e.g., supervisor).
	// Default at the store layer is "persistent" when the field is empty.
	// Plan 125.
	Mode string
}
