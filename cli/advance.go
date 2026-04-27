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
	advanceDBPath     string
	advanceAnsweredBy string
)

var advanceCmd = &cobra.Command{
	Use:   "advance",
	Short: "Approve the most-recent unanswered checkpoint on the active run.",
	Long: `Advance past the next pending checkpoint on the active session's run.
Resolves both the attention item (so observers see it answered) and the
durable checkpoint row (so the audit trail records the approval).

If no checkpoint is waiting, the command prints "no checkpoint waiting"
and exits 0.

Example:
  coworker advance
  coworker advance --answered-by alice`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAdvance(cmd, args)
	},
}

func init() {
	advanceCmd.Flags().StringVar(&advanceDBPath, "db", "", "Path to SQLite database (default: .coworker/state.db)")
	advanceCmd.Flags().StringVar(&advanceAnsweredBy, "answered-by", "cli", "Identity recorded as the checkpoint answerer")
	rootCmd.AddCommand(advanceCmd)
}

func runAdvance(cmd *cobra.Command, args []string) error {
	_ = args
	dbPath := advanceDBPath
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
	runID, err := sm.CurrentSession()
	if err != nil {
		if errors.Is(err, session.ErrNoActiveSession) {
			return fmt.Errorf("no active session — start one with `coworker session`")
		}
		return fmt.Errorf("read session: %w", err)
	}

	as := store.NewAttentionStore(db)
	cs := store.NewCheckpointStore(db, eventStore)

	item, err := as.GetAnyUnansweredCheckpointForRun(cmd.Context(), runID)
	if err != nil {
		return fmt.Errorf("find unanswered checkpoint: %w", err)
	}
	if item == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "no checkpoint waiting on the active run")
		return nil
	}

	out, err := mcpserver.CallCheckpointAdvance(cmd.Context(), as, item.ID, advanceAnsweredBy, cs)
	if err != nil {
		return fmt.Errorf("advance checkpoint %s: %w", item.ID, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "advanced checkpoint %s (status=%v)\n", item.ID, out["status"])
	return nil
}
