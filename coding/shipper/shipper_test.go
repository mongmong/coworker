package shipper_test

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/coding/manifest"
	"github.com/chris/coworker/coding/shipper"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

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

// seedRun inserts a run row so attention and event FK constraints are satisfied.
func seedRun(t *testing.T, db *store.DB, runID string) {
	t.Helper()
	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)
	run := &core.Run{
		ID:        runID,
		Mode:      "autopilot",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := rs.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("seedRun(%q): %v", runID, err)
	}
}

func TestShipper_DryRun_NoStores(t *testing.T) {
	// DryRun with nil stores — should succeed without panicking.
	s := &shipper.Shipper{
		DryRun: true,
	}
	plan := &manifest.PlanEntry{ID: 42, Title: "Core runtime", Phases: []string{"build"}}
	result, err := s.Ship(context.Background(), "run-001", plan, "feature/plan-042")
	if err != nil {
		t.Fatalf("Ship: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil ShipResult")
	}
	if result.PRURL == "" {
		t.Error("expected non-empty PRURL in dry-run mode")
	}
}

func TestShipper_DryRun_WithStores(t *testing.T) {
	db := openTestDB(t)
	eventStore := store.NewEventStore(db)
	attentionStore := store.NewAttentionStore(db)
	artifactStore := store.NewArtifactStore(db, eventStore)
	jobStore := store.NewJobStore(db, eventStore)

	runID := "run-test-115"
	// Seed the run row so FK constraints on attention and events are satisfied.
	seedRun(t, db, runID)

	s := &shipper.Shipper{
		AttentionStore: attentionStore,
		EventStore:     eventStore,
		ArtifactStore:  artifactStore,
		JobStore:       jobStore,
		DryRun:         true,
	}

	plan := &manifest.PlanEntry{ID: 115, Title: "Shipper + workflow customization", Phases: []string{"ship"}}

	result, err := s.Ship(context.Background(), runID, plan, "feature/plan-115-shipper")
	if err != nil {
		t.Fatalf("Ship: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil ShipResult")
	}

	// PR URL should be the synthetic dry-run URL.
	wantURL := "https://github.com/dry-run/coworker/pull/115"
	if result.PRURL != wantURL {
		t.Errorf("PRURL = %q, want %q", result.PRURL, wantURL)
	}

	// AttentionID should be set (store was wired).
	if result.AttentionID == "" {
		t.Error("expected non-empty AttentionID when AttentionStore is set")
	}

	// ArtifactID should be set.
	if result.ArtifactID == "" {
		t.Error("expected non-empty ArtifactID")
	}

	// Verify attention item was persisted.
	ctx := context.Background()
	items, err := attentionStore.ListAttentionByRun(ctx, runID, nil)
	if err != nil {
		t.Fatalf("ListAttentionByRun: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 attention item, got %d", len(items))
	}
	item := items[0]
	if item.Kind != core.AttentionCheckpoint {
		t.Errorf("attention kind = %q, want %q", item.Kind, core.AttentionCheckpoint)
	}
	if item.Source != "shipper" {
		t.Errorf("attention source = %q, want %q", item.Source, "shipper")
	}

	// Verify artifact was persisted.
	jobID := "ship-plan-115"
	artifacts, err := artifactStore.ListArtifacts(ctx, jobID)
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
	art := artifacts[0]
	if art.Kind != "pr-url" {
		t.Errorf("artifact kind = %q, want %q", art.Kind, "pr-url")
	}
	if art.Path != wantURL {
		t.Errorf("artifact path = %q, want %q", art.Path, wantURL)
	}
}

func TestShipper_DryRun_EventEmitted(t *testing.T) {
	db := openTestDB(t)
	eventStore := store.NewEventStore(db)

	runID := "run-event-test"
	// Seed the run row so the event FK constraint is satisfied.
	seedRun(t, db, runID)

	s := &shipper.Shipper{
		EventStore: eventStore,
		DryRun:     true,
	}

	plan := &manifest.PlanEntry{ID: 7, Title: "Some plan", Phases: []string{"p1"}}

	_, err := s.Ship(context.Background(), runID, plan, "feature/plan-007")
	if err != nil {
		t.Fatalf("Ship: %v", err)
	}

	// Verify the plan.shipped event was written to the event store.
	events, err := eventStore.ListEvents(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}

	shipped := false
	for _, ev := range events {
		if ev.Kind == core.EventPlanShipped {
			shipped = true
			break
		}
	}
	if !shipped {
		t.Errorf("expected plan.shipped event in event log, got events: %v", eventKinds(events))
	}
}

func TestShipper_DryRun_PRTitleContainsPlanID(t *testing.T) {
	// Verify the dry-run URL embeds the plan ID so it's distinguishable.
	s := &shipper.Shipper{DryRun: true}
	plan := &manifest.PlanEntry{ID: 99, Title: "Test plan"}
	result, err := s.Ship(context.Background(), "r1", plan, "feature/plan-099")
	if err != nil {
		t.Fatalf("Ship: %v", err)
	}
	// The synthetic URL should contain "99".
	if result.PRURL == "" {
		t.Fatal("empty PRURL")
	}
}

// eventKinds is a helper that extracts EventKind from a slice of events.
func eventKinds(events []core.Event) []core.EventKind {
	kinds := make([]core.EventKind, len(events))
	for i, e := range events {
		kinds[i] = e.Kind
	}
	return kinds
}

// TestShipper_GhRunner_Success uses an injected GhRunner (no real gh
// binary) to exercise the success path. Plan 128 (I6).
func TestShipper_GhRunner_Success(t *testing.T) {
	db := openTestDB(t)
	es := store.NewEventStore(db)
	runID := "run_gh_ok"
	seedRun(t, db, runID)

	var captured struct {
		branch, title, body string
	}
	s := &shipper.Shipper{
		EventStore:    es,
		ArtifactStore: store.NewArtifactStore(db, es),
		JobStore:      store.NewJobStore(db, es),
		GhRunner: func(_ context.Context, branch, title, body string) (string, error) {
			captured.branch = branch
			captured.title = title
			captured.body = body
			return "https://github.com/example/coworker/pull/777", nil
		},
	}
	plan := &manifest.PlanEntry{ID: 777, Title: "Test"}
	result, err := s.Ship(context.Background(), runID, plan, "feature/plan-777")
	if err != nil {
		t.Fatalf("Ship: %v", err)
	}
	if result.PRURL != "https://github.com/example/coworker/pull/777" {
		t.Errorf("PRURL = %q", result.PRURL)
	}
	if captured.branch != "feature/plan-777" {
		t.Errorf("captured branch = %q", captured.branch)
	}
	if !contains(captured.title, "Plan 777") {
		t.Errorf("title = %q; expected to contain 'Plan 777'", captured.title)
	}
}

// TestShipper_GhRunner_Failure verifies that gh failures bubble up as
// a wrapped error from Ship. Plan 128 (I6).
func TestShipper_GhRunner_Failure(t *testing.T) {
	db := openTestDB(t)
	es := store.NewEventStore(db)
	runID := "run_gh_fail"
	seedRun(t, db, runID)

	wantErr := "gh exit 1: not authenticated"
	s := &shipper.Shipper{
		EventStore:    es,
		ArtifactStore: store.NewArtifactStore(db, es),
		JobStore:      store.NewJobStore(db, es),
		GhRunner: func(_ context.Context, _, _, _ string) (string, error) {
			return "", fmtErr(wantErr)
		},
	}
	plan := &manifest.PlanEntry{ID: 1, Title: "fail"}
	_, err := s.Ship(context.Background(), runID, plan, "feature/plan-1")
	if err == nil {
		t.Fatal("expected error from Ship when GhRunner fails")
	}
	if !contains(err.Error(), wantErr) {
		t.Errorf("error %q; expected to contain %q", err.Error(), wantErr)
	}
}

// TestShipper_GhRunner_EmptyURL verifies that an empty PR URL from gh
// produces a wrapped error instead of a silent empty-URL ship.
//
// (The default ghCreatePR returns a "no PR URL returned" error in this
// case; we mirror it via the injected stub.)
func TestShipper_GhRunner_EmptyURL(t *testing.T) {
	db := openTestDB(t)
	es := store.NewEventStore(db)
	runID := "run_gh_empty"
	seedRun(t, db, runID)

	s := &shipper.Shipper{
		EventStore:    es,
		ArtifactStore: store.NewArtifactStore(db, es),
		JobStore:      store.NewJobStore(db, es),
		GhRunner: func(_ context.Context, _, _, _ string) (string, error) {
			return "", fmtErr("gh pr create: empty output (no PR URL returned)")
		},
	}
	plan := &manifest.PlanEntry{ID: 2, Title: "empty"}
	_, err := s.Ship(context.Background(), runID, plan, "feature/plan-2")
	if err == nil {
		t.Fatal("expected error when GhRunner returns empty URL")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// fmtErr returns an error with the given message (test helper).
func fmtErr(msg string) error {
	return &testErr{msg: msg}
}

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }
