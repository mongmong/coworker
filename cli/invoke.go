package cli

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/store"
	"github.com/spf13/cobra"
)

var (
	invokeDiffPath   string
	invokeSpecPath   string
	invokeDBPath     string
	invokeCliBinary  string
	invokeRoleDir    string
	invokePromptDir  string
)

var invokeCmd = &cobra.Command{
	Use:   "invoke <role>",
	Short: "Invoke a role as an ephemeral job.",
	Long: `Invoke a role as an ephemeral job. The role is loaded from the
role directory, a run and job are created, the prompt is rendered,
an agent is dispatched, and findings are persisted to SQLite.

Example:
  coworker invoke reviewer.arch --diff path/to/diff --spec path/to/spec`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		roleName := args[0]
		return runInvoke(cmd, roleName)
	},
}

func init() {
	invokeCmd.Flags().StringVar(&invokeDiffPath, "diff", "", "Path to the diff file (required for reviewer roles)")
	invokeCmd.Flags().StringVar(&invokeSpecPath, "spec", "", "Path to the spec file (required for reviewer roles)")
	invokeCmd.Flags().StringVar(&invokeDBPath, "db", "", "Path to SQLite database (default: .coworker/state.db)")
	invokeCmd.Flags().StringVar(&invokeCliBinary, "cli-binary", "", "Path to the CLI binary (default: looks up role's CLI name in PATH)")
	invokeCmd.Flags().StringVar(&invokeRoleDir, "role-dir", "", "Path to the role YAML directory (default: .coworker/roles or coding/roles)")
	invokeCmd.Flags().StringVar(&invokePromptDir, "prompt-dir", "", "Path to the prompt template directory (default: .coworker or coding)")
	rootCmd.AddCommand(invokeCmd)
}

func runInvoke(cmd *cobra.Command, roleName string) error {
	ctx := cmd.Context()

	// Determine database path.
	dbPath := invokeDBPath
	if dbPath == "" {
		dbPath = filepath.Join(".coworker", "state.db")
	}

	// Ensure the directory exists.
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return fmt.Errorf("create db directory %q: %w", dbDir, err)
	}

	// Open the database.
	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// Determine role and prompt directories.
	roleDir := invokeRoleDir
	promptDir := invokePromptDir

	if roleDir == "" {
		// Look for .coworker/roles/ first, fall back to coding/roles/.
		roleDir = filepath.Join(".coworker", "roles")
		if _, err := os.Stat(roleDir); os.IsNotExist(err) {
			// Fall back to the project's bundled roles.
			roleDir = filepath.Join("coding", "roles")
		}
	}

	if promptDir == "" {
		// Look for .coworker/ first, fall back to coding/.
		coworkerDir := ".coworker"
		if _, err := os.Stat(coworkerDir); os.IsNotExist(err) {
			promptDir = "coding"
		} else {
			promptDir = coworkerDir
		}
	}

	// Determine agent binary based on role's CLI field.
	// For now, just use "codex" command. In future plans, this will
	// look up the CLI path from config.
	agentBinary := invokeCliBinary
	if agentBinary == "" {
		agentBinary = "codex"
	}

	// Build inputs from flags.
	inputs := make(map[string]string)
	if invokeDiffPath != "" {
		inputs["diff_path"] = invokeDiffPath
	}
	if invokeSpecPath != "" {
		inputs["spec_path"] = invokeSpecPath
	}

	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	d := &coding.Dispatcher{
		RoleDir:   roleDir,
		PromptDir: promptDir,
		Agent:     agent.NewCliAgent(agentBinary),
		DB:        db,
		Logger:    logger,
	}

	result, err := d.Orchestrate(ctx, &coding.DispatchInput{
		RoleName: roleName,
		Inputs:   inputs,
	})
	if err != nil {
		return err
	}

	// Print findings to stdout.
	fmt.Fprintf(cmd.OutOrStdout(), "Run: %s\n", result.RunID)
	fmt.Fprintf(cmd.OutOrStdout(), "Job: %s\n", result.JobID)
	fmt.Fprintf(cmd.OutOrStdout(), "Findings: %d\n\n", len(result.Findings))

	for i, f := range result.Findings {
		data, _ := json.Marshal(map[string]interface{}{
			"path":        f.Path,
			"line":        f.Line,
			"severity":    f.Severity,
			"body":        f.Body,
			"fingerprint": f.Fingerprint,
		})
		fmt.Fprintf(cmd.OutOrStdout(), "  %d. %s\n", i+1, string(data))
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("agent exited with code %d", result.ExitCode)
	}

	return nil
}
