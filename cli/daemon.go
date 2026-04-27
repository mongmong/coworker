package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/coding/eventbus"
	"github.com/chris/coworker/core"
	mcpserver "github.com/chris/coworker/mcp"
	"github.com/chris/coworker/store"
)

var (
	daemonDBPath         string
	daemonHTTPPort       int
	daemonRoleDir        string
	daemonPromptDir      string
	daemonCliBinary      string
	daemonClaudeBinary   string
	daemonCodexBinary    string
	daemonOpenCodeBinary string
	daemonOpenCodeServer string
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Start the coworker daemon (scheduler + MCP server).",
	Long: `Start the coworker daemon.

The daemon runs the job scheduler, event bus, and MCP server. CLI workers
(Claude Code, Codex, OpenCode) connect to the MCP server via stdio transport
to receive dispatches and report results.

An HTTP/SSE server is also started (default port 7700) providing:
  GET  /events                — SSE event stream
  GET  /runs                  — list runs
  GET  /runs/{id}             — run details
  GET  /runs/{id}/jobs        — jobs for a run
  GET  /attention             — list pending attention items
  POST /attention/{id}/answer — answer an attention item

The daemon blocks until interrupted (SIGINT / SIGTERM).

Example:
  coworker daemon
  coworker daemon --db /path/to/state.db
  coworker daemon --http-port 8080`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runDaemon(cmd)
	},
}

func init() {
	daemonCmd.Flags().StringVar(&daemonDBPath, "db", "", "Path to SQLite database (default: .coworker/state.db)")
	daemonCmd.Flags().IntVar(&daemonHTTPPort, "http-port", 7700, "Port for the HTTP/SSE server")
	daemonCmd.Flags().StringVar(&daemonRoleDir, "role-dir", "", "Path to the role YAML directory (default: .coworker/roles or coding/roles)")
	daemonCmd.Flags().StringVar(&daemonPromptDir, "prompt-dir", "", "Path to the prompt template directory (default: .coworker or coding)")
	daemonCmd.Flags().StringVar(&daemonCliBinary, "cli-binary", "", "Fallback CLI binary for all roles (default: codex). Overridden by per-CLI flags.")
	daemonCmd.Flags().StringVar(&daemonClaudeBinary, "claude-binary", "", "Path to the claude-code binary (default: resolved from PATH)")
	daemonCmd.Flags().StringVar(&daemonCodexBinary, "codex-binary", "", "Path to the codex binary (default: resolved from PATH)")
	daemonCmd.Flags().StringVar(&daemonOpenCodeBinary, "opencode-binary", "", "Path to the opencode binary (default: resolved from PATH)")
	daemonCmd.Flags().StringVar(&daemonOpenCodeServer, "opencode-server", agent.DefaultOpenCodeServerURL, "OpenCode HTTP server URL for HTTP-primary dispatch. When non-empty, uses OpenCodeHTTPAgent instead of CliAgent for opencode roles. Set to empty string to fall back to the CLI binary.")
	rootCmd.AddCommand(daemonCmd)
}

func runDaemon(cmd *cobra.Command) error {
	// Build root context that cancels on SIGINT/SIGTERM.
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

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
			logger.Error("close database", "error", closeErr)
		}
	}()

	logger.Info("database opened", "path", dbPath)

	// Create the event bus.
	bus := eventbus.NewInMemoryBus()

	// Build the Dispatcher so MCP role invocations drive real jobs.
	dispatcher, err := buildDaemonDispatcher(db, daemonRoleDir, daemonPromptDir, daemonCliBinary, daemonClaudeBinary, daemonCodexBinary, daemonOpenCodeBinary, daemonOpenCodeServer, logger)
	if err != nil {
		return fmt.Errorf("build dispatcher: %w", err)
	}

	// Create the MCP server with the real Dispatcher wired in.
	srv, err := mcpserver.NewServer(mcpserver.ServerConfig{
		DB:         db,
		EventBus:   bus,
		Dispatcher: dispatcher,
	})
	if err != nil {
		return fmt.Errorf("create mcp server: %w", err)
	}

	// Build the HTTP mux sharing the same DB and event bus.
	es := store.NewEventStore(db)
	mux := buildHTTPMux(bus, httpStores{
		run:        store.NewRunStore(db, es),
		job:        store.NewJobStore(db, es),
		attention:  store.NewAttentionStore(db),
		checkpoint: store.NewCheckpointStore(db, es),
	})

	logger.Info("coworker daemon starting", "db", dbPath, "http_port", daemonHTTPPort)

	// Run MCP stdio server and HTTP server concurrently with mutual cancellation.
	// Whichever goroutine exits first calls cancel(), shutting down the other.
	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		defer cancel() // MCP exit cancels HTTP peer
		if err := srv.Run(gCtx); err != nil {
			return fmt.Errorf("mcp server: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		defer cancel() // HTTP exit cancels MCP peer
		httpSrv := &http.Server{
			Addr:              fmt.Sprintf(":%d", daemonHTTPPort),
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second, // mitigate Slowloris (gosec G112)
		}
		// Graceful shutdown when context is cancelled.
		go func() {
			<-gCtx.Done()
			_ = httpSrv.Shutdown(context.Background())
		}()
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return err
	}

	logger.Info("coworker daemon stopped")
	return nil
}

// buildDaemonDispatcher constructs a *coding.Dispatcher for the daemon.
// It resolves role/prompt directories using the same fallback logic as
// buildRunDispatcher in cli/run.go.
//
// Per-CLI binary flags (claudeBinary, codexBinary, openCodeBinary) populate the
// CLIAgents map so each role is dispatched to the correct tool. The cliBinary
// fallback is used as the default Agent when a role's CLI is not in the map.
// When openCodeServer is non-empty, the "opencode" slot uses OpenCodeHTTPAgent
// instead of CliAgent.
func buildDaemonDispatcher(db *store.DB, roleDir, promptDir, cliBinary, claudeBinary, codexBinary, openCodeBinary, openCodeServer string, logger *slog.Logger) (*coding.Dispatcher, error) {
	if roleDir == "" {
		roleDir = filepath.Join(".coworker", "roles")
		if _, err := os.Stat(roleDir); os.IsNotExist(err) {
			roleDir = filepath.Join("coding", "roles")
		}
	}

	// Resolve the effective coworker directory for JSONL log persistence.
	// The daemon always uses .coworker (derived from the DB path's parent).
	coworkerDir := daemonDBPath
	if coworkerDir == "" {
		coworkerDir = ".coworker"
	} else {
		coworkerDir = filepath.Dir(coworkerDir)
	}

	if promptDir == "" {
		if _, err := os.Stat(coworkerDir); os.IsNotExist(err) {
			promptDir = "coding"
		} else {
			promptDir = coworkerDir
		}
	}

	if cliBinary == "" {
		cliBinary = "codex"
	}

	// Build per-CLI agent map. Defaults from PATH when flags are empty.
	if claudeBinary == "" {
		claudeBinary = "claude"
	}
	if codexBinary == "" {
		codexBinary = "codex"
	}
	if openCodeBinary == "" {
		openCodeBinary = "opencode"
	}

	newAgentWithDir := func(bin string) *agent.CliAgent {
		a := agent.NewCliAgent(bin)
		a.CoworkerDir = coworkerDir
		return a
	}

	// Build the opencode agent: use HTTP-primary when --opencode-server is set,
	// fall back to CliAgent otherwise.
	var openCodeAgent core.Agent
	if openCodeServer != "" {
		openCodeAgent = &agent.OpenCodeHTTPAgent{ServerURL: openCodeServer}
	} else {
		openCodeAgent = newAgentWithDir(openCodeBinary)
	}

	cliAgents := map[string]core.Agent{
		"claude-code": newAgentWithDir(claudeBinary),
		"codex":       newAgentWithDir(codexBinary),
		"opencode":    openCodeAgent,
	}

	d := &coding.Dispatcher{
		RoleDir:          roleDir,
		PromptDir:        promptDir,
		Agent:            newAgentWithDir(cliBinary),
		CLIAgents:        cliAgents,
		DB:               db,
		Logger:           logger,
		SupervisorWriter: store.NewSupervisorEventStore(db, store.NewEventStore(db)),
		CostWriter:       store.NewCostEventStore(db, store.NewEventStore(db)),
	}
	return d, nil
}
