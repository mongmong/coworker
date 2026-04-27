package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding/supervisor"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// warnPolicy returns a minimal Policy with on_undeclared=warn.
// Use this in tests that exercise supervisor/retry logic but don't test
// permission enforcement — it prevents permission hard-fails when the mock
// binary basename doesn't match the role's allowed_tools.
func warnPolicy() *core.Policy {
	return &core.Policy{
		PermissionPolicy: core.PermissionPolicy{OnUndeclared: "warn"},
	}
}

type stubJobHandle struct {
	wait func(ctx context.Context) (*core.JobResult, error)
}

func (h stubJobHandle) Wait(ctx context.Context) (*core.JobResult, error) {
	return h.wait(ctx)
}

func (stubJobHandle) Cancel() error {
	return nil
}

type countingAgent struct {
	dispatches int
	wait       func(ctx context.Context, job *core.Job, prompt string) (*core.JobResult, error)
}

func (a *countingAgent) Dispatch(ctx context.Context, job *core.Job, prompt string) (core.JobHandle, error) {
	a.dispatches++
	return stubJobHandle{
		wait: func(waitCtx context.Context) (*core.JobResult, error) {
			return a.wait(waitCtx, job, prompt)
		},
	}, nil
}

type captureSupervisorWriter struct {
	rows []core.RuleResult
}

func (c *captureSupervisorWriter) RecordVerdict(_ context.Context, _ string, _ string, r core.RuleResult) error {
	c.rows = append(c.rows, r)
	return nil
}

type failingSupervisorWriter struct{}

func (failingSupervisorWriter) RecordVerdict(context.Context, string, string, core.RuleResult) error {
	return fmt.Errorf("writer failed")
}

type staticSupervisor struct {
	verdict *core.SupervisorVerdict
}

func (s staticSupervisor) Evaluate(*supervisor.EvalContext) (*core.SupervisorVerdict, error) {
	return s.verdict, nil
}

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

func TestOrchestrate_PersistsSupervisorRuleResults(t *testing.T) {
	repoRoot := findRepoRoot(t)
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	writer := &captureSupervisorWriter{}
	d := &Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent: &countingAgent{wait: func(context.Context, *core.Job, string) (*core.JobResult, error) {
			return &core.JobResult{ExitCode: 0}, nil
		}},
		DB: db,
		Supervisor: staticSupervisor{verdict: &core.SupervisorVerdict{
			Pass: true,
			Results: []core.RuleResult{
				{RuleName: "rule-a", Passed: true, Message: "ok"},
				{RuleName: "rule-b", Passed: true, Message: "ok"},
			},
		}},
		SupervisorWriter: writer,
		Policy:           warnPolicy(),
	}

	if _, err := d.Orchestrate(context.Background(), &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	}); err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}
	if len(writer.rows) != 2 {
		t.Fatalf("captured rule results = %d, want 2", len(writer.rows))
	}
}

func TestOrchestrate_SupervisorWriterFailureDoesNotFailDispatch(t *testing.T) {
	repoRoot := findRepoRoot(t)
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	d := &Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent: &countingAgent{wait: func(context.Context, *core.Job, string) (*core.JobResult, error) {
			return &core.JobResult{ExitCode: 0}, nil
		}},
		DB: db,
		Supervisor: staticSupervisor{verdict: &core.SupervisorVerdict{
			Pass:    true,
			Results: []core.RuleResult{{RuleName: "rule-a", Passed: true}},
		}},
		SupervisorWriter: failingSupervisorWriter{},
		Policy:           warnPolicy(),
	}

	if _, err := d.Orchestrate(context.Background(), &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	}); err != nil {
		t.Fatalf("Orchestrate: %v", err)
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
		RoleDir:          filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:        filepath.Join(repoRoot, "coding"),
		Agent:            agent.NewCliAgent(mockBin),
		DB:               db,
		Supervisor:       engine,
		SupervisorWriter: store.NewSupervisorEventStore(db, store.NewEventStore(db)),
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
		RoleDir:          filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:        filepath.Join(repoRoot, "coding"),
		Agent:            agent.NewCliAgent(mockBin),
		DB:               db,
		Supervisor:       engine,
		SupervisorWriter: store.NewSupervisorEventStore(db, store.NewEventStore(db)),
		MaxRetries:       3,
		Policy:           warnPolicy(), // mock binary basename differs from allowed_tools; test supervisor logic only
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
	// 4 verdicts: two rule results on the failed attempt, two on the passing attempt.
	if verdictCount != 4 {
		t.Errorf("supervisor.verdict events = %d, want 4", verdictCount)
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
		RoleDir:          filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:        filepath.Join(repoRoot, "coding"),
		Agent:            agent.NewCliAgent(mockBin),
		DB:               db,
		Supervisor:       engine,
		SupervisorWriter: store.NewSupervisorEventStore(db, store.NewEventStore(db)),
		Policy:           warnPolicy(), // mock binary basename differs from allowed_tools; test supervisor logic only
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
	wantVerdicts := 2 * (DefaultMaxRetries + 1)
	if verdictCount != wantVerdicts {
		t.Errorf("supervisor.verdict events = %d, want %d", verdictCount, wantVerdicts)
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
	if verdictCount != 0 {
		t.Errorf("supervisor.verdict events = %d, want 0", verdictCount)
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

func TestOrchestrate_CanceledBeforeRetry(t *testing.T) {
	repoRoot := findRepoRoot(t)

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	engine, err := supervisor.NewRuleEngineFromBytes([]byte(`
rules:
  exit_zero:
    applies_to: [reviewer.*]
    check: exit_code_is(0)
    message: "reviewer must exit zero"
`))
	if err != nil {
		t.Fatalf("NewRuleEngineFromBytes: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	agent := &countingAgent{
		wait: func(ctx context.Context, job *core.Job, prompt string) (*core.JobResult, error) {
			cancel()
			return &core.JobResult{
				ExitCode: 1,
			}, nil
		},
	}

	d := &Dispatcher{
		RoleDir:    filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:  filepath.Join(repoRoot, "coding"),
		Agent:      agent,
		DB:         db,
		Supervisor: engine,
		MaxRetries: 3,
	}

	result, err := d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if result != nil {
		t.Fatalf("result = %#v, want nil", result)
	}
	if err != context.Canceled {
		t.Fatalf("Orchestrate error = %v, want %v", err, context.Canceled)
	}
	if agent.dispatches != 1 {
		t.Fatalf("dispatches = %d, want 1", agent.dispatches)
	}
}

// ---- Permission enforcement tests -------------------------------------------

// makeMockDispatcher returns a Dispatcher wired to a namedCountingAgent whose
// BinaryBasename returns binaryBasename. The default RoleDir points to
// coding/roles so tests that override it must set d.RoleDir after construction.
func makeMockDispatcher(t *testing.T, binaryBasename string, db *store.DB, policy *core.Policy, attnStore *store.AttentionStore) (*Dispatcher, *countingAgent) {
	t.Helper()
	repoRoot := findRepoRoot(t)

	ca := &countingAgent{
		wait: func(_ context.Context, _ *core.Job, _ string) (*core.JobResult, error) {
			return &core.JobResult{ExitCode: 0}, nil
		},
	}
	wrapped := &namedCountingAgent{countingAgent: ca, name: binaryBasename}

	d := &Dispatcher{
		RoleDir:        filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:      filepath.Join(repoRoot, "coding"),
		Agent:          wrapped,
		DB:             db,
		Policy:         policy,
		AttentionStore: attnStore,
	}
	return d, ca
}

// namedCountingAgent wraps countingAgent with a BinaryBasename method.
type namedCountingAgent struct {
	*countingAgent
	name string
}

func (a *namedCountingAgent) BinaryBasename() string { return a.name }

func TestOrchestrate_PermissionAllow(t *testing.T) {
	// Role reviewer.arch has "bash:codex" in allowed_tools. Agent binary is "codex".
	// Expect: dispatch succeeds.
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	d, ca := makeMockDispatcher(t, "codex", db, &core.Policy{
		PermissionPolicy: core.PermissionPolicy{OnUndeclared: "deny"},
	}, nil)
	d.RoleDir = filepath.Join(repoRoot, "coding", "roles")

	_, err = d.Orchestrate(context.Background(), &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if ca.dispatches != 1 {
		t.Errorf("dispatches = %d, want 1", ca.dispatches)
	}
}

func TestOrchestrate_PermissionHardDeny(t *testing.T) {
	// reviewer.arch has "bash:rm" in never. If agent binary is "rm" it should hard-fail.
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	repoRoot := findRepoRoot(t)
	d, ca := makeMockDispatcher(t, "rm", db, &core.Policy{
		PermissionPolicy: core.PermissionPolicy{OnUndeclared: "deny"},
	}, nil)
	d.RoleDir = filepath.Join(repoRoot, "coding", "roles")

	_, err = d.Orchestrate(context.Background(), &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err == nil {
		t.Fatal("expected permission error, got nil")
	}
	if !strings.Contains(err.Error(), "never") {
		t.Errorf("error should mention 'never', got: %v", err)
	}
	// No subprocess should have been started.
	if ca.dispatches != 0 {
		t.Errorf("dispatches = %d, want 0 (no subprocess should start)", ca.dispatches)
	}
}

func TestOrchestrate_PermissionUndeclaredDeny(t *testing.T) {
	// Agent binary is "something-not-listed". Policy on_undeclared=deny (default).
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	repoRoot := findRepoRoot(t)
	d, ca := makeMockDispatcher(t, "something-not-listed", db, &core.Policy{
		PermissionPolicy: core.PermissionPolicy{OnUndeclared: "deny"},
	}, nil)
	d.RoleDir = filepath.Join(repoRoot, "coding", "roles")

	_, err = d.Orchestrate(context.Background(), &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err == nil {
		t.Fatal("expected permission error, got nil")
	}
	if !strings.Contains(err.Error(), "undeclared") {
		t.Errorf("error should mention 'undeclared', got: %v", err)
	}
	if ca.dispatches != 0 {
		t.Errorf("dispatches = %d, want 0", ca.dispatches)
	}
}

func TestOrchestrate_PermissionUndeclaredWarn(t *testing.T) {
	// Agent binary is "something-not-listed". Policy on_undeclared=warn → proceeds.
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	repoRoot := findRepoRoot(t)
	d, ca := makeMockDispatcher(t, "something-not-listed", db, &core.Policy{
		PermissionPolicy: core.PermissionPolicy{OnUndeclared: "warn"},
	}, nil)
	d.RoleDir = filepath.Join(repoRoot, "coding", "roles")

	_, err = d.Orchestrate(context.Background(), &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err != nil {
		t.Fatalf("expected success with warn policy, got: %v", err)
	}
	if ca.dispatches != 1 {
		t.Errorf("dispatches = %d, want 1", ca.dispatches)
	}
}

func TestOrchestrate_PermissionRequiresHuman_WithAttentionStore(t *testing.T) {
	// Build a custom role (in a temp dir) with "bash:special" in requires_human.
	// Expect: attention.permission item created AND hard-fail returned.
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Write a custom role YAML to a temp directory.
	tmpRoleDir := t.TempDir()
	tmpPromptDir := t.TempDir()
	roleYAML := `
name: testrole
concurrency: single
cli: special
prompt_template: prompt.md
inputs:
  required:
    - some_input
permissions:
  allowed_tools:
    - read
  never: []
  requires_human:
    - "bash:special"
`
	if writeErr := os.WriteFile(filepath.Join(tmpRoleDir, "testrole.yaml"), []byte(roleYAML), 0o600); writeErr != nil {
		t.Fatalf("write role: %v", writeErr)
	}
	// Write a minimal prompt template.
	if writeErr := os.WriteFile(filepath.Join(tmpPromptDir, "prompt.md"), []byte("hello {{.SomeInput}}"), 0o600); writeErr != nil {
		t.Fatalf("write prompt: %v", writeErr)
	}

	attnStore := store.NewAttentionStore(db)

	// We also need a run row so the attention FK doesn't fail. The Dispatcher
	// creates the run before the permission check, so FK constraint is satisfied.
	ca := &countingAgent{
		wait: func(ctx context.Context, _ *core.Job, _ string) (*core.JobResult, error) {
			return &core.JobResult{ExitCode: 0}, nil
		},
	}
	wrapped := &namedCountingAgent{countingAgent: ca, name: "special"}

	d := &Dispatcher{
		RoleDir:        tmpRoleDir,
		PromptDir:      tmpPromptDir,
		Agent:          wrapped,
		DB:             db,
		Policy:         &core.Policy{PermissionPolicy: core.PermissionPolicy{OnUndeclared: "deny"}},
		AttentionStore: attnStore,
	}

	_, err = d.Orchestrate(context.Background(), &DispatchInput{
		RoleName: "testrole",
		Inputs:   map[string]string{"some_input": "value"},
	})
	if err == nil {
		t.Fatal("expected requires_human hard-fail, got nil")
	}
	if !strings.Contains(err.Error(), "requires human approval") {
		t.Errorf("error should mention 'requires human approval', got: %v", err)
	}

	// Verify the attention.permission item was created by querying all pending items.
	ctx := context.Background()
	pending, listErr := attnStore.ListAllPending(ctx)
	if listErr != nil {
		t.Fatalf("ListAllPending: %v", listErr)
	}
	var permItems []*core.AttentionItem
	for _, item := range pending {
		if item.Kind == core.AttentionPermission {
			permItems = append(permItems, item)
		}
	}
	if len(permItems) == 0 {
		t.Error("expected at least one attention.permission item to be created")
	}

	// No subprocess should have been started.
	if ca.dispatches != 0 {
		t.Errorf("dispatches = %d, want 0", ca.dispatches)
	}
}

// --- Phase 4 (I-4): Supervisor error handling ---

// errorSupervisor is a stub SupervisorEvaluator that always returns an error.
type errorSupervisor struct{ msg string }

func (e *errorSupervisor) Evaluate(ctx *supervisor.EvalContext) (*core.SupervisorVerdict, error) {
	return nil, fmt.Errorf("%s", e.msg)
}

func TestOrchestrate_SupervisorEvalError_FailsJob(t *testing.T) {
	// A supervisor that returns an error should cause the job and run to be
	// marked as failed — not silently passed.
	repoRoot := findRepoRoot(t)

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	ca := &countingAgent{
		wait: func(_ context.Context, _ *core.Job, _ string) (*core.JobResult, error) {
			return &core.JobResult{ExitCode: 0}, nil
		},
	}

	d := &Dispatcher{
		RoleDir:    filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:  filepath.Join(repoRoot, "coding"),
		Agent:      ca,
		DB:         db,
		Supervisor: &errorSupervisor{msg: "rule engine internal error"},
		Policy:     warnPolicy(),
		MaxRetries: -1, // no retries
	}

	ctx := context.Background()
	_, err = d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err == nil {
		t.Fatal("expected error when supervisor returns an error, got nil")
	}
	if !strings.Contains(err.Error(), "supervisor.Evaluate") {
		t.Errorf("expected error to mention supervisor.Evaluate, got: %v", err)
	}
}

func TestOrchestrate_SupervisorEvalError_JobAndRunFailed(t *testing.T) {
	// After supervisor eval error: verify job state=failed and run state=failed
	// are persisted.
	repoRoot := findRepoRoot(t)

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	ca := &countingAgent{
		wait: func(_ context.Context, _ *core.Job, _ string) (*core.JobResult, error) {
			return &core.JobResult{ExitCode: 0}, nil
		},
	}

	d := &Dispatcher{
		RoleDir:    filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:  filepath.Join(repoRoot, "coding"),
		Agent:      ca,
		DB:         db,
		Supervisor: &errorSupervisor{msg: "rule engine boom"},
		Policy:     warnPolicy(),
		MaxRetries: -1,
	}

	ctx := context.Background()
	_, orchestErr := d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if orchestErr == nil {
		t.Fatal("expected error from Orchestrate, got nil")
	}

	// Job state must be "failed".
	es := store.NewEventStore(db)
	jobStore := store.NewJobStore(db, es)
	runStore := store.NewRunStore(db, es)

	// List all jobs via events (we don't know the run ID since Orchestrate failed).
	// Instead, directly query the DB.
	rows, queryErr := db.QueryContext(ctx, "SELECT id, state FROM jobs LIMIT 1")
	if queryErr != nil {
		t.Fatalf("query jobs: %v", queryErr)
	}
	defer rows.Close()
	var jobID, jobState string
	var runID string
	for rows.Next() {
		if scanErr := rows.Scan(&jobID, &jobState); scanErr != nil {
			t.Fatalf("scan job: %v", scanErr)
		}
	}
	if jobState != string(core.JobStateFailed) {
		t.Errorf("job state = %q, want %q", jobState, core.JobStateFailed)
	}

	// Get run_id from the job.
	rowR := db.QueryRowContext(ctx, "SELECT run_id FROM jobs WHERE id = ?", jobID)
	if scanErr := rowR.Scan(&runID); scanErr != nil {
		t.Fatalf("scan run_id from job: %v", scanErr)
	}

	job, getErr := jobStore.GetJob(ctx, jobID)
	if getErr != nil {
		t.Fatalf("GetJob: %v", getErr)
	}
	if job.State != core.JobStateFailed {
		t.Errorf("job state = %q, want %q", job.State, core.JobStateFailed)
	}

	// Run state must be "failed".
	run, getRunErr := runStore.GetRun(ctx, runID)
	if getRunErr != nil {
		t.Fatalf("GetRun: %v", getRunErr)
	}
	if run.State != core.RunStateFailed {
		t.Errorf("run state = %q, want %q", run.State, core.RunStateFailed)
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
