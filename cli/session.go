package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chris/coworker/coding/session"
	"github.com/chris/coworker/store"
	"github.com/spf13/cobra"
)

var (
	sessionDBPath string
	sessionEnd    bool
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Start or end an interactive session.",
	Long: `Start a new interactive session, creating a run and session lock.
Use --end to complete the active session and remove the session lock.

Example:
  coworker session
  coworker session --end`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSession(cmd)
	},
}

func init() {
	sessionCmd.Flags().BoolVar(&sessionEnd, "end", false, "End the active session")
	sessionCmd.Flags().StringVar(&sessionDBPath, "db", "", "Path to SQLite database (default: .coworker/state.db)")
	rootCmd.AddCommand(sessionCmd)
}

func runSession(cmd *cobra.Command) error {
	dbPath := sessionDBPath
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

	sm := &session.SessionManager{
		RunStore: store.NewRunStore(db, store.NewEventStore(db)),
		LockPath: filepath.Join(".coworker", "session.lock"),
	}

	if sessionEnd {
		if err := sm.EndSession(); err != nil {
			return fmt.Errorf("end session: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Session ended\n")
		return nil
	}

	runID, err := sm.StartSession()
	if err != nil {
		return fmt.Errorf("start session: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Session started: %s\n", runID)
	return nil
}
