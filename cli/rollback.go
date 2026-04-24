package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chris/coworker/coding/session"
	"github.com/chris/coworker/store"
	"github.com/spf13/cobra"
)

var rollbackDBPath string

var rollbackCmd = &cobra.Command{
	Use:   "rollback <checkpoint-id>",
	Short: "Rollback a prior decision in the active session.",
	Long: `Rollback to a prior checkpoint in the active session.
This placeholder currently reports that rollback is not implemented.

Example:
  coworker rollback chk_abc123`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRollback(cmd, args)
	},
}

func init() {
	rollbackCmd.Flags().StringVar(&rollbackDBPath, "db", "", "Path to SQLite database (default: .coworker/state.db)")
	rootCmd.AddCommand(rollbackCmd)
}

func runRollback(cmd *cobra.Command, args []string) error {
	dbPath := rollbackDBPath
	if dbPath == "" {
		dbPath = filepath.Join(".coworker", "state.db")
	}

	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return fmt.Errorf("create db directory %q: %w", dbDir, err)
	}

	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	sm := &session.Manager{
		RunStore: store.NewRunStore(db, store.NewEventStore(db)),
		LockPath: sessionLockPath(dbPath),
	}
	_, err = sm.CurrentSession()
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}

	checkpointID := args[0]

	fmt.Fprintf(cmd.OutOrStdout(), "not yet implemented\n")
	_ = checkpointID
	return nil
}
