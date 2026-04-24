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

// --- helpers -----------------------------------------------------------------

// createQueryTestRunAndJob creates a run + job required by findings/artifacts FKs.
func createQueryTestRunAndJob(t *testing.T, db *store.DB, runID, jobID string) {
	t.Helper()
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)
	js := store.NewJobStore(db, es)
	ctx := context.Background()

	run := &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := rs.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun(%q): %v", runID, err)
	}

	job := &core.Job{
		ID:           jobID,
		RunID:        runID,
		Role:         "reviewer.arch",
		State:        core.JobStatePending,
		DispatchedBy: "scheduler",
		CLI:          "codex",
		StartedAt:    time.Now(),
	}
	if err := js.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob(%q): %v", jobID, err)
	}
}

// newFindingStore creates a FindingStore backed by the given DB.
func newFindingStore(t *testing.T, db *store.DB) *store.FindingStore {
	t.Helper()
	es := store.NewEventStore(db)
	return store.NewFindingStore(db, es)
}

// newArtifactStore creates an ArtifactStore backed by the given DB.
func newQueryArtifactStore(t *testing.T, db *store.DB) *store.ArtifactStore {
	t.Helper()
	es := store.NewEventStore(db)
	return store.NewArtifactStore(db, es)
}

// insertTestFinding is a convenience wrapper that inserts a finding for tests.
func insertTestFinding(t *testing.T, fs *store.FindingStore, id, runID, jobID, path string, line int, severity core.Severity, body string) {
	t.Helper()
	finding := &core.Finding{
		ID:       id,
		RunID:    runID,
		JobID:    jobID,
		Path:     path,
		Line:     line,
		Severity: severity,
		Body:     body,
	}
	if err := fs.InsertFinding(context.Background(), finding); err != nil {
		t.Fatalf("InsertFinding(%q): %v", id, err)
	}
}

// --- orch_findings_list tests ------------------------------------------------

func TestHandleFindingsList_EmptyResult(t *testing.T) {
	db := openTestDB(t)
	fs := newFindingStore(t, db)

	out, err := mcpserver.CallFindingsList(context.Background(), fs, "nonexistent-run")
	if err != nil {
		t.Fatalf("CallFindingsList: %v", err)
	}

	findingsRaw, ok := out["findings"]
	if !ok {
		t.Fatal("findings field missing from output")
	}
	// Must not be nil — should be an empty JSON array.
	b, err := json.Marshal(findingsRaw)
	if err != nil {
		t.Fatalf("marshal findings: %v", err)
	}
	if string(b) == "null" {
		t.Error("findings should be [] not null when empty")
	}
	findings, ok := findingsRaw.([]interface{})
	if !ok {
		t.Fatalf("findings wrong type: %T", findingsRaw)
	}
	if len(findings) != 0 {
		t.Errorf("findings count = %d, want 0", len(findings))
	}
}

func TestHandleFindingsList_MissingRunID(t *testing.T) {
	db := openTestDB(t)
	fs := newFindingStore(t, db)

	_, err := mcpserver.CallFindingsList(context.Background(), fs, "")
	if err == nil {
		t.Fatal("expected error for empty run_id, got nil")
	}
}

func TestHandleFindingsList_HappyPath(t *testing.T) {
	db := openTestDB(t)
	createQueryTestRunAndJob(t, db, "run_fl1", "job_fl1")
	fs := newFindingStore(t, db)

	insertTestFinding(t, fs, "find_fl1", "run_fl1", "job_fl1", "main.go", 42, core.SeverityImportant, "Missing error check")
	insertTestFinding(t, fs, "find_fl2", "run_fl1", "job_fl1", "store.go", 17, core.SeverityMinor, "Use prepared statement")

	out, err := mcpserver.CallFindingsList(context.Background(), fs, "run_fl1")
	if err != nil {
		t.Fatalf("CallFindingsList: %v", err)
	}

	findings, ok := out["findings"].([]interface{})
	if !ok {
		t.Fatalf("findings wrong type: %T", out["findings"])
	}
	if len(findings) != 2 {
		t.Errorf("findings count = %d, want 2", len(findings))
	}
}

func TestHandleFindingsList_FindingFields(t *testing.T) {
	db := openTestDB(t)
	createQueryTestRunAndJob(t, db, "run_fl2", "job_fl2")
	fs := newFindingStore(t, db)

	insertTestFinding(t, fs, "find_fl_fields", "run_fl2", "job_fl2", "pkg/foo.go", 99, core.SeverityCritical, "Nil pointer dereference risk")

	out, err := mcpserver.CallFindingsList(context.Background(), fs, "run_fl2")
	if err != nil {
		t.Fatalf("CallFindingsList: %v", err)
	}

	findings, ok := out["findings"].([]interface{})
	if !ok {
		t.Fatalf("findings wrong type: %T", out["findings"])
	}
	if len(findings) != 1 {
		t.Fatalf("findings count = %d, want 1", len(findings))
	}

	item, ok := findings[0].(map[string]interface{})
	if !ok {
		t.Fatalf("finding item wrong type: %T", findings[0])
	}

	if item["id"] != "find_fl_fields" {
		t.Errorf("id = %q, want %q", item["id"], "find_fl_fields")
	}
	if item["path"] != "pkg/foo.go" {
		t.Errorf("path = %q, want %q", item["path"], "pkg/foo.go")
	}
	if item["line"] != float64(99) {
		t.Errorf("line = %v, want 99", item["line"])
	}
	if item["severity"] != "critical" {
		t.Errorf("severity = %q, want %q", item["severity"], "critical")
	}
	if item["body"] != "Nil pointer dereference risk" {
		t.Errorf("body = %q, want %q", item["body"], "Nil pointer dereference risk")
	}
	if item["fingerprint"] == "" {
		t.Error("fingerprint should not be empty")
	}
	if item["resolved"] != false {
		t.Errorf("resolved = %v, want false", item["resolved"])
	}
}

func TestHandleFindingsList_ResolvedFlag(t *testing.T) {
	db := openTestDB(t)
	createQueryTestRunAndJob(t, db, "run_fl3", "job_fl3")
	fs := newFindingStore(t, db)

	insertTestFinding(t, fs, "find_resolved", "run_fl3", "job_fl3", "main.go", 1, core.SeverityNit, "Typo in comment")
	insertTestFinding(t, fs, "find_open", "run_fl3", "job_fl3", "main.go", 2, core.SeverityMinor, "Missing blank line")

	// Resolve the first finding.
	if err := fs.ResolveFinding(context.Background(), "find_resolved", "fix_job_1"); err != nil {
		t.Fatalf("ResolveFinding: %v", err)
	}

	out, err := mcpserver.CallFindingsList(context.Background(), fs, "run_fl3")
	if err != nil {
		t.Fatalf("CallFindingsList: %v", err)
	}

	findings, ok := out["findings"].([]interface{})
	if !ok {
		t.Fatalf("findings wrong type: %T", out["findings"])
	}
	if len(findings) != 2 {
		t.Fatalf("findings count = %d, want 2", len(findings))
	}

	// Both findings should be returned; one resolved, one not.
	resolvedCount := 0
	for _, f := range findings {
		item := f.(map[string]interface{})
		if item["resolved"] == true {
			resolvedCount++
		}
	}
	if resolvedCount != 1 {
		t.Errorf("resolved count = %d, want 1", resolvedCount)
	}
}

func TestHandleFindingsList_IsolatedByRunID(t *testing.T) {
	db := openTestDB(t)
	createQueryTestRunAndJob(t, db, "run_fl_a", "job_fl_a")
	createQueryTestRunAndJob(t, db, "run_fl_b", "job_fl_b")
	fs := newFindingStore(t, db)

	insertTestFinding(t, fs, "find_a1", "run_fl_a", "job_fl_a", "main.go", 1, core.SeverityMinor, "Finding in run A")
	insertTestFinding(t, fs, "find_b1", "run_fl_b", "job_fl_b", "main.go", 2, core.SeverityMinor, "Finding in run B")

	// List for run A should only return run A's finding.
	outA, err := mcpserver.CallFindingsList(context.Background(), fs, "run_fl_a")
	if err != nil {
		t.Fatalf("CallFindingsList (run A): %v", err)
	}
	findingsA, _ := outA["findings"].([]interface{})
	if len(findingsA) != 1 {
		t.Errorf("run A findings count = %d, want 1", len(findingsA))
	}

	// List for run B should only return run B's finding.
	outB, err := mcpserver.CallFindingsList(context.Background(), fs, "run_fl_b")
	if err != nil {
		t.Fatalf("CallFindingsList (run B): %v", err)
	}
	findingsB, _ := outB["findings"].([]interface{})
	if len(findingsB) != 1 {
		t.Errorf("run B findings count = %d, want 1", len(findingsB))
	}
}

func TestHandleFindingsList_NilDB_ReturnsStub(t *testing.T) {
	s, err := mcpserver.NewServer(mcpserver.ServerConfig{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	tools := s.Tools()
	found := false
	for _, name := range tools {
		if name == "orch_findings_list" {
			found = true
			break
		}
	}
	if !found {
		t.Error("orch_findings_list tool should be registered even without a DB")
	}
}

// --- orch_artifact_read tests ------------------------------------------------

func TestHandleArtifactRead_EmptyResult(t *testing.T) {
	db := openTestDB(t)
	createQueryTestRunAndJob(t, db, "run_ar1", "job_ar1")
	as := newQueryArtifactStore(t, db)

	out, err := mcpserver.CallArtifactRead(context.Background(), as, "job_ar1")
	if err != nil {
		t.Fatalf("CallArtifactRead: %v", err)
	}

	artifactsRaw, ok := out["artifacts"]
	if !ok {
		t.Fatal("artifacts field missing from output")
	}
	// Must not be nil — should be an empty JSON array.
	b, err := json.Marshal(artifactsRaw)
	if err != nil {
		t.Fatalf("marshal artifacts: %v", err)
	}
	if string(b) == "null" {
		t.Error("artifacts should be [] not null when empty")
	}
	artifacts, ok := artifactsRaw.([]interface{})
	if !ok {
		t.Fatalf("artifacts wrong type: %T", artifactsRaw)
	}
	if len(artifacts) != 0 {
		t.Errorf("artifacts count = %d, want 0", len(artifacts))
	}
}

func TestHandleArtifactRead_MissingJobID(t *testing.T) {
	db := openTestDB(t)
	as := newQueryArtifactStore(t, db)

	_, err := mcpserver.CallArtifactRead(context.Background(), as, "")
	if err == nil {
		t.Fatal("expected error for empty job_id, got nil")
	}
}

func TestHandleArtifactRead_HappyPath(t *testing.T) {
	db := openTestDB(t)
	createQueryTestRunAndJob(t, db, "run_ar2", "job_ar2")
	as := newQueryArtifactStore(t, db)

	// Write two artifacts via the write handler.
	if _, err := mcpserver.CallArtifactWrite(context.Background(), as, "job_ar2", "log", ".coworker/runs/run_ar2/jobs/job_ar2.jsonl", "run_ar2"); err != nil {
		t.Fatalf("CallArtifactWrite (log): %v", err)
	}
	if _, err := mcpserver.CallArtifactWrite(context.Background(), as, "job_ar2", "diff", ".coworker/runs/run_ar2/jobs/job_ar2.diff", "run_ar2"); err != nil {
		t.Fatalf("CallArtifactWrite (diff): %v", err)
	}

	out, err := mcpserver.CallArtifactRead(context.Background(), as, "job_ar2")
	if err != nil {
		t.Fatalf("CallArtifactRead: %v", err)
	}

	artifacts, ok := out["artifacts"].([]interface{})
	if !ok {
		t.Fatalf("artifacts wrong type: %T", out["artifacts"])
	}
	if len(artifacts) != 2 {
		t.Errorf("artifacts count = %d, want 2", len(artifacts))
	}
}

func TestHandleArtifactRead_ArtifactFields(t *testing.T) {
	db := openTestDB(t)
	createQueryTestRunAndJob(t, db, "run_ar3", "job_ar3")
	as := newQueryArtifactStore(t, db)

	writeOut, err := mcpserver.CallArtifactWrite(context.Background(), as, "job_ar3", "spec", ".coworker/runs/run_ar3/spec.md", "run_ar3")
	if err != nil {
		t.Fatalf("CallArtifactWrite: %v", err)
	}
	artifactID, ok := writeOut["artifact_id"].(string)
	if !ok || artifactID == "" {
		t.Fatalf("artifact_id missing or wrong type: %T %v", writeOut["artifact_id"], writeOut["artifact_id"])
	}

	out, err := mcpserver.CallArtifactRead(context.Background(), as, "job_ar3")
	if err != nil {
		t.Fatalf("CallArtifactRead: %v", err)
	}

	artifacts, ok := out["artifacts"].([]interface{})
	if !ok {
		t.Fatalf("artifacts wrong type: %T", out["artifacts"])
	}
	if len(artifacts) != 1 {
		t.Fatalf("artifacts count = %d, want 1", len(artifacts))
	}

	item, ok := artifacts[0].(map[string]interface{})
	if !ok {
		t.Fatalf("artifact item wrong type: %T", artifacts[0])
	}

	if item["id"] != artifactID {
		t.Errorf("id = %q, want %q", item["id"], artifactID)
	}
	if item["kind"] != "spec" {
		t.Errorf("kind = %q, want %q", item["kind"], "spec")
	}
	if item["path"] != ".coworker/runs/run_ar3/spec.md" {
		t.Errorf("path = %q, want %q", item["path"], ".coworker/runs/run_ar3/spec.md")
	}
}

func TestHandleArtifactRead_IsolatedByJobID(t *testing.T) {
	db := openTestDB(t)
	createQueryTestRunAndJob(t, db, "run_ar_iso", "job_ar_iso_a")
	createQueryTestRunAndJob(t, db, "run_ar_iso2", "job_ar_iso_b")
	as := newQueryArtifactStore(t, db)

	if _, err := mcpserver.CallArtifactWrite(context.Background(), as, "job_ar_iso_a", "log", "path/a.log", "run_ar_iso"); err != nil {
		t.Fatalf("CallArtifactWrite (job A): %v", err)
	}
	if _, err := mcpserver.CallArtifactWrite(context.Background(), as, "job_ar_iso_b", "log", "path/b.log", "run_ar_iso2"); err != nil {
		t.Fatalf("CallArtifactWrite (job B): %v", err)
	}

	outA, err := mcpserver.CallArtifactRead(context.Background(), as, "job_ar_iso_a")
	if err != nil {
		t.Fatalf("CallArtifactRead (job A): %v", err)
	}
	artsA, _ := outA["artifacts"].([]interface{})
	if len(artsA) != 1 {
		t.Errorf("job A artifacts count = %d, want 1", len(artsA))
	}

	outB, err := mcpserver.CallArtifactRead(context.Background(), as, "job_ar_iso_b")
	if err != nil {
		t.Fatalf("CallArtifactRead (job B): %v", err)
	}
	artsB, _ := outB["artifacts"].([]interface{})
	if len(artsB) != 1 {
		t.Errorf("job B artifacts count = %d, want 1", len(artsB))
	}
}

func TestHandleArtifactRead_NilDB_ReturnsStub(t *testing.T) {
	s, err := mcpserver.NewServer(mcpserver.ServerConfig{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	tools := s.Tools()
	found := false
	for _, name := range tools {
		if name == "orch_artifact_read" {
			found = true
			break
		}
	}
	if !found {
		t.Error("orch_artifact_read tool should be registered even without a DB")
	}
}

// --- orch_artifact_write tests -----------------------------------------------

func TestHandleArtifactWrite_HappyPath(t *testing.T) {
	db := openTestDB(t)
	createQueryTestRunAndJob(t, db, "run_aw1", "job_aw1")
	as := newQueryArtifactStore(t, db)

	out, err := mcpserver.CallArtifactWrite(context.Background(), as, "job_aw1", "log", ".coworker/runs/run_aw1/jobs/job_aw1.jsonl", "run_aw1")
	if err != nil {
		t.Fatalf("CallArtifactWrite: %v", err)
	}

	if out["artifact_id"] == "" {
		t.Error("artifact_id should not be empty")
	}
	if out["status"] != "ok" {
		t.Errorf("status = %q, want %q", out["status"], "ok")
	}
}

func TestHandleArtifactWrite_MissingJobID(t *testing.T) {
	db := openTestDB(t)
	as := newQueryArtifactStore(t, db)

	_, err := mcpserver.CallArtifactWrite(context.Background(), as, "", "log", "path/to/file", "run_x")
	if err == nil {
		t.Fatal("expected error for empty job_id, got nil")
	}
}

func TestHandleArtifactWrite_MissingKind(t *testing.T) {
	db := openTestDB(t)
	as := newQueryArtifactStore(t, db)

	_, err := mcpserver.CallArtifactWrite(context.Background(), as, "job_x", "", "path/to/file", "run_x")
	if err == nil {
		t.Fatal("expected error for empty kind, got nil")
	}
}

func TestHandleArtifactWrite_MissingPath(t *testing.T) {
	db := openTestDB(t)
	as := newQueryArtifactStore(t, db)

	_, err := mcpserver.CallArtifactWrite(context.Background(), as, "job_x", "log", "", "run_x")
	if err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
}

func TestHandleArtifactWrite_MissingRunID(t *testing.T) {
	db := openTestDB(t)
	as := newQueryArtifactStore(t, db)

	_, err := mcpserver.CallArtifactWrite(context.Background(), as, "job_x", "log", "path/to/file", "")
	if err == nil {
		t.Fatal("expected error for empty run_id, got nil")
	}
}

func TestHandleArtifactWrite_PersistsInDB(t *testing.T) {
	db := openTestDB(t)
	createQueryTestRunAndJob(t, db, "run_aw2", "job_aw2")
	as := newQueryArtifactStore(t, db)

	out, err := mcpserver.CallArtifactWrite(context.Background(), as, "job_aw2", "plan", ".coworker/runs/run_aw2/plan.md", "run_aw2")
	if err != nil {
		t.Fatalf("CallArtifactWrite: %v", err)
	}

	artifactID, ok := out["artifact_id"].(string)
	if !ok || artifactID == "" {
		t.Fatalf("artifact_id missing or wrong type")
	}

	// Verify it exists via the read handler.
	readOut, err := mcpserver.CallArtifactRead(context.Background(), as, "job_aw2")
	if err != nil {
		t.Fatalf("CallArtifactRead: %v", err)
	}
	artifacts, ok := readOut["artifacts"].([]interface{})
	if !ok {
		t.Fatalf("artifacts wrong type: %T", readOut["artifacts"])
	}
	if len(artifacts) != 1 {
		t.Fatalf("artifacts count = %d, want 1", len(artifacts))
	}
	item, ok := artifacts[0].(map[string]interface{})
	if !ok {
		t.Fatalf("artifact item wrong type: %T", artifacts[0])
	}
	if item["id"] != artifactID {
		t.Errorf("id = %q, want %q", item["id"], artifactID)
	}
	if item["kind"] != "plan" {
		t.Errorf("kind = %q, want %q", item["kind"], "plan")
	}
	if item["path"] != ".coworker/runs/run_aw2/plan.md" {
		t.Errorf("path = %q, want %q", item["path"], ".coworker/runs/run_aw2/plan.md")
	}
}

func TestHandleArtifactWrite_GeneratesUniqueIDs(t *testing.T) {
	db := openTestDB(t)
	createQueryTestRunAndJob(t, db, "run_aw3", "job_aw3")
	as := newQueryArtifactStore(t, db)

	out1, err := mcpserver.CallArtifactWrite(context.Background(), as, "job_aw3", "log", "path/a.log", "run_aw3")
	if err != nil {
		t.Fatalf("CallArtifactWrite 1: %v", err)
	}
	out2, err := mcpserver.CallArtifactWrite(context.Background(), as, "job_aw3", "diff", "path/a.diff", "run_aw3")
	if err != nil {
		t.Fatalf("CallArtifactWrite 2: %v", err)
	}

	id1, _ := out1["artifact_id"].(string)
	id2, _ := out2["artifact_id"].(string)
	if id1 == "" || id2 == "" {
		t.Fatal("artifact IDs should not be empty")
	}
	if id1 == id2 {
		t.Errorf("artifact IDs should be unique, both are %q", id1)
	}
}

func TestHandleArtifactWrite_NilDB_ReturnsStub(t *testing.T) {
	s, err := mcpserver.NewServer(mcpserver.ServerConfig{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	tools := s.Tools()
	found := false
	for _, name := range tools {
		if name == "orch_artifact_write" {
			found = true
			break
		}
	}
	if !found {
		t.Error("orch_artifact_write tool should be registered even without a DB")
	}
}

// --- end-to-end write → read cycle -------------------------------------------

func TestArtifactCycle_WriteRead(t *testing.T) {
	db := openTestDB(t)
	createQueryTestRunAndJob(t, db, "run_cycle_art", "job_cycle_art")
	as := newQueryArtifactStore(t, db)

	// Step 1: write an artifact.
	writeOut, err := mcpserver.CallArtifactWrite(context.Background(), as, "job_cycle_art", "report", ".coworker/runs/run_cycle_art/report.md", "run_cycle_art")
	if err != nil {
		t.Fatalf("CallArtifactWrite: %v", err)
	}
	if writeOut["status"] != "ok" {
		t.Errorf("write status = %q, want %q", writeOut["status"], "ok")
	}
	artifactID, ok := writeOut["artifact_id"].(string)
	if !ok || artifactID == "" {
		t.Fatalf("artifact_id missing: %v", writeOut)
	}

	// Step 2: read back.
	readOut, err := mcpserver.CallArtifactRead(context.Background(), as, "job_cycle_art")
	if err != nil {
		t.Fatalf("CallArtifactRead: %v", err)
	}
	artifacts, ok := readOut["artifacts"].([]interface{})
	if !ok {
		t.Fatalf("artifacts wrong type: %T", readOut["artifacts"])
	}
	if len(artifacts) != 1 {
		t.Fatalf("artifacts count = %d, want 1", len(artifacts))
	}
	item, ok := artifacts[0].(map[string]interface{})
	if !ok {
		t.Fatalf("artifact item wrong type: %T", artifacts[0])
	}
	if item["id"] != artifactID {
		t.Errorf("id = %q, want %q", item["id"], artifactID)
	}
	if item["kind"] != "report" {
		t.Errorf("kind = %q, want %q", item["kind"], "report")
	}
}
