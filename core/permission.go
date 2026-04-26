package core

import (
	"fmt"
	"strings"
)

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

// PermissionKind is a permission category string used in role YAML.
type PermissionKind string

const (
	PermKindRead    PermissionKind = "read"
	PermKindWrite   PermissionKind = "write"
	PermKindEdit    PermissionKind = "edit"
	PermKindNetwork PermissionKind = "network"
	PermKindBash    PermissionKind = "bash" // bash:<command>
	PermKindMCP     PermissionKind = "mcp"  // mcp:<tool_name>
	PermKindGrep    PermissionKind = "grep"
	PermKindGlob    PermissionKind = "glob"
)

// validPermissionKinds is the set of recognised kind strings.
var validPermissionKinds = map[PermissionKind]bool{
	PermKindRead:    true,
	PermKindWrite:   true,
	PermKindEdit:    true,
	PermKindNetwork: true,
	PermKindBash:    true,
	PermKindMCP:     true,
	PermKindGrep:    true,
	PermKindGlob:    true,
}

// kindsWithSubject lists kinds that require a non-empty subject (the part after the colon).
var kindsWithSubject = map[PermissionKind]bool{
	PermKindBash: true,
	PermKindMCP:  true,
}

// Permission represents a parsed permission entry from a role YAML.
// Examples: "read" → {Kind:"read"}, "bash:git" → {Kind:"bash", Subject:"git"}.
type Permission struct {
	Kind    PermissionKind
	Subject string // the part after the colon, e.g. "git" for "bash:git"; empty for simple kinds
	Raw     string // original string, used for display in error messages
}

// ParsePermission parses a single permission string from role YAML.
// Simple kinds ("read", "write", "network") have no subject.
// Compound kinds ("bash:git", "mcp:coworker_run") require a non-empty subject.
// Returns an error for unrecognised kinds or malformed compound strings.
func ParsePermission(s string) (Permission, error) {
	if s == "" {
		return Permission{}, fmt.Errorf("empty permission string")
	}

	// Split on the first colon only.
	idx := strings.IndexByte(s, ':')

	var kindStr, subject string
	if idx < 0 {
		// Simple kind: no colon.
		kindStr = s
	} else {
		kindStr = s[:idx]
		subject = s[idx+1:]
	}

	kind := PermissionKind(kindStr)
	if !validPermissionKinds[kind] {
		return Permission{}, fmt.Errorf("unknown permission kind %q in %q", kindStr, s)
	}

	needsSubject := kindsWithSubject[kind]
	if needsSubject && subject == "" {
		return Permission{}, fmt.Errorf("permission %q requires a subject (e.g. %s:git)", kind, kind)
	}
	if !needsSubject && subject != "" {
		return Permission{}, fmt.Errorf("permission kind %q does not accept a subject (got %q)", kind, s)
	}

	// Reject extra colons in the subject (e.g., "bash:cmd:extra").
	if strings.Contains(subject, ":") {
		return Permission{}, fmt.Errorf("permission subject must not contain additional colons: %q", s)
	}

	return Permission{
		Kind:    kind,
		Subject: subject,
		Raw:     s,
	}, nil
}

// ParsePermissions parses a slice of permission strings and returns all of
// them as a slice. Errors from individual entries are joined into a single
// error returned after processing all inputs.
func ParsePermissions(ss []string) ([]Permission, error) {
	out := make([]Permission, 0, len(ss))
	var errs []string
	for _, s := range ss {
		p, err := ParsePermission(s)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		out = append(out, p)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("permission parse errors: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

// MatchPermission reports whether action matches the given perm from a role's
// permission list.
//
// Matching rules:
//   - Kind must match exactly (case-sensitive).
//   - For kinds with a subject, subject "*" in perm matches any action subject.
//   - Otherwise, subject comparison is case-insensitive exact match.
func MatchPermission(perm, action Permission) bool {
	if perm.Kind != action.Kind {
		return false
	}
	// Wildcard: perm with subject "*" matches any action subject.
	if perm.Subject == "*" {
		return true
	}
	return strings.EqualFold(perm.Subject, action.Subject)
}

// matchesAny returns true if action matches any permission in the list.
// Entries that fail to parse are silently skipped (best-effort; callers
// should validate at role load time).
func matchesAny(action Permission, entries []string) bool {
	for _, s := range entries {
		p, err := ParsePermission(s)
		if err != nil {
			continue
		}
		if MatchPermission(p, action) {
			return true
		}
	}
	return false
}

// EvaluateAction evaluates a single action against the permission lists of a
// role.  Priority order (first match wins):
//
//  1. action ∈ never       → HardDeny
//  2. action ∈ requires_human → RequiresHuman
//  3. action ∈ allowed_tools  → Allow
//  4. otherwise            → Undeclared
func EvaluateAction(perms RolePermissions, action Permission) PermissionDecision {
	if matchesAny(action, perms.Never) {
		return PermDecisionHardDeny
	}
	if matchesAny(action, perms.RequiresHuman) {
		return PermDecisionRequiresHuman
	}
	if matchesAny(action, perms.AllowedTools) {
		return PermDecisionAllow
	}
	return PermDecisionUndeclared
}
