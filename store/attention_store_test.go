package store

import (
	"context"
	"testing"

	"github.com/chris/coworker/core"
)

func TestAttentionStore_InsertGetByID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db, _ := Open(":memory:")
	defer db.Close()

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

	retrieved, _ := store.GetAttentionByID(ctx, item.ID)
	if retrieved == nil {
		t.Fatal("retrieved nil")
	}
	if retrieved.Question != "Proceed?" {
		t.Errorf("got question %q, want %q", retrieved.Question, "Proceed?")
	}
}

func TestAttentionStore_AnswerAttention(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db, _ := Open(":memory:")
	defer db.Close()

	store := NewAttentionStore(db)

	item := &core.AttentionItem{
		RunID:  "run_answer_test",
		Kind:   core.AttentionQuestion,
		Source: "user",
		Question: "Proceed?",
	}

	if err := store.InsertAttention(ctx, item); err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	if err := store.AnswerAttention(ctx, item.ID, "yes", "tui"); err != nil {
		t.Fatalf("answer failed: %v", err)
	}

	retrieved, _ := store.GetAttentionByID(ctx, item.ID)
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

	db, _ := Open(":memory:")
	defer db.Close()

	store := NewAttentionStore(db)
	runID := "run_unanswered_test"

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

	unanswered, _ := store.ListUnansweredByRun(ctx, runID)
	if len(unanswered) != 2 {
		t.Errorf("got %d unanswered, want 2", len(unanswered))
	}
}
