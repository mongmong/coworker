package quality

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// openTestDB opens an in-memory SQLite database for testing.
func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// mustCreateRun creates a run row in the DB so attention FK constraints pass.
func mustCreateRun(t *testing.T, db *store.DB, runID string) {
	t.Helper()
	ctx := context.Background()
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)
	run := &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := rs.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run %q: %v", runID, err)
	}
}

// allPassJudge returns a MockJudge where every rule passes.
func allPassJudge() *MockJudge {
	return &MockJudge{Verdicts: map[string]*Verdict{}}
}

// advisoryFailJudge returns a MockJudge where only the specified
// advisory rule fails.
func advisoryFailJudge(ruleName string) *MockJudge {
	return &MockJudge{
		Verdicts: map[string]*Verdict{
			ruleName: {
				Pass:       false,
				Category:   "spec_adherence",
				Findings:   []string{"implementation drifts from spec"},
				Confidence: 0.7,
			},
		},
	}
}

// blockingFailJudge returns a MockJudge where only the specified
// block-capable rule fails.
func blockingFailJudge(ruleName string, cat Category) *MockJudge {
	return &MockJudge{
		Verdicts: map[string]*Verdict{
			ruleName: {
				Pass:       false,
				Category:   string(cat),
				Findings:   []string{"Function Foo has no test"},
				Confidence: 0.9,
			},
		},
	}
}

func TestEvaluateAtCheckpoint_AllPass(t *testing.T) {
	db := openTestDB(t)
	mustCreateRun(t, db, "run-1")
	attStore := store.NewAttentionStore(db)
	evtStore := store.NewEventStore(db)

	rules := []*Rule{
		{Name: "rule_a", Category: CategoryMissingTests, Prompt: "p", Severity: "block"},
		{Name: "rule_b", Category: "spec_adherence", Prompt: "p", Severity: "advisory"},
	}

	ev := &Evaluator{
		Judge:          allPassJudge(),
		Rules:          rules,
		AttentionStore: attStore,
		EventStore:     evtStore,
	}

	cpCtx := &CheckpointContext{RunID: "run-1", JobID: "job-1"}
	result, err := ev.EvaluateAtCheckpoint(context.Background(), cpCtx, "diff", "ctx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Pass {
		t.Error("expected result to pass when all rules pass")
	}
	if len(result.BlockingFindings) != 0 {
		t.Errorf("expected 0 blocking findings, got %d", len(result.BlockingFindings))
	}
	if len(result.AdvisoryFindings) != 0 {
		t.Errorf("expected 0 advisory findings, got %d", len(result.AdvisoryFindings))
	}
	if result.QualityGateEscalated {
		t.Error("expected no quality-gate escalation")
	}
}

func TestEvaluateAtCheckpoint_AdvisoryFinding(t *testing.T) {
	db := openTestDB(t)
	mustCreateRun(t, db, "run-2")
	attStore := store.NewAttentionStore(db)
	evtStore := store.NewEventStore(db)

	rules := []*Rule{
		{Name: "spec_adherence", Category: "spec_adherence", Prompt: "p", Severity: "advisory"},
	}

	ev := &Evaluator{
		Judge:          advisoryFailJudge("spec_adherence"),
		Rules:          rules,
		AttentionStore: attStore,
		EventStore:     evtStore,
	}

	cpCtx := &CheckpointContext{RunID: "run-2", JobID: "job-2"}
	result, err := ev.EvaluateAtCheckpoint(context.Background(), cpCtx, "diff", "ctx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Advisory findings do not block.
	if !result.Pass {
		t.Error("expected result to pass (advisory finding does not block)")
	}
	if len(result.AdvisoryFindings) != 1 {
		t.Errorf("expected 1 advisory finding, got %d", len(result.AdvisoryFindings))
	}
	if len(result.BlockingFindings) != 0 {
		t.Errorf("expected 0 blocking findings, got %d", len(result.BlockingFindings))
	}
	// No attention items for advisory findings.
	if len(result.AttentionItemIDs) != 0 {
		t.Errorf("expected 0 attention items for advisory finding, got %d", len(result.AttentionItemIDs))
	}
}

func TestEvaluateAtCheckpoint_BlockingFinding(t *testing.T) {
	db := openTestDB(t)
	mustCreateRun(t, db, "run-3")
	attStore := store.NewAttentionStore(db)
	evtStore := store.NewEventStore(db)

	rules := []*Rule{
		{Name: "missing_tests", Category: CategoryMissingTests, Prompt: "p", Severity: "block"},
	}

	ev := &Evaluator{
		Judge:          blockingFailJudge("missing_tests", CategoryMissingTests),
		Rules:          rules,
		AttentionStore: attStore,
		EventStore:     evtStore,
	}

	cpCtx := &CheckpointContext{RunID: "run-3", JobID: "job-3"}
	result, err := ev.EvaluateAtCheckpoint(context.Background(), cpCtx, "diff", "ctx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Blocking finding makes result fail.
	if result.Pass {
		t.Error("expected result to fail for blocking finding")
	}
	if len(result.BlockingFindings) != 1 {
		t.Errorf("expected 1 blocking finding, got %d", len(result.BlockingFindings))
	}
	if len(result.AdvisoryFindings) != 0 {
		t.Errorf("expected 0 advisory findings, got %d", len(result.AdvisoryFindings))
	}
	// Attention item must be created.
	if len(result.AttentionItemIDs) != 1 {
		t.Errorf("expected 1 attention item, got %d", len(result.AttentionItemIDs))
	}
	// Verify the attention item is in the DB.
	item, err := attStore.GetAttentionByID(context.Background(), result.AttentionItemIDs[0])
	if err != nil {
		t.Fatalf("get attention item: %v", err)
	}
	if item == nil {
		t.Fatal("attention item not found in DB")
	}
	if item.RunID != "run-3" {
		t.Errorf("expected run_id 'run-3', got %q", item.RunID)
	}
	if item.Source != "quality-judge" {
		t.Errorf("expected source 'quality-judge', got %q", item.Source)
	}
}

func TestEvaluateAtCheckpoint_Mixed(t *testing.T) {
	db := openTestDB(t)
	mustCreateRun(t, db, "run-4")
	attStore := store.NewAttentionStore(db)
	evtStore := store.NewEventStore(db)

	rules := []*Rule{
		{Name: "missing_tests", Category: CategoryMissingTests, Prompt: "p", Severity: "block"},
		{Name: "spec_adherence", Category: "spec_adherence", Prompt: "p", Severity: "advisory"},
	}

	judge := &MockJudge{
		Verdicts: map[string]*Verdict{
			"missing_tests": {
				Pass:       false,
				Category:   string(CategoryMissingTests),
				Findings:   []string{"Function Bar has no test"},
				Confidence: 0.85,
			},
			"spec_adherence": {
				Pass:       false,
				Category:   "spec_adherence",
				Findings:   []string{"module structure diverges"},
				Confidence: 0.6,
			},
		},
	}

	ev := &Evaluator{
		Judge:          judge,
		Rules:          rules,
		AttentionStore: attStore,
		EventStore:     evtStore,
	}

	cpCtx := &CheckpointContext{RunID: "run-4", JobID: "job-4"}
	result, err := ev.EvaluateAtCheckpoint(context.Background(), cpCtx, "diff", "ctx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Pass {
		t.Error("expected result to fail (blocking finding present)")
	}
	if len(result.BlockingFindings) != 1 {
		t.Errorf("expected 1 blocking finding, got %d", len(result.BlockingFindings))
	}
	if result.BlockingFindings[0].RuleName != "missing_tests" {
		t.Errorf("unexpected blocking finding rule: %q", result.BlockingFindings[0].RuleName)
	}
	if len(result.AdvisoryFindings) != 1 {
		t.Errorf("expected 1 advisory finding, got %d", len(result.AdvisoryFindings))
	}
	if result.AdvisoryFindings[0].RuleName != "spec_adherence" {
		t.Errorf("unexpected advisory finding rule: %q", result.AdvisoryFindings[0].RuleName)
	}
	if len(result.AttentionItemIDs) != 1 {
		t.Errorf("expected 1 attention item (only for blocking), got %d", len(result.AttentionItemIDs))
	}
}

func TestEvaluateAtCheckpoint_Escalation(t *testing.T) {
	db := openTestDB(t)
	mustCreateRun(t, db, "run-5")
	attStore := store.NewAttentionStore(db)
	evtStore := store.NewEventStore(db)

	rules := []*Rule{
		{Name: "missing_tests", Category: CategoryMissingTests, Prompt: "p", Severity: "block"},
	}

	ev := &Evaluator{
		Judge:          blockingFailJudge("missing_tests", CategoryMissingTests),
		Rules:          rules,
		AttentionStore: attStore,
		EventStore:     evtStore,
	}

	// RetryCount >= MaxRetries → escalate.
	cpCtx := &CheckpointContext{
		RunID:      "run-5",
		JobID:      "job-5",
		RetryCount: 3,
		MaxRetries: 3,
	}
	result, err := ev.EvaluateAtCheckpoint(context.Background(), cpCtx, "diff", "ctx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Pass {
		t.Error("expected result to fail")
	}
	if !result.QualityGateEscalated {
		t.Error("expected quality-gate escalation when retry count >= max retries")
	}
}

func TestEvaluateAtCheckpoint_NoEscalationBeforeLimit(t *testing.T) {
	db := openTestDB(t)
	mustCreateRun(t, db, "run-6")
	attStore := store.NewAttentionStore(db)
	evtStore := store.NewEventStore(db)

	rules := []*Rule{
		{Name: "missing_tests", Category: CategoryMissingTests, Prompt: "p", Severity: "block"},
	}

	ev := &Evaluator{
		Judge:          blockingFailJudge("missing_tests", CategoryMissingTests),
		Rules:          rules,
		AttentionStore: attStore,
		EventStore:     evtStore,
	}

	// RetryCount < MaxRetries → no escalation yet.
	cpCtx := &CheckpointContext{
		RunID:      "run-6",
		JobID:      "job-6",
		RetryCount: 1,
		MaxRetries: 3,
	}
	result, err := ev.EvaluateAtCheckpoint(context.Background(), cpCtx, "diff", "ctx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.QualityGateEscalated {
		t.Error("expected no escalation before retry limit is reached")
	}
}

func TestEvaluateAtCheckpoint_NilStores(t *testing.T) {
	// With nil AttentionStore and EventStore, blocking findings should
	// still be recorded in the result — just without DB persistence.
	rules := []*Rule{
		{Name: "missing_tests", Category: CategoryMissingTests, Prompt: "p", Severity: "block"},
	}

	ev := &Evaluator{
		Judge:          blockingFailJudge("missing_tests", CategoryMissingTests),
		Rules:          rules,
		AttentionStore: nil,
		EventStore:     nil,
	}

	cpCtx := &CheckpointContext{RunID: "run-7", JobID: "job-7"}
	result, err := ev.EvaluateAtCheckpoint(context.Background(), cpCtx, "diff", "ctx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Pass {
		t.Error("expected result to fail for blocking finding")
	}
	if len(result.BlockingFindings) != 1 {
		t.Errorf("expected 1 blocking finding, got %d", len(result.BlockingFindings))
	}
	// No attention item IDs since store is nil.
	if len(result.AttentionItemIDs) != 0 {
		t.Errorf("expected 0 attention item IDs (nil store), got %d", len(result.AttentionItemIDs))
	}
}

func TestEvaluateAtCheckpoint_DefaultMaxRetries(t *testing.T) {
	rules := []*Rule{
		{Name: "missing_tests", Category: CategoryMissingTests, Prompt: "p", Severity: "block"},
	}

	ev := &Evaluator{
		Judge: blockingFailJudge("missing_tests", CategoryMissingTests),
		Rules: rules,
	}

	// MaxRetries=0 uses default (3). RetryCount=3 → escalate.
	cpCtx := &CheckpointContext{
		RunID:      "run-8",
		JobID:      "job-8",
		RetryCount: DefaultMaxQualityRetries,
		MaxRetries: 0, // use default
	}
	result, err := ev.EvaluateAtCheckpoint(context.Background(), cpCtx, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.QualityGateEscalated {
		t.Error("expected escalation at default max retries")
	}
}

func TestCheckpointContext_EffectiveMaxRetries(t *testing.T) {
	tests := []struct {
		name     string
		ctx      CheckpointContext
		expected int
	}{
		{
			name:     "zero uses default",
			ctx:      CheckpointContext{MaxRetries: 0},
			expected: DefaultMaxQualityRetries,
		},
		{
			name:     "explicit max",
			ctx:      CheckpointContext{MaxRetries: 5},
			expected: 5,
		},
		{
			name:     "one",
			ctx:      CheckpointContext{MaxRetries: 1},
			expected: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.ctx.effectiveMaxRetries()
			if got != tc.expected {
				t.Errorf("expected %d, got %d", tc.expected, got)
			}
		})
	}
}
