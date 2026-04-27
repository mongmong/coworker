package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func runLogsForTest(t *testing.T, dbPath, jobID string) (string, error) {
	t.Helper()
	origDB := logsDBPath
	origFollow := logsFollow
	logsDBPath = dbPath
	logsFollow = false
	t.Cleanup(func() {
		logsDBPath = origDB
		logsFollow = origFollow
	})
	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	err := runLogs(cmd, []string{jobID})
	return buf.String(), err
}

func TestLogs_UnknownJob(t *testing.T) {
	dbPath, _ := seedStatusDB(t)
	_, err := runLogsForTest(t, dbPath, "missing")
	if err == nil {
		t.Fatal("expected error for unknown job")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %q; expected 'not found'", err.Error())
	}
}

func TestLogs_MissingLogFile(t *testing.T) {
	dbPath, _ := seedStatusDB(t)
	// Job exists in DB but no JSONL log file.
	_, err := runLogsForTest(t, dbPath, "job_status_1")
	if err == nil {
		t.Fatal("expected error when log file missing")
	}
	if !strings.Contains(err.Error(), "log file not found") {
		t.Errorf("err = %q; expected 'log file not found'", err.Error())
	}
}

func TestLogs_PrintsLog(t *testing.T) {
	dbPath, runID := seedStatusDB(t)
	// Create the JSONL log file matching the convention.
	coworkerDir := filepath.Dir(dbPath)
	logDir := filepath.Join(coworkerDir, "runs", runID, "jobs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "job_status_1.jsonl")
	content := `{"type":"finding","path":"main.go","line":1,"severity":"minor","body":"x"}` + "\n" +
		`{"type":"done","exit_code":0}` + "\n"
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runLogsForTest(t, dbPath, "job_status_1")
	if err != nil {
		t.Fatalf("runLogs: %v", err)
	}
	if !strings.Contains(out, "main.go") {
		t.Errorf("output = %q; expected log content", out)
	}
}
