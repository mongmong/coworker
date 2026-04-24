package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/coding/eventbus"
	"github.com/chris/coworker/store"
)

// stores bundles the store layer objects derived from ServerConfig.DB.
// They are nil when DB is nil (stubs active during early plan phases).
type stores struct {
	run       *store.RunStore
	event     *store.EventStore
	dispatch  *store.DispatchStore
	job       *store.JobStore
	attention *store.AttentionStore
}

// ServerConfig holds the runtime dependencies required to construct the MCP
// server. All fields are optional during early plan phases; the server
// registers stub handlers regardless.
type ServerConfig struct {
	DB         *store.DB
	Dispatcher *coding.Dispatcher
	EventBus   *eventbus.InMemoryBus
}

// Server wraps the official MCP SDK server and holds coworker runtime deps.
type Server struct {
	inner  *mcp.Server
	cfg    ServerConfig
	stores stores
}

// notImplemented is the shared output type for stub tool handlers.
type notImplemented struct {
	Status string `json:"status"`
}

// stubResult returns the standard not-implemented response.
func stubResult() notImplemented {
	return notImplemented{Status: "not_implemented"}
}

// NewServer creates an MCP server, registers all orch.* tool stubs, and
// returns it ready to serve. The caller invokes Run to start listening.
func NewServer(cfg ServerConfig) (*Server, error) {
	inner := mcp.NewServer(&mcp.Implementation{
		Name:    "coworker",
		Version: "v0.1.0",
	}, nil)

	s := &Server{inner: inner, cfg: cfg}

	// Build store layer when a DB is provided.
	if cfg.DB != nil {
		es := store.NewEventStore(cfg.DB)
		s.stores = stores{
			event:     es,
			run:       store.NewRunStore(cfg.DB, es),
			dispatch:  store.NewDispatchStore(cfg.DB, es),
			job:       store.NewJobStore(cfg.DB, es),
			attention: store.NewAttentionStore(cfg.DB),
		}
	}

	s.registerTools()

	return s, nil
}

// Run starts the MCP server on stdio transport, blocking until ctx is done or
// the transport closes.
func (s *Server) Run(ctx context.Context) error {
	if err := s.inner.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}
	return nil
}

// registerTools wires all orch.* tool stubs onto the inner MCP server.
func (s *Server) registerTools() {
	// --- run tools -----------------------------------------------------------

	type emptyInput struct{}

	if s.stores.run != nil && s.stores.event != nil {
		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_run_status",
				Description: "Return the status of a run by run_id.",
			},
			handleRunStatus(s.stores.run),
		)

		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_run_inspect",
				Description: "Return full details for a specific run including its events.",
			},
			handleRunInspect(s.stores.run, s.stores.event),
		)
	} else {
		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_run_status",
				Description: "Return the status of a run by run_id.",
			},
			func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, notImplemented, error) {
				return nil, stubResult(), nil
			},
		)

		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_run_inspect",
				Description: "Return full details for a specific run including its events.",
			},
			func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, notImplemented, error) {
				return nil, stubResult(), nil
			},
		)
	}

	// --- role tools ----------------------------------------------------------

	if s.cfg.Dispatcher != nil {
		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_role_invoke",
				Description: "Invoke a named role synchronously, creating a run and waiting for completion.",
			},
			handleRoleInvoke(s.cfg.Dispatcher),
		)
	} else {
		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_role_invoke",
				Description: "Invoke a named role synchronously, creating a run and waiting for completion.",
			},
			func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, notImplemented, error) {
				return nil, stubResult(), nil
			},
		)
	}

	// --- dispatch tools (pull model) -----------------------------------------

	if s.stores.dispatch != nil {
		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_next_dispatch",
				Description: "Poll the orchestrator for the next pending dispatch assigned to the calling worker. Returns the dispatch payload or {\"status\": \"idle\"} when there is nothing to do.",
			},
			handleNextDispatch(s.stores.dispatch),
		)

		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_job_complete",
				Description: "Report that a dispatched job is complete. Provide the job_id from the dispatch and structured outputs.",
			},
			handleJobComplete(s.stores.dispatch, s.stores.job),
		)
	} else {
		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_next_dispatch",
				Description: "Poll the orchestrator for the next pending dispatch assigned to the calling worker. Returns the dispatch payload or {\"status\": \"idle\"} when there is nothing to do.",
			},
			func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, notImplemented, error) {
				return nil, stubResult(), nil
			},
		)

		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_job_complete",
				Description: "Report that a dispatched job is complete. Provide the job_id from the dispatch and structured outputs.",
			},
			func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, notImplemented, error) {
				return nil, stubResult(), nil
			},
		)
	}

	// --- human-in-the-loop tools ---------------------------------------------

	if s.stores.attention != nil {
		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_ask_user",
				Description: "Post a question to the human operator and block until they answer via the attention queue.",
			},
			handleAskUser(s.stores.attention),
		)
	} else {
		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_ask_user",
				Description: "Post a question to the human operator and block until they answer via the attention queue.",
			},
			func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, notImplemented, error) {
				return nil, stubResult(), nil
			},
		)
	}

	// --- attention tools -----------------------------------------------------

	if s.stores.attention != nil {
		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_attention_list",
				Description: "List all open attention requests awaiting a human answer.",
			},
			handleAttentionList(s.stores.attention),
		)

		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_attention_answer",
				Description: "Submit a human answer for an open attention request, unblocking the waiting job.",
			},
			handleAttentionAnswer(s.stores.attention),
		)
	} else {
		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_attention_list",
				Description: "List all open attention requests awaiting a human answer.",
			},
			func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, notImplemented, error) {
				return nil, stubResult(), nil
			},
		)

		mcp.AddTool(s.inner,
			&mcp.Tool{
				Name:        "orch_attention_answer",
				Description: "Submit a human answer for an open attention request, unblocking the waiting job.",
			},
			func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, notImplemented, error) {
				return nil, stubResult(), nil
			},
		)
	}

	// --- findings tools ------------------------------------------------------

	mcp.AddTool(s.inner,
		&mcp.Tool{
			Name:        "orch_findings_list",
			Description: "List findings recorded for a run or job, optionally filtered by severity.",
		},
		func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, notImplemented, error) {
			return nil, stubResult(), nil
		},
	)

	// --- artifact tools ------------------------------------------------------

	mcp.AddTool(s.inner,
		&mcp.Tool{
			Name:        "orch_artifact_read",
			Description: "Read the contents of an artifact produced by a job.",
		},
		func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, notImplemented, error) {
			return nil, stubResult(), nil
		},
	)

	mcp.AddTool(s.inner,
		&mcp.Tool{
			Name:        "orch_artifact_write",
			Description: "Write or update an artifact associated with the current job.",
		},
		func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, notImplemented, error) {
			return nil, stubResult(), nil
		},
	)
}

// Tools returns the names of all registered tools, in registration order.
// Used by tests to assert the full tool surface is present.
func (s *Server) Tools() []string {
	return []string{
		"orch_run_status",
		"orch_run_inspect",
		"orch_role_invoke",
		"orch_next_dispatch",
		"orch_job_complete",
		"orch_ask_user",
		"orch_attention_list",
		"orch_attention_answer",
		"orch_findings_list",
		"orch_artifact_read",
		"orch_artifact_write",
	}
}
