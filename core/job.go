package core

import "time"

// JobState represents the lifecycle state of a job.
type JobState string

const (
	JobStatePending    JobState = "pending"
	JobStateDispatched JobState = "dispatched"
	JobStateRunning    JobState = "running"
	JobStateComplete   JobState = "complete"
	JobStateFailed     JobState = "failed"
	JobStateCancelled  JobState = "cancelled"
)

// Job is one execution of one role. The atomic unit of retry, cost, and audit.
type Job struct {
	ID           string
	RunID        string
	Role         string
	State        JobState
	DispatchedBy string // "scheduler" | "user" | "supervisor-retry" | "self"
	CLI          string // "codex" | "claude-code" | "opencode"
	StartedAt    time.Time
	EndedAt      *time.Time
	PlanID       string
	PhaseIndex   int
	CostUSD      float64
}

// JobResult holds the output of a completed job.
type JobResult struct {
	Findings  []Finding
	Artifacts []Artifact
	ExitCode  int
	Stdout    string
	Stderr    string

	// Cost is populated when the agent's stream-json output contained a
	// recognized cost-bearing event (Claude `result`, Codex `turn.completed`).
	// Nil otherwise. The dispatcher persists this via core.CostWriter when
	// configured. Plan 121.
	Cost *CostSample
}
