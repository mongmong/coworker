package cli

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/chris/coworker/coding/humanedit"
	"github.com/chris/coworker/coding/session"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
	"github.com/spf13/cobra"
)

var (
	recordHumanEditDBPath string
	recordHumanEditCommit string
)

type recordHumanEditEventWriter struct {
	store *store.EventStore
}

func (w *recordHumanEditEventWriter) WriteEventThenRow(
	ctx context.Context,
	event *core.Event,
	applyFn func(tx interface{}) error,
) error {
	if applyFn == nil {
		return w.store.WriteEventThenRow(ctx, event, nil)
	}

	return w.store.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		return applyFn(tx)
	})
}

var recordHumanEditCmd = &cobra.Command{
	Use:   "record-human-edit",
	Short: "Record a manual git commit as a synthetic human-edit job.",
	Long: `Record a human-authored git commit against the active session.
The commit is represented as a completed synthetic human-edit job.

Example:
  coworker record-human-edit --commit abc123def456`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runRecordHumanEdit(cmd)
	},
}

func init() {
	recordHumanEditCmd.Flags().StringVar(&recordHumanEditDBPath, "db", "", "Path to SQLite database (default: .coworker/state.db)")
	recordHumanEditCmd.Flags().StringVar(&recordHumanEditCommit, "commit", "", "Git commit SHA to record (required)")
	_ = recordHumanEditCmd.MarkFlagRequired("commit")
	rootCmd.AddCommand(recordHumanEditCmd)
}

func runRecordHumanEdit(cmd *cobra.Command) error {
	dbPath := recordHumanEditDBPath
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

	eventStore := store.NewEventStore(db)
	runStore := store.NewRunStore(db, eventStore)
	sm := &session.Manager{
		RunStore: runStore,
		LockPath: sessionLockPath(dbPath),
	}
	runID, err := sm.CurrentSession()
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}

	recorder := &humanedit.Recorder{
		JobStore:    store.NewJobStore(db, eventStore),
		EventWriter: &recordHumanEditEventWriter{store: eventStore},
		RepoPath:    ".",
		Logger: slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})),
	}

	if err := recorder.RecordCommit(cmd.Context(), runID, recordHumanEditCommit); err != nil {
		return fmt.Errorf("record commit %s: %w", recordHumanEditCommit, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Recorded human edit: %s\n", recordHumanEditCommit)
	return nil
}
