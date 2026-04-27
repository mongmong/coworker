package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/coding/manifest"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

var (
	runDBPath               string
	runPolicyPath           string
	runMaxParallelPlans     int
	runNoShip               bool
	runDryRun               bool
	runManifestPath         string
	runResumeAfterAttention string
	runRoleDir              string
	runPromptDir            string
	runCliBinary            string
)

var runCmd = &cobra.Command{
	Use:   "run <prd.md>",
	Short: "Run the autopilot workflow from a PRD.",
	Long: `Run the autopilot workflow starting from a PRD (product requirements document).

The workflow:
  1. Validates the PRD file exists.
  2. Dispatches the architect role to produce a spec and plan manifest.
  3. Inserts a spec-approved checkpoint — pauses for human review.
  4. On resume (--resume-after-attention), iterates ready plans:
     - Dispatches the planner role to elaborate each plan.
     - Runs phase executor: developer → reviewer/tester → dedupe → fix-loop.
     - Ships the plan as a PR (unless --no-ship).
  5. Prints a success summary when all plans are complete.

Use --manifest to bypass architect dispatch and load an existing manifest directly.
Use --dry-run to validate the PRD and print the planned schedule without executing.

Example:
  coworker run docs/prd.md
  coworker run docs/prd.md --dry-run
  coworker run docs/prd.md --manifest docs/specs/001-manifest.yaml
  coworker run docs/prd.md --resume-after-attention attn_abc123`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAutopilot(cmd, args[0])
	},
}

func init() {
	runCmd.Flags().StringVar(&runDBPath, "db", "", "Path to SQLite database (default: .coworker/state.db)")
	runCmd.Flags().StringVar(&runPolicyPath, "policy", "", "Path to policy YAML (default: .coworker/policy.yaml)")
	runCmd.Flags().IntVar(&runMaxParallelPlans, "max-parallel-plans", 0, "Override max_parallel_plans from policy")
	runCmd.Flags().BoolVar(&runNoShip, "no-ship", false, "Skip shipper (do not create PRs)")
	runCmd.Flags().BoolVar(&runDryRun, "dry-run", false, "Validate PRD and dispatch architect, but do not execute plans")
	runCmd.Flags().StringVar(&runManifestPath, "manifest", "", "OPTIONAL: bypass architect, load existing manifest (logs WARNING)")
	runCmd.Flags().StringVar(&runResumeAfterAttention, "resume-after-attention", "", "Resume after a human approved or rejected a checkpoint")
	runCmd.Flags().StringVar(&runRoleDir, "role-dir", "", "Path to the role YAML directory (default: .coworker/roles or coding/roles)")
	runCmd.Flags().StringVar(&runPromptDir, "prompt-dir", "", "Path to the prompt template directory (default: .coworker or coding)")
	runCmd.Flags().StringVar(&runCliBinary, "cli-binary", "", "Path to the CLI binary (default: codex)")
	rootCmd.AddCommand(runCmd)
}

// runAutopilot is the main entry point for the `coworker run` command.
//
//nolint:gocyclo // Linear orchestration flow; complexity is inherent.
func runAutopilot(cmd *cobra.Command, prdPath string) error {
	ctx := cmd.Context()
	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// --dry-run: validate PRD exists, print schedule, exit without touching DB.
	if runDryRun {
		return runDryRunMode(cmd, prdPath)
	}

	// Validate PRD path (unless --manifest bypass is set).
	if runManifestPath == "" {
		if err := validateRunFileExists(prdPath, "PRD"); err != nil {
			return err
		}
	}

	// Open database.
	db, err := openRunDB(runDBPath)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck

	// Set up stores.
	eventStore := store.NewEventStore(db)
	runStore := store.NewRunStore(db, eventStore)
	attentionStore := store.NewAttentionStore(db)

	// Load policy (optional; nil if file doesn't exist).
	policy := loadRunPolicy(runPolicyPath, logger)
	if runMaxParallelPlans > 0 {
		if policy == nil {
			policy = &core.Policy{}
		}
		policy.ConcurrencyLimits.MaxParallelPlans = runMaxParallelPlans
	}

	// --resume-after-attention path.
	if runResumeAfterAttention != "" {
		return resumeAfterAttention(ctx, cmd, runResumeAfterAttention, prdPath, db, runStore, attentionStore, eventStore, policy, logger)
	}

	// Obtain a run ID and resolved manifest path.
	runID, resolvedManifestPath, err := prepareRunAndManifest(ctx, cmd, prdPath, db, runStore, policy, logger)
	if err != nil {
		return err
	}

	// Insert spec-approved checkpoint and pause.
	return insertSpecApprovedCheckpoint(ctx, cmd, prdPath, runID, resolvedManifestPath, attentionStore, logger)
}

// runDryRunMode prints the dry-run schedule without touching the DB.
func runDryRunMode(cmd *cobra.Command, prdPath string) error {
	if err := validateRunFileExists(prdPath, "PRD"); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Dry-run mode: PRD validated at %q\n", prdPath)
	fmt.Fprintln(cmd.OutOrStdout(), "No DB writes or agent dispatches will occur in dry-run mode.")
	if runManifestPath == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "Architect would be dispatched to produce spec + manifest.")
		return nil
	}
	if err := validateRunFileExists(runManifestPath, "manifest"); err != nil {
		return err
	}
	m, err := manifest.LoadManifest(runManifestPath)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Manifest: %s (%d plans)\n", runManifestPath, len(m.Plans))
	for _, p := range m.Plans {
		fmt.Fprintf(cmd.OutOrStdout(), "  Plan %d: %s (%d phases)\n", p.ID, p.Title, len(p.Phases))
	}
	return nil
}

// openRunDB resolves the DB path and opens the SQLite database.
func openRunDB(dbPath string) (*store.DB, error) {
	if dbPath == "" {
		dbPath = filepath.Join(".coworker", "state.db")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return db, nil
}

// prepareRunAndManifest creates a run and resolves the manifest path.
// When --manifest is set, it skips architect dispatch. Otherwise it dispatches
// the architect role and extracts the manifest path from its output.
func prepareRunAndManifest(
	ctx context.Context,
	cmd *cobra.Command,
	prdPath string,
	db *store.DB,
	runStore *store.RunStore,
	policy *core.Policy,
	logger *slog.Logger,
) (runID string, manifestPath string, err error) {
	if runManifestPath != "" {
		return prepareManifestBypass(ctx, runStore, logger)
	}
	return prepareArchitectDispatch(ctx, prdPath, db, runStore, policy, logger)
}

// prepareManifestBypass creates a run and uses the --manifest flag path directly.
func prepareManifestBypass(
	ctx context.Context,
	runStore *store.RunStore,
	logger *slog.Logger,
) (string, string, error) {
	logger.Warn("--manifest flag bypasses architect dispatch; for production use omit this flag")
	if err := validateRunFileExists(runManifestPath, "manifest"); err != nil {
		return "", "", err
	}
	run := &core.Run{
		ID:        core.NewID(),
		Mode:      "autopilot",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := runStore.CreateRun(ctx, run); err != nil {
		return "", "", fmt.Errorf("create run: %w", err)
	}
	logger.Info("run created (manifest bypass)", "run_id", run.ID)
	return run.ID, runManifestPath, nil
}

// prepareArchitectDispatch creates a run, dispatches the architect role, and
// extracts the manifest path from the architect's output artifacts.
func prepareArchitectDispatch(
	ctx context.Context,
	prdPath string,
	db *store.DB,
	runStore *store.RunStore,
	policy *core.Policy,
	logger *slog.Logger,
) (string, string, error) {
	dispatcher, err := buildRunDispatcher(db, policy, logger)
	if err != nil {
		return "", "", fmt.Errorf("build dispatcher: %w", err)
	}

	run := &core.Run{
		ID:        core.NewID(),
		Mode:      "autopilot",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := runStore.CreateRun(ctx, run); err != nil {
		return "", "", fmt.Errorf("create run: %w", err)
	}
	logger.Info("run created", "run_id", run.ID)

	logger.Info("dispatching architect role", "prd_path", prdPath)
	result, archErr := dispatcher.Orchestrate(ctx, &coding.DispatchInput{
		RoleName: "architect",
		Inputs:   map[string]string{"prd_path": prdPath},
	})
	if archErr != nil {
		return "", "", fmt.Errorf("architect dispatch: %w", archErr)
	}
	logger.Info("architect completed", "job_id", result.JobID, "artifacts", len(result.Artifacts))

	manifestPath := extractRunManifestPath(result.Artifacts)
	if manifestPath == "" {
		manifestPath = discoverRunManifestPath(result.Artifacts)
	}
	if manifestPath == "" {
		return "", "", fmt.Errorf("architect did not produce a manifest artifact; check architect role output")
	}
	return run.ID, manifestPath, nil
}

// insertSpecApprovedCheckpoint inserts the spec-approved attention item and
// prints the resume instructions.
func insertSpecApprovedCheckpoint(
	ctx context.Context,
	cmd *cobra.Command,
	prdPath string,
	runID string,
	manifestPath string,
	attentionStore *store.AttentionStore,
	logger *slog.Logger,
) error {
	checkpointID, err := insertRunCheckpoint(ctx, attentionStore, runID, "spec-approved", "run-command")
	if err != nil {
		return fmt.Errorf("insert spec-approved checkpoint: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"Spec generated. Manifest at: %s\nReview and run:\n  coworker run %s --resume-after-attention %s\nor answer via HTTP POST /attention/%s/answer\n",
		manifestPath, prdPath, checkpointID, checkpointID,
	)
	logger.Info("spec-approved checkpoint inserted; pausing for human review",
		"attention_id", checkpointID,
		"manifest_path", manifestPath,
	)
	return nil
}

// resumeAfterAttention handles --resume-after-attention: look up the checkpoint,
// check its answer, reconstruct scheduler state from event log, then continue.
func resumeAfterAttention(
	ctx context.Context,
	cmd *cobra.Command,
	attentionID string,
	prdPath string,
	db *store.DB,
	runStore *store.RunStore,
	attentionStore *store.AttentionStore,
	eventStore *store.EventStore,
	policy *core.Policy,
	logger *slog.Logger,
) error {
	// Retrieve the attention item.
	item, err := attentionStore.GetAttentionByID(ctx, attentionID)
	if err != nil {
		return fmt.Errorf("look up attention item: %w", err)
	}
	if item == nil {
		return fmt.Errorf("attention item %q not found", attentionID)
	}

	// Check if it has been answered.
	if !item.IsAnswered() {
		fmt.Fprintf(cmd.OutOrStdout(),
			"Attention item %s is still pending human review.\nAnswer it via 'POST /attention/%s/answer' then re-run with --resume-after-attention %s\n",
			attentionID, attentionID, attentionID,
		)
		return fmt.Errorf("attention item %s not yet answered", attentionID)
	}

	// Check the answer.
	if item.Answer == core.AttentionAnswerReject {
		// Mark the run as aborted.
		if item.RunID != "" {
			if abortErr := runStore.CompleteRun(ctx, item.RunID, core.RunStateAborted); abortErr != nil {
				logger.Error("failed to mark run as aborted", "run_id", item.RunID, "error", abortErr)
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Checkpoint was rejected. Run aborted.")
		return fmt.Errorf("run aborted: checkpoint %s was rejected", attentionID)
	}

	if item.Answer != core.AttentionAnswerApprove {
		return fmt.Errorf("unexpected answer %q for attention item %s", item.Answer, attentionID)
	}

	// Answer is "approve" — reconstruct state from event log and continue.
	runID := item.RunID
	if runID == "" {
		return fmt.Errorf("attention item %s has no run_id", attentionID)
	}

	// Replay events to reconstruct completed/active plan sets.
	events, err := eventStore.ListEvents(ctx, runID)
	if err != nil {
		return fmt.Errorf("replay events for run %s: %w", runID, err)
	}
	completed, active := reconstructPlanState(events)
	logger.Info("reconstructed plan state from event log",
		"run_id", runID,
		"completed", len(completed),
		"active_at_crash", len(active),
	)

	// Determine manifest path: check --manifest flag first, then look for
	// a persisted manifest path in the run's event log.
	manifestPath := runManifestPath
	if manifestPath == "" {
		manifestPath = discoverManifestFromEvents(events)
	}
	if manifestPath == "" {
		return fmt.Errorf("cannot determine manifest path for run %s; use --manifest flag", runID)
	}
	if err := validateRunFileExists(manifestPath, "manifest"); err != nil {
		return err
	}

	// Run the plan iteration loop.
	return runPlanLoop(ctx, cmd, runID, manifestPath, completed, active, policy, attentionStore, eventStore, logger)
}

// runPlanLoop iterates the manifest's ready plans sequentially, gating each
// plan on a plan-approved checkpoint before continuing.
func runPlanLoop(
	ctx context.Context,
	cmd *cobra.Command,
	runID string,
	manifestPath string,
	completed map[int]bool,
	active map[int]bool,
	policy *core.Policy,
	attentionStore *store.AttentionStore,
	eventStore *store.EventStore,
	logger *slog.Logger,
) error {
	m, err := manifest.LoadManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	logger.Info("manifest loaded", "spec_path", m.SpecPath, "plans", len(m.Plans))

	scheduler := manifest.NewDAGScheduler(m, policy)

	ready := scheduler.ReadyPlans(completed, active)
	if len(ready) > 0 {
		// Take the first ready plan. This command pauses after each plan-approved
		// checkpoint; the caller resumes via --resume-after-attention.
		plan := ready[0]
		active[plan.ID] = true

		// Emit phase.started event to record plan activation in the event log.
		planStartedEvent := &core.Event{
			ID:            core.NewID(),
			RunID:         runID,
			Kind:          core.EventPhaseStarted,
			SchemaVersion: 1,
			CorrelationID: fmt.Sprintf("plan-%d", plan.ID),
			Payload:       fmt.Sprintf(`{"plan_id":%d,"title":%q}`, plan.ID, plan.Title),
			CreatedAt:     time.Now(),
		}
		if evtErr := eventStore.WriteEventThenRow(ctx, planStartedEvent, nil); evtErr != nil {
			logger.Error("write phase.started event", "error", evtErr, "plan_id", plan.ID)
		}

		logger.Info("gating on plan-approved checkpoint",
			"plan_id", plan.ID,
			"title", plan.Title,
		)

		// Insert plan-approved checkpoint — pause for human review before
		// executing any phases for this plan.
		checkpointID, chkErr := insertRunCheckpoint(ctx, attentionStore, runID, "plan-approved", fmt.Sprintf("plan-%d", plan.ID))
		if chkErr != nil {
			return fmt.Errorf("insert plan-approved checkpoint for plan %d: %w", plan.ID, chkErr)
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"Plan %d (%s) ready. Review and resume with --resume-after-attention %s\n",
			plan.ID, plan.Title, checkpointID,
		)
		logger.Info("plan-approved checkpoint inserted; pausing",
			"plan_id", plan.ID,
			"attention_id", checkpointID,
		)
		// Pause here — caller must resume via --resume-after-attention.
		return nil
	}

	// All ready plans are exhausted (all completed or no more ready plans).
	allDone := true
	for _, p := range m.Plans {
		if !completed[p.ID] {
			allDone = false
			break
		}
	}
	if allDone {
		fmt.Fprintf(cmd.OutOrStdout(), "All plans complete. Total: %d plans\n", len(m.Plans))
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "No more plans ready. Completed: %d/%d\n", len(completed), len(m.Plans))
	}
	return nil
}

// buildRunDispatcher creates a coding.Dispatcher from the run command flags.
// This mirrors the pattern from invoke.go; Phase 4 will extract a shared
// buildDispatcher helper in cli/runtime.go.
func buildRunDispatcher(db *store.DB, policy *core.Policy, logger *slog.Logger) (*coding.Dispatcher, error) {
	roleDir := runRoleDir
	if roleDir == "" {
		roleDir = filepath.Join(".coworker", "roles")
		if _, err := os.Stat(roleDir); os.IsNotExist(err) {
			roleDir = filepath.Join("coding", "roles")
		}
	}

	promptDir := runPromptDir
	if promptDir == "" {
		coworkerDir := ".coworker"
		if _, err := os.Stat(coworkerDir); os.IsNotExist(err) {
			promptDir = "coding"
		} else {
			promptDir = coworkerDir
		}
	}

	agentBinary := runCliBinary
	if agentBinary == "" {
		agentBinary = "codex"
	}

	d := &coding.Dispatcher{
		RoleDir:   roleDir,
		PromptDir: promptDir,
		Agent:     agent.NewCliAgent(agentBinary),
		DB:        db,
		Logger:    logger,
		Policy:    policy,
	}
	return d, nil
}

// insertRunCheckpoint inserts a checkpoint attention item and returns its ID.
func insertRunCheckpoint(ctx context.Context, as *store.AttentionStore, runID, label, source string) (string, error) {
	item := &core.AttentionItem{
		ID:        core.NewID(),
		RunID:     runID,
		Kind:      core.AttentionCheckpoint,
		Source:    source,
		Question:  label,
		CreatedAt: time.Now(),
	}
	if err := as.InsertAttention(ctx, item); err != nil {
		return "", fmt.Errorf("insert checkpoint %q: %w", label, err)
	}
	return item.ID, nil
}

// loadRunPolicy loads the policy YAML from policyPath. If policyPath is empty,
// it tries .coworker/policy.yaml. Returns nil (not an error) when the file
// does not exist — the caller uses defaults.
func loadRunPolicy(policyPath string, logger *slog.Logger) *core.Policy {
	path := policyPath
	if path == "" {
		path = filepath.Join(".coworker", "policy.yaml")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("could not read policy file", "path", path, "error", err)
		}
		return nil
	}
	var p core.Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		logger.Warn("could not parse policy file", "path", path, "error", err)
		return nil
	}
	return &p
}

// validateRunFileExists returns a descriptive error if the file at path does
// not exist or is not accessible.
func validateRunFileExists(path, label string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s file not found: %q", label, path)
		}
		return fmt.Errorf("access %s file %q: %w", label, path, err)
	}
	return nil
}

// extractRunManifestPath looks for an artifact with kind "manifest" and returns
// its Path field. Returns "" when not found.
func extractRunManifestPath(artifacts []core.Artifact) string {
	for _, a := range artifacts {
		if a.Kind == "manifest" {
			return a.Path
		}
	}
	return ""
}

// discoverRunManifestPath tries to find a manifest by convention: it looks for
// an artifact with kind "spec" and derives the manifest path as
// "<spec_base>-manifest.yaml".
func discoverRunManifestPath(artifacts []core.Artifact) string {
	for _, a := range artifacts {
		if a.Kind == "spec" && a.Path != "" {
			ext := filepath.Ext(a.Path)
			withoutExt := a.Path[:len(a.Path)-len(ext)]
			return withoutExt + "-manifest.yaml"
		}
	}
	return ""
}

// reconstructPlanState replays the event log for a run and returns:
//   - completed: plan IDs with plan.shipped events
//   - active: plan IDs with phase.started events but no plan.shipped events
//
// This is used during --resume-after-attention to restore scheduler state.
func reconstructPlanState(events []core.Event) (completed map[int]bool, active map[int]bool) {
	completed = make(map[int]bool)
	active = make(map[int]bool)
	for _, e := range events {
		switch e.Kind {
		case core.EventPlanShipped:
			var payload struct {
				PlanID int `json:"plan_id"`
			}
			if err := json.Unmarshal([]byte(e.Payload), &payload); err == nil && payload.PlanID > 0 {
				completed[payload.PlanID] = true
				delete(active, payload.PlanID)
			}
		case core.EventPhaseStarted:
			var payload struct {
				PlanID int `json:"plan_id"`
			}
			if err := json.Unmarshal([]byte(e.Payload), &payload); err == nil && payload.PlanID > 0 {
				if !completed[payload.PlanID] {
					active[payload.PlanID] = true
				}
			}
		}
	}
	return completed, active
}

// discoverManifestFromEvents tries to find the manifest path recorded in the
// event log payload of a job.completed event (written by architect dispatch).
func discoverManifestFromEvents(events []core.Event) string {
	for _, e := range events {
		if e.Kind == core.EventJobCompleted {
			var payload struct {
				ManifestPath string `json:"manifest_path"`
			}
			if err := json.Unmarshal([]byte(e.Payload), &payload); err == nil && payload.ManifestPath != "" {
				return payload.ManifestPath
			}
		}
	}
	return ""
}
