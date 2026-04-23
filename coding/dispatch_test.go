package coding

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding/supervisor"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// coding/ is one level below the repo root.
	return filepath.Dir(wd)
}

func TestOrchestrate_WithMockCodex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex")
	if _, err := os.Stat(mockBin); err != nil {
		t.Fatalf("mock binary not found: %v", err)
	}

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	d := &Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     agent.NewCliAgent(mockBin),
		DB:        db,
	}

	ctx := context.Background()
	result, err := d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	if result.RunID == "" {
		t.Error("run ID should not be empty")
	}
	if result.JobID == "" {
		t.Error("job ID should not be empty")
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}
	if len(result.Findings) != 2 {
		t.Fatalf("findings count = %d, want 2", len(result.Findings))
	}

	// Verify findings were persisted.
	findingStore := store.NewFindingStore(db, store.NewEventStore(db))
	findings, err := findingStore.ListFindings(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(findings) != 2 {
		t.Errorf("persisted findings = %d, want 2", len(findings))
	}

	// Verify run was completed.
	runStore := store.NewRunStore(db, store.NewEventStore(db))
	run, err := runStore.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.State != core.RunStateCompleted {
		t.Errorf("run state = %q, want %q", run.State, core.RunStateCompleted)
	}

	// Verify job was completed.
	jobStore := store.NewJobStore(db, store.NewEventStore(db))
	job, err := jobStore.GetJob(ctx, result.JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.State != core.JobStateComplete {
		t.Errorf("job state = %q, want %q", job.State, core.JobStateComplete)
	}

	// Verify event log.
	events, err := store.NewEventStore(db).ListEvents(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	// Expected events: run.created, job.created, job.leased (dispatched),
	// finding.created x2, job.completed, run.completed = 7
	if len(events) != 7 {
		t.Errorf("event count = %d, want 7", len(events))
		for i, e := range events {
			t.Logf("  event[%d]: seq=%d kind=%s", i, e.Sequence, e.Kind)
		}
	}
}

func TestOrchestrate_WithSupervisor_AllPass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex")

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	// The mock codex emits findings with path and line, so
	// all_findings_have(["path", "line"]) should pass.
	engine, err := supervisor.NewRuleEngineFromBytes([]byte(`
rules:
  findings_have_path_line:
    applies_to: [reviewer.*]
    check: all_findings_have(["path", "line"])
    message: "findings must have path and line"
`))
	if err != nil {
		t.Fatalf("NewRuleEngineFromBytes: %v", err)
	}

	d := &Dispatcher{
		RoleDir:    filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:  filepath.Join(repoRoot, "coding"),
		Agent:      agent.NewCliAgent(mockBin),
		DB:         db,
		Supervisor: engine,
	}

	ctx := context.Background()
	result, err := d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	if result.SupervisorVerdict == nil {
		t.Fatal("expected supervisor verdict, got nil")
	}
	if !result.SupervisorVerdict.Pass {
		t.Error("expected supervisor pass")
	}
	if result.RetryCount != 0 {
		t.Errorf("retry count = %d, want 0", result.RetryCount)
	}

	// Verify supervisor.verdict event was emitted.
	events, err := store.NewEventStore(db).ListEvents(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var hasVerdictEvent bool
	for _, e := range events {
		if e.Kind == core.EventSupervisorVerdict {
			hasVerdictEvent = true
			break
		}
	}
	if !hasVerdictEvent {
		t.Error("expected supervisor.verdict event in event log")
		for i, e := range events {
			t.Logf("  event[%d]: seq=%d kind=%s", i, e.Sequence, e.Kind)
		}
	}
}

func TestOrchestrate_WithSupervisor_RetryThenPass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex-retry-then-pass")
	if _, err := os.Stat(mockBin); err != nil {
		t.Fatalf("mock binary not found: %v", err)
	}

	// Create a unique state file for this test.
	stateFile := filepath.Join(t.TempDir(), "retry-state")
	t.Setenv("COWORKER_MOCK_STATE", stateFile)
	t.Cleanup(func() { os.Remove(stateFile) })

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	engine, err := supervisor.NewRuleEngineFromBytes([]byte(`
rules:
  findings_have_path:
    applies_to: [reviewer.*]
    check: all_findings_have(["path"])
    message: "findings must have path"
  findings_have_line:
    applies_to: [reviewer.*]
    check: all_findings_have(["line"])
    message: "findings must have line"
`))
	if err != nil {
		t.Fatalf("NewRuleEngineFromBytes: %v", err)
	}

	d := &Dispatcher{
		RoleDir:    filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:  filepath.Join(repoRoot, "coding"),
		Agent:      agent.NewCliAgent(mockBin),
		DB:         db,
		Supervisor: engine,
		MaxRetries: 3,
	}

	ctx := context.Background()
	result, err := d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	// Should have retried once, then passed.
	if result.RetryCount != 1 {
		t.Errorf("retry count = %d, want 1", result.RetryCount)
	}
	if result.SupervisorVerdict == nil {
		t.Fatal("expected supervisor verdict")
	}
	if !result.SupervisorVerdict.Pass {
		t.Error("expected final verdict to pass")
	}
	// Final findings should be the good ones (2 findings with path/line).
	if len(result.Findings) != 2 {
		t.Errorf("findings count = %d, want 2", len(result.Findings))
	}

	// Verify events include supervisor.verdict and supervisor.retry.
	events, err := store.NewEventStore(db).ListEvents(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var verdictCount, retryCount int
	for _, e := range events {
		switch e.Kind {
		case core.EventSupervisorVerdict:
			verdictCount++
		case core.EventSupervisorRetry:
			retryCount++
		}
	}
	// 2 verdicts: one for failed attempt, one for passing attempt.
	if verdictCount != 2 {
		t.Errorf("supervisor.verdict events = %d, want 2", verdictCount)
	}
	// 1 retry event (between attempt 0 and attempt 1).
	if retryCount != 1 {
		t.Errorf("supervisor.retry events = %d, want 1", retryCount)
	}
}

func TestOrchestrate_WithSupervisor_MaxRetriesExhausted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex-bad-findings")
	if _, err := os.Stat(mockBin); err != nil {
		t.Fatalf("mock binary not found: %v", err)
	}

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	engine, err := supervisor.NewRuleEngineFromBytes([]byte(`
rules:
  findings_have_path:
    applies_to: [reviewer.*]
    check: all_findings_have(["path"])
    message: "findings must have a non-empty path"
  findings_have_line:
    applies_to: [reviewer.*]
    check: all_findings_have(["line"])
    message: "findings must have a non-zero line"
`))
	if err != nil {
		t.Fatalf("NewRuleEngineFromBytes: %v", err)
	}

	d := &Dispatcher{
		RoleDir:    filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:  filepath.Join(repoRoot, "coding"),
		Agent:      agent.NewCliAgent(mockBin),
		DB:         db,
		Supervisor: engine,
	}

	ctx := context.Background()
	result, err := d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	if result.RetryCount != DefaultMaxRetries {
		t.Errorf("retry count = %d, want %d", result.RetryCount, DefaultMaxRetries)
	}
	if result.SupervisorVerdict == nil {
		t.Fatal("expected supervisor verdict")
	}
	if result.SupervisorVerdict.Pass {
		t.Error("expected final verdict to fail after max retries exhausted")
	}

	events, err := store.NewEventStore(db).ListEvents(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var verdictCount, retryCount, breachCount int
	var breachPayload struct {
		JobID       string   `json:"job_id"`
		Role        string   `json:"role"`
		FailedRules []string `json:"failed_rules"`
		Attempts    int      `json:"attempts"`
	}
	for _, e := range events {
		switch e.Kind {
		case core.EventSupervisorVerdict:
			verdictCount++
		case core.EventSupervisorRetry:
			retryCount++
		case core.EventComplianceBreach:
			breachCount++
			if err := json.Unmarshal([]byte(e.Payload), &breachPayload); err != nil {
				t.Fatalf("unmarshal compliance-breach payload: %v", err)
			}
		}
	}
	if verdictCount != DefaultMaxRetries+1 {
		t.Errorf("supervisor.verdict events = %d, want %d", verdictCount, DefaultMaxRetries+1)
	}
	if retryCount != DefaultMaxRetries {
		t.Errorf("supervisor.retry events = %d, want %d", retryCount, DefaultMaxRetries)
	}
	if breachCount != 1 {
		t.Errorf("compliance-breach events = %d, want 1", breachCount)
	}
	if breachPayload.JobID != result.JobID {
		t.Errorf("compliance-breach job_id = %q, want %q", breachPayload.JobID, result.JobID)
	}
	if breachPayload.Role != "reviewer.arch" {
		t.Errorf("compliance-breach role = %q, want %q", breachPayload.Role, "reviewer.arch")
	}
	if breachPayload.Attempts != DefaultMaxRetries+1 {
		t.Errorf("compliance-breach attempts = %d, want %d", breachPayload.Attempts, DefaultMaxRetries+1)
	}
	if len(breachPayload.FailedRules) != 2 {
		t.Fatalf("compliance-breach failed_rules len = %d, want 2", len(breachPayload.FailedRules))
	}

	runStore := store.NewRunStore(db, store.NewEventStore(db))
	run, err := runStore.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.State != core.RunStateFailed {
		t.Errorf("run state = %q, want %q", run.State, core.RunStateFailed)
	}

	jobStore := store.NewJobStore(db, store.NewEventStore(db))
	job, err := jobStore.GetJob(ctx, result.JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.State != core.JobStateFailed {
		t.Errorf("job state = %q, want %q", job.State, core.JobStateFailed)
	}
	if job.DispatchedBy != "supervisor-retry" {
		t.Errorf("dispatched_by = %q, want %q", job.DispatchedBy, "supervisor-retry")
	}
}

func TestOrchestrate_WithSupervisor_NoApplicableRules(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex")
	if _, err := os.Stat(mockBin); err != nil {
		t.Fatalf("mock binary not found: %v", err)
	}

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	engine, err := supervisor.NewRuleEngineFromBytes([]byte(`
rules:
  dev_rule:
    applies_to: [developer]
    check: exit_code_is(99)
    message: "developer must exit 99"
`))
	if err != nil {
		t.Fatalf("NewRuleEngineFromBytes: %v", err)
	}

	d := &Dispatcher{
		RoleDir:    filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:  filepath.Join(repoRoot, "coding"),
		Agent:      agent.NewCliAgent(mockBin),
		DB:         db,
		Supervisor: engine,
	}

	ctx := context.Background()
	result, err := d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	if result.SupervisorVerdict == nil {
		t.Fatal("expected supervisor verdict")
	}
	if !result.SupervisorVerdict.Pass {
		t.Error("expected pass when no rules apply to role")
	}
	if result.RetryCount != 0 {
		t.Errorf("retry count = %d, want 0", result.RetryCount)
	}

	events, err := store.NewEventStore(db).ListEvents(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var verdictCount, retryCount, breachCount int
	for _, e := range events {
		switch e.Kind {
		case core.EventSupervisorVerdict:
			verdictCount++
		case core.EventSupervisorRetry:
			retryCount++
		case core.EventComplianceBreach:
			breachCount++
		}
	}
	if verdictCount != 1 {
		t.Errorf("supervisor.verdict events = %d, want 1", verdictCount)
	}
	if retryCount != 0 {
		t.Errorf("supervisor.retry events = %d, want 0", retryCount)
	}
	if breachCount != 0 {
		t.Errorf("compliance-breach events = %d, want 0", breachCount)
	}

	runStore := store.NewRunStore(db, store.NewEventStore(db))
	run, err := runStore.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.State != core.RunStateCompleted {
		t.Errorf("run state = %q, want %q", run.State, core.RunStateCompleted)
	}

	jobStore := store.NewJobStore(db, store.NewEventStore(db))
	job, err := jobStore.GetJob(ctx, result.JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.State != core.JobStateComplete {
		t.Errorf("job state = %q, want %q", job.State, core.JobStateComplete)
	}
}

func TestOrchestrate_MissingRequiredInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex")

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	d := &Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     agent.NewCliAgent(mockBin),
		DB:        db,
	}

	ctx := context.Background()
	_, err = d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			// Missing spec_path.
		},
	})
	if err == nil {
		t.Error("expected error for missing required input, got nil")
	}
}

func TestOrchestrate_InvalidRole(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	d := &Dispatcher{
		RoleDir:   "/nonexistent",
		PromptDir: "/nonexistent",
		Agent:     agent.NewCliAgent("/bin/true"),
		DB:        db,
	}

	ctx := context.Background()
	_, err = d.Orchestrate(ctx, &DispatchInput{
		RoleName: "nonexistent.role",
		Inputs:   map[string]string{},
	})
	if err == nil {
		t.Error("expected error for invalid role, got nil")
	}
}

func TestSnakeToPascal(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"diff_path", "DiffPath"},
		{"spec_path", "SpecPath"},
		{"simple", "Simple"},
		{"a_b_c", "ABC"},
	}
	for _, tt := range tests {
		got := snakeToPascal(tt.input)
		if got != tt.want {
			t.Errorf("snakeToPascal(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
