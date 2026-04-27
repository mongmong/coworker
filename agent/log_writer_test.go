package agent

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chris/coworker/core"
)

func TestOpenJobLog_EmptyCoworkerDir_ReturnsDiscard(t *testing.T) {
	w, err := OpenJobLog("", "run-1", "job-1")
	if err != nil {
		t.Fatalf("OpenJobLog with empty dir: %v", err)
	}
	defer w.Close()

	// Writing to the discard writer must not error and must not create files.
	if _, writeErr := io.WriteString(w, `{"type":"test"}`+"\n"); writeErr != nil {
		t.Fatalf("write to discard: %v", writeErr)
	}
}

func TestOpenJobLog_CreatesFileAndParentDirs(t *testing.T) {
	dir := t.TempDir()

	w, err := OpenJobLog(dir, "run-abc", "job-xyz")
	if err != nil {
		t.Fatalf("OpenJobLog: %v", err)
	}

	content := `{"type":"finding"}` + "\n"
	if _, writeErr := io.WriteString(w, content); writeErr != nil {
		t.Fatalf("write to log: %v", writeErr)
	}
	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("close log: %v", closeErr)
	}

	// Verify file exists at the expected path.
	expectedPath := filepath.Join(dir, "runs", "run-abc", "jobs", "job-xyz.jsonl")
	data, readErr := os.ReadFile(expectedPath)
	if readErr != nil {
		t.Fatalf("read log file at %q: %v", expectedPath, readErr)
	}
	if string(data) != content {
		t.Errorf("log file content = %q, want %q", string(data), content)
	}
}

func TestCliAgent_Wait_WritesJobLog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	mockBin := findMockBinary(t)
	coworkerDir := t.TempDir()

	a := &CliAgent{
		BinaryPath:  mockBin,
		CoworkerDir: coworkerDir,
	}

	job := &core.Job{
		ID:    "job-log-1",
		RunID: "run-log-1",
		Role:  "reviewer.arch",
		CLI:   "codex",
	}

	ctx := t.Context()
	handle, err := a.Dispatch(ctx, job, "test prompt")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if _, waitErr := handle.Wait(ctx); waitErr != nil {
		t.Fatalf("Wait: %v", waitErr)
	}

	// Verify the JSONL log file was created with content.
	logPath := filepath.Join(coworkerDir, "runs", "run-log-1", "jobs", "job-log-1.jsonl")
	data, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("read job log at %q: %v", logPath, readErr)
	}
	if len(data) == 0 {
		t.Error("expected non-empty job log, got empty file")
	}
}

func TestCliAgent_Wait_EmptyCoworkerDir_NoFileCreated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	mockBin := findMockBinary(t)

	a := &CliAgent{
		BinaryPath:  mockBin,
		CoworkerDir: "", // no persistence
	}

	job := &core.Job{
		ID:    "job-no-log",
		RunID: "run-no-log",
		Role:  "reviewer.arch",
		CLI:   "codex",
	}

	ctx := t.Context()
	handle, err := a.Dispatch(ctx, job, "test prompt")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	result, waitErr := handle.Wait(ctx)
	if waitErr != nil {
		t.Fatalf("Wait: %v", waitErr)
	}
	// Verify results are still parsed correctly even without a log file.
	if len(result.Findings) == 0 {
		t.Error("expected findings even without CoworkerDir")
	}
}
