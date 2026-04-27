package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/chris/coworker/store"
)

func runInspectForTest(t *testing.T, dbPath, jobID string) (string, error) {
	t.Helper()
	orig := inspectDBPath
	inspectDBPath = dbPath
	t.Cleanup(func() { inspectDBPath = orig })

	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	err := runInspect(cmd, []string{jobID})
	return buf.String(), err
}

func TestInspect_UnknownJob(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "state.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = runInspectForTest(t, dbPath, "missing")
	if err == nil {
		t.Fatal("expected error for unknown job")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %q; expected 'not found'", err.Error())
	}
}

func TestInspect_KnownJob(t *testing.T) {
	dbPath, _ := seedStatusDB(t)
	out, err := runInspectForTest(t, dbPath, "job_status_1")
	if err != nil {
		t.Fatalf("runInspect: %v", err)
	}
	if !strings.Contains(out, "job_status_1") {
		t.Errorf("output = %q; expected job ID", out)
	}
	if !strings.Contains(out, "developer") {
		t.Errorf("output should contain role 'developer'")
	}
}
