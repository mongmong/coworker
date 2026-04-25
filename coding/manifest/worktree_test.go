package manifest_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chris/coworker/coding/manifest"
)

// initGitRepo creates a temporary git repository with an initial commit
// and returns its path. The repo is git-init'd and has a committed file
// so that branch operations (worktree add, branch -D) work normally.
func initGitRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")

	// Write and commit a file so HEAD is valid.
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("# test\n"), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "README.md")
	run("commit", "-m", "initial commit")

	return dir
}

func TestWorktreeManager_WorktreePath(t *testing.T) {
	m := manifest.NewWorktreeManager("/repo", "/repo/.coworker/worktrees")
	got := m.WorktreePath(106, "Build from PRD")
	want := "/repo/.coworker/worktrees/plan-106-build-from-prd"
	if got != want {
		t.Errorf("WorktreePath = %q, want %q", got, want)
	}
}

func TestWorktreeManager_Create(t *testing.T) {
	repoRoot := initGitRepo(t)
	baseDir := filepath.Join(repoRoot, ".coworker", "worktrees")

	m := manifest.NewWorktreeManager(repoRoot, baseDir)
	ctx := context.Background()

	path, err := m.Create(ctx, 200, "My Plan", "main")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Worktree directory should exist.
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		t.Fatalf("worktree path %q does not exist after Create", path)
	}

	// Path should match WorktreePath.
	expected := m.WorktreePath(200, "My Plan")
	if path != expected {
		t.Errorf("Create returned %q, want %q", path, expected)
	}

	// Worktree should be on the feature branch.
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch --show-current in worktree: %v", err)
	}
	branch := strings.TrimSpace(string(out))
	wantBranch := manifest.BranchName(200, "My Plan")
	if branch != wantBranch {
		t.Errorf("worktree branch = %q, want %q", branch, wantBranch)
	}
}

func TestWorktreeManager_Create_Idempotent(t *testing.T) {
	repoRoot := initGitRepo(t)
	baseDir := filepath.Join(repoRoot, ".coworker", "worktrees")

	m := manifest.NewWorktreeManager(repoRoot, baseDir)
	ctx := context.Background()

	path1, err := m.Create(ctx, 201, "Idempotent Plan", "main")
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}

	// Second Create should succeed and return the same path.
	path2, err := m.Create(ctx, 201, "Idempotent Plan", "main")
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if path1 != path2 {
		t.Errorf("idempotent Create: path1=%q != path2=%q", path1, path2)
	}
}

func TestWorktreeManager_Remove(t *testing.T) {
	repoRoot := initGitRepo(t)
	baseDir := filepath.Join(repoRoot, ".coworker", "worktrees")

	m := manifest.NewWorktreeManager(repoRoot, baseDir)
	ctx := context.Background()

	if _, err := m.Create(ctx, 202, "Remove Me", "main"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := m.Remove(ctx, 202, "Remove Me"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Worktree directory should be gone.
	path := m.WorktreePath(202, "Remove Me")
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("worktree path %q still exists after Remove", path)
	}
}

func TestWorktreeManager_Remove_NonExistent(t *testing.T) {
	repoRoot := initGitRepo(t)
	baseDir := filepath.Join(repoRoot, ".coworker", "worktrees")

	m := manifest.NewWorktreeManager(repoRoot, baseDir)
	ctx := context.Background()

	// Remove a plan that was never created — should not error.
	if err := m.Remove(ctx, 999, "Never Created"); err != nil {
		t.Errorf("Remove of non-existent worktree should not error, got: %v", err)
	}
}
