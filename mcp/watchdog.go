package mcp

import (
	"context"
	"log/slog"
	"time"

	"github.com/chris/coworker/store"
)

// WatchdogConfig controls heartbeat watchdog timing.
// Zero values use the spec defaults.
type WatchdogConfig struct {
	// Interval is how often the watchdog runs. Default: 15 s.
	Interval time.Duration
	// StaleAfter is how long since the last heartbeat before a worker
	// transitions to 'stale'. Default: 45 s (3 missed 15-second heartbeats).
	StaleAfter time.Duration
	// EvictAfter is how long since the last heartbeat before a stale worker
	// is evicted and its dispatches are requeued. Default: StaleAfter + Interval.
	// This gives workers one full watchdog interval to recover from the stale
	// state before eviction fires, making the two-phase transition (live→stale
	// on one tick, stale→evicted on the next tick) effective even at default
	// configuration.
	EvictAfter time.Duration
}

func (c WatchdogConfig) interval() time.Duration {
	if c.Interval <= 0 {
		return 15 * time.Second
	}
	return c.Interval
}

func (c WatchdogConfig) staleAfter() time.Duration {
	if c.StaleAfter <= 0 {
		return 45 * time.Second
	}
	return c.StaleAfter
}

func (c WatchdogConfig) evictAfter() time.Duration {
	if c.EvictAfter <= 0 {
		// Default: one full interval beyond StaleAfter so the two-phase
		// transition (live→stale, stale→evicted) always spans at least two
		// watchdog ticks, even at default configuration.
		return c.staleAfter() + c.interval()
	}
	return c.EvictAfter
}

// HeartbeatWatchdog checks live workers on a fixed interval, marks stale
// workers, evicts those that stay stale, and requeues their dispatches.
// It runs until ctx is cancelled.
//
// The two-phase transition (live→stale, stale→evicted) gives workers one
// full watchdog interval to recover before dispatch requeue:
//   - Tick N:   last_heartbeat_at < now-StaleAfter  → live becomes stale
//   - Tick N+1: last_heartbeat_at < now-EvictAfter  → stale becomes evicted
//
// With defaults (interval=15s, staleAfter=45s, evictAfter=60s), a worker
// must miss heartbeats for ~75 s before its dispatches are requeued.
type HeartbeatWatchdog struct {
	workers  *store.WorkerStore
	dispatch *store.DispatchStore
	cfg      WatchdogConfig
	logger   *slog.Logger
}

// NewHeartbeatWatchdog creates a watchdog. Pass a nil logger to use slog.Default().
func NewHeartbeatWatchdog(
	workers *store.WorkerStore,
	dispatch *store.DispatchStore,
	cfg WatchdogConfig,
	logger *slog.Logger,
) *HeartbeatWatchdog {
	if logger == nil {
		logger = slog.Default()
	}
	return &HeartbeatWatchdog{
		workers:  workers,
		dispatch: dispatch,
		cfg:      cfg,
		logger:   logger,
	}
}

// Run starts the watchdog loop. It blocks until ctx is cancelled.
// Call it in a goroutine; the caller is responsible for context lifecycle.
func (w *HeartbeatWatchdog) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.interval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *HeartbeatWatchdog) tick(ctx context.Context) {
	now := time.Now()

	// Phase 1: mark live workers that missed StaleAfter as stale.
	staleCutoff := now.Add(-w.cfg.staleAfter())
	stale, err := w.workers.MarkStale(ctx, staleCutoff)
	if err != nil {
		w.logger.Error("watchdog: mark stale failed", "error", err)
	} else if len(stale) > 0 {
		w.logger.Warn("watchdog: marked workers stale", "count", len(stale), "handles", stale)
	}

	// Phase 2: evict workers that have been stale since EvictAfter.
	evictCutoff := now.Add(-w.cfg.evictAfter())
	evicted, err := w.workers.MarkEvicted(ctx, evictCutoff)
	if err != nil {
		w.logger.Error("watchdog: evict failed", "error", err)
		return
	}

	for _, handle := range evicted {
		w.logger.Warn("watchdog: evicting worker, requeueing dispatches", "handle", handle)
		if err := w.dispatch.RequeueByWorker(ctx, handle); err != nil {
			w.logger.Error("watchdog: requeue dispatches failed", "handle", handle, "error", err)
		}
	}
}
