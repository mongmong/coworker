package mcp_test

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/core"
	mcpserver "github.com/chris/coworker/mcp"
	"github.com/chris/coworker/store"
)

// insertCheckpointItem inserts a checkpoint attention item into the DB and returns its ID.
func insertCheckpointItem(t *testing.T, as *store.AttentionStore, runID, source, question string) string {
	t.Helper()
	item := &core.AttentionItem{
		ID:        core.NewID(),
		RunID:     runID,
		Kind:      core.AttentionCheckpoint,
		Source:    source,
		Question:  question,
		CreatedAt: time.Now(),
	}
	if err := as.InsertAttention(context.Background(), item); err != nil {
		t.Fatalf("InsertAttention (checkpoint): %v", err)
	}
	return item.ID
}

// insertQuestionItem inserts a question-kind attention item and returns its ID.
func insertQuestionItem(t *testing.T, as *store.AttentionStore, runID string) string {
	t.Helper()
	item := &core.AttentionItem{
		ID:        core.NewID(),
		RunID:     runID,
		Kind:      core.AttentionQuestion,
		Source:    "test",
		Question:  "Proceed?",
		CreatedAt: time.Now(),
	}
	if err := as.InsertAttention(context.Background(), item); err != nil {
		t.Fatalf("InsertAttention (question): %v", err)
	}
	return item.ID
}

// --- orch_checkpoint_list tests ----------------------------------------------

func TestHandleCheckpointList_MissingRunID(t *testing.T) {
	db := openTestDB(t)
	as := newAttentionStore(t, db)

	_, err := mcpserver.CallCheckpointList(context.Background(), as, "")
	if err == nil {
		t.Fatal("expected error for empty run_id, got nil")
	}
	if err.Error() != "run_id is required" {
		t.Errorf("error = %q, want %q", err.Error(), "run_id is required")
	}
}

func TestHandleCheckpointList_EmptyResult(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_chk_empty")
	as := newAttentionStore(t, db)

	out, err := mcpserver.CallCheckpointList(context.Background(), as, "run_chk_empty")
	if err != nil {
		t.Fatalf("CallCheckpointList: %v", err)
	}

	items, ok := out["items"].([]interface{})
	if !ok {
		t.Fatalf("items wrong type: %T", out["items"])
	}
	if len(items) != 0 {
		t.Errorf("items count = %d, want 0", len(items))
	}
}

func TestHandleCheckpointList_OnlyCheckpointsReturned(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_chk_filter")
	as := newAttentionStore(t, db)

	// Insert one checkpoint and one question item for the same run.
	_ = insertCheckpointItem(t, as, "run_chk_filter", "phase-loop", "phase clean?")
	_ = insertQuestionItem(t, as, "run_chk_filter")

	out, err := mcpserver.CallCheckpointList(context.Background(), as, "run_chk_filter")
	if err != nil {
		t.Fatalf("CallCheckpointList: %v", err)
	}

	items, ok := out["items"].([]interface{})
	if !ok {
		t.Fatalf("items wrong type: %T", out["items"])
	}
	if len(items) != 1 {
		t.Errorf("items count = %d, want 1 (only checkpoints)", len(items))
	}

	// Verify the returned item is a checkpoint.
	item, ok := items[0].(map[string]interface{})
	if !ok {
		t.Fatalf("item wrong type: %T", items[0])
	}
	if item["kind"] != "checkpoint" {
		t.Errorf("kind = %q, want %q", item["kind"], "checkpoint")
	}
}

func TestHandleCheckpointList_FilteredByRunID(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_chk_a")
	createRunForAttention(t, db, "run_chk_b")
	as := newAttentionStore(t, db)

	// Two checkpoints for run A, one for run B.
	_ = insertCheckpointItem(t, as, "run_chk_a", "phase-loop", "chk 1")
	_ = insertCheckpointItem(t, as, "run_chk_a", "phase-loop", "chk 2")
	_ = insertCheckpointItem(t, as, "run_chk_b", "phase-loop", "chk b")

	out, err := mcpserver.CallCheckpointList(context.Background(), as, "run_chk_a")
	if err != nil {
		t.Fatalf("CallCheckpointList: %v", err)
	}
	items := out["items"].([]interface{})
	if len(items) != 2 {
		t.Errorf("run A: items count = %d, want 2", len(items))
	}

	out2, err := mcpserver.CallCheckpointList(context.Background(), as, "run_chk_b")
	if err != nil {
		t.Fatalf("CallCheckpointList run B: %v", err)
	}
	items2 := out2["items"].([]interface{})
	if len(items2) != 1 {
		t.Errorf("run B: items count = %d, want 1", len(items2))
	}
}

// --- orch_checkpoint_advance tests ------------------------------------------

func TestHandleCheckpointAdvance_HappyPath(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_adv_1")
	as := newAttentionStore(t, db)

	id := insertCheckpointItem(t, as, "run_adv_1", "phase-loop", "phase clean?")

	out, err := mcpserver.CallCheckpointAdvance(context.Background(), as, id, "mcp")
	if err != nil {
		t.Fatalf("CallCheckpointAdvance: %v", err)
	}

	if out["status"] != "approved" {
		t.Errorf("status = %q, want %q", out["status"], "approved")
	}
	if out["attention_id"] != id {
		t.Errorf("attention_id = %q, want %q", out["attention_id"], id)
	}

	// Verify the answer is "approve" in DB.
	item, err := as.GetAttentionByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAttentionByID: %v", err)
	}
	if item.Answer != core.AttentionAnswerApprove {
		t.Errorf("answer = %q, want %q", item.Answer, core.AttentionAnswerApprove)
	}
	if item.ResolvedAt == nil {
		t.Error("resolved_at should be set after advance")
	}
}

func TestHandleCheckpointAdvance_MissingAttentionID(t *testing.T) {
	db := openTestDB(t)
	as := newAttentionStore(t, db)

	_, err := mcpserver.CallCheckpointAdvance(context.Background(), as, "", "mcp")
	if err == nil {
		t.Fatal("expected error for empty attention_id, got nil")
	}
}

func TestHandleCheckpointAdvance_NotFound(t *testing.T) {
	db := openTestDB(t)
	as := newAttentionStore(t, db)

	_, err := mcpserver.CallCheckpointAdvance(context.Background(), as, "nonexistent-id", "mcp")
	if err == nil {
		t.Fatal("expected error for nonexistent attention_id, got nil")
	}
}

func TestHandleCheckpointAdvance_NotACheckpoint(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_adv_notchk")
	as := newAttentionStore(t, db)

	// Insert a question-kind item instead of a checkpoint.
	qID := insertQuestionItem(t, as, "run_adv_notchk")

	_, err := mcpserver.CallCheckpointAdvance(context.Background(), as, qID, "mcp")
	if err == nil {
		t.Fatal("expected error for non-checkpoint item, got nil")
	}
}

func TestHandleCheckpointAdvance_DefaultAnsweredBy(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_adv_default")
	as := newAttentionStore(t, db)

	id := insertCheckpointItem(t, as, "run_adv_default", "phase-loop", "chk?")

	// Pass empty answered_by — should default to "user".
	_, err := mcpserver.CallCheckpointAdvance(context.Background(), as, id, "")
	if err != nil {
		t.Fatalf("CallCheckpointAdvance: %v", err)
	}

	item, err := as.GetAttentionByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAttentionByID: %v", err)
	}
	if item.AnsweredBy != "user" {
		t.Errorf("answered_by = %q, want %q", item.AnsweredBy, "user")
	}
}

// --- orch_checkpoint_rollback tests ------------------------------------------

func TestHandleCheckpointRollback_HappyPath(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_roll_1")
	as := newAttentionStore(t, db)

	id := insertCheckpointItem(t, as, "run_roll_1", "phase-loop", "phase clean?")

	out, err := mcpserver.CallCheckpointRollback(context.Background(), as, id, "mcp")
	if err != nil {
		t.Fatalf("CallCheckpointRollback: %v", err)
	}

	if out["status"] != "rejected" {
		t.Errorf("status = %q, want %q", out["status"], "rejected")
	}
	if out["attention_id"] != id {
		t.Errorf("attention_id = %q, want %q", out["attention_id"], id)
	}

	// Verify the answer is "reject" in DB.
	item, err := as.GetAttentionByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAttentionByID: %v", err)
	}
	if item.Answer != core.AttentionAnswerReject {
		t.Errorf("answer = %q, want %q", item.Answer, core.AttentionAnswerReject)
	}
	if item.ResolvedAt == nil {
		t.Error("resolved_at should be set after rollback")
	}
}

func TestHandleCheckpointRollback_MissingAttentionID(t *testing.T) {
	db := openTestDB(t)
	as := newAttentionStore(t, db)

	_, err := mcpserver.CallCheckpointRollback(context.Background(), as, "", "mcp")
	if err == nil {
		t.Fatal("expected error for empty attention_id, got nil")
	}
}

func TestHandleCheckpointRollback_NotFound(t *testing.T) {
	db := openTestDB(t)
	as := newAttentionStore(t, db)

	_, err := mcpserver.CallCheckpointRollback(context.Background(), as, "nonexistent", "mcp")
	if err == nil {
		t.Fatal("expected error for nonexistent ID, got nil")
	}
}

func TestHandleCheckpointRollback_NotACheckpoint(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_roll_notchk")
	as := newAttentionStore(t, db)

	qID := insertQuestionItem(t, as, "run_roll_notchk")

	_, err := mcpserver.CallCheckpointRollback(context.Background(), as, qID, "mcp")
	if err == nil {
		t.Fatal("expected error for non-checkpoint item, got nil")
	}
}

// TestHandleCheckpointRollback_DefaultAnsweredBy verifies the default answered_by is "user".
func TestHandleCheckpointRollback_DefaultAnsweredBy(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_roll_default")
	as := newAttentionStore(t, db)

	id := insertCheckpointItem(t, as, "run_roll_default", "phase-loop", "chk?")

	_, err := mcpserver.CallCheckpointRollback(context.Background(), as, id, "")
	if err != nil {
		t.Fatalf("CallCheckpointRollback: %v", err)
	}

	item, err := as.GetAttentionByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAttentionByID: %v", err)
	}
	if item.AnsweredBy != "user" {
		t.Errorf("answered_by = %q, want %q", item.AnsweredBy, "user")
	}
}

// TestHandleCheckpointAdvanceVsRollback_AnswerConstants verifies the correct
// constants are written: approve for advance, reject for rollback.
func TestHandleCheckpointAdvanceVsRollback_AnswerConstants(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_const")
	as := newAttentionStore(t, db)

	advID := insertCheckpointItem(t, as, "run_const", "phase-loop", "advance chk")
	rollID := insertCheckpointItem(t, as, "run_const", "phase-loop", "rollback chk")

	if _, err := mcpserver.CallCheckpointAdvance(context.Background(), as, advID, "mcp"); err != nil {
		t.Fatalf("CallCheckpointAdvance: %v", err)
	}
	if _, err := mcpserver.CallCheckpointRollback(context.Background(), as, rollID, "mcp"); err != nil {
		t.Fatalf("CallCheckpointRollback: %v", err)
	}

	adv, _ := as.GetAttentionByID(context.Background(), advID)
	roll, _ := as.GetAttentionByID(context.Background(), rollID)

	if adv.Answer != core.AttentionAnswerApprove {
		t.Errorf("advance answer = %q, want %q", adv.Answer, core.AttentionAnswerApprove)
	}
	if roll.Answer != core.AttentionAnswerReject {
		t.Errorf("rollback answer = %q, want %q", roll.Answer, core.AttentionAnswerReject)
	}
}

// TestServerTools_CheckpointToolsRegistered verifies the 17-tool count.
func TestServerTools_CheckpointToolsRegistered(t *testing.T) {
	s, err := mcpserver.NewServer(mcpserver.ServerConfig{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	tools := s.Tools()
	want := []string{"orch_checkpoint_list", "orch_checkpoint_advance", "orch_checkpoint_rollback"}

	toolSet := make(map[string]bool, len(tools))
	for _, name := range tools {
		toolSet[name] = true
	}

	for _, name := range want {
		if !toolSet[name] {
			t.Errorf("checkpoint tool %q not registered", name)
		}
	}

	if len(tools) != 17 {
		t.Errorf("tool count = %d, want 17", len(tools))
	}
}
