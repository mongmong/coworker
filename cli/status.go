package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/chris/coworker/store"
)

var (
	statusDBPath string
	statusRunID  string
)

var statusCmd = &cobra.Command{
	Use:   "status [run-id]",
	Short: "Show the state of one or all runs.",
	Long: `Show the state of runs in the active database.

Without arguments, prints a table of all runs (ID, mode, state, started_at).
With a run ID argument, prints the run's details plus a job summary.

Examples:
  coworker status
  coworker status run_abc123`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			statusRunID = args[0]
		}
		return runStatus(cmd, args)
	},
}

func init() {
	statusCmd.Flags().StringVar(&statusDBPath, "db", "", "Path to SQLite database (default: .coworker/state.db)")
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, _ []string) error {
	dbPath := statusDBPath
	if dbPath == "" {
		dbPath = filepath.Join(".coworker", "state.db")
	}
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("database not found at %q (start with `coworker daemon` or use --db): %w", dbPath, err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	es := store.NewEventStore(db)
	rs := store.NewRunStore(db, es)
	js := store.NewJobStore(db, es)

	if statusRunID != "" {
		return printRunDetails(cmd, rs, js, statusRunID)
	}
	return printRunList(cmd, rs)
}

func printRunList(cmd *cobra.Command, rs *store.RunStore) error {
	runs, err := rs.ListRuns(cmd.Context())
	if err != nil {
		return fmt.Errorf("list runs: %w", err)
	}
	if len(runs) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no runs in database")
		return nil
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RUN ID\tMODE\tSTATE\tSTARTED\tENDED")
	for _, r := range runs {
		ended := "—"
		if r.EndedAt != nil {
			ended = r.EndedAt.Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.Mode, r.State,
			r.StartedAt.Format("2006-01-02 15:04:05"),
			ended,
		)
	}
	return w.Flush()
}

func printRunDetails(cmd *cobra.Command, rs *store.RunStore, js *store.JobStore, runID string) error {
	ctx := context.Background()
	run, err := rs.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("run not found: %s", runID)
		}
		return fmt.Errorf("get run %q: %w", runID, err)
	}
	if run == nil {
		return fmt.Errorf("run not found: %s", runID)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Run:        %s\n", run.ID)
	fmt.Fprintf(out, "Mode:       %s\n", run.Mode)
	fmt.Fprintf(out, "State:      %s\n", run.State)
	fmt.Fprintf(out, "Started:    %s\n", run.StartedAt.Format("2006-01-02 15:04:05"))
	if run.EndedAt != nil {
		fmt.Fprintf(out, "Ended:      %s\n", run.EndedAt.Format("2006-01-02 15:04:05"))
	}
	if run.PRDPath != "" {
		fmt.Fprintf(out, "PRD:        %s\n", run.PRDPath)
	}
	if run.SpecPath != "" {
		fmt.Fprintf(out, "Spec:       %s\n", run.SpecPath)
	}
	if run.CostUSD > 0 {
		fmt.Fprintf(out, "Cost USD:   %.4f\n", run.CostUSD)
	}
	if run.BudgetUSD != nil {
		fmt.Fprintf(out, "Budget USD: %.2f\n", *run.BudgetUSD)
	}

	jobs, err := js.ListJobsByRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}
	fmt.Fprintf(out, "\nJobs (%d):\n", len(jobs))
	if len(jobs) == 0 {
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  JOB ID\tROLE\tSTATE\tCLI\tSTARTED")
	for _, j := range jobs {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n",
			j.ID, j.Role, j.State, j.CLI,
			j.StartedAt.Format("15:04:05"),
		)
	}
	return w.Flush()
}
