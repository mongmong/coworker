package mcp_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/chris/coworker/core"
	mcpserver "github.com/chris/coworker/mcp"
	"github.com/chris/coworker/store"
)

// newAttentionStore creates an AttentionStore backed by an in-memory test DB.
func newAttentionStore(t *testing.T, db *store.DB) *store.AttentionStore {
	t.Helper()
	return store.NewAttentionStore(db)
}

// createRunForAttention creates a run record required by the attention FK.
func createRunForAttention(t *testing.T, db *store.DB, runID string) {
	t.Helper()
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)
	run := &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := rs.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("createRunForAttention(%q): %v", runID, err)
	}
}

// --- orch_ask_user tests -----------------------------------------------------

func TestHandleAskUser_HappyPath(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_ask_1")
	as := newAttentionStore(t, db)

	out, err := mcpserver.CallAskUser(context.Background(), as, "Proceed with deployment?", []string{"yes", "no"}, "", "run_ask_1")
	if err != nil {
		t.Fatalf("CallAskUser: %v", err)
	}

	if out["attention_id"] == "" {
		t.Error("attention_id should not be empty")
	}
	if out["status"] != "pending" {
		t.Errorf("status = %q, want %q", out["status"], "pending")
	}
}

func TestHandleAskUser_MissingQuestion(t *testing.T) {
	db := openTestDB(t)
	as := newAttentionStore(t, db)

	_, err := mcpserver.CallAskUser(context.Background(), as, "", nil, "", "")
	if err == nil {
		t.Fatal("expected error for empty question, got nil")
	}
}

func TestHandleAskUser_NoOptions(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_ask_2")
	as := newAttentionStore(t, db)

	out, err := mcpserver.CallAskUser(context.Background(), as, "What is your name?", nil, "", "run_ask_2")
	if err != nil {
		t.Fatalf("CallAskUser (no options): %v", err)
	}
	if out["attention_id"] == "" {
		t.Error("attention_id should not be empty")
	}
}

func TestHandleAskUser_WithJobID(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_ask_3")

	// Also create a job so the FK on job_id is satisfied if enforced.
	// The attention schema has job_id as nullable with no FK — safe to pass any string.
	as := newAttentionStore(t, db)

	out, err := mcpserver.CallAskUser(context.Background(), as, "Approve the diff?", []string{"approve", "reject"}, "job_abc", "run_ask_3")
	if err != nil {
		t.Fatalf("CallAskUser (with job_id): %v", err)
	}
	if out["attention_id"] == "" {
		t.Error("attention_id should not be empty")
	}
}

func TestHandleAskUser_StoresItemInDB(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_ask_store")
	as := newAttentionStore(t, db)

	out, err := mcpserver.CallAskUser(context.Background(), as, "Ready?", []string{"yes", "no"}, "", "run_ask_store")
	if err != nil {
		t.Fatalf("CallAskUser: %v", err)
	}

	attentionID, ok := out["attention_id"].(string)
	if !ok || attentionID == "" {
		t.Fatalf("attention_id missing or wrong type: %T %v", out["attention_id"], out["attention_id"])
	}

	// Verify the item exists in the store.
	item, err := as.GetAttentionByID(context.Background(), attentionID)
	if err != nil {
		t.Fatalf("GetAttentionByID: %v", err)
	}
	if item == nil {
		t.Fatal("item not found in store after CallAskUser")
	}
	if item.Question != "Ready?" {
		t.Errorf("question = %q, want %q", item.Question, "Ready?")
	}
	if item.Kind != core.AttentionQuestion {
		t.Errorf("kind = %q, want %q", item.Kind, core.AttentionQuestion)
	}
	if item.Source != "mcp" {
		t.Errorf("source = %q, want %q", item.Source, "mcp")
	}
	if len(item.Options) != 2 {
		t.Errorf("options count = %d, want 2", len(item.Options))
	}
}

func TestHandleAskUser_NilDB_ReturnsStub(t *testing.T) {
	// When no DB is configured, the server should register the stub handler.
	s, err := mcpserver.NewServer(mcpserver.ServerConfig{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	tools := s.Tools()
	found := false
	for _, name := range tools {
		if name == "orch_ask_user" {
			found = true
			break
		}
	}
	if !found {
		t.Error("orch_ask_user tool should be registered even without a DB")
	}
}

// --- orch_attention_list tests -----------------------------------------------

func TestHandleAttentionList_EmptyResult(t *testing.T) {
	db := openTestDB(t)
	as := newAttentionStore(t, db)

	out, err := mcpserver.CallAttentionList(context.Background(), as, "")
	if err != nil {
		t.Fatalf("CallAttentionList: %v", err)
	}

	itemsRaw, ok := out["items"]
	if !ok {
		t.Fatal("items field missing from output")
	}
	// Must not be nil — should be an empty JSON array.
	b, err := json.Marshal(itemsRaw)
	if err != nil {
		t.Fatalf("marshal items: %v", err)
	}
	if string(b) == "null" {
		t.Error("items should be [] not null when empty")
	}
	items, ok := itemsRaw.([]interface{})
	if !ok {
		t.Fatalf("items wrong type: %T", itemsRaw)
	}
	if len(items) != 0 {
		t.Errorf("items count = %d, want 0", len(items))
	}
}

func TestHandleAttentionList_AllPending_NoRunID(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_list_all_1")
	createRunForAttention(t, db, "run_list_all_2")
	as := newAttentionStore(t, db)

	// Insert one pending item per run.
	_, err := mcpserver.CallAskUser(context.Background(), as, "Q1?", nil, "", "run_list_all_1")
	if err != nil {
		t.Fatalf("CallAskUser run 1: %v", err)
	}
	_, err = mcpserver.CallAskUser(context.Background(), as, "Q2?", nil, "", "run_list_all_2")
	if err != nil {
		t.Fatalf("CallAskUser run 2: %v", err)
	}

	// List all pending (no run_id filter).
	out, err := mcpserver.CallAttentionList(context.Background(), as, "")
	if err != nil {
		t.Fatalf("CallAttentionList (all): %v", err)
	}

	items, ok := out["items"].([]interface{})
	if !ok {
		t.Fatalf("items wrong type: %T", out["items"])
	}
	if len(items) != 2 {
		t.Errorf("items count = %d, want 2", len(items))
	}
}

func TestHandleAttentionList_FilteredByRunID(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_list_filter_a")
	createRunForAttention(t, db, "run_list_filter_b")
	as := newAttentionStore(t, db)

	// Two items for run A, one for run B.
	if _, err := mcpserver.CallAskUser(context.Background(), as, "Q1?", nil, "", "run_list_filter_a"); err != nil {
		t.Fatalf("CallAskUser 1: %v", err)
	}
	if _, err := mcpserver.CallAskUser(context.Background(), as, "Q2?", nil, "", "run_list_filter_a"); err != nil {
		t.Fatalf("CallAskUser 2: %v", err)
	}
	if _, err := mcpserver.CallAskUser(context.Background(), as, "Q3?", nil, "", "run_list_filter_b"); err != nil {
		t.Fatalf("CallAskUser 3: %v", err)
	}

	// Filter to run A only.
	out, err := mcpserver.CallAttentionList(context.Background(), as, "run_list_filter_a")
	if err != nil {
		t.Fatalf("CallAttentionList (run A): %v", err)
	}
	items, ok := out["items"].([]interface{})
	if !ok {
		t.Fatalf("items wrong type: %T", out["items"])
	}
	if len(items) != 2 {
		t.Errorf("items count for run A = %d, want 2", len(items))
	}

	// Filter to run B only.
	out, err = mcpserver.CallAttentionList(context.Background(), as, "run_list_filter_b")
	if err != nil {
		t.Fatalf("CallAttentionList (run B): %v", err)
	}
	items, ok = out["items"].([]interface{})
	if !ok {
		t.Fatalf("items wrong type: %T", out["items"])
	}
	if len(items) != 1 {
		t.Errorf("items count for run B = %d, want 1", len(items))
	}
}

func TestHandleAttentionList_DoesNotIncludeAnswered(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_list_answered")
	as := newAttentionStore(t, db)

	// Insert two items.
	out1, err := mcpserver.CallAskUser(context.Background(), as, "Will be answered?", nil, "", "run_list_answered")
	if err != nil {
		t.Fatalf("CallAskUser 1: %v", err)
	}
	if _, err := mcpserver.CallAskUser(context.Background(), as, "Still pending?", nil, "", "run_list_answered"); err != nil {
		t.Fatalf("CallAskUser 2: %v", err)
	}

	// Answer the first item directly via the store.
	attentionID1 := out1["attention_id"].(string)
	if err := as.AnswerAttention(context.Background(), attentionID1, "yes", "tui"); err != nil {
		t.Fatalf("AnswerAttention: %v", err)
	}

	// List should only return the unanswered item.
	out, err := mcpserver.CallAttentionList(context.Background(), as, "run_list_answered")
	if err != nil {
		t.Fatalf("CallAttentionList: %v", err)
	}
	items, ok := out["items"].([]interface{})
	if !ok {
		t.Fatalf("items wrong type: %T", out["items"])
	}
	if len(items) != 1 {
		t.Errorf("items count = %d, want 1 (answered item must be excluded)", len(items))
	}
}

func TestHandleAttentionList_ItemFields(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_list_fields")
	as := newAttentionStore(t, db)

	out, err := mcpserver.CallAskUser(context.Background(), as, "Which env?", []string{"prod", "staging"}, "job_xyz", "run_list_fields")
	if err != nil {
		t.Fatalf("CallAskUser: %v", err)
	}
	attentionID := out["attention_id"].(string)

	listOut, err := mcpserver.CallAttentionList(context.Background(), as, "run_list_fields")
	if err != nil {
		t.Fatalf("CallAttentionList: %v", err)
	}
	items := listOut["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("items count = %d, want 1", len(items))
	}

	item, ok := items[0].(map[string]interface{})
	if !ok {
		t.Fatalf("item wrong type: %T", items[0])
	}

	if item["id"] != attentionID {
		t.Errorf("id = %q, want %q", item["id"], attentionID)
	}
	if item["run_id"] != "run_list_fields" {
		t.Errorf("run_id = %q, want %q", item["run_id"], "run_list_fields")
	}
	if item["kind"] != "question" {
		t.Errorf("kind = %q, want %q", item["kind"], "question")
	}
	if item["source"] != "mcp" {
		t.Errorf("source = %q, want %q", item["source"], "mcp")
	}
	if item["question"] != "Which env?" {
		t.Errorf("question = %q, want %q", item["question"], "Which env?")
	}
	if item["job_id"] != "job_xyz" {
		t.Errorf("job_id = %q, want %q", item["job_id"], "job_xyz")
	}
	opts, ok := item["options"].([]interface{})
	if !ok {
		t.Fatalf("options wrong type: %T", item["options"])
	}
	if len(opts) != 2 {
		t.Errorf("options count = %d, want 2", len(opts))
	}
	if _, ok := item["created_at"]; !ok {
		t.Error("created_at field missing")
	}
}

// --- orch_attention_answer tests ---------------------------------------------

func TestHandleAttentionAnswer_HappyPath(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_answer_1")
	as := newAttentionStore(t, db)

	// Create an attention item.
	askOut, err := mcpserver.CallAskUser(context.Background(), as, "Deploy now?", []string{"yes", "no"}, "", "run_answer_1")
	if err != nil {
		t.Fatalf("CallAskUser: %v", err)
	}
	attentionID := askOut["attention_id"].(string)

	// Answer it.
	out, err := mcpserver.CallAttentionAnswer(context.Background(), as, attentionID, "yes", "mcp")
	if err != nil {
		t.Fatalf("CallAttentionAnswer: %v", err)
	}
	if out["status"] != "ok" {
		t.Errorf("status = %q, want %q", out["status"], "ok")
	}
}

func TestHandleAttentionAnswer_MissingAttentionID(t *testing.T) {
	db := openTestDB(t)
	as := newAttentionStore(t, db)

	_, err := mcpserver.CallAttentionAnswer(context.Background(), as, "", "yes", "mcp")
	if err == nil {
		t.Fatal("expected error for empty attention_id, got nil")
	}
}

func TestHandleAttentionAnswer_MissingAnswer(t *testing.T) {
	db := openTestDB(t)
	as := newAttentionStore(t, db)

	_, err := mcpserver.CallAttentionAnswer(context.Background(), as, "some-id", "", "mcp")
	if err == nil {
		t.Fatal("expected error for empty answer, got nil")
	}
}

func TestHandleAttentionAnswer_MissingAnsweredBy(t *testing.T) {
	db := openTestDB(t)
	as := newAttentionStore(t, db)

	_, err := mcpserver.CallAttentionAnswer(context.Background(), as, "some-id", "yes", "")
	if err == nil {
		t.Fatal("expected error for empty answered_by, got nil")
	}
}

func TestHandleAttentionAnswer_PersistsAnswerInDB(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_answer_persist")
	as := newAttentionStore(t, db)

	askOut, err := mcpserver.CallAskUser(context.Background(), as, "Roll back?", []string{"yes", "no"}, "", "run_answer_persist")
	if err != nil {
		t.Fatalf("CallAskUser: %v", err)
	}
	attentionID := askOut["attention_id"].(string)

	if _, err := mcpserver.CallAttentionAnswer(context.Background(), as, attentionID, "no", "mcp"); err != nil {
		t.Fatalf("CallAttentionAnswer: %v", err)
	}

	// Verify the answer persisted.
	item, err := as.GetAttentionByID(context.Background(), attentionID)
	if err != nil {
		t.Fatalf("GetAttentionByID: %v", err)
	}
	if item == nil {
		t.Fatal("item not found after answer")
	}
	if item.Answer != "no" {
		t.Errorf("answer = %q, want %q", item.Answer, "no")
	}
	if item.AnsweredBy != "mcp" {
		t.Errorf("answered_by = %q, want %q", item.AnsweredBy, "mcp")
	}
	if item.ResolvedAt == nil {
		t.Error("resolved_at should be set after answer")
	}
}

// --- end-to-end ask → list → answer cycle ------------------------------------

func TestAttentionCycle_AskListAnswer(t *testing.T) {
	db := openTestDB(t)
	createRunForAttention(t, db, "run_cycle")
	as := newAttentionStore(t, db)

	// Step 1: ask.
	askOut, err := mcpserver.CallAskUser(context.Background(), as, "Approve release v1.2?", []string{"approve", "reject"}, "", "run_cycle")
	if err != nil {
		t.Fatalf("CallAskUser: %v", err)
	}
	attentionID, ok := askOut["attention_id"].(string)
	if !ok || attentionID == "" {
		t.Fatalf("attention_id missing: %v", askOut)
	}
	if askOut["status"] != "pending" {
		t.Errorf("ask status = %q, want %q", askOut["status"], "pending")
	}

	// Step 2: list — item should appear.
	listOut, err := mcpserver.CallAttentionList(context.Background(), as, "run_cycle")
	if err != nil {
		t.Fatalf("CallAttentionList: %v", err)
	}
	items, ok := listOut["items"].([]interface{})
	if !ok {
		t.Fatalf("items wrong type: %T", listOut["items"])
	}
	if len(items) != 1 {
		t.Fatalf("items count = %d, want 1", len(items))
	}
	listedItem, ok := items[0].(map[string]interface{})
	if !ok {
		t.Fatalf("item wrong type: %T", items[0])
	}
	if listedItem["id"] != attentionID {
		t.Errorf("listed item id = %q, want %q", listedItem["id"], attentionID)
	}

	// Step 3: answer.
	answerOut, err := mcpserver.CallAttentionAnswer(context.Background(), as, attentionID, "approve", "mcp")
	if err != nil {
		t.Fatalf("CallAttentionAnswer: %v", err)
	}
	if answerOut["status"] != "ok" {
		t.Errorf("answer status = %q, want %q", answerOut["status"], "ok")
	}

	// Step 4: list again — item should no longer appear (it's answered).
	listOut2, err := mcpserver.CallAttentionList(context.Background(), as, "run_cycle")
	if err != nil {
		t.Fatalf("CallAttentionList after answer: %v", err)
	}
	items2, ok := listOut2["items"].([]interface{})
	if !ok {
		t.Fatalf("items wrong type after answer: %T", listOut2["items"])
	}
	if len(items2) != 0 {
		t.Errorf("items count after answer = %d, want 0", len(items2))
	}
}
