// Package shipper implements the plan shipping step: creating a GitHub pull
// request after all phases of a plan complete cleanly.
//
// Flow:
//  1. Create a ready-to-ship attention checkpoint (V1: non-blocking, record only).
//  2. Call gh pr create to open the PR from the feature branch.
//  3. Record the PR URL as an artifact (kind "pr-url").
//  4. Emit a plan.shipped event.
package shipper

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/chris/coworker/coding/manifest"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// Shipper creates a GitHub pull request after a plan's phases complete.
type Shipper struct {
	// AttentionStore, when non-nil, records the ready-to-ship checkpoint item.
	// True blocking is deferred to Plan 103; here we only record.
	AttentionStore *store.AttentionStore

	// EventStore writes the plan.shipped event.
	EventStore *store.EventStore

	// ArtifactStore records the PR URL artifact.
	// When non-nil, JobStore must also be set (artifact FK requires a jobs row).
	ArtifactStore *store.ArtifactStore

	// JobStore, when non-nil, creates a minimal ship-job row before inserting
	// the pr-url artifact (satisfying the artifacts.job_id FK constraint).
	// Required when ArtifactStore is set.
	JobStore *store.JobStore

	// Logger is the structured logger. Uses slog.Default() if nil.
	Logger *slog.Logger

	// DryRun, when true, skips the real gh call and returns a synthetic URL.
	// Used in tests.
	DryRun bool
}

// ShipResult holds the output of a successful Ship call.
type ShipResult struct {
	// PRURL is the URL of the created pull request.
	PRURL string

	// ArtifactID is the ID of the recorded pr-url artifact.
	ArtifactID string

	// AttentionID is the ID of the ready-to-ship attention item (may be empty
	// when AttentionStore is nil).
	AttentionID string
}

// Ship creates a pull request for the given plan and branch.
//
//   - runID: the parent run identifier (used for event and artifact correlation).
//   - planEntry: the plan being shipped (ID and Title used in PR title/body).
//   - branch: the feature branch to open the PR from.
//
// Ship always emits a plan.shipped event and records a pr-url artifact on
// success. It returns an error if gh pr create fails.
func (s *Shipper) Ship(
	ctx context.Context,
	runID string,
	planEntry *manifest.PlanEntry,
	branch string,
) (*ShipResult, error) {
	log := s.logger()

	log.Info("shipper: starting",
		"run_id", runID,
		"plan_id", planEntry.ID,
		"plan_title", planEntry.Title,
		"branch", branch,
	)

	// Step 1: create ready-to-ship attention checkpoint.
	// In V1, true blocking is deferred — we record and proceed.
	attentionID := ""
	if s.AttentionStore != nil {
		item := &core.AttentionItem{
			ID:       core.NewID(),
			RunID:    runID,
			Kind:     core.AttentionCheckpoint,
			Source:   "shipper",
			Question: fmt.Sprintf("Plan %d (%q) is ready to ship on branch %q. Approve PR creation?", planEntry.ID, planEntry.Title, branch),
		}
		if err := s.AttentionStore.InsertAttention(ctx, item); err != nil {
			// Non-fatal: log and continue (attention is observability, not a gate in V1).
			log.Error("shipper: failed to insert ready-to-ship attention item",
				"plan_id", planEntry.ID,
				"error", err,
			)
		} else {
			attentionID = item.ID
			log.Info("shipper: ready-to-ship checkpoint created", "attention_id", attentionID)
		}
	}

	// Step 2: call gh pr create (or dry-run).
	prTitle := fmt.Sprintf("Plan %d: %s", planEntry.ID, planEntry.Title)
	prBody := s.buildPRBody(planEntry)

	var prURL string
	if s.DryRun {
		prURL = fmt.Sprintf("https://github.com/dry-run/coworker/pull/%d", planEntry.ID)
		log.Info("shipper: dry-run mode, skipping gh call", "synthetic_url", prURL)
	} else {
		var err error
		prURL, err = ghCreatePR(ctx, branch, prTitle, prBody)
		if err != nil {
			return nil, fmt.Errorf("shipper: plan %d: %w", planEntry.ID, err)
		}
	}

	log.Info("shipper: PR created", "plan_id", planEntry.ID, "pr_url", prURL)

	// Step 3: record PR URL as artifact.
	// Emit plan.shipped event first (event-log-before-state invariant).
	jobID := fmt.Sprintf("ship-plan-%d", planEntry.ID)

	if err := s.emitShippedEvent(ctx, runID, planEntry, branch, prURL, jobID); err != nil {
		log.Error("shipper: failed to emit plan.shipped event",
			"plan_id", planEntry.ID,
			"error", err,
		)
		// Non-fatal for the PR itself, but log prominently.
	}

	// artifactID is only set after a successful artifact insert so callers can
	// use ArtifactID != "" as a reliable signal that the row exists in the DB.
	artifactID := ""

	if s.ArtifactStore != nil {
		// Ensure the ship job row exists so the artifacts FK constraint is satisfied.
		if s.JobStore != nil {
			shipJob := &core.Job{
				ID:           jobID,
				RunID:        runID,
				Role:         "shipper",
				State:        core.JobStateComplete,
				DispatchedBy: "workflow",
				CLI:          "claude-code",
				StartedAt:    time.Now(),
			}
			if err := s.JobStore.CreateJob(ctx, shipJob); err != nil {
				log.Error("shipper: failed to create ship job row",
					"plan_id", planEntry.ID,
					"job_id", jobID,
					"error", err,
				)
				// Non-fatal: skip artifact insertion if job creation failed.
			} else {
				intendedID := core.NewID()
				artifact := &core.Artifact{
					ID:    intendedID,
					JobID: jobID,
					Kind:  "pr-url",
					Path:  prURL,
				}
				if err := s.ArtifactStore.InsertArtifact(ctx, artifact, runID); err != nil {
					log.Error("shipper: failed to record pr-url artifact",
						"plan_id", planEntry.ID,
						"artifact_id", intendedID,
						"error", err,
					)
					// Non-fatal: the PR was created successfully.
				} else {
					// Only expose the artifact ID once the row is persisted.
					artifactID = intendedID
				}
			}
		} else {
			log.Warn("shipper: ArtifactStore set but JobStore is nil; skipping artifact insertion",
				"plan_id", planEntry.ID,
			)
		}
	}

	log.Info("shipper: done",
		"run_id", runID,
		"plan_id", planEntry.ID,
		"pr_url", prURL,
	)

	return &ShipResult{
		PRURL:       prURL,
		ArtifactID:  artifactID,
		AttentionID: attentionID,
	}, nil
}

// buildPRBody constructs a minimal PR body for the given plan.
func (s *Shipper) buildPRBody(planEntry *manifest.PlanEntry) string {
	return fmt.Sprintf("## Plan %d — %s\n\nAutopilot PR created by coworker.\n", planEntry.ID, planEntry.Title)
}

// emitShippedEvent writes the plan.shipped event to the event store.
// Errors are returned so the caller can log them.
func (s *Shipper) emitShippedEvent(
	ctx context.Context,
	runID string,
	planEntry *manifest.PlanEntry,
	branch string,
	prURL string,
	jobID string,
) error {
	if s.EventStore == nil {
		return nil
	}

	payload, err := json.Marshal(map[string]interface{}{
		"plan_id":    planEntry.ID,
		"plan_title": planEntry.Title,
		"branch":     branch,
		"pr_url":     prURL,
	})
	if err != nil {
		return fmt.Errorf("marshal plan.shipped payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventPlanShipped,
		SchemaVersion: 1,
		CorrelationID: jobID,
		Payload:       string(payload),
		CreatedAt:     time.Now(),
	}

	return s.EventStore.WriteEventThenRow(ctx, event, nil)
}

// logger returns the configured logger or slog.Default().
func (s *Shipper) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}
