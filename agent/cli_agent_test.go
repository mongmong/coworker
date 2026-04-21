package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chris/coworker/core"
)

func findMockBinary(t *testing.T) string {
	t.Helper()
	// Find the repo root by looking for go.mod.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// agent/ is one level below the repo root.
	repoRoot := filepath.Dir(wd)
	mockPath := filepath.Join(repoRoot, "testdata", "mocks", "codex")
	if _, err := os.Stat(mockPath); err != nil {
		t.Fatalf("mock binary not found at %q: %v", mockPath, err)
	}
	return mockPath
}

func TestCliAgent_Dispatch_And_Wait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	mockBin := findMockBinary(t)
	agent := NewCliAgent(mockBin)

	job := &core.Job{
		ID:    "test-job-1",
		RunID: "test-run-1",
		Role:  "reviewer.arch",
		CLI:   "codex",
	}

	ctx := context.Background()
	handle, err := agent.Dispatch(ctx, job, "Review this code")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	result, err := handle.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}

	if len(result.Findings) != 2 {
		t.Fatalf("findings count = %d, want 2", len(result.Findings))
	}

	// Verify first finding.
	f1 := result.Findings[0]
	if f1.Path != "main.go" {
		t.Errorf("finding[0].path = %q, want %q", f1.Path, "main.go")
	}
	if f1.Line != 42 {
		t.Errorf("finding[0].line = %d, want 42", f1.Line)
	}
	if f1.Severity != core.SeverityImportant {
		t.Errorf("finding[0].severity = %q, want %q", f1.Severity, core.SeverityImportant)
	}
	if f1.Body != "Missing error check on Close()" {
		t.Errorf("finding[0].body = %q, want %q", f1.Body, "Missing error check on Close()")
	}

	// Verify second finding.
	f2 := result.Findings[1]
	if f2.Path != "store.go" {
		t.Errorf("finding[1].path = %q, want %q", f2.Path, "store.go")
	}
	if f2.Line != 17 {
		t.Errorf("finding[1].line = %d, want 17", f2.Line)
	}
	if f2.Severity != core.SeverityMinor {
		t.Errorf("finding[1].severity = %q, want %q", f2.Severity, core.SeverityMinor)
	}
}

func TestCliAgent_Dispatch_Cancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	// Use a long-running command that we can cancel.
	agent := NewCliAgent("sleep", "60")

	job := &core.Job{
		ID:    "test-job-cancel",
		RunID: "test-run-cancel",
		Role:  "reviewer.arch",
		CLI:   "codex",
	}

	ctx := context.Background()
	handle, err := agent.Dispatch(ctx, job, "")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if err := handle.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}

func TestCliAgent_Dispatch_MissingBinary(t *testing.T) {
	agent := NewCliAgent("/nonexistent/binary")

	job := &core.Job{
		ID:    "test-job-missing",
		RunID: "test-run-missing",
		Role:  "reviewer.arch",
		CLI:   "codex",
	}

	ctx := context.Background()
	_, err := agent.Dispatch(ctx, job, "test")
	if err == nil {
		t.Error("expected error for missing binary, got nil")
	}
}
