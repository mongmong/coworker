package store

import (
	"context"
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
