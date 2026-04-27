package humanedit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/chris/coworker/core"
)

const humanEditEventKind = core.EventKind("human-edit")

// JobCreator creates job records in the store.
type JobCreator interface {
	CreateJob(ctx context.Context, job *core.Job) error
}

// Recorder emits synthetic jobs for human commit activity.
type Recorder struct {
	JobStore    JobCreator
	EventWriter core.EventWriter
	RepoPath    string
	Logger      *slog.Logger
}

type gitResult struct {
	output string
	err    error
}

// humanEditGitTimeout is the deadline applied to each git subprocess in the
// human-edit recorder. git show / git log are fast; 30 seconds is generous.
const humanEditGitTimeout = 30 * time.Second

func runGitCommand(ctx context.Context, repoPath string, args ...string) (string, error) {
	gitCtx, cancel := context.WithTimeout(ctx, humanEditGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(gitCtx, "git", args...)
	cmd.Dir = repoPath
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %q: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// RecordCommit emits a synthetic completed human-edit job and a human-edit event.
func (r *Recorder) RecordCommit(ctx context.Context, runID, commitSHA string) error {
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}

	if r.JobStore == nil {
		return fmt.Errorf("job store is required")
	}
	if r.EventWriter == nil {
		return fmt.Errorf("event writer is required")
	}
	if runID == "" {
		return fmt.Errorf("runID is required")
	}
	if commitSHA == "" {
		return fmt.Errorf("commit SHA is required")
	}

	repoPath := r.RepoPath
	if repoPath == "" {
		repoPath = "."
	}

	diffStatsCh := make(chan gitResult, 1)
	commitMsgCh := make(chan gitResult, 1)

	go func() {
		out, err := runGitCommand(ctx, repoPath, "show", "--stat", commitSHA)
		diffStatsCh <- gitResult{output: out, err: err}
	}()
	go func() {
		out, err := runGitCommand(ctx, repoPath, "log", "-1", "--format=%s", commitSHA)
		commitMsgCh <- gitResult{output: out, err: err}
	}()

	var (
		diffStats, commitMsg string
		diffErr, msgErr      error
	)

	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-diffStatsCh:
			diffStats, diffErr = result.output, result.err
		case result := <-commitMsgCh:
			commitMsg, msgErr = result.output, result.err
		}
	}

	if diffErr != nil {
		return fmt.Errorf("collect commit stats for %q: %w", commitSHA, diffErr)
	}
	if msgErr != nil {
		return fmt.Errorf("collect commit message for %q: %w", commitSHA, msgErr)
	}

	now := time.Now()
	jobID := core.NewID()
	job := &core.Job{
		ID:           jobID,
		RunID:        runID,
		Role:         "human-edit",
		State:        core.JobStateComplete,
		DispatchedBy: "self",
		CLI:          "human-edit",
		StartedAt:    now,
		EndedAt:      &now,
	}

	if err := r.JobStore.CreateJob(ctx, job); err != nil {
		return fmt.Errorf("create synthetic job for commit %q: %w", commitSHA, err)
	}
	logger.Info("recorded human-edit job", "job_id", job.ID, "run_id", runID, "commit", commitSHA)

	payload, err := json.Marshal(map[string]string{
		"commit_sha":     commitSHA,
		"commit_message": commitMsg,
		"diff_stat":      diffStats,
		"job_id":         jobID,
		"run_id":         runID,
	})
	if err != nil {
		return fmt.Errorf("marshal human-edit event payload for %q: %w", commitSHA, err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          humanEditEventKind,
		SchemaVersion: 1,
		CausationID:   jobID,
		CorrelationID: jobID,
		Payload:       string(payload),
		CreatedAt:     now,
	}
	if err := r.EventWriter.WriteEventThenRow(ctx, event, nil); err != nil {
		return fmt.Errorf("write human-edit event for commit %q: %w", commitSHA, err)
	}
	logger.Info("emitted human-edit event", "event_id", event.ID, "kind", event.Kind)

	return nil
}
