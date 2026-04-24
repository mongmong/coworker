package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chris/coworker/coding/session"
	"github.com/chris/coworker/store"
	"github.com/spf13/cobra"
)

var advanceDBPath string

var advanceCmd = &cobra.Command{
	Use:   "advance",
	Short: "Advance the current session checkpoint.",
	Long: `Advance past a checkpoint in the active session. Real checkpoint
resolution is planned for a later task; this command is a placeholder.

Example:
  coworker advance`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAdvance(cmd, args)
	},
}

func init() {
	advanceCmd.Flags().StringVar(&advanceDBPath, "db", "", "Path to SQLite database (default: .coworker/state.db)")
	rootCmd.AddCommand(advanceCmd)
}

func runAdvance(cmd *cobra.Command, args []string) error {
	dbPath := advanceDBPath
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
		LockPath: filepath.Join(".coworker", "session.lock"),
	}
	_, err = sm.CurrentSession()
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}

	_ = args

	fmt.Fprintln(cmd.OutOrStdout(), "not yet implemented")
	return nil
}
