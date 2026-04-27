package phaseloop

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// stubOrchestrator is a test double for Orchestrator that allows configuring
// per-role responses via a function.
type stubOrchestrator struct {
	mu sync.Mutex

	// fn is called for each Orchestrate invocation. If nil, returns an empty
	// DispatchResult with ExitCode 0.
	fn func(role string, attempt int) (*coding.DispatchResult, error)

	// callCount tracks total Orchestrate invocations.
	callCount int

	// roleCounts tracks invocations per role name.
	roleCounts map[string]int
}

func newStubOrchestrator(fn func(role string, attempt int) (*coding.DispatchResult, error)) *stubOrchestrator {
	return &stubOrchestrator{
		fn:         fn,
		roleCounts: make(map[string]int),
	}
}

func (s *stubOrchestrator) Orchestrate(_ context.Context, input *coding.DispatchInput) (*coding.DispatchResult, error) {
	s.mu.Lock()
	s.callCount++
	s.roleCounts[input.RoleName]++
	attempt := s.roleCounts[input.RoleName]
	fn := s.fn
	s.mu.Unlock()

	if fn != nil {
		return fn(input.RoleName, attempt)
	}
	return &coding.DispatchResult{ExitCode: 0}, nil
}

// openTestDB opens an in-memory SQLite database for testing.
func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open(:memory:): %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newTestExecutor creates a PhaseExecutor wired to the given stub and in-memory DB.
func newTestExecutor(t *testing.T, orch Orchestrator, policy *core.Policy) *PhaseExecutor {
	t.Helper()
	db := openTestDB(t)
	eventStore := store.NewEventStore(db)
	return &PhaseExecutor{
		Dispatcher: orch,
		EventStore: eventStore,
		Policy:     policy,
	}
}

// --- Tests ---

func TestPhaseExecutor_CleanOnFirstPass(t *testing.T) {
	// All roles return no findings and exit 0 → phase is clean without any fix cycle.
	orch := newStubOrchestrator(nil)
	exec := newTestExecutor(t, orch, nil)

	result, err := exec.Execute(context.Background(), "run-1", 100, 0, "SQLite schema", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Clean {
		t.Error("expected Clean=true when no findings")
	}
	if result.FixCycles != 0 {
		t.Errorf("expected FixCycles=0, got %d", result.FixCycles)
	}
	if !result.TestsPassed {
		t.Error("expected TestsPassed=true when all exit codes are 0")
	}

	// developer + 3 reviewers dispatched (developer:1, reviewer.arch:1, reviewer.frontend:1, tester:1).
	if orch.callCount != 4 {
		t.Errorf("expected 4 dispatches, got %d", orch.callCount)
	}
}

func TestPhaseExecutor_FixCycleThenClean(t *testing.T) {
	// First developer pass → reviewer.arch emits a finding.
	// Second developer pass (fix cycle 1) → no findings → clean.
	var reviewerCallCount int32

	orch := newStubOrchestrator(func(role string, attempt int) (*coding.DispatchResult, error) {
		if role == "reviewer.arch" {
			n := atomic.AddInt32(&reviewerCallCount, 1)
			if n == 1 {
				// First reviewer pass: return one finding.
				return &coding.DispatchResult{
					ExitCode: 0,
					Findings: []core.Finding{
						{
							ID:       "f1",
							JobID:    "job-rev-1",
							Path:     "core/run.go",
							Line:     10,
							Severity: core.SeverityImportant,
							Body:     "missing error check",
						},
					},
				}, nil
			}
			// Second reviewer pass: clean.
			return &coding.DispatchResult{ExitCode: 0}, nil
		}
		return &coding.DispatchResult{ExitCode: 0}, nil
	})

	exec := newTestExecutor(t, orch, nil)

	result, err := exec.Execute(context.Background(), "run-2", 101, 0, "event intake", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Clean {
		t.Errorf("expected Clean=true after fix cycle, findings: %v", result.Findings)
	}
	if result.FixCycles != 1 {
		t.Errorf("expected FixCycles=1, got %d", result.FixCycles)
	}

	// Dispatches: dev(1) + rev.arch(1) + rev.frontend(1) + tester(1) +
	//             dev(2) + rev.arch(2) + rev.frontend(2) + tester(2) = 8
	if orch.callCount != 8 {
		t.Errorf("expected 8 dispatches, got %d", orch.callCount)
	}
}

func TestPhaseExecutor_ExhaustFixCycles(t *testing.T) {
	// reviewer.arch always returns the same finding → fix-loop never converges.
	persistentFinding := core.Finding{
		ID:       "f-persistent",
		JobID:    "job-x",
		Path:     "core/run.go",
		Line:     5,
		Severity: core.SeverityCritical,
		Body:     "persistent issue",
	}

	orch := newStubOrchestrator(func(role string, _ int) (*coding.DispatchResult, error) {
		if role == "reviewer.arch" {
			return &coding.DispatchResult{
				ExitCode: 0,
				Findings: []core.Finding{persistentFinding},
			}, nil
		}
		return &coding.DispatchResult{ExitCode: 0}, nil
	})

	policy := &core.Policy{
		SupervisorLimits: core.SupervisorLimits{MaxFixCyclesPerPhase: 2},
	}
	exec := newTestExecutor(t, orch, policy)

	result, err := exec.Execute(context.Background(), "run-3", 102, 1, "worker shell-out", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Clean {
		t.Error("expected Clean=false when fix-loop is exhausted")
	}
	if result.FixCycles != 2 {
		t.Errorf("expected FixCycles=2 (max), got %d", result.FixCycles)
	}
	if len(result.Findings) == 0 {
		t.Error("expected remaining findings in result")
	}
}

func TestPhaseExecutor_TestFailureBlocksClean(t *testing.T) {
	// No findings but tester exits 1 → phase is not clean.
	orch := newStubOrchestrator(func(role string, _ int) (*coding.DispatchResult, error) {
		if role == "tester" {
			return &coding.DispatchResult{ExitCode: 1}, nil
		}
		return &coding.DispatchResult{ExitCode: 0}, nil
	})

	policy := &core.Policy{
		SupervisorLimits: core.SupervisorLimits{MaxFixCyclesPerPhase: 1},
	}
	exec := newTestExecutor(t, orch, policy)

	result, err := exec.Execute(context.Background(), "run-4", 103, 0, "dispatch", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Clean {
		t.Error("expected Clean=false when tester fails")
	}
	if result.TestsPassed {
		t.Error("expected TestsPassed=false when tester exits non-zero")
	}
}

func TestPhaseExecutor_DeveloperDispatchError(t *testing.T) {
	// developer dispatch fails → Execute returns an error.
	orch := newStubOrchestrator(func(role string, _ int) (*coding.DispatchResult, error) {
		if role == "developer" {
			return nil, fmt.Errorf("agent crashed")
		}
		return &coding.DispatchResult{ExitCode: 0}, nil
	})

	exec := newTestExecutor(t, orch, nil)

	_, err := exec.Execute(context.Background(), "run-5", 104, 0, "phase", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err == nil {
		t.Fatal("expected error when developer dispatch fails")
	}
}

func TestPhaseExecutor_ReviewerDispatchError(t *testing.T) {
	// reviewer.arch dispatch fails → Execute returns an error.
	orch := newStubOrchestrator(func(role string, _ int) (*coding.DispatchResult, error) {
		if role == "reviewer.arch" {
			return nil, fmt.Errorf("reviewer unavailable")
		}
		return &coding.DispatchResult{ExitCode: 0}, nil
	})

	exec := newTestExecutor(t, orch, nil)

	_, err := exec.Execute(context.Background(), "run-6", 105, 0, "phase", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err == nil {
		t.Fatal("expected error when reviewer dispatch fails")
	}
}

func TestPhaseExecutor_DedupeAcrossReviewers(t *testing.T) {
	// Two reviewers return the same finding (same fingerprint, distinct JobIDs).
	// After dedup the result should have 1 finding with 2 source job IDs.

	orch := newStubOrchestrator(func(role string, attempt int) (*coding.DispatchResult, error) {
		switch role {
		case "reviewer.arch":
			return &coding.DispatchResult{
				ExitCode: 0,
				Findings: []core.Finding{
					{
						JobID:    "job-arch-1",
						Path:     "core/run.go",
						Line:     15,
						Severity: core.SeverityImportant,
						Body:     "shared finding",
					},
				},
			}, nil
		case "reviewer.frontend":
			return &coding.DispatchResult{
				ExitCode: 0,
				Findings: []core.Finding{
					{
						JobID:    "job-frontend-1",
						Path:     "core/run.go",
						Line:     15,
						Severity: core.SeverityImportant,
						Body:     "shared finding",
					},
				},
			}, nil
		default:
			return &coding.DispatchResult{ExitCode: 0}, nil
		}
	})

	// max 0 fix cycles so we stop after first pass.
	policy := &core.Policy{
		SupervisorLimits: core.SupervisorLimits{MaxFixCyclesPerPhase: 0},
	}
	exec := newTestExecutor(t, orch, policy)

	result, err := exec.Execute(context.Background(), "run-7", 106, 0, "phase", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Two reviewers produce same fingerprint → deduplicated to 1.
	if len(result.Findings) != 1 {
		t.Errorf("expected 1 deduplicated finding, got %d", len(result.Findings))
	}
	// Both source job IDs preserved.
	if len(result.Findings[0].SourceJobIDs) < 2 {
		t.Errorf("expected at least 2 source job IDs, got %v", result.Findings[0].SourceJobIDs)
	}
}

func TestPhaseExecutor_FeedbackPassedToDevOnFixCycle(t *testing.T) {
	// On the fix cycle, the developer should receive fix_feedback in inputs.
	var capturedInputs []map[string]string

	orchCapture := &capturingOrchestratorForFeedback{
		capturedInputs: &capturedInputs,
	}

	db := openTestDB(t)
	eventStore := store.NewEventStore(db)
	exec := &PhaseExecutor{
		Dispatcher: orchCapture,
		EventStore: eventStore,
		Policy: &core.Policy{
			SupervisorLimits: core.SupervisorLimits{MaxFixCyclesPerPhase: 1},
		},
	}

	_, err := exec.Execute(context.Background(), "run-8", 107, 0, "phase", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Should have two developer dispatches: initial + 1 fix cycle.
	if len(capturedInputs) < 2 {
		t.Fatalf("expected at least 2 developer dispatches, got %d", len(capturedInputs))
	}

	// First dispatch should not have fix_feedback.
	if _, ok := capturedInputs[0]["fix_feedback"]; ok {
		t.Error("first developer dispatch should not have fix_feedback")
	}

	// Second dispatch should have fix_feedback.
	if _, ok := capturedInputs[1]["fix_feedback"]; !ok {
		t.Error("second developer dispatch (fix cycle) should have fix_feedback")
	}
}

// --- Phase 3 (I-3): applies_when tests ---

func TestPhaseExecutor_RoleShouldSkip_NilAppliesWhen(t *testing.T) {
	exec := &PhaseExecutor{}
	role := &core.Role{Name: "reviewer.arch"}
	skip, err := exec.roleShouldSkip(context.Background(), role)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skip {
		t.Error("expected skip=false when AppliesWhen is nil")
	}
}

func TestPhaseExecutor_RoleShouldSkip_EmptyWorkDir(t *testing.T) {
	// WorkDir not set → error, caller logs warning and dispatches anyway.
	exec := &PhaseExecutor{WorkDir: ""}
	role := &core.Role{
		Name:        "reviewer.frontend",
		AppliesWhen: &core.RoleAppliesWhen{ChangesTouch: []string{"web/**"}},
	}
	_, err := exec.roleShouldSkip(context.Background(), role)
	if err == nil {
		t.Error("expected error when WorkDir is empty and applies_when is set")
	}
}

func TestPhaseExecutor_FanOut_RoleNotInDir_DispatchesAnyway(t *testing.T) {
	// When RoleDir is set but role file doesn't exist, fanOut dispatches anyway
	// (graceful degradation).
	orch := newStubOrchestrator(nil)
	exec := newTestExecutor(t, orch, nil)
	exec.RoleDir = t.TempDir() // empty dir — no role files
	exec.ReviewerRoles = []string{"custom.reviewer"}
	exec.TesterRoles = []string{} // disable tester

	_, err := exec.Execute(context.Background(), "run-aw1", 300, 0, "phase", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// developer + custom.reviewer = 2 dispatches (even though role file is missing).
	if orch.roleCounts["custom.reviewer"] != 1 {
		t.Errorf("expected 1 custom.reviewer dispatch, got %d", orch.roleCounts["custom.reviewer"])
	}
}

// --- Phase 2 (I-2): TesterRoles tests ---

func TestPhaseExecutor_TesterNil_UsesDefault(t *testing.T) {
	// TesterRoles=nil → "tester" is dispatched via defaultTesterRoles.
	orch := newStubOrchestrator(nil)
	exec := newTestExecutor(t, orch, nil)
	// TesterRoles is nil by default.

	_, err := exec.Execute(context.Background(), "run-t1", 200, 0, "phase", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// developer + reviewer.arch + reviewer.frontend + tester = 4.
	if orch.roleCounts["tester"] != 1 {
		t.Errorf("expected 1 tester dispatch, got %d", orch.roleCounts["tester"])
	}
}

func TestPhaseExecutor_CustomTesterRoles(t *testing.T) {
	// TesterRoles=["perf-tester"] → perf-tester dispatched, not default tester.
	orch := newStubOrchestrator(nil)
	exec := newTestExecutor(t, orch, nil)
	exec.TesterRoles = []string{"perf-tester"}

	_, err := exec.Execute(context.Background(), "run-t2", 201, 0, "phase", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if orch.roleCounts["perf-tester"] != 1 {
		t.Errorf("expected 1 perf-tester dispatch, got %d", orch.roleCounts["perf-tester"])
	}
	if orch.roleCounts["tester"] != 0 {
		t.Errorf("expected 0 tester dispatches when custom TesterRoles set, got %d", orch.roleCounts["tester"])
	}
}

func TestPhaseExecutor_TesterDisabled_EmptyNonNilSlice(t *testing.T) {
	// TesterRoles=[]string{} (non-nil empty) → no tester dispatch.
	orch := newStubOrchestrator(nil)
	exec := newTestExecutor(t, orch, nil)
	exec.TesterRoles = []string{} // non-nil empty = disabled

	_, err := exec.Execute(context.Background(), "run-t3", 202, 0, "phase", map[string]string{
		"diff_path": "/tmp/test.diff",
		"spec_path": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if orch.roleCounts["tester"] != 0 {
		t.Errorf("expected 0 tester dispatches when disabled, got %d", orch.roleCounts["tester"])
	}
	// developer + reviewer.arch + reviewer.frontend = 3 only.
	if orch.callCount != 3 {
		t.Errorf("expected 3 dispatches (no tester), got %d", orch.callCount)
	}
}

// capturingOrchestratorForFeedback captures developer inputs across calls.
type capturingOrchestratorForFeedback struct {
	capturedInputs *[]map[string]string
	devCallCount   int
}

func (c *capturingOrchestratorForFeedback) Orchestrate(_ context.Context, input *coding.DispatchInput) (*coding.DispatchResult, error) {
	if input.RoleName == "developer" {
		c.devCallCount++
		// Copy inputs map for capture.
		captured := make(map[string]string)
		for k, v := range input.Inputs {
			captured[k] = v
		}
		*c.capturedInputs = append(*c.capturedInputs, captured)

		// First developer call: return a finding so the fix cycle triggers.
		if c.devCallCount == 1 {
			return &coding.DispatchResult{
				ExitCode: 0,
				Findings: []core.Finding{
					{
						ID:       "cap-f1",
						JobID:    "cap-job-1",
						Path:     "x.go",
						Line:     1,
						Severity: core.SeverityMinor,
						Body:     "issue to fix",
					},
				},
			}, nil
		}
		// Subsequent developer calls: clean.
		return &coding.DispatchResult{ExitCode: 0}, nil
	}

	// Reviewers: on first pass, also return the same finding so fix cycle triggers.
	// On second pass (after fix), return clean.
	return &coding.DispatchResult{ExitCode: 0}, nil
}
