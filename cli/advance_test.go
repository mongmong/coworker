package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
	"github.com/spf13/cobra"
)

// advanceTestEnv prepares a fresh DB + active session + (optionally) an
// unanswered checkpoint pair, returning the dbPath, runID, and the
// optional attention/checkpoint ID.
func advanceTestEnv(t *testing.T, withCheckpoint bool) (dbPath, runID, attentionID string) {
	t.Helper()
	tmp := t.TempDir()
	dbPath = filepath.Join(tmp, "state.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)
	runID = "run_advance_test"
	if err := rs.CreateRun(context.Background(), &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Write the session lock file.
	lockPath := sessionLockPath(dbPath)
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%s\n%d", runID, os.Getpid())), 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	if withCheckpoint {
		as := store.NewAttentionStore(db)
		cs := store.NewCheckpointStore(db, es)
		item := &core.AttentionItem{
			RunID:    runID,
			Kind:     core.AttentionCheckpoint,
			Source:   "test",
			Question: "Approve?",
		}
		if err := as.InsertAttention(context.Background(), item); err != nil {
			t.Fatalf("insert attention: %v", err)
		}
		if err := cs.CreateCheckpoint(context.Background(), core.CheckpointRecord{
			ID: item.ID, RunID: runID, Kind: string(item.Kind),
		}); err != nil {
			t.Fatalf("insert checkpoint: %v", err)
		}
		attentionID = item.ID
	}
	return
}

// runAdvanceForTest invokes the advance command against the given DB. It
// avoids touching package globals after run by saving/restoring them.
func runAdvanceForTest(t *testing.T, dbPath string) (string, error) {
	t.Helper()
	saveAndRestoreAdvanceFlags(t)
	advanceDBPath = dbPath
	advanceAnsweredBy = "cli"

	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	err := runAdvance(cmd, nil)
	return buf.String(), err
}

func saveAndRestoreAdvanceFlags(t *testing.T) {
	t.Helper()
	origDB := advanceDBPath
	origBy := advanceAnsweredBy
	t.Cleanup(func() {
		advanceDBPath = origDB
		advanceAnsweredBy = origBy
	})
}

func TestAdvance_NoSession(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "state.db")
	// DB exists but no session lock file.
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	_, err = runAdvanceForTest(t, dbPath)
	if err == nil {
		t.Fatal("expected error when no active session, got nil")
	}
	if !strings.Contains(err.Error(), "no active session") {
		t.Errorf("error = %q; expected 'no active session' message", err.Error())
	}
}

func TestAdvance_NoCheckpoint(t *testing.T) {
	dbPath, _, _ := advanceTestEnv(t, false)
	out, err := runAdvanceForTest(t, dbPath)
	if err != nil {
		t.Fatalf("runAdvance: %v", err)
	}
	if !strings.Contains(out, "no checkpoint waiting") {
		t.Errorf("output = %q; expected 'no checkpoint waiting'", out)
	}
}

func TestAdvance_ResolvesCheckpoint(t *testing.T) {
	dbPath, runID, attentionID := advanceTestEnv(t, true)

	out, err := runAdvanceForTest(t, dbPath)
	if err != nil {
		t.Fatalf("runAdvance: %v", err)
	}
	if !strings.Contains(out, attentionID) || !strings.Contains(out, "approved") {
		t.Errorf("output = %q; want substring %q and status=approved", out, attentionID)
	}

	// Verify both attention and checkpoint resolved.
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
	if item == nil || item.Answer != core.AttentionAnswerApprove {
		t.Errorf("attention answer = %q, want %q", item.Answer, core.AttentionAnswerApprove)
	}

	cp, err := cs.GetCheckpoint(context.Background(), attentionID)
	if err != nil {
		t.Fatal(err)
	}
	if cp.State != "resolved" || cp.Decision != core.AttentionAnswerApprove {
		t.Errorf("checkpoint state/decision = %q/%q; want resolved/approve",
			cp.State, cp.Decision)
	}
	_ = runID // already verified via the resolved rows
}

func TestAdvance_AnsweredByFlag(t *testing.T) {
	dbPath, _, attentionID := advanceTestEnv(t, true)

	saveAndRestoreAdvanceFlags(t)
	advanceDBPath = dbPath
	advanceAnsweredBy = "alice"

	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	if err := runAdvance(cmd, nil); err != nil {
		t.Fatalf("runAdvance: %v", err)
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
