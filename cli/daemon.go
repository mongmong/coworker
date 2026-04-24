package cli

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/chris/coworker/coding/eventbus"
	mcpserver "github.com/chris/coworker/mcp"
	"github.com/chris/coworker/store"
)

var daemonDBPath string

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Start the coworker daemon (scheduler + MCP server).",
	Long: `Start the coworker daemon.

The daemon runs the job scheduler, event bus, and MCP server. CLI workers
(Claude Code, Codex, OpenCode) connect to the MCP server via stdio transport
to receive dispatches and report results.

The daemon blocks until interrupted (SIGINT / SIGTERM).

Example:
  coworker daemon
  coworker daemon --db /path/to/state.db`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runDaemon(cmd)
	},
}

func init() {
	daemonCmd.Flags().StringVar(&daemonDBPath, "db", "", "Path to SQLite database (default: .coworker/state.db)")
	rootCmd.AddCommand(daemonCmd)
}

func runDaemon(cmd *cobra.Command) error {
	ctx := cmd.Context()

	// Resolve DB path.
	dbPath := daemonDBPath
	if dbPath == "" {
		dbPath = filepath.Join(".coworker", "state.db")
	}

	// Ensure the directory exists.
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("create db directory %q: %w", dbPath, err)
	}

	// Open the database and run migrations.
	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			slog.Error("close database", "error", closeErr)
		}
	}()

	slog.Info("database opened", "path", dbPath)

	// Create the event bus.
	bus := eventbus.NewInMemoryBus()

	// Create the MCP server.
	srv, err := mcpserver.NewServer(mcpserver.ServerConfig{
		DB:       db,
		EventBus: bus,
		// Dispatcher is wired in a later plan phase once the full dispatch
		// pipeline is integrated with the persistent-worker protocol.
	})
	if err != nil {
		return fmt.Errorf("create mcp server: %w", err)
	}

	slog.Info("coworker daemon starting", "db", dbPath)

	// Run blocks until ctx is cancelled (SIGINT/SIGTERM via Execute's
	// signal.NotifyContext) or the stdio transport closes.
	if err := srv.Run(ctx); err != nil {
		return fmt.Errorf("daemon: %w", err)
	}

	slog.Info("coworker daemon stopped")
	return nil
}
