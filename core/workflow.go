package core

// Workflow is a strategy interface for running role orchestration.
type Workflow interface {
	// Name returns the workflow name.
	Name() string
}

// DispatchInput contains the minimal input for role dispatch from a workflow.
type DispatchInput struct {
	// Role is the role name to execute.
	Role string
	// Inputs are role template inputs.
	Inputs map[string]string
}
