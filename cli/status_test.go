package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

func saveAndRestoreStatusFlags(t *testing.T) {
	t.Helper()
	origDB := statusDBPath
	origRun := statusRunID
	t.Cleanup(func() {
		statusDBPath = origDB
		statusRunID = origRun
	})
}

func runStatusForTest(t *testing.T, dbPath string, args []string) (string, error) {
	t.Helper()
	saveAndRestoreStatusFlags(t)
	statusDBPath = dbPath
	statusRunID = ""
	if len(args) > 0 {
		statusRunID = args[0]
	}
	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	err := runStatus(cmd, args)
	return buf.String(), err
}

func seedStatusDB(t *testing.T) (dbPath, runID string) {
	t.Helper()
	tmp := t.TempDir()
	dbPath = filepath.Join(tmp, "state.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)
	js := store.NewJobStore(db, es)
	runID = "run_status_1"
	if err := rs.CreateRun(context.Background(), &core.Run{
		ID: runID, Mode: "interactive", State: core.RunStateActive,
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := js.CreateJob(context.Background(), &core.Job{
		ID: "job_status_1", RunID: runID, Role: "developer",
		State: core.JobStatePending, DispatchedBy: "test",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return
}

func TestStatus_NoRuns(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "state.db")
	db, _ := store.Open(dbPath)
	db.Close()
	out, err := runStatusForTest(t, dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no runs") {
		t.Errorf("output = %q; expected 'no runs'", out)
	}
}

func TestStatus_ListRuns(t *testing.T) {
	dbPath, runID := seedStatusDB(t)
	out, err := runStatusForTest(t, dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, runID) {
		t.Errorf("output = %q; expected to contain run ID %q", out, runID)
	}
	if !strings.Contains(out, "RUN ID") {
		t.Error("expected table header in output")
	}
}

func TestStatus_RunDetails(t *testing.T) {
	dbPath, runID := seedStatusDB(t)
	out, err := runStatusForTest(t, dbPath, []string{runID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, runID) || !strings.Contains(out, "developer") {
		t.Errorf("output = %q; expected run ID + job role", out)
	}
}

func TestStatus_UnknownRun(t *testing.T) {
	dbPath, _ := seedStatusDB(t)
	_, err := runStatusForTest(t, dbPath, []string{"unknown"})
	if err == nil {
		t.Fatal("expected error for unknown run")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %q; expected 'not found'", err.Error())
	}
}
