package core

// PermissionDecision is the result of evaluating a requested action against a
// role's declared permissions.
type PermissionDecision int

const (
	// PermDecisionAllow means the action is explicitly permitted by the role's
	// allowed_tools list.
	PermDecisionAllow PermissionDecision = iota

	// PermDecisionHardDeny means the action is explicitly forbidden by the
	// role's never list. The job must not start.
	PermDecisionHardDeny

	// PermDecisionRequiresHuman means the action matches the role's
	// requires_human list. An attention.permission item is created and the job
	// is blocked until a human approves.
	PermDecisionRequiresHuman

	// PermDecisionUndeclared means the action is not mentioned in any
	// permission list. The runtime enforces default-deny unless the operator
	// has opted into on_undeclared=warn via policy.
	PermDecisionUndeclared
)
