package coding

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// RouteMode describes how a dispatch was routed.
type RouteMode string

const (
	// RouteModeWorker means the dispatch was routed to one or more live
	// registered workers. The caller should NOT spawn an ephemeral process.
	RouteModeWorker RouteMode = "worker"

	// RouteModeEphemeral means no live worker was found. A dispatch row with
	// worker_handle=NULL has been enqueued; the caller should spawn an
	// ephemeral CLI process which will claim it via orch_next_dispatch.
	RouteModeEphemeral RouteMode = "ephemeral"
)

// RouteResult is returned by DispatchRouter.Route.
type RouteResult struct {
	// Mode is "worker" when dispatched to live workers, "ephemeral" otherwise.
	Mode RouteMode

	// Workers contains the handles that received the dispatch (Mode=worker).
	// Empty when Mode=ephemeral.
	Workers []string

	// DispatchIDs are the IDs of the enqueued dispatch rows, one per worker
	// (or one for ephemeral).
	DispatchIDs []string
}

// DispatchRouter routes a job dispatch to live registered workers when they
// exist, or enqueues an ephemeral dispatch for an on-demand CLI spawn.
//
// Routing rules (from spec §Lifecycle):
//   - single concurrency: route to the oldest live worker. If none, ephemeral.
//     If >1, use oldest and log a warning.
//   - many concurrency: route to every live worker. If none, ephemeral (one job).
type DispatchRouter struct {
	workers  *store.WorkerStore
	dispatch *store.DispatchStore
	logger   *slog.Logger
}

// NewDispatchRouter creates a DispatchRouter.
func NewDispatchRouter(ws *store.WorkerStore, ds *store.DispatchStore, logger *slog.Logger) *DispatchRouter {
	if logger == nil {
		logger = slog.Default()
	}
	return &DispatchRouter{workers: ws, dispatch: ds, logger: logger}
}

// Route resolves live workers for the given dispatch template and enqueues
// rows in the dispatches table.
//
// For persistent routing (Mode=worker), each row has worker_handle set to the
// target worker. For ephemeral routing (Mode=ephemeral), one row is created
// with worker_handle=NULL; the caller must spawn an ephemeral CLI process
// which will claim it via orch_next_dispatch(handle="").
func (r *DispatchRouter) Route(ctx context.Context, d *core.Dispatch, concurrency string) (*RouteResult, error) {
	live, err := r.workers.LiveWorkersForRole(ctx, d.Role)
	if err != nil {
		return nil, fmt.Errorf("query live workers: %w", err)
	}

	switch concurrency {
	case "single", "":
		return r.routeSingle(ctx, d, live)
	case "many":
		return r.routeMany(ctx, d, live)
	default:
		return nil, fmt.Errorf("unknown concurrency %q (want 'single' or 'many')", concurrency)
	}
}

func (r *DispatchRouter) routeSingle(ctx context.Context, d *core.Dispatch, live []core.Worker) (*RouteResult, error) {
	if len(live) == 0 {
		// No live workers — enqueue with NULL worker_handle for ephemeral pickup.
		return r.enqueueEphemeral(ctx, d)
	}

	if len(live) > 1 {
		r.logger.Warn("multiple live workers for single-concurrency role; routing to oldest",
			"role", d.Role, "count", len(live))
	}

	target := live[0] // oldest (registered_at ASC)
	return r.enqueue(ctx, d, []core.Worker{target})
}

func (r *DispatchRouter) routeMany(ctx context.Context, d *core.Dispatch, live []core.Worker) (*RouteResult, error) {
	if len(live) == 0 {
		// No live workers — enqueue one dispatch with NULL handle for ephemeral pickup.
		return r.enqueueEphemeral(ctx, d)
	}
	return r.enqueue(ctx, d, live)
}

// enqueueEphemeral creates one dispatch row with worker_handle=NULL.
func (r *DispatchRouter) enqueueEphemeral(ctx context.Context, template *core.Dispatch) (*RouteResult, error) {
	ephemeral := &core.Dispatch{
		ID:     core.NewID(),
		RunID:  template.RunID,
		Role:   template.Role,
		JobID:  template.JobID,
		Prompt: template.Prompt,
		Inputs: template.Inputs,
		Mode:   core.DispatchModeEphemeral,
		// WorkerHandle: "" → stored as NULL
	}
	if err := r.dispatch.EnqueueDispatch(ctx, ephemeral); err != nil {
		return nil, fmt.Errorf("enqueue ephemeral dispatch: %w", err)
	}
	return &RouteResult{
		Mode:        RouteModeEphemeral,
		DispatchIDs: []string{ephemeral.ID},
	}, nil
}

// enqueue creates one dispatch row per worker with worker_handle set.
func (r *DispatchRouter) enqueue(ctx context.Context, template *core.Dispatch, workers []core.Worker) (*RouteResult, error) {
	result := &RouteResult{Mode: RouteModeWorker}

	for _, w := range workers {
		d := &core.Dispatch{
			ID:           core.NewID(),
			RunID:        template.RunID,
			Role:         template.Role,
			JobID:        template.JobID,
			Prompt:       template.Prompt,
			Inputs:       template.Inputs,
			WorkerHandle: w.Handle,
			Mode:         core.DispatchModePersistent,
		}
		if err := r.dispatch.EnqueueDispatch(ctx, d); err != nil {
			return nil, fmt.Errorf("enqueue dispatch for worker %q: %w", w.Handle, err)
		}
		result.Workers = append(result.Workers, w.Handle)
		result.DispatchIDs = append(result.DispatchIDs, d.ID)
	}

	return result, nil
}
