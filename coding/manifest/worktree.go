package manifest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WorktreeManager creates and removes git worktrees for parallel plan execution.
// It shells out to git so that worktree state is always consistent with the
// repository's own worktree registry (.git/worktrees/).
//
// Worktree lifecycle (per spec §Workspace model):
//  1. Call Create at plan-start → creates branch + worktree.
//  2. All plan jobs run inside the worktree (cwd = WorktreePath).
//  3. At ready-to-ship, the user PR is created from the feature branch.
//  4. After ship, worktree is kept for inspection.
//  5. Call Remove via `coworker cleanup` to reclaim disk space.
type WorktreeManager struct {
	// RepoRoot is the absolute path to the git repository root (the directory
	// that contains .git/).
	RepoRoot string

	// BaseDir is the absolute path to the directory where worktrees are
	// created, e.g. "/path/to/repo/.coworker/worktrees". It is created on
	// first use if it does not exist.
	BaseDir string
}

// NewWorktreeManager creates a WorktreeManager for the given repository.
// repoRoot is the repository root; baseDir is the worktree parent directory.
func NewWorktreeManager(repoRoot, baseDir string) *WorktreeManager {
	return &WorktreeManager{RepoRoot: repoRoot, BaseDir: baseDir}
}

// WorktreePath returns the expected absolute path for the plan's worktree.
// It does not create anything on disk.
func (m *WorktreeManager) WorktreePath(planID int, title string) string {
	return filepath.Join(m.BaseDir, WorktreeDirName(planID, title))
}

// Create creates a feature branch and a git worktree for the given plan.
// If the worktree already exists at the expected path, it returns the path
// without error (idempotent).
//
// baseBranch is the ref (branch name, commit SHA, or tag) that the new
// feature branch is rooted at, e.g. "main".
//
// Returns the absolute path to the worktree directory.
func (m *WorktreeManager) Create(ctx context.Context, planID int, title, baseBranch string) (string, error) {
	if err := os.MkdirAll(m.BaseDir, 0o750); err != nil {
		return "", fmt.Errorf("create worktree base dir: %w", err)
	}

	branch := BranchName(planID, title)
	path := m.WorktreePath(planID, title)

	// Idempotent: if the worktree directory already exists, skip creation.
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	// git worktree add <path> -b <branch> <base>
	args := []string{"worktree", "add", path, "-b", branch, baseBranch}
	if err := m.git(ctx, args...); err != nil {
		// If the branch already exists (e.g. from a prior interrupted run),
		// try without -b so git just checks out the existing branch.
		if isBranchExistsError(err) {
			args = []string{"worktree", "add", path, branch}
			if err2 := m.git(ctx, args...); err2 != nil {
				return "", fmt.Errorf("git worktree add (existing branch %q): %w", branch, err2)
			}
			return path, nil
		}
		return "", fmt.Errorf("git worktree add: %w", err)
	}
	return path, nil
}

// Remove removes the git worktree and deletes the feature branch for a plan.
// It is safe to call even if the worktree no longer exists on disk.
//
// Removal order:
//  1. git worktree remove --force <path>  (removes the directory)
//  2. git worktree prune                  (cleans stale registry entries)
//  3. git branch -D <branch>              (deletes the feature branch)
func (m *WorktreeManager) Remove(ctx context.Context, planID int, title string) error {
	branch := BranchName(planID, title)
	path := m.WorktreePath(planID, title)

	// Remove worktree (force to ignore uncommitted changes or lock files).
	if _, statErr := os.Stat(path); statErr == nil {
		if err := m.git(ctx, "worktree", "remove", "--force", path); err != nil {
			return fmt.Errorf("git worktree remove: %w", err)
		}
	}

	// Prune stale worktree registry entries.
	// Ignore errors — prune is best-effort.
	_ = m.git(ctx, "worktree", "prune")

	// Delete the feature branch. Ignore "not found" errors gracefully.
	if err := m.git(ctx, "branch", "-D", branch); err != nil {
		if !isBranchNotFoundError(err) {
			return fmt.Errorf("git branch -D %q: %w", branch, err)
		}
	}

	return nil
}

// git runs a git command with the given args, rooted at m.RepoRoot.
// stderr is captured and returned in the error on non-zero exit.
func (m *WorktreeManager) git(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = m.RepoRoot

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), msg, err)
	}
	return nil
}

// isBranchExistsError reports whether the git error indicates that the
// branch already exists (so we can retry without -b).
func isBranchExistsError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "A branch named")
}

// isBranchNotFoundError reports whether the git error indicates that the
// branch was not found (benign during cleanup).
func isBranchNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "error: branch")
}
