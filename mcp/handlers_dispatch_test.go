package mcp_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/core"
	mcpserver "github.com/chris/coworker/mcp"
	"github.com/chris/coworker/store"
)

// ---- stub agent helpers (mirrored from coding/dispatch_test.go) ----

type stubDispatchHandle struct {
	wait func(ctx context.Context) (*core.JobResult, error)
}

func (h stubDispatchHandle) Wait(ctx context.Context) (*core.JobResult, error) {
	return h.wait(ctx)
}

func (stubDispatchHandle) Cancel() error {
	return nil
}

type stubDispatchAgent struct {
	result *core.JobResult
	err    error
}

func (a *stubDispatchAgent) Dispatch(_ context.Context, _ *core.Job, _ string) (core.JobHandle, error) {
	if a.err != nil {
		return nil, a.err
	}
	result := a.result
	return stubDispatchHandle{
		wait: func(_ context.Context) (*core.JobResult, error) {
			return result, nil
		},
	}, nil
}

// findRepoRootFromMCP resolves the repo root from the mcp/ package directory.
func findRepoRootFromMCP(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// mcp/ is one level below the repo root.
	return filepath.Dir(wd)
}

// newDispatcher creates a Dispatcher with the given agent and an in-memory DB.
func newDispatcher(t *testing.T, a core.Agent, db *store.DB) *coding.Dispatcher {
	t.Helper()
	repoRoot := findRepoRootFromMCP(t)
	return &coding.Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     a,
		DB:        db,
	}
}

// ---- orch_role_invoke tests ----

func TestHandleRoleInvoke_NilDispatcher_ReturnsStub(t *testing.T) {
	// When no Dispatcher is configured, the stub handler should be active.
	s, err := mcpserver.NewServer(mcpserver.ServerConfig{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	// Verify the tool is still registered (stub path).
	tools := s.Tools()
	found := false
	for _, name := range tools {
		if name == "orch_role_invoke" {
			found = true
			break
		}
	}
	if !found {
		t.Error("orch_role_invoke tool should be registered even without a Dispatcher")
	}
}

func TestHandleRoleInvoke_MissingRole(t *testing.T) {
	db := openTestDB(t)
	a := &stubDispatchAgent{result: &core.JobResult{ExitCode: 0}}
	d := newDispatcher(t, a, db)

	_, err := mcpserver.CallRoleInvoke(context.Background(), d, "", map[string]string{})
	if err == nil {
		t.Fatal("expected error for empty role, got nil")
	}
}

func TestHandleRoleInvoke_NilInputs(t *testing.T) {
	db := openTestDB(t)
	a := &stubDispatchAgent{result: &core.JobResult{ExitCode: 0}}
	d := newDispatcher(t, a, db)

	_, err := mcpserver.CallRoleInvoke(context.Background(), d, "reviewer.arch", nil)
	if err == nil {
		t.Fatal("expected error for nil inputs, got nil")
	}
}

func TestHandleRoleInvoke_HappyPath_WithMockCodex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRootFromMCP(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex")
	if _, err := os.Stat(mockBin); err != nil {
		t.Fatalf("mock binary not found: %v", err)
	}

	db := openTestDB(t)
	d := &coding.Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     agent.NewCliAgent(mockBin),
		DB:        db,
	}

	out, err := mcpserver.CallRoleInvoke(context.Background(), d, "reviewer.arch", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("CallRoleInvoke: %v", err)
	}

	if out["run_id"] == "" {
		t.Error("run_id should not be empty")
	}
	if out["job_id"] == "" {
		t.Error("job_id should not be empty")
	}
	exitCode, ok := out["exit_code"].(float64)
	if !ok {
		t.Fatalf("exit_code wrong type: %T", out["exit_code"])
	}
	if exitCode != 0 {
		t.Errorf("exit_code = %v, want 0", exitCode)
	}

	findingsRaw, ok := out["findings"].([]interface{})
	if !ok {
		t.Fatalf("findings wrong type: %T", out["findings"])
	}
	if len(findingsRaw) != 2 {
		t.Errorf("findings count = %d, want 2", len(findingsRaw))
	}

	// Verify each finding has the expected fields.
	for i, fRaw := range findingsRaw {
		f, ok := fRaw.(map[string]interface{})
		if !ok {
			t.Fatalf("findings[%d] wrong type: %T", i, fRaw)
		}
		for _, field := range []string{"id", "path", "line", "severity", "body", "fingerprint"} {
			if f[field] == nil {
				t.Errorf("findings[%d].%s is nil", i, field)
			}
		}
	}
}

func TestHandleRoleInvoke_HappyPath_WithStubAgent(t *testing.T) {
	repoRoot := findRepoRootFromMCP(t)
	db := openTestDB(t)

	// Build a stub agent that returns two findings.
	a := &stubDispatchAgent{
		result: &core.JobResult{
			ExitCode: 0,
			Findings: []core.Finding{
				{
					ID:       "f1",
					Path:     "main.go",
					Line:     10,
					Severity: core.SeverityImportant,
					Body:     "unused variable",
				},
				{
					ID:       "f2",
					Path:     "core/job.go",
					Line:     42,
					Severity: core.SeverityMinor,
					Body:     "missing comment",
				},
			},
		},
	}

	d := &coding.Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     a,
		DB:        db,
	}

	out, err := mcpserver.CallRoleInvoke(context.Background(), d, "reviewer.arch", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("CallRoleInvoke: %v", err)
	}

	if out["run_id"] == "" {
		t.Error("run_id should not be empty")
	}
	if out["job_id"] == "" {
		t.Error("job_id should not be empty")
	}

	findingsRaw, ok := out["findings"].([]interface{})
	if !ok {
		t.Fatalf("findings wrong type: %T", out["findings"])
	}
	if len(findingsRaw) != 2 {
		t.Errorf("findings count = %d, want 2", len(findingsRaw))
	}

	// Spot-check first finding.
	f0 := findingsRaw[0].(map[string]interface{})
	if f0["path"] != "main.go" {
		t.Errorf("findings[0].path = %q, want %q", f0["path"], "main.go")
	}
	if f0["severity"] != "important" {
		t.Errorf("findings[0].severity = %q, want %q", f0["severity"], "important")
	}
}

func TestHandleRoleInvoke_EmptyInputMap_PassedThrough(t *testing.T) {
	repoRoot := findRepoRootFromMCP(t)
	db := openTestDB(t)

	a := &stubDispatchAgent{result: &core.JobResult{ExitCode: 0}}
	d := &coding.Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     a,
		DB:        db,
	}

	// reviewer.arch requires diff_path and spec_path; an empty inputs map
	// should produce an error from Orchestrate's validation.
	_, err := mcpserver.CallRoleInvoke(context.Background(), d, "reviewer.arch", map[string]string{})
	if err == nil {
		t.Fatal("expected error for missing required inputs, got nil")
	}
}

func TestHandleRoleInvoke_NonZeroExitCode(t *testing.T) {
	repoRoot := findRepoRootFromMCP(t)
	db := openTestDB(t)

	a := &stubDispatchAgent{result: &core.JobResult{ExitCode: 1}}
	d := &coding.Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     a,
		DB:        db,
	}

	out, err := mcpserver.CallRoleInvoke(context.Background(), d, "reviewer.arch", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("CallRoleInvoke: %v", err)
	}
	exitCode, ok := out["exit_code"].(float64)
	if !ok {
		t.Fatalf("exit_code wrong type: %T", out["exit_code"])
	}
	if exitCode != 1 {
		t.Errorf("exit_code = %v, want 1", exitCode)
	}
}

func TestHandleRoleInvoke_InvalidRole(t *testing.T) {
	repoRoot := findRepoRootFromMCP(t)
	db := openTestDB(t)

	a := &stubDispatchAgent{result: &core.JobResult{ExitCode: 0}}
	d := &coding.Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     a,
		DB:        db,
	}

	_, err := mcpserver.CallRoleInvoke(context.Background(), d, "nonexistent.role", map[string]string{})
	if err == nil {
		t.Fatal("expected error for nonexistent role, got nil")
	}
}

func TestHandleRoleInvoke_FindingsEmptyNotNull(t *testing.T) {
	repoRoot := findRepoRootFromMCP(t)
	db := openTestDB(t)

	// Agent returns no findings.
	a := &stubDispatchAgent{result: &core.JobResult{ExitCode: 0, Findings: nil}}
	d := &coding.Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     a,
		DB:        db,
	}

	out, err := mcpserver.CallRoleInvoke(context.Background(), d, "reviewer.arch", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("CallRoleInvoke: %v", err)
	}

	findingsRaw, ok := out["findings"]
	if !ok {
		t.Fatal("findings field missing from output")
	}
	// Must not be nil — should be an empty JSON array.
	findings, ok := findingsRaw.([]interface{})
	if !ok {
		t.Fatalf("findings wrong type: %T", findingsRaw)
	}
	if len(findings) != 0 {
		t.Errorf("findings count = %d, want 0", len(findings))
	}
}

func TestNewServer_WithDispatcher_RegistersRealHandler(t *testing.T) {
	db := openTestDB(t)
	repoRoot := findRepoRootFromMCP(t)

	a := &stubDispatchAgent{result: &core.JobResult{ExitCode: 0}}
	d := &coding.Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     a,
		DB:        db,
	}

	s, err := mcpserver.NewServer(mcpserver.ServerConfig{
		DB:         db,
		Dispatcher: d,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	// All tools (including orch_role_invoke) must still be present.
	tools := s.Tools()
	if len(tools) == 0 {
		t.Fatal("expected at least one tool registered")
	}
	found := false
	for _, name := range tools {
		if name == "orch_role_invoke" {
			found = true
			break
		}
	}
	if !found {
		t.Error("orch_role_invoke not in tools list")
	}
}
