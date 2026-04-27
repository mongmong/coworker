package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/chris/coworker/store"
)

var inspectDBPath string

var inspectCmd = &cobra.Command{
	Use:   "inspect <job-id>",
	Short: "Print details for a job (state, findings, artifacts, supervisor verdicts).",
	Long: `Print rich detail for a single job: state, started/ended timestamps,
plan/phase attribution, recorded findings (with plan/phase/severity),
artifacts produced, supervisor verdicts, and any cost rows.

Example:
  coworker inspect job_abc123`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInspect(cmd, args)
	},
}

func init() {
	inspectCmd.Flags().StringVar(&inspectDBPath, "db", "", "Path to SQLite database (default: .coworker/state.db)")
	rootCmd.AddCommand(inspectCmd)
}

func runInspect(cmd *cobra.Command, args []string) error {
	jobID := args[0]
	dbPath := inspectDBPath
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
	fs := store.NewFindingStore(db, es)
	supStore := store.NewSupervisorEventStore(db, es)
	costStore := store.NewCostEventStore(db, es)

	job, err := js.GetJob(cmd.Context(), jobID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("job not found: %s", jobID)
		}
		return fmt.Errorf("get job: %w", err)
	}
	if job == nil {
		return fmt.Errorf("job not found: %s", jobID)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Job:           %s\n", job.ID)
	fmt.Fprintf(out, "Run:           %s\n", job.RunID)
	fmt.Fprintf(out, "Role:          %s\n", job.Role)
	fmt.Fprintf(out, "State:         %s\n", job.State)
	fmt.Fprintf(out, "CLI:           %s\n", job.CLI)
	fmt.Fprintf(out, "Dispatched by: %s\n", job.DispatchedBy)
	fmt.Fprintf(out, "Started:       %s\n", job.StartedAt.Format("2006-01-02 15:04:05"))
	if job.EndedAt != nil {
		fmt.Fprintf(out, "Ended:         %s\n", job.EndedAt.Format("2006-01-02 15:04:05"))
	}
	if job.PlanID != "" {
		fmt.Fprintf(out, "Plan:          %s (phase %d)\n", job.PlanID, job.PhaseIndex)
	}
	if job.CostUSD > 0 {
		fmt.Fprintf(out, "Cost USD:      %.4f\n", job.CostUSD)
	}

	// Findings authored by this job, filtered by JobID after listing run findings.
	allFindings, err := fs.ListFindings(cmd.Context(), job.RunID)
	if err != nil {
		return fmt.Errorf("list findings: %w", err)
	}
	jobFindings := make([]int, 0)
	for i, f := range allFindings {
		if f.JobID == jobID {
			jobFindings = append(jobFindings, i)
		}
	}
	fmt.Fprintf(out, "\nFindings (%d):\n", len(jobFindings))
	if len(jobFindings) > 0 {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  PATH:LINE\tSEVERITY\tBODY")
		for _, idx := range jobFindings {
			f := allFindings[idx]
			fmt.Fprintf(w, "  %s:%d\t%s\t%s\n", f.Path, f.Line, f.Severity, f.Body)
		}
		_ = w.Flush()
	}

	// Supervisor verdicts.
	sups, err := supStore.ListByJob(cmd.Context(), jobID)
	if err != nil {
		return fmt.Errorf("list supervisor events: %w", err)
	}
	fmt.Fprintf(out, "\nSupervisor verdicts (%d):\n", len(sups))
	if len(sups) > 0 {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  RULE\tVERDICT\tMESSAGE")
		for _, s := range sups {
			fmt.Fprintf(w, "  %s\t%s\t%s\n", s.RuleID, s.Verdict, s.Message)
		}
		_ = w.Flush()
	}

	// Cost samples.
	costs, err := costStore.ListByJob(cmd.Context(), jobID)
	if err != nil {
		return fmt.Errorf("list cost events: %w", err)
	}
	fmt.Fprintf(out, "\nCost samples (%d):\n", len(costs))
	if len(costs) > 0 {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  PROVIDER/MODEL\tTOKENS_IN\tTOKENS_OUT\tUSD")
		for _, c := range costs {
			fmt.Fprintf(w, "  %s/%s\t%d\t%d\t%.4f\n",
				c.Provider, c.Model, c.TokensIn, c.TokensOut, c.USD)
		}
		_ = w.Flush()
	}

	return nil
}
