package core

import "context"

// PlanWriter is implemented by stores that persist plan rows.
// It lets coding/ consumers depend on core abstractions instead of store/.
type PlanWriter interface {
	CreatePlan(ctx context.Context, p PlanRecord) error
	UpdatePlanState(ctx context.Context, planID, state string) error
	UpdatePlanBranchAndPR(ctx context.Context, planID, branch, prURL string) error
}

// PlanRecord is the input needed to record a plan row.
type PlanRecord struct {
	ID       string
	RunID    string
	Number   int
	Title    string
	BlocksOn []int
	Branch   string
	PRURL    string
	State    string
}

// CheckpointWriter is implemented by stores that persist checkpoint rows.
type CheckpointWriter interface {
	CreateCheckpoint(ctx context.Context, c CheckpointRecord) error
	ResolveCheckpoint(ctx context.Context, id, decision, decidedBy, notes string) error
}

// CheckpointRecord is the input needed to record a checkpoint row.
type CheckpointRecord struct {
	ID     string
	RunID  string
	PlanID string
	Kind   string
	Notes  string
}

// SupervisorWriter records supervisor rule results paired with a supervisor.verdict event.
type SupervisorWriter interface {
	RecordVerdict(ctx context.Context, runID, jobID string, result RuleResult) error
}

// CostWriter records token/cost samples paired with a cost.delta event.
type CostWriter interface {
	RecordCost(ctx context.Context, runID, jobID string, sample CostSample) error
}

// CostSample is one provider/model usage and cost sample.
type CostSample struct {
	Provider  string
	Model     string
	TokensIn  int
	TokensOut int
	USD       float64
}
