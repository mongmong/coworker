package humanedit

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/chris/coworker/core"
)

type fakeJobStore struct {
	job   *core.Job
	calls int
	err   error
}

func (f *fakeJobStore) CreateJob(_ context.Context, job *core.Job) error {
	f.calls++
	f.job = job
	return f.err
}

type fakeEventWriter struct {
	event *core.Event
	calls int
	err   error
}

func (f *fakeEventWriter) WriteEventThenRow(_ context.Context, event *core.Event, _ func(interface{}) error) error {
	f.calls++
	f.event = event
	return f.err
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=humanedit-test",
		"GIT_AUTHOR_EMAIL=humanedit-test@example.com",
		"GIT_COMMITTER_NAME=humanedit-test",
		"GIT_COMMITTER_EMAIL=humanedit-test@example.com",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func setupGitRepoWithCommit(t *testing.T) (string, string) {
	t.Helper()
	repoDir := t.TempDir()
	t.Cleanup(func() {
		_ = os.RemoveAll(repoDir)
	})

	runGit(t, repoDir, "init")
	writeFile(t, repoDir+"/file.txt", "initial\n")
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "human edit commit")
	commitSHA := runGit(t, repoDir, "rev-parse", "HEAD")
	return repoDir, commitSHA
}

func TestRecordCommit_HappyPath(t *testing.T) {
	ctx := context.Background()
	repoDir, commitSHA := setupGitRepoWithCommit(t)

	jobStore := &fakeJobStore{}
	eventWriter := &fakeEventWriter{}
	recorder := &HumanEditRecorder{
		RepoPath:    repoDir,
		JobStore:    jobStore,
		EventWriter: eventWriter,
	}

	runID := core.NewID()
	if err := recorder.RecordCommit(ctx, runID, commitSHA); err != nil {
		t.Fatalf("RecordCommit: %v", err)
	}

	if jobStore.calls != 1 {
		t.Fatalf("CreateJob calls = %d, want 1", jobStore.calls)
	}
	job := jobStore.job
	if job == nil {
		t.Fatal("job was not created")
	}
	if job.Role != "human-edit" {
		t.Errorf("job.Role = %q, want %q", job.Role, "human-edit")
	}
	if job.State != core.JobStateComplete {
		t.Errorf("job.State = %q, want %q", job.State, core.JobStateComplete)
	}
	if job.DispatchedBy != "self" {
		t.Errorf("job.DispatchedBy = %q, want %q", job.DispatchedBy, "self")
	}
	if job.CLI != "human-edit" {
		t.Errorf("job.CLI = %q, want %q", job.CLI, "human-edit")
	}
	if job.RunID != runID {
		t.Errorf("job.RunID = %q, want %q", job.RunID, runID)
	}
	if job.EndedAt == nil {
		t.Error("job.EndedAt should be set for completed human-edit job")
	}

	if eventWriter.calls != 1 {
		t.Fatalf("WriteEventThenRow calls = %d, want 1", eventWriter.calls)
	}
	event := eventWriter.event
	if event == nil {
		t.Fatal("event was not emitted")
	}
	if event.Kind != humanEditEventKind {
		t.Errorf("event.Kind = %q, want %q", event.Kind, humanEditEventKind)
	}
	if event.CorrelationID != job.ID {
		t.Errorf("event.CorrelationID = %q, want %q", event.CorrelationID, job.ID)
	}

	var payload map[string]string
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["commit_sha"] != commitSHA {
		t.Errorf("payload commit_sha = %q, want %q", payload["commit_sha"], commitSHA)
	}
	if payload["commit_message"] != "human edit commit" {
		t.Errorf("payload commit_message = %q, want %q", payload["commit_message"], "human edit commit")
	}
	if payload["job_id"] != job.ID {
		t.Errorf("payload job_id = %q, want %q", payload["job_id"], job.ID)
	}
	if payload["run_id"] != runID {
		t.Errorf("payload run_id = %q, want %q", payload["run_id"], runID)
	}
	if payload["diff_stat"] == "" {
		t.Error("payload diff_stat should not be empty")
	}
}

func TestRecordCommit_InvalidSHA(t *testing.T) {
	ctx := context.Background()
	repoDir, _ := setupGitRepoWithCommit(t)

	jobStore := &fakeJobStore{}
	eventWriter := &fakeEventWriter{}
	recorder := &HumanEditRecorder{
		RepoPath:    repoDir,
		JobStore:    jobStore,
		EventWriter: eventWriter,
	}

	err := recorder.RecordCommit(ctx, core.NewID(), "0000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected error for invalid commit SHA")
	}

	if jobStore.calls != 0 {
		t.Errorf("CreateJob calls = %d, want 0", jobStore.calls)
	}
	if eventWriter.calls != 0 {
		t.Errorf("WriteEventThenRow calls = %d, want 0", eventWriter.calls)
	}
}

func TestRecordCommit_MissingRepo(t *testing.T) {
	ctx := context.Background()
	missingRepo := t.TempDir() + "/missing-repo"
	t.Cleanup(func() {
		_ = os.RemoveAll(missingRepo)
	})

	jobStore := &fakeJobStore{}
	eventWriter := &fakeEventWriter{}
	recorder := &HumanEditRecorder{
		RepoPath:    missingRepo,
		JobStore:    jobStore,
		EventWriter: eventWriter,
	}

	err := recorder.RecordCommit(ctx, core.NewID(), "HEAD")
	if err == nil {
		t.Fatal("expected error for missing repo path")
	}

	if jobStore.calls != 0 {
		t.Errorf("CreateJob calls = %d, want 0", jobStore.calls)
	}
	if eventWriter.calls != 0 {
		t.Errorf("WriteEventThenRow calls = %d, want 0", eventWriter.calls)
	}
}
