package core

import (
	"testing"
)

// ---- ParsePermission --------------------------------------------------------

func TestParsePermission_SimpleKinds(t *testing.T) {
	cases := []struct {
		input    string
		wantKind PermissionKind
	}{
		{"read", PermKindRead},
		{"write", PermKindWrite},
		{"edit", PermKindEdit},
		{"network", PermKindNetwork},
		{"grep", PermKindGrep},
		{"glob", PermKindGlob},
	}
	for _, tc := range cases {
		p, err := ParsePermission(tc.input)
		if err != nil {
			t.Errorf("ParsePermission(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if p.Kind != tc.wantKind {
			t.Errorf("ParsePermission(%q).Kind = %q, want %q", tc.input, p.Kind, tc.wantKind)
		}
		if p.Subject != "" {
			t.Errorf("ParsePermission(%q).Subject = %q, want empty", tc.input, p.Subject)
		}
		if p.Raw != tc.input {
			t.Errorf("ParsePermission(%q).Raw = %q, want %q", tc.input, p.Raw, tc.input)
		}
	}
}

func TestParsePermission_BashCmd(t *testing.T) {
	p, err := ParsePermission("bash:git")
	if err != nil {
		t.Fatalf("ParsePermission(\"bash:git\") error: %v", err)
	}
	if p.Kind != PermKindBash {
		t.Errorf("Kind = %q, want %q", p.Kind, PermKindBash)
	}
	if p.Subject != "git" {
		t.Errorf("Subject = %q, want %q", p.Subject, "git")
	}
	if p.Raw != "bash:git" {
		t.Errorf("Raw = %q, want %q", p.Raw, "bash:git")
	}
}

func TestParsePermission_BashWildcard(t *testing.T) {
	p, err := ParsePermission("bash:*")
	if err != nil {
		t.Fatalf("ParsePermission(\"bash:*\") error: %v", err)
	}
	if p.Kind != PermKindBash {
		t.Errorf("Kind = %q, want %q", p.Kind, PermKindBash)
	}
	if p.Subject != "*" {
		t.Errorf("Subject = %q, want \"*\"", p.Subject)
	}
}

func TestParsePermission_MCPTool(t *testing.T) {
	p, err := ParsePermission("mcp:orch_run")
	if err != nil {
		t.Fatalf("ParsePermission(\"mcp:orch_run\") error: %v", err)
	}
	if p.Kind != PermKindMCP {
		t.Errorf("Kind = %q, want %q", p.Kind, PermKindMCP)
	}
	if p.Subject != "orch_run" {
		t.Errorf("Subject = %q, want %q", p.Subject, "orch_run")
	}
}

func TestParsePermission_InvalidBashNoValue(t *testing.T) {
	_, err := ParsePermission("bash:")
	if err == nil {
		t.Error("expected error for 'bash:' (no subject), got nil")
	}
}

func TestParsePermission_InvalidBashExtraColon(t *testing.T) {
	_, err := ParsePermission("bash:cmd:extra")
	if err == nil {
		t.Error("expected error for 'bash:cmd:extra', got nil")
	}
}

func TestParsePermission_UnknownKind(t *testing.T) {
	_, err := ParsePermission("unknown:foo")
	if err == nil {
		t.Error("expected error for unknown kind, got nil")
	}
}

func TestParsePermission_SimpleKindWithSubject(t *testing.T) {
	// "read" is a simple kind; supplying a subject is invalid.
	_, err := ParsePermission("read:something")
	if err == nil {
		t.Error("expected error for 'read:something', got nil")
	}
}

func TestParsePermission_EmptyString(t *testing.T) {
	_, err := ParsePermission("")
	if err == nil {
		t.Error("expected error for empty string, got nil")
	}
}

// ---- ParsePermissions -------------------------------------------------------

func TestParsePermissions_AllValid(t *testing.T) {
	ss := []string{"read", "bash:git", "mcp:tool_name"}
	ps, err := ParsePermissions(ss)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ps) != 3 {
		t.Errorf("len = %d, want 3", len(ps))
	}
}

func TestParsePermissions_SomeInvalid(t *testing.T) {
	ss := []string{"read", "bash:", "mcp:tool"}
	_, err := ParsePermissions(ss)
	if err == nil {
		t.Error("expected error for invalid entry, got nil")
	}
}

// ---- MatchPermission --------------------------------------------------------

func TestMatchPermission_ExactMatch(t *testing.T) {
	perm := Permission{Kind: PermKindBash, Subject: "git"}
	action := Permission{Kind: PermKindBash, Subject: "git"}
	if !MatchPermission(perm, action) {
		t.Error("expected match for exact bash:git vs bash:git")
	}
}

func TestMatchPermission_KindMismatch(t *testing.T) {
	perm := Permission{Kind: PermKindRead}
	action := Permission{Kind: PermKindWrite}
	if MatchPermission(perm, action) {
		t.Error("expected no match for read vs write")
	}
}

func TestMatchPermission_WildcardSubject(t *testing.T) {
	perm := Permission{Kind: PermKindBash, Subject: "*"}
	action := Permission{Kind: PermKindBash, Subject: "git"}
	if !MatchPermission(perm, action) {
		t.Error("expected wildcard match for bash:* vs bash:git")
	}
}

func TestMatchPermission_ValueMismatch(t *testing.T) {
	perm := Permission{Kind: PermKindBash, Subject: "git"}
	action := Permission{Kind: PermKindBash, Subject: "curl"}
	if MatchPermission(perm, action) {
		t.Error("expected no match for bash:git vs bash:curl")
	}
}

func TestMatchPermission_CaseInsensitiveSubject(t *testing.T) {
	perm := Permission{Kind: PermKindBash, Subject: "GIT"}
	action := Permission{Kind: PermKindBash, Subject: "git"}
	if !MatchPermission(perm, action) {
		t.Error("expected case-insensitive match for bash:GIT vs bash:git")
	}
}

func TestMatchPermission_SimpleKindsNoSubject(t *testing.T) {
	perm := Permission{Kind: PermKindRead}
	action := Permission{Kind: PermKindRead}
	if !MatchPermission(perm, action) {
		t.Error("expected match for read vs read")
	}
}

// ---- EvaluateAction ---------------------------------------------------------

func TestEvaluateAction_Allow(t *testing.T) {
	perms := RolePermissions{
		AllowedTools: []string{"bash:codex", "read"},
	}
	action, _ := ParsePermission("bash:codex")
	got := EvaluateAction(perms, action)
	if got != PermDecisionAllow {
		t.Errorf("EvaluateAction = %d, want PermDecisionAllow (%d)", got, PermDecisionAllow)
	}
}

func TestEvaluateAction_HardDeny(t *testing.T) {
	perms := RolePermissions{
		Never: []string{"bash:rm"},
	}
	action, _ := ParsePermission("bash:rm")
	got := EvaluateAction(perms, action)
	if got != PermDecisionHardDeny {
		t.Errorf("EvaluateAction = %d, want PermDecisionHardDeny (%d)", got, PermDecisionHardDeny)
	}
}

func TestEvaluateAction_RequiresHuman(t *testing.T) {
	perms := RolePermissions{
		RequiresHuman: []string{"bash:sudo"},
	}
	action, _ := ParsePermission("bash:sudo")
	got := EvaluateAction(perms, action)
	if got != PermDecisionRequiresHuman {
		t.Errorf("EvaluateAction = %d, want PermDecisionRequiresHuman (%d)", got, PermDecisionRequiresHuman)
	}
}

func TestEvaluateAction_Undeclared(t *testing.T) {
	perms := RolePermissions{
		AllowedTools: []string{"read"},
		Never:        []string{"bash:rm"},
	}
	action, _ := ParsePermission("bash:codex")
	got := EvaluateAction(perms, action)
	if got != PermDecisionUndeclared {
		t.Errorf("EvaluateAction = %d, want PermDecisionUndeclared (%d)", got, PermDecisionUndeclared)
	}
}

// TestEvaluateAction_NeverWinsOverAllowed verifies the priority: never > allowed_tools.
func TestEvaluateAction_NeverWinsOverAllowed(t *testing.T) {
	perms := RolePermissions{
		AllowedTools: []string{"bash:codex"},
		Never:        []string{"bash:codex"}, // same action in both lists
	}
	action, _ := ParsePermission("bash:codex")
	got := EvaluateAction(perms, action)
	if got != PermDecisionHardDeny {
		t.Errorf("EvaluateAction = %d, want PermDecisionHardDeny (%d) — never should win", got, PermDecisionHardDeny)
	}
}

// TestEvaluateAction_NeverWinsOverRequiresHuman verifies: never > requires_human.
func TestEvaluateAction_NeverWinsOverRequiresHuman(t *testing.T) {
	perms := RolePermissions{
		Never:         []string{"bash:dangerous"},
		RequiresHuman: []string{"bash:dangerous"},
	}
	action, _ := ParsePermission("bash:dangerous")
	got := EvaluateAction(perms, action)
	if got != PermDecisionHardDeny {
		t.Errorf("EvaluateAction = %d, want PermDecisionHardDeny (%d)", got, PermDecisionHardDeny)
	}
}

// TestEvaluateAction_WildcardAllow verifies bash:* allows any bash command.
func TestEvaluateAction_WildcardAllow(t *testing.T) {
	perms := RolePermissions{
		AllowedTools: []string{"bash:*"},
	}
	action, _ := ParsePermission("bash:anything")
	got := EvaluateAction(perms, action)
	if got != PermDecisionAllow {
		t.Errorf("EvaluateAction = %d, want PermDecisionAllow (%d)", got, PermDecisionAllow)
	}
}

// TestEvaluateAction_EmptyPerms verifies empty permission lists → Undeclared.
func TestEvaluateAction_EmptyPerms(t *testing.T) {
	perms := RolePermissions{}
	action, _ := ParsePermission("bash:codex")
	got := EvaluateAction(perms, action)
	if got != PermDecisionUndeclared {
		t.Errorf("EvaluateAction = %d, want PermDecisionUndeclared (%d)", got, PermDecisionUndeclared)
	}
}

// TestEvaluateAction_InvalidEntrySkipped verifies that a malformed entry in a
// permission list is skipped rather than panicking.
func TestEvaluateAction_InvalidEntrySkipped(t *testing.T) {
	perms := RolePermissions{
		// "bash:" is invalid (no subject); it should be skipped, not panic.
		AllowedTools: []string{"bash:", "bash:codex"},
	}
	action, _ := ParsePermission("bash:codex")
	got := EvaluateAction(perms, action)
	if got != PermDecisionAllow {
		t.Errorf("EvaluateAction = %d, want PermDecisionAllow (%d) — invalid entries should be skipped", got, PermDecisionAllow)
	}
}
