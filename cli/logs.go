package cli

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/chris/coworker/store"
)

var (
	logsDBPath string
	logsFollow bool
)

var logsCmd = &cobra.Command{
	Use:   "logs <job-id>",
	Short: "Print the JSONL stream for a job.",
	Long: `Print the per-job JSONL log captured under
.coworker/runs/<run-id>/jobs/<job-id>.jsonl.

With --follow, tails the file as new lines arrive (useful for in-flight jobs).

Examples:
  coworker logs job_abc123
  coworker logs job_abc123 --follow`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLogs(cmd, args)
	},
}

func init() {
	logsCmd.Flags().StringVar(&logsDBPath, "db", "", "Path to SQLite database (default: .coworker/state.db)")
	logsCmd.Flags().BoolVar(&logsFollow, "follow", false, "Tail the log file as new lines arrive")
	rootCmd.AddCommand(logsCmd)
}

func runLogs(cmd *cobra.Command, args []string) error {
	jobID := args[0]
	dbPath := logsDBPath
	if dbPath == "" {
		dbPath = filepath.Join(".coworker", "state.db")
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	es := store.NewEventStore(db)
	js := store.NewJobStore(db, es)
	job, err := js.GetJob(cmd.Context(), jobID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("job not found: %s", jobID)
		}
		return fmt.Errorf("get job %q: %w", jobID, err)
	}
	if job == nil {
		return fmt.Errorf("job not found: %s", jobID)
	}

	// Resolve the JSONL path. The same convention is used by agent/log_writer.go::OpenJobLog.
	coworkerDir := filepath.Dir(dbPath)
	logPath := filepath.Join(coworkerDir, "runs", job.RunID, "jobs", jobID+".jsonl")
	if _, err := os.Stat(logPath); err != nil {
		return fmt.Errorf("log file not found at %q: %w", logPath, err)
	}

	if logsFollow {
		return tailFile(cmd.Context(), cmd.OutOrStdout(), logPath)
	}

	f, err := os.Open(logPath) //nolint:gosec // user-provided jobID validated above by GetJob
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(cmd.OutOrStdout(), f); err != nil {
		return fmt.Errorf("copy log: %w", err)
	}
	return nil
}

// tailFile streams lines from path, polling for new content. Exits on
// ctx cancellation. Plan 129 (I1 logs --follow).
func tailFile(ctx context.Context, w io.Writer, path string) error {
	f, err := os.Open(path) //nolint:gosec // path constructed from controlled inputs
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(line); werr != nil {
				return werr
			}
		}
		if readErr != nil && readErr != io.EOF {
			return fmt.Errorf("read log: %w", readErr)
		}
		if readErr == io.EOF {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(200 * time.Millisecond):
				// Keep polling.
			}
		}
	}
}
