package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chris/coworker/coding/session"
	mcpserver "github.com/chris/coworker/mcp"
	"github.com/chris/coworker/store"
	"github.com/spf13/cobra"
)

var (
	rollbackDBPath     string
	rollbackAnsweredBy string
)

var rollbackCmd = &cobra.Command{
	Use:   "rollback <checkpoint-id>",
	Short: "Reject a specific checkpoint by attention ID.",
	Long: `Reject a checkpoint on the active session's run. The checkpoint ID
is the attention ID printed when the checkpoint was created.

Both the attention item (UI surface) and the durable checkpoint row are
flipped to the rejected/resolved state. The rollback decision is recorded
with --answered-by (default "cli") so audit trails distinguish rollback
origins.

Example:
  coworker rollback chk_abc123
  coworker rollback chk_abc123 --answered-by alice`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRollback(cmd, args)
	},
}

func init() {
	rollbackCmd.Flags().StringVar(&rollbackDBPath, "db", "", "Path to SQLite database (default: .coworker/state.db)")
	rollbackCmd.Flags().StringVar(&rollbackAnsweredBy, "answered-by", "cli", "Identity recorded as the checkpoint rejecter")
	rootCmd.AddCommand(rollbackCmd)
}

func runRollback(cmd *cobra.Command, args []string) error {
	checkpointID := args[0]
	dbPath := rollbackDBPath
	if dbPath == "" {
		dbPath = filepath.Join(".coworker", "state.db")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("create db directory %q: %w", filepath.Dir(dbPath), err)
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

	as := store.NewAttentionStore(db)
	cs := store.NewCheckpointStore(db, eventStore)

	out, err := mcpserver.CallCheckpointRollback(cmd.Context(), as, checkpointID, rollbackAnsweredBy, cs)
	if err != nil {
		return fmt.Errorf("rollback checkpoint %s: %w", checkpointID, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "rolled back checkpoint %s (status=%v)\n", checkpointID, out["status"])
	return nil
}
