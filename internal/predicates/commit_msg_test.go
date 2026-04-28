package predicates

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

// initGitRepoWithCommit creates a fresh git repo at dir with a single
// commit whose message is msg. Returns the dir path.
func initGitRepoWithCommit(t *testing.T, msg string) string {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"commit", "--allow-empty", "-m", msg},
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestCommitMsgContains_Match(t *testing.T) {
	dir := initGitRepoWithCommit(t, "feat: add new widget\n\nDetails here.")
	got, err := CommitMsgContains(dir, `^feat:`)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("expected match for ^feat:")
	}
}

func TestCommitMsgContains_NoMatch(t *testing.T) {
	dir := initGitRepoWithCommit(t, "fix: existing widget")
	got, err := CommitMsgContains(dir, `^feat:`)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("expected no match for ^feat: against fix: commit")
	}
}

func TestCommitMsgContains_InvalidRegex(t *testing.T) {
	dir := initGitRepoWithCommit(t, "ok")
	_, err := CommitMsgContains(dir, `[unclosed`)
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestCommitMsgContains_EmptyPattern(t *testing.T) {
	_, err := CommitMsgContains(filepath.Clean("."), "")
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
}

func TestCommitMsgContains_NotARepo(t *testing.T) {
	dir := t.TempDir()
	_, err := CommitMsgContains(dir, `feat`)
	if err == nil {
		t.Fatal("expected error when workDir is not a git repo")
	}
}
