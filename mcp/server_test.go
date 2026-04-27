package mcp_test

import (
	"testing"

	mcpserver "github.com/chris/coworker/mcp"
)

// TestNewServer verifies that NewServer succeeds with a zero-value config
// (all fields nil — acceptable during early plan phases where real deps are
// not yet wired).
func TestNewServer(t *testing.T) {
	s, err := mcpserver.NewServer(mcpserver.ServerConfig{})
	if err != nil {
		t.Fatalf("NewServer returned unexpected error: %v", err)
	}
	if s == nil {
		t.Fatal("NewServer returned nil server")
	}
}

// TestServerToolsRegistered verifies that every orch.* tool required by the
// Plan 104/105/116 spec is present in the server's tool list.
func TestServerToolsRegistered(t *testing.T) {
	want := []string{
		"orch_run_status",
		"orch_run_inspect",
		"orch_role_invoke",
		"orch_next_dispatch",
		"orch_job_complete",
		"orch_ask_user",
		"orch_attention_list",
		"orch_attention_answer",
		"orch_findings_list",
		"orch_artifact_read",
		"orch_artifact_write",
		"orch_checkpoint_list",     // Plan 116
		"orch_checkpoint_advance",  // Plan 116
		"orch_checkpoint_rollback", // Plan 116
		"orch_register",            // Plan 105
		"orch_heartbeat",           // Plan 105
		"orch_deregister",          // Plan 105
	}

	s, err := mcpserver.NewServer(mcpserver.ServerConfig{})
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	got := s.Tools()

	// Build a set for O(1) lookups.
	gotSet := make(map[string]bool, len(got))
	for _, name := range got {
		gotSet[name] = true
	}

	for _, name := range want {
		if !gotSet[name] {
			t.Errorf("tool %q not registered", name)
		}
	}

	// Also check for unexpected extra tools (forward-compat guard).
	wantSet := make(map[string]bool, len(want))
	for _, name := range want {
		wantSet[name] = true
	}
	for _, name := range got {
		if !wantSet[name] {
			t.Errorf("unexpected tool registered: %q", name)
		}
	}
}
