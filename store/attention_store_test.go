package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func mustCreateRunForAttention(t *testing.T, db *DB, ctx context.Context, runID string) {
	t.Helper()
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	run := &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := rs.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun(%q): %v", runID, err)
	}
}

func TestAttentionStore_InsertGetByID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := setupTestDB(t)
	mustCreateRunForAttention(t, db, ctx, "run_att_test")

	store := NewAttentionStore(db)

	item := &core.AttentionItem{
		RunID:    "run_att_test",
		Kind:     core.AttentionQuestion,
		Source:   "user",
		Question: "Proceed?",
		Options:  []string{"yes", "no"},
	}

	if err := store.InsertAttention(ctx, item); err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	retrieved, err := store.GetAttentionByID(ctx, item.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("retrieved nil")
	}
	if retrieved.Question != "Proceed?" {
		t.Errorf("got question %q, want %q", retrieved.Question, "Proceed?")
	}
	if len(retrieved.Options) != 2 {
		t.Errorf("got %d options, want 2", len(retrieved.Options))
	}
}

func TestAttentionStore_AnswerAttention(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := setupTestDB(t)
	mustCreateRunForAttention(t, db, ctx, "run_answer_test")

	store := NewAttentionStore(db)

	item := &core.AttentionItem{
		RunID:    "run_answer_test",
		Kind:     core.AttentionQuestion,
		Source:   "user",
		Question: "Proceed?",
	}

	if err := store.InsertAttention(ctx, item); err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	if err := store.AnswerAttention(ctx, item.ID, "yes", "tui"); err != nil {
		t.Fatalf("answer failed: %v", err)
	}

	retrieved, err := store.GetAttentionByID(ctx, item.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if retrieved.Answer != "yes" {
		t.Errorf("got answer %q, want %q", retrieved.Answer, "yes")
	}
	if retrieved.AnsweredBy != "tui" {
		t.Errorf("got answered_by %q, want %q", retrieved.AnsweredBy, "tui")
	}
}

func TestAttentionStore_ListUnanswered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := setupTestDB(t)
	runID := "run_unanswered_test"
	mustCreateRunForAttention(t, db, ctx, runID)

	store := NewAttentionStore(db)

	for i := 1; i <= 3; i++ {
		item := &core.AttentionItem{
			RunID:    runID,
			Kind:     core.AttentionQuestion,
			Source:   "user",
			Question: "Q" + string(rune('0'+i)),
		}
		if err := store.InsertAttention(ctx, item); err != nil {
			t.Fatalf("insert %d failed: %v", i, err)
		}

		if i == 2 {
			if err := store.AnswerAttention(ctx, item.ID, "yes", "tui"); err != nil {
				t.Fatalf("answer failed: %v", err)
			}
		}
	}

	unanswered, err := store.ListUnansweredByRun(ctx, runID)
	if err != nil {
		t.Fatalf("list unanswered failed: %v", err)
	}
	if len(unanswered) != 2 {
		t.Errorf("got %d unanswered, want 2", len(unanswered))
	}
}

func TestAttentionStore_FKValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := setupTestDB(t)
	store := NewAttentionStore(db)

	// Attempting to insert with a non-existent run_id should fail FK constraint.
	item := &core.AttentionItem{
		RunID:    "nonexistent_run",
		Kind:     core.AttentionQuestion,
		Source:   "user",
		Question: "Proceed?",
	}

	err := store.InsertAttention(ctx, item)
	if err == nil {
		t.Fatal("expected FK constraint error, got nil")
	}
}

func TestAttentionStore_GetNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := setupTestDB(t)
	store := NewAttentionStore(db)

	got, err := store.GetAttentionByID(ctx, "nonexistent_id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing item, got %+v", got)
	}
}

func TestAttentionStore_AnswerAttention_AnsweredOnPopulated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := setupTestDB(t)
	mustCreateRunForAttention(t, db, ctx, "run_answeredon_test")

	as := NewAttentionStore(db)

	item := &core.AttentionItem{
		RunID:    "run_answeredon_test",
		Kind:     core.AttentionQuestion,
		Source:   "user",
		Question: "Ready?",
	}

	if err := as.InsertAttention(ctx, item); err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	if err := as.AnswerAttention(ctx, item.ID, "yes", "tui"); err != nil {
		t.Fatalf("answer failed: %v", err)
	}

	retrieved, err := as.GetAttentionByID(ctx, item.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("retrieved nil")
	}

	if len(retrieved.AnsweredOn) != 1 || retrieved.AnsweredOn[0] != "tui" {
		t.Errorf("AnsweredOn = %v, want [\"tui\"]", retrieved.AnsweredOn)
	}
}

// TestAttentionStore_GetUnansweredCheckpointForRun tests the filter logic for
// GetUnansweredCheckpointForRun across all expected cases.
// TestAttentionStore_AnswerAttention_NotFound verifies that AnswerAttention
// returns ErrAttentionNotFound when the target row does not exist (Important #2).
func TestAttentionStore_AnswerAttention_NotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := setupTestDB(t)
	as := NewAttentionStore(db)

	err := as.AnswerAttention(ctx, "nonexistent-id", "approve", "human")
	if err == nil {
		t.Fatal("expected error for nonexistent attention ID, got nil")
	}
	if !errors.Is(err, ErrAttentionNotFound) {
		t.Errorf("expected ErrAttentionNotFound, got: %v", err)
	}
}

func TestAttentionStore_GetUnansweredCheckpointForRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := setupTestDB(t)
	runID := "run_checkpoint_filter_test"
	otherRunID := "run_other_checkpoint"
	mustCreateRunForAttention(t, db, ctx, runID)
	mustCreateRunForAttention(t, db, ctx, otherRunID)

	as := NewAttentionStore(db)

	t.Run("no items returns nil", func(t *testing.T) {
		got, err := as.GetUnansweredCheckpointForRun(ctx, runID, "phase-loop")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil when no items exist, got %+v", got)
		}
	})

	t.Run("answered checkpoint returns nil", func(t *testing.T) {
		item := &core.AttentionItem{
			RunID:  runID,
			Kind:   core.AttentionCheckpoint,
			Source: "phase-loop",
		}
		if err := as.InsertAttention(ctx, item); err != nil {
			t.Fatalf("insert: %v", err)
		}
		// Answer it.
		if err := as.AnswerAttention(ctx, item.ID, core.AttentionAnswerApprove, "tui"); err != nil {
			t.Fatalf("answer: %v", err)
		}

		got, err := as.GetUnansweredCheckpointForRun(ctx, runID, "phase-loop")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil for answered checkpoint, got %+v", got)
		}
	})

	t.Run("wrong kind returns nil", func(t *testing.T) {
		item := &core.AttentionItem{
			RunID:  runID,
			Kind:   core.AttentionQuestion, // not "checkpoint"
			Source: "phase-loop",
		}
		if err := as.InsertAttention(ctx, item); err != nil {
			t.Fatalf("insert: %v", err)
		}

		got, err := as.GetUnansweredCheckpointForRun(ctx, runID, "phase-loop")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil for wrong kind, got %+v", got)
		}
	})

	t.Run("wrong source returns nil", func(t *testing.T) {
		item := &core.AttentionItem{
			RunID:  runID,
			Kind:   core.AttentionCheckpoint,
			Source: "run-command", // not "phase-loop"
		}
		if err := as.InsertAttention(ctx, item); err != nil {
			t.Fatalf("insert: %v", err)
		}

		got, err := as.GetUnansweredCheckpointForRun(ctx, runID, "phase-loop")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil for wrong source, got %+v", got)
		}
	})

	t.Run("matching item returned", func(t *testing.T) {
		item := &core.AttentionItem{
			RunID:    runID,
			Kind:     core.AttentionCheckpoint,
			Source:   "phase-loop",
			Question: "Phase did not converge",
		}
		if err := as.InsertAttention(ctx, item); err != nil {
			t.Fatalf("insert: %v", err)
		}

		got, err := as.GetUnansweredCheckpointForRun(ctx, runID, "phase-loop")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("expected item, got nil")
		}
		if got.ID != item.ID {
			t.Errorf("got ID %q, want %q", got.ID, item.ID)
		}
		if got.Kind != core.AttentionCheckpoint {
			t.Errorf("got kind %q, want checkpoint", got.Kind)
		}
		if got.Source != "phase-loop" {
			t.Errorf("got source %q, want phase-loop", got.Source)
		}
	})

	t.Run("permission kind not returned", func(t *testing.T) {
		permItem := &core.AttentionItem{
			RunID:  runID,
			Kind:   core.AttentionPermission,
			Source: "phase-loop",
		}
		if err := as.InsertAttention(ctx, permItem); err != nil {
			t.Fatalf("insert permission item: %v", err)
		}
		// The previously inserted matching checkpoint (from "matching item returned")
		// means we can verify the filter isolates kind=checkpoint.
		got, err := as.GetUnansweredCheckpointForRun(ctx, runID, "phase-loop")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should still return the checkpoint item (not the permission item).
		if got == nil {
			t.Fatal("expected checkpoint item, got nil")
		}
		if got.Kind != core.AttentionCheckpoint {
			t.Errorf("got kind %q, want checkpoint", got.Kind)
		}
		if got.ID == permItem.ID {
			t.Error("returned the permission item instead of the checkpoint item")
		}
	})
}
