package cli

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// defaultConfigYAML is written to .coworker/config.yaml on init.
// Values mirror the Appendix B example from the design spec.
const defaultConfigYAML = `daemon:
  bind: local_socket
  data_dir: .coworker

cli_selection:
  interactive_driver: claude-code
  fallback_driver: opencode

providers:
  claude-code:
    rate_limit_concurrent: 3
  codex:
    sandbox_default: workspace-write
    rate_limit_concurrent: 2
  opencode:
    server_url: http://127.0.0.1:7777
    rate_limit_concurrent: 4

telemetry:
  event_log_retention_days: 90
  cost_ledger_retention_days: 365
`

// defaultPolicyYAML is written to .coworker/policy.yaml on init.
// Values are derived from coding/policy/defaults.go — keep them in sync.
const defaultPolicyYAML = `checkpoints:
  spec-approved: block
  plan-approved: block
  phase-clean: on-failure
  ready-to-ship: block
  compliance-breach: block
  quality-gate: block

supervisor_limits:
  max_retries_per_job: 3
  max_fix_cycles_per_phase: 5

concurrency:
  max_parallel_plans: 2
  max_parallel_reviewers: 3

permissions:
  on_undeclared: block
`

// gitignoreEntries lists the lines appended to .gitignore by coworker init.
var gitignoreEntries = []string{
	".coworker/state.db",
	".coworker/runs/",
}

// initOptions holds the parsed flags for the init command.
type initOptions struct {
	Force       bool
	WithPlugins bool
	Global      bool
}

var initOpts initOptions

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Scaffold a coworker project in the current directory.",
	Long: `Scaffold a coworker project in the current directory.

Creates .coworker/ with default config.yaml, policy.yaml, role definitions,
prompt templates, and supervisor rules. Augments .gitignore to exclude runtime
state files. Optionally installs CLI plugins for Claude Code, Codex, and
OpenCode.

Re-running coworker init is idempotent: existing files are skipped unless
--force is provided.

Examples:
  coworker init
  coworker init --force
  coworker init --with-plugins
  coworker init --with-plugins --global`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runInit(cmd, &initOpts)
	},
}

func init() {
	initCmd.Flags().BoolVar(&initOpts.Force, "force", false, "Overwrite existing files")
	initCmd.Flags().BoolVar(&initOpts.WithPlugins, "with-plugins", false, "Install CLI plugins (claude, codex, opencode)")
	initCmd.Flags().BoolVar(&initOpts.Global, "global", false, "Install plugins to global locations (~/.claude, etc.)")
	rootCmd.AddCommand(initCmd)
}

// runInit performs the full init sequence.
func runInit(cmd *cobra.Command, opts *initOptions) error {
	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	coworkerDir := filepath.Join(cwd, ".coworker")

	// Prompt if .coworker already exists and --force not set.
	if _, err := os.Stat(coworkerDir); err == nil && !opts.Force {
		fmt.Fprintf(cmd.OutOrStdout(),
			".coworker/ already exists. Use --force to overwrite existing files.\n")
	}

	// Step 1: Create directory structure.
	dirs := []string{
		coworkerDir,
		filepath.Join(coworkerDir, "roles"),
		filepath.Join(coworkerDir, "prompts"),
		filepath.Join(coworkerDir, "rules"),
		filepath.Join(coworkerDir, "runs"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create directory %q: %w", d, err)
		}
	}

	// Step 2: Write config.yaml.
	if err := writeInitFile(filepath.Join(coworkerDir, "config.yaml"), defaultConfigYAML, opts.Force); err != nil {
		return fmt.Errorf("write config.yaml: %w", err)
	}

	// Step 3: Write policy.yaml.
	if err := writeInitFile(filepath.Join(coworkerDir, "policy.yaml"), defaultPolicyYAML, opts.Force); err != nil {
		return fmt.Errorf("write policy.yaml: %w", err)
	}

	// Step 4: Write .version (always updated to current binary version).
	versionPath := filepath.Join(coworkerDir, ".version")
	if err := os.WriteFile(versionPath, []byte(Version+"\n"), 0o600); err != nil {
		return fmt.Errorf("write .version: %w", err)
	}

	// Step 5: Copy role/prompt/rule assets.
	if _, err := copyInitAssets(coworkerDir, opts.Force, logger); err != nil {
		return fmt.Errorf("copy assets: %w", err)
	}

	// Step 6: Augment .gitignore.
	gitignorePath := filepath.Join(cwd, ".gitignore")
	addedEntries, err := augmentGitignore(gitignorePath, gitignoreEntries)
	if err != nil {
		// Non-fatal — log and continue.
		logger.Warn("augment .gitignore", "err", err)
	}

	// Step 7: Install plugins if requested.
	if opts.WithPlugins {
		fmt.Fprintf(cmd.OutOrStdout(), "\nInstalling CLI plugins...\n")
		installErrors := 0
		// Save and restore the package-level pluginCLI so init does not leave it
		// mutated for the rest of the process.
		savedPluginCLI := pluginCLI
		defer func() { pluginCLI = savedPluginCLI }()
		for _, cliName := range []string{"claude", "codex", "opencode"} {
			pluginCLI = cliName
			if err := runPluginInstall(cmd); err != nil {
				logger.Warn("install plugin", "cli", cliName, "err", err)
				fmt.Fprintf(cmd.OutOrStdout(), "  [skip] %s plugin: %v\n", cliName, err)
				installErrors++
			}
		}
		if installErrors > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "\nNote: %d plugin(s) could not be installed. "+
				"Run 'coworker plugin install --cli <name>' individually after setup.\n", installErrors)
		}
	}

	// Step 8: Print summary.
	fmt.Fprintf(cmd.OutOrStdout(), "\ncoworker init complete.\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  dir:     %s\n", coworkerDir)
	fmt.Fprintf(cmd.OutOrStdout(), "  version: %s\n", Version)
	if len(addedEntries) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  .gitignore: added %s\n", strings.Join(addedEntries, ", "))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nNext: run 'coworker daemon' to start the runtime.\n")

	return nil
}

// writeInitFile writes content to path. If the file already exists and force
// is false, it is a no-op. Returns an error only for unexpected I/O failures.
func writeInitFile(path, content string, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			// File exists and no --force: skip.
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// copyStats accumulates written/skipped counts from asset copying.
type copyStats struct {
	written int
	skipped int
}

// copyInitAssets finds the canonical coding/ source directory and copies roles,
// prompts, and rules into the .coworker/ subdirectories. Missing source files
// are skipped with a warning rather than aborting init.
func copyInitAssets(coworkerDir string, force bool, logger *slog.Logger) (*copyStats, error) {
	srcRoot, err := findInitAssets()
	if err != nil {
		logger.Warn("cannot find coding/ source directory — roles, prompts, rules not copied", "err", err)
		return &copyStats{}, nil
	}

	stats := &copyStats{}

	// Roles: coding/roles/*.yaml → .coworker/roles/*.yaml
	rolesStats, err := copyGlob(
		filepath.Join(srcRoot, "roles"),
		filepath.Join(coworkerDir, "roles"),
		"*.yaml",
		force,
		logger,
	)
	if err != nil {
		logger.Warn("copy roles", "err", err)
	}
	stats.written += rolesStats.written
	stats.skipped += rolesStats.skipped

	// Prompts: coding/prompts/*.md → .coworker/prompts/*.md
	promptsStats, err := copyGlob(
		filepath.Join(srcRoot, "prompts"),
		filepath.Join(coworkerDir, "prompts"),
		"*.md",
		force,
		logger,
	)
	if err != nil {
		logger.Warn("copy prompts", "err", err)
	}
	stats.written += promptsStats.written
	stats.skipped += promptsStats.skipped

	// Supervisor contract rules: coding/supervisor/rules.yaml → .coworker/rules/supervisor-contract.yaml
	supervisorSrc := filepath.Join(srcRoot, "supervisor", "rules.yaml")
	supervisorDst := filepath.Join(coworkerDir, "rules", "supervisor-contract.yaml")
	if copied, err := copyInitFileSrc(supervisorSrc, supervisorDst, force); err != nil {
		logger.Warn("copy supervisor rules", "src", supervisorSrc, "err", err)
	} else if copied {
		stats.written++
	} else {
		stats.skipped++
	}

	// Quality rules: coding/quality/rules.yaml → .coworker/rules/quality.yaml
	qualitySrc := filepath.Join(srcRoot, "quality", "rules.yaml")
	qualityDst := filepath.Join(coworkerDir, "rules", "quality.yaml")
	if copied, err := copyInitFileSrc(qualitySrc, qualityDst, force); err != nil {
		logger.Warn("copy quality rules", "src", qualitySrc, "err", err)
	} else if copied {
		stats.written++
	} else {
		stats.skipped++
	}

	return stats, nil
}

// findInitAssets locates the canonical coding/ directory, checking:
//  1. <binary-dir>/coding/
//  2. <cwd>/coding/  (development: running from repo root)
func findInitAssets() (string, error) {
	candidates := []string{}

	// Option 1: sibling of the binary.
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "coding"))
	}

	// Option 2: relative to cwd (development).
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "coding"))
	}

	for _, dir := range candidates {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, nil
		}
	}

	return "", fmt.Errorf("coding/ directory not found in any of %v", candidates)
}

// copyGlob copies all files matching pattern from srcDir to dstDir.
// Files that already exist in dstDir are skipped unless force is true.
func copyGlob(srcDir, dstDir, pattern string, force bool, logger *slog.Logger) (*copyStats, error) {
	matches, err := filepath.Glob(filepath.Join(srcDir, pattern))
	if err != nil {
		return &copyStats{}, fmt.Errorf("glob %q: %w", filepath.Join(srcDir, pattern), err)
	}

	stats := &copyStats{}
	for _, src := range matches {
		dst := filepath.Join(dstDir, filepath.Base(src))
		if !force {
			if _, statErr := os.Stat(dst); statErr == nil {
				logger.Debug("skip existing file", "path", dst)
				stats.skipped++
				continue
			}
		}
		if err := copyFile(src, dst); err != nil {
			logger.Warn("copy file", "src", src, "dst", dst, "err", err)
		} else {
			stats.written++
		}
	}
	return stats, nil
}

// copyInitFileSrc copies src to dst, skipping if dst exists and force is false.
// Returns (true, nil) if the file was copied, (false, nil) if it was skipped,
// or (false, err) on I/O failure.
func copyInitFileSrc(src, dst string, force bool) (bool, error) {
	if !force {
		if _, err := os.Stat(dst); err == nil {
			return false, nil // already exists, skip
		}
	}
	if _, err := os.Stat(src); err != nil {
		return false, fmt.Errorf("source file %q not found: %w", src, err)
	}
	if err := copyFile(src, dst); err != nil {
		return false, err
	}
	return true, nil
}

// augmentGitignore appends missing entries to the .gitignore at path.
// It reads the existing file (if any), checks for exact-line matches, and
// appends only the missing entries. Returns the list of entries actually added.
func augmentGitignore(path string, entries []string) ([]string, error) {
	// Read existing content.
	existing := map[string]bool{}
	if data, err := os.ReadFile(path); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			existing[strings.TrimRight(scanner.Text(), " \t")] = true
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read .gitignore: %w", err)
	}

	// Determine which entries are missing.
	var missing []string
	for _, e := range entries {
		if !existing[e] {
			missing = append(missing, e)
		}
	}

	if len(missing) == 0 {
		return nil, nil
	}

	// Append missing entries.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open .gitignore for append: %w", err)
	}
	defer f.Close()

	// Ensure we start on a new line if the file is non-empty.
	info, _ := f.Stat()
	if info != nil && info.Size() > 0 {
		// Read the last byte to check for trailing newline.
		rf, err := os.Open(path)
		if err == nil {
			if _, err := rf.Seek(-1, 2); err == nil {
				buf := make([]byte, 1)
				if _, err := rf.Read(buf); err == nil && buf[0] != '\n' {
					_, _ = f.WriteString("\n")
				}
			}
			rf.Close()
		}
	}

	// Skip leading newline when creating a fresh .gitignore — avoid blank first line.
	header := "\n# coworker runtime state (generated by coworker init)\n"
	if info, err := f.Stat(); err == nil && info.Size() == 0 {
		header = "# coworker runtime state (generated by coworker init)\n"
	}
	_, err = f.WriteString(header)
	if err != nil {
		return nil, fmt.Errorf("write gitignore header: %w", err)
	}
	for _, e := range missing {
		if _, err := fmt.Fprintln(f, e); err != nil {
			return nil, fmt.Errorf("write gitignore entry %q: %w", e, err)
		}
	}

	return missing, nil
}
