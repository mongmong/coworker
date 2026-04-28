package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/chris/coworker/coding/session"
	"github.com/chris/coworker/store"
	"github.com/spf13/cobra"
)

var editDBPath string

var editCmd = &cobra.Command{
	Use:   "edit <path>",
	Short: "Open an artifact in $EDITOR with session-aware hints.",
	Long: `Open the given path in $EDITOR (default: vim). Verifies an
active session is present, runs the editor synchronously, then prints
next-step hints if the file is dirty in git.

This is a workflow shortcut: the runtime does NOT auto-commit your
changes. After saving, commit normally and run:

  coworker record-human-edit --commit <sha>

…to register the edit as a human-edit job in the active run.

Filesystem watching for auto-detect is deferred to a follow-up plan.

Example:
  coworker edit docs/specs/foo.md
  EDITOR=code coworker edit cli/run.go`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEdit(cmd, args)
	},
}

func init() {
	editCmd.Flags().StringVar(&editDBPath, "db", "", "Path to SQLite database (default: .coworker/state.db)")
	rootCmd.AddCommand(editCmd)
}

func runEdit(cmd *cobra.Command, args []string) error {
	target := args[0]
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("artifact not found: %w", err)
	}

	dbPath := editDBPath
	if dbPath == "" {
		dbPath = filepath.Join(".coworker", "state.db")
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	eventStore := store.NewEventStore(db)
	sm := &session.Manager{
		RunStore: store.NewRunStore(db, eventStore),
		LockPath: sessionLockPath(dbPath),
	}
	if _, err := sm.CurrentSession(); err != nil {
		if errors.Is(err, session.ErrNoActiveSession) {
			return fmt.Errorf("no active session — start one with `coworker session`")
		}
		return fmt.Errorf("read session: %w", err)
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}

	editCmd := exec.CommandContext(cmd.Context(), editor, target) //nolint:gosec // editor + path are user-provided shell input by intent
	editCmd.Stdin = cmd.InOrStdin()
	editCmd.Stdout = cmd.OutOrStdout()
	editCmd.Stderr = cmd.ErrOrStderr()
	if err := editCmd.Run(); err != nil {
		return fmt.Errorf("run editor %q: %w", editor, err)
	}

	dirty := isPathDirtyInGit(cmd.Context(), target)
	if dirty {
		fmt.Fprintf(cmd.OutOrStdout(),
			"\n%s is dirty. Commit and record:\n  git add %s && git commit -m 'edit: %s'\n  coworker record-human-edit --commit <sha>\n",
			target, target, filepath.Base(target),
		)
	}
	return nil
}

// isPathDirtyInGit returns true when the path has uncommitted changes in
// the git working tree. False when not a git repo, or path is clean, or
// any git error.
func isPathDirtyInGit(ctx context.Context, path string) bool {
	c := exec.CommandContext(ctx, "git", "status", "--porcelain", "--", path)
	out, err := c.Output()
	if err != nil {
		return false
	}
	return len(out) > 0
}
