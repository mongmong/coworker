package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
	"github.com/spf13/cobra"
)

func saveAndRestoreRollbackFlags(t *testing.T) {
	t.Helper()
	origDB := rollbackDBPath
	origBy := rollbackAnsweredBy
	t.Cleanup(func() {
		rollbackDBPath = origDB
		rollbackAnsweredBy = origBy
	})
}

func runRollbackForTest(t *testing.T, dbPath, checkpointID string) (string, error) {
	t.Helper()
	saveAndRestoreRollbackFlags(t)
	rollbackDBPath = dbPath
	rollbackAnsweredBy = "cli"

	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	err := runRollback(cmd, []string{checkpointID})
	return buf.String(), err
}

func TestRollback_NoSession(t *testing.T) {
	tmp := t.TempDir()
	dbPath := tmp + "/state.db"
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	_, err = runRollbackForTest(t, dbPath, "missing")
	if err == nil {
		t.Fatal("expected error when no active session")
	}
	if !strings.Contains(err.Error(), "no active session") {
		t.Errorf("error = %q; expected 'no active session'", err.Error())
	}
}

func TestRollback_UnknownID(t *testing.T) {
	dbPath, _, _ := advanceTestEnv(t, false) // session active, no checkpoint
	_, err := runRollbackForTest(t, dbPath, "chk_does_not_exist")
	if err == nil {
		t.Fatal("expected error when checkpoint ID does not exist")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q; expected 'not found' substring", err.Error())
	}
}

func TestRollback_NotACheckpoint(t *testing.T) {
	// advanceTestEnv with no checkpoint; manually insert a non-checkpoint
	// attention item and try to roll it back.
	dbPath, runID, _ := advanceTestEnv(t, false)
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	as := store.NewAttentionStore(db)
	item := &core.AttentionItem{
		RunID:  runID,
		Kind:   core.AttentionPermission,
		Source: "test",
	}
	if err := as.InsertAttention(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	db.Close()

	_, err = runRollbackForTest(t, dbPath, item.ID)
	if err == nil {
		t.Fatal("expected error when ID is not a checkpoint")
	}
	if !strings.Contains(err.Error(), "not a checkpoint") {
		t.Errorf("error = %q; expected 'not a checkpoint' substring", err.Error())
	}
}

func TestRollback_AnsweredByFlag(t *testing.T) {
	dbPath, _, attentionID := advanceTestEnv(t, true)

	saveAndRestoreRollbackFlags(t)
	rollbackDBPath = dbPath
	rollbackAnsweredBy = "alice"

	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	if err := runRollback(cmd, []string{attentionID}); err != nil {
		t.Fatalf("runRollback: %v", err)
	}

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	as := store.NewAttentionStore(db)
	item, err := as.GetAttentionByID(context.Background(), attentionID)
	if err != nil {
		t.Fatal(err)
	}
	if item.AnsweredBy != "alice" {
		t.Errorf("AnsweredBy = %q, want %q", item.AnsweredBy, "alice")
	}
}

func TestRollback_ResolvesCheckpoint(t *testing.T) {
	dbPath, _, attentionID := advanceTestEnv(t, true)

	out, err := runRollbackForTest(t, dbPath, attentionID)
	if err != nil {
		t.Fatalf("runRollback: %v", err)
	}
	if !strings.Contains(out, attentionID) || !strings.Contains(out, "rejected") {
		t.Errorf("output = %q; want substring %q and 'rejected'", out, attentionID)
	}

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	es := store.NewEventStore(db)
	as := store.NewAttentionStore(db)
	cs := store.NewCheckpointStore(db, es)

	item, err := as.GetAttentionByID(context.Background(), attentionID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Answer != core.AttentionAnswerReject {
		t.Errorf("attention answer = %q, want %q", item.Answer, core.AttentionAnswerReject)
	}
	cp, err := cs.GetCheckpoint(context.Background(), attentionID)
	if err != nil {
		t.Fatal(err)
	}
	if cp.State != "resolved" || cp.Decision != core.AttentionAnswerReject {
		t.Errorf("checkpoint state/decision = %q/%q; want resolved/reject",
			cp.State, cp.Decision)
	}
}
