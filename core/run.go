package core

import "time"

// RunState represents the lifecycle state of a run.
type RunState string

const (
	RunStateActive    RunState = "active"
	RunStateCompleted RunState = "completed"
	RunStateFailed    RunState = "failed"
	RunStateAborted   RunState = "aborted"
)

// Run is a correlated tree of jobs sharing a run-id, a context store,
// and a workflow. A PRD-to-PRs autopilot is one run; an interactive
// session is also one run.
type Run struct {
	ID        string
	Mode      string // "autopilot" | "interactive"
	State     RunState
	StartedAt time.Time
	EndedAt   *time.Time
	PRDPath   string
	SpecPath  string
	CostUSD   float64
	BudgetUSD *float64 // nil = no budget
}
