package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	t.Cleanup(func() { db.Close() }) //nolint:errcheck
	return db
}

// newTestLogger creates a logger that discards output (for unit tests).
func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// saveAndRestoreRunFlags saves the current package-level flag values for the
// run command and restores them via t.Cleanup. This avoids cross-test pollution
// when tests that modify flags run sequentially.
func saveAndRestoreRunFlags(t *testing.T) {
	t.Helper()
	origDBPath := runDBPath
	origPolicyPath := runPolicyPath
	origMaxParallelPlans := runMaxParallelPlans
	origNoShip := runNoShip
	origDryRun := runDryRun
	origManifestPath := runManifestPath
	origResume := runResumeAfterAttention
	origRoleDir := runRoleDir
	origPromptDir := runPromptDir
	origCliBinary := runCliBinary
	t.Cleanup(func() {
		runDBPath = origDBPath
		runPolicyPath = origPolicyPath
		runMaxParallelPlans = origMaxParallelPlans
		runNoShip = origNoShip
		runDryRun = origDryRun
		runManifestPath = origManifestPath
		runResumeAfterAttention = origResume
		runRoleDir = origRoleDir
		runPromptDir = origPromptDir
		runCliBinary = origCliBinary
	})
}

// TestRunCommand_MissingPRD tests that a missing PRD file returns an error.
// Not parallel: modifies shared runCmd output writers.
func TestRunCommand_MissingPRD(t *testing.T) {
	saveAndRestoreRunFlags(t)
	// Ensure no manifest bypass is set so PRD validation runs.
	runManifestPath = ""
	runDBPath = filepath.Join(t.TempDir(), "state.db")

	cmd := runCmd
	buf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(errBuf)
	cmd.SetContext(context.Background())
	t.Cleanup(func() {
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})

	err := runAutopilot(cmd, "/nonexistent/path/to/prd.md")
	if err == nil {
		t.Fatal("expected error for missing PRD, got nil")
	}
	if !strings.Contains(err.Error(), "PRD file not found") {
		t.Errorf("expected 'PRD file not found' in error, got: %v", err)
	}
}

// TestRunCommand_DryRunMissingPRD tests --dry-run with a missing PRD.
func TestRunCommand_DryRunMissingPRD(t *testing.T) {
	saveAndRestoreRunFlags(t)
	runDryRun = true
	runManifestPath = ""

	cmd := runCmd
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	t.Cleanup(func() { cmd.SetOut(nil) })

	err := runAutopilot(cmd, "/nonexistent/prd.md")
	if err == nil {
		t.Fatal("expected error for missing PRD in dry-run mode, got nil")
	}
	if !strings.Contains(err.Error(), "PRD file not found") {
		t.Errorf("expected 'PRD file not found' in error, got: %v", err)
	}
}

// TestRunCommand_DryRunValidPRD tests --dry-run with a valid PRD returns no error.
func TestRunCommand_DryRunValidPRD(t *testing.T) {
	saveAndRestoreRunFlags(t)
	runDryRun = true
	runManifestPath = ""

	dir := t.TempDir()
	prdFile := filepath.Join(dir, "prd.md")
	if err := os.WriteFile(prdFile, []byte("# Test PRD\n"), 0o600); err != nil {
		t.Fatalf("write prd file: %v", err)
	}

	cmd := runCmd
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	t.Cleanup(func() { cmd.SetOut(nil) })

	err := runAutopilot(cmd, prdFile)
	if err != nil {
		t.Fatalf("dry-run should succeed with valid PRD, got error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Dry-run mode") {
		t.Errorf("expected 'Dry-run mode' in output, got: %q", output)
	}
}

// TestRunCommand_DryRunWithManifest tests --dry-run + --manifest prints plan list.
func TestRunCommand_DryRunWithManifest(t *testing.T) {
	saveAndRestoreRunFlags(t)
	runDryRun = true

	dir := t.TempDir()

	prdFile := filepath.Join(dir, "prd.md")
	if err := os.WriteFile(prdFile, []byte("# Test PRD\n"), 0o600); err != nil {
		t.Fatalf("write prd file: %v", err)
	}

	manifestContent := `spec_path: docs/specs/001-test.md
plans:
  - id: 1
    title: "Phase 1: foundation"
    phases: ["implement", "review"]
    blocks_on: []
  - id: 2
    title: "Phase 2: features"
    phases: ["implement"]
    blocks_on: [1]
`
	manifestFile := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestFile, []byte(manifestContent), 0o600); err != nil {
		t.Fatalf("write manifest file: %v", err)
	}

	runManifestPath = manifestFile

	cmd := runCmd
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	t.Cleanup(func() { cmd.SetOut(nil) })

	err := runAutopilot(cmd, prdFile)
	if err != nil {
		t.Fatalf("dry-run with manifest should succeed, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Plan 1") {
		t.Errorf("expected Plan 1 in output, got: %q", output)
	}
	if !strings.Contains(output, "Plan 2") {
		t.Errorf("expected Plan 2 in output, got: %q", output)
	}
	if !strings.Contains(output, "2 plans") {
		t.Errorf("expected '2 plans' in output, got: %q", output)
	}
}

// TestRunCommand_ManifestBypass tests --manifest bypasses architect dispatch.
func TestRunCommand_ManifestBypass(t *testing.T) {
	saveAndRestoreRunFlags(t)

	dir := t.TempDir()

	prdFile := filepath.Join(dir, "prd.md")
	if err := os.WriteFile(prdFile, []byte("# Test PRD\n"), 0o600); err != nil {
		t.Fatalf("write prd file: %v", err)
	}

	manifestContent := `spec_path: docs/specs/001-test.md
plans:
  - id: 1
    title: "Foundation plan"
    phases: ["implement"]
    blocks_on: []
`
	manifestFile := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestFile, []byte(manifestContent), 0o600); err != nil {
		t.Fatalf("write manifest file: %v", err)
	}

	runManifestPath = manifestFile
	runDBPath = filepath.Join(dir, "state.db")

	cmd := runCmd
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(errBuf)
	t.Cleanup(func() {
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})

	err := runAutopilot(cmd, prdFile)
	if err != nil {
		t.Fatalf("--manifest bypass should succeed, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Manifest at") && !strings.Contains(output, "resume-after-attention") {
		t.Errorf("expected checkpoint output with manifest path, got: %q", output)
	}
}

// TestRunCommand_ManifestBypassMissingFile tests --manifest with missing file returns error.
func TestRunCommand_ManifestBypassMissingFile(t *testing.T) {
	saveAndRestoreRunFlags(t)

	dir := t.TempDir()
	prdFile := filepath.Join(dir, "prd.md")
	if err := os.WriteFile(prdFile, []byte("# PRD\n"), 0o600); err != nil {
		t.Fatalf("write prd file: %v", err)
	}

	runManifestPath = "/nonexistent/manifest.yaml"
	runDBPath = filepath.Join(dir, "state.db")

	cmd := runCmd
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	t.Cleanup(func() { cmd.SetOut(nil) })

	err := runAutopilot(cmd, prdFile)
	if err == nil {
		t.Fatal("expected error for missing manifest file, got nil")
	}
	if !strings.Contains(err.Error(), "manifest file not found") {
		t.Errorf("expected 'manifest file not found' in error, got: %v", err)
	}
}

// TestResumeAfterAttention_NotFound tests that an unknown attention ID returns error.
func TestResumeAfterAttention_NotFound(t *testing.T) {
	// Not parallel: shares runCmd writer.
	db := openTestDB(t)
	eventStore := store.NewEventStore(db)
	runStore := store.NewRunStore(db, eventStore)
	attentionStore := store.NewAttentionStore(db)

	cmd := runCmd
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	t.Cleanup(func() { cmd.SetOut(nil) })

	err := resumeAfterAttention(
		context.Background(),
		cmd,
		"nonexistent_id",
		"prd.md",
		db,
		runStore,
		attentionStore,
		eventStore,
		nil,
		newTestLogger(),
	)
	if err == nil {
		t.Fatal("expected error for nonexistent attention ID, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

// TestResumeAfterAttention_Unanswered tests that an unanswered item returns error.
func TestResumeAfterAttention_Unanswered(t *testing.T) {
	// Not parallel: shares runCmd writer.
	db := openTestDB(t)
	eventStore := store.NewEventStore(db)
	runStore := store.NewRunStore(db, eventStore)
	attentionStore := store.NewAttentionStore(db)

	ctx := context.Background()

	// Create a run.
	run := &core.Run{
		ID:        core.NewID(),
		Mode:      "autopilot",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := runStore.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Insert an unanswered attention item.
	item := &core.AttentionItem{
		ID:        core.NewID(),
		RunID:     run.ID,
		Kind:      core.AttentionCheckpoint,
		Source:    "run-command",
		Question:  "spec-approved",
		CreatedAt: time.Now(),
	}
	if err := attentionStore.InsertAttention(ctx, item); err != nil {
		t.Fatalf("insert attention: %v", err)
	}

	cmd := runCmd
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	t.Cleanup(func() { cmd.SetOut(nil) })

	err := resumeAfterAttention(
		ctx,
		cmd,
		item.ID,
		"prd.md",
		db,
		runStore,
		attentionStore,
		eventStore,
		nil,
		newTestLogger(),
	)
	if err == nil {
		t.Fatal("expected error for unanswered attention item, got nil")
	}
	if !strings.Contains(err.Error(), "not yet answered") {
		t.Errorf("expected 'not yet answered' in error, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "pending human review") {
		t.Errorf("expected 'pending human review' in output, got: %q", output)
	}
}

// TestResumeAfterAttention_Rejected tests that a rejected checkpoint aborts the run.
func TestResumeAfterAttention_Rejected(t *testing.T) {
	// Not parallel: shares runCmd writer.
	db := openTestDB(t)
	eventStore := store.NewEventStore(db)
	runStore := store.NewRunStore(db, eventStore)
	attentionStore := store.NewAttentionStore(db)

	ctx := context.Background()

	// Create a run.
	run := &core.Run{
		ID:        core.NewID(),
		Mode:      "autopilot",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := runStore.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Insert a rejected attention item.
	item := &core.AttentionItem{
		ID:         core.NewID(),
		RunID:      run.ID,
		Kind:       core.AttentionCheckpoint,
		Source:     "run-command",
		Question:   "spec-approved",
		Answer:     core.AttentionAnswerReject,
		AnsweredBy: "human",
		CreatedAt:  time.Now(),
	}
	if err := attentionStore.InsertAttention(ctx, item); err != nil {
		t.Fatalf("insert attention: %v", err)
	}

	cmd := runCmd
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	t.Cleanup(func() { cmd.SetOut(nil) })

	err := resumeAfterAttention(
		ctx,
		cmd,
		item.ID,
		"prd.md",
		db,
		runStore,
		attentionStore,
		eventStore,
		nil,
		newTestLogger(),
	)
	if err == nil {
		t.Fatal("expected error for rejected checkpoint, got nil")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Errorf("expected 'rejected' in error, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "rejected") || !strings.Contains(output, "aborted") {
		t.Errorf("expected abort message in output, got: %q", output)
	}

	// Verify run was marked aborted.
	updated, err := runStore.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.State != core.RunStateAborted {
		t.Errorf("expected run state %q, got %q", core.RunStateAborted, updated.State)
	}
}

// TestResumeAfterAttention_ApprovedNoManifest tests that an approved checkpoint
// without a manifest (and no --manifest flag) returns an error.
func TestResumeAfterAttention_ApprovedNoManifest(t *testing.T) {
	saveAndRestoreRunFlags(t)
	// Ensure no manifest flag is set.
	runManifestPath = ""

	db := openTestDB(t)
	eventStore := store.NewEventStore(db)
	runStore := store.NewRunStore(db, eventStore)
	attentionStore := store.NewAttentionStore(db)

	ctx := context.Background()

	run := &core.Run{
		ID:        core.NewID(),
		Mode:      "autopilot",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := runStore.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	item := &core.AttentionItem{
		ID:         core.NewID(),
		RunID:      run.ID,
		Kind:       core.AttentionCheckpoint,
		Source:     "run-command",
		Question:   "spec-approved",
		Answer:     core.AttentionAnswerApprove,
		AnsweredBy: "human",
		CreatedAt:  time.Now(),
	}
	if err := attentionStore.InsertAttention(ctx, item); err != nil {
		t.Fatalf("insert attention: %v", err)
	}

	cmd := runCmd
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	t.Cleanup(func() { cmd.SetOut(nil) })

	err := resumeAfterAttention(
		ctx,
		cmd,
		item.ID,
		"prd.md",
		db,
		runStore,
		attentionStore,
		eventStore,
		nil,
		newTestLogger(),
	)
	if err == nil {
		t.Fatal("expected error when manifest path cannot be determined, got nil")
	}
	if !strings.Contains(err.Error(), "manifest path") {
		t.Errorf("expected manifest path error, got: %v", err)
	}
}

// TestResumeAfterAttention_ApprovedWithManifest tests the happy path:
// approved checkpoint + valid manifest → enters plan loop.
func TestResumeAfterAttention_ApprovedWithManifest(t *testing.T) {
	saveAndRestoreRunFlags(t)

	dir := t.TempDir()
	db := openTestDB(t)
	eventStore := store.NewEventStore(db)
	runStore := store.NewRunStore(db, eventStore)
	attentionStore := store.NewAttentionStore(db)

	ctx := context.Background()

	run := &core.Run{
		ID:        core.NewID(),
		Mode:      "autopilot",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := runStore.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	item := &core.AttentionItem{
		ID:         core.NewID(),
		RunID:      run.ID,
		Kind:       core.AttentionCheckpoint,
		Source:     "run-command",
		Question:   "spec-approved",
		Answer:     core.AttentionAnswerApprove,
		AnsweredBy: "human",
		CreatedAt:  time.Now(),
	}
	if err := attentionStore.InsertAttention(ctx, item); err != nil {
		t.Fatalf("insert attention: %v", err)
	}

	// Create a manifest file.
	manifestContent := `spec_path: docs/specs/001-test.md
plans:
  - id: 1
    title: "Plan One"
    phases: ["implement"]
    blocks_on: []
`
	manifestFile := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestFile, []byte(manifestContent), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	runManifestPath = manifestFile

	// Use a dedicated output buffer for the plan loop call.
	planBuf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}

	cmd := runCmd
	cmd.SetContext(ctx)
	cmd.SetOut(planBuf)
	cmd.SetErr(errBuf)
	t.Cleanup(func() {
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})

	err := resumeAfterAttention(
		ctx,
		cmd,
		item.ID,
		"prd.md",
		db,
		runStore,
		attentionStore,
		eventStore,
		nil,
		newTestLogger(),
	)
	// Should succeed (plan loop inserts plan-approved checkpoint and returns nil).
	if err != nil {
		t.Fatalf("approved resume should succeed, got: %v", err)
	}

	output := planBuf.String()
	// Should see plan-approved checkpoint output.
	if !strings.Contains(output, "Plan 1") {
		t.Errorf("expected Plan 1 in stdout, got: %q", output)
	}
	if !strings.Contains(output, "resume-after-attention") {
		t.Errorf("expected resume-after-attention prompt in stdout, got: %q", output)
	}
}

// TestReconstructPlanState_EmptyEvents returns empty maps.
func TestReconstructPlanState_EmptyEvents(t *testing.T) {
	t.Parallel()

	completed, active := reconstructPlanState(nil)
	if len(completed) != 0 {
		t.Errorf("expected empty completed map, got %v", completed)
	}
	if len(active) != 0 {
		t.Errorf("expected empty active map, got %v", active)
	}
}

// TestReconstructPlanState_ShippedPlansAreCompleted tests plan.shipped events.
func TestReconstructPlanState_ShippedPlansAreCompleted(t *testing.T) {
	t.Parallel()

	events := []core.Event{
		{
			Kind:    core.EventPlanShipped,
			Payload: `{"plan_id":1,"pr_url":"https://github.com/x/y/pull/1"}`,
		},
		{
			Kind:    core.EventPlanShipped,
			Payload: `{"plan_id":3,"pr_url":"https://github.com/x/y/pull/3"}`,
		},
	}

	completed, active := reconstructPlanState(events)
	if !completed[1] {
		t.Error("plan 1 should be completed")
	}
	if !completed[3] {
		t.Error("plan 3 should be completed")
	}
	if len(active) != 0 {
		t.Errorf("expected no active plans, got %v", active)
	}
}

// TestReconstructPlanState_StartedButNotShipped returns active plans.
func TestReconstructPlanState_StartedButNotShipped(t *testing.T) {
	t.Parallel()

	events := []core.Event{
		{
			Kind:    core.EventPhaseStarted,
			Payload: `{"plan_id":2,"title":"WIP plan"}`,
		},
	}

	completed, active := reconstructPlanState(events)
	if len(completed) != 0 {
		t.Errorf("expected no completed plans, got %v", completed)
	}
	if !active[2] {
		t.Error("plan 2 should be active")
	}
}

// TestReconstructPlanState_ShippedAfterStartedIsCompleted tests that shipped
// plans are removed from active even if started event appeared first.
func TestReconstructPlanState_ShippedAfterStartedIsCompleted(t *testing.T) {
	t.Parallel()

	events := []core.Event{
		{Kind: core.EventPhaseStarted, Payload: `{"plan_id":5}`},
		{Kind: core.EventPlanShipped, Payload: `{"plan_id":5}`},
	}

	completed, active := reconstructPlanState(events)
	if !completed[5] {
		t.Error("plan 5 should be completed after ship event")
	}
	if active[5] {
		t.Error("plan 5 should not be active after ship event")
	}
}

// TestExtractRunManifestPath_HappyPath returns path for manifest kind.
func TestExtractRunManifestPath_HappyPath(t *testing.T) {
	t.Parallel()

	artifacts := []core.Artifact{
		{Kind: "spec", Path: "docs/specs/001-foo.md"},
		{Kind: "manifest", Path: "docs/specs/001-foo-manifest.yaml"},
	}
	got := extractRunManifestPath(artifacts)
	if got != "docs/specs/001-foo-manifest.yaml" {
		t.Errorf("expected manifest path, got %q", got)
	}
}

// TestExtractRunManifestPath_NoManifest returns empty string.
func TestExtractRunManifestPath_NoManifest(t *testing.T) {
	t.Parallel()

	artifacts := []core.Artifact{
		{Kind: "spec", Path: "docs/specs/001-foo.md"},
	}
	got := extractRunManifestPath(artifacts)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// TestDiscoverRunManifestPath_DeriveFromSpec derives manifest from spec artifact.
func TestDiscoverRunManifestPath_DeriveFromSpec(t *testing.T) {
	t.Parallel()

	artifacts := []core.Artifact{
		{Kind: "spec", Path: "docs/specs/001-foo.md"},
	}
	got := discoverRunManifestPath(artifacts)
	if got != "docs/specs/001-foo-manifest.yaml" {
		t.Errorf("expected derived manifest path, got %q", got)
	}
}

// TestDiscoverRunManifestPath_NoSpec returns empty string.
func TestDiscoverRunManifestPath_NoSpec(t *testing.T) {
	t.Parallel()

	got := discoverRunManifestPath(nil)
	if got != "" {
		t.Errorf("expected empty string for nil artifacts, got %q", got)
	}
}

// TestDiscoverManifestFromEvents_Found extracts manifest path from job.completed.
func TestDiscoverManifestFromEvents_Found(t *testing.T) {
	t.Parallel()

	events := []core.Event{
		{Kind: core.EventJobCreated, Payload: `{"job_id":"j1"}`},
		{Kind: core.EventJobCompleted, Payload: `{"job_id":"j1","manifest_path":"docs/specs/001-manifest.yaml"}`},
	}
	got := discoverManifestFromEvents(events)
	if got != "docs/specs/001-manifest.yaml" {
		t.Errorf("expected manifest path, got %q", got)
	}
}

// TestDiscoverManifestFromEvents_NotFound returns empty string.
func TestDiscoverManifestFromEvents_NotFound(t *testing.T) {
	t.Parallel()

	events := []core.Event{
		{Kind: core.EventJobCreated, Payload: `{"job_id":"j1"}`},
		{Kind: core.EventJobCompleted, Payload: `{"job_id":"j1"}`},
	}
	got := discoverManifestFromEvents(events)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// TestRunCommand_Help verifies the run command exposes expected flags.
func TestRunCommand_Help(t *testing.T) {
	buf := &bytes.Buffer{}
	runCmd.SetOut(buf)
	t.Cleanup(func() { runCmd.SetOut(nil) })

	if err := runCmd.Help(); err != nil {
		t.Fatalf("run Help(): %v", err)
	}

	output := buf.String()
	for _, flag := range []string{"--db", "--policy", "--max-parallel-plans", "--no-ship", "--dry-run", "--manifest", "--resume-after-attention"} {
		if !strings.Contains(output, flag) {
			t.Errorf("expected flag %q in help output, got:\n%s", flag, output)
		}
	}
}

// TestLoadRunPolicy_MissingFile returns nil without error.
func TestLoadRunPolicy_MissingFile(t *testing.T) {
	t.Parallel()

	result := loadRunPolicy("/nonexistent/policy.yaml", newTestLogger())
	if result != nil {
		t.Errorf("expected nil for missing policy, got %+v", result)
	}
}

// TestLoadRunPolicy_ValidFile parses concurrency limits.
func TestLoadRunPolicy_ValidFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	policyFile := filepath.Join(dir, "policy.yaml")
	content := `concurrency:
  max_parallel_plans: 3
`
	if err := os.WriteFile(policyFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}

	result := loadRunPolicy(policyFile, newTestLogger())
	if result == nil {
		t.Fatal("expected parsed policy, got nil")
	}
	if result.ConcurrencyLimits.MaxParallelPlans != 3 {
		t.Errorf("expected MaxParallelPlans=3, got %d", result.ConcurrencyLimits.MaxParallelPlans)
	}
}
