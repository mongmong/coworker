# Plan 102 — Event Bus + SSE Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** In-memory event bus with SSE streaming and `coworker watch` CLI for real-time runtime observability.

**Architecture:** New packages core/bus.go (EventBus interface), coding/eventbus/ (InMemoryBus + SSE handler), cli/watch.go (coworker watch command), and a modification to store/event_store.go to publish events on write.

**Tech Stack:** Go 1.25+, net/http stdlib, encoding/json, SSE protocol (text/event-stream).

**Branch:** `feature/plan-102-event-bus-sse`.

**Manifest entry:** `docs/specs/001-plan-manifest.md` section 102.

---

## Architecture

Plan 102 adds a live, in-memory fan-out path on top of the existing append-only SQLite event log from Plan 100. The `events` table remains the authoritative history of a run; the new event bus is only a mirror of successfully committed writes, which preserves the event-log-before-state invariant and keeps replay semantics unchanged.

The SSE endpoint is intentionally live-only. It streams already-committed events to connected clients and does not add replay or retention state inside the bus. That matches the spec's observability model: persistence and replay stay in SQLite, while runtime surfaces consume a typed live stream. Fan-in aggregation also stays outside the bus; the bus publishes each committed event as-is so downstream consumers still see duplicate/conflicting reviewer outputs until an aggregator or UI explicitly merges them.

### File layout

New files:

- `core/bus.go` — `core.EventBus` interface.
- `coding/eventbus/bus.go` — `InMemoryBus` implementation.
- `coding/eventbus/bus_test.go` — unit tests for subscribe/publish/unsubscribe, slow subscribers, concurrent publish.
- `coding/eventbus/sse.go` — SSE `http.Handler` for `/events`.
- `coding/eventbus/sse_test.go` — handler tests with `httptest.NewRecorder`.
- `cli/watch.go` — `coworker watch` command plus SSE client/pretty-print helpers.
- `cli/watch_test.go` — integration-style CLI test against `httptest.NewServer`.
- `store/snapshot_test_helper.go` — reusable golden snapshot assertion helper for event logs.
- `testdata/events/invoke_reviewer_arch.golden.json` — normalized golden snapshot for the Plan 100 end-to-end integration test.

Modified files:

- `store/event_store.go` — add optional `Bus core.EventBus`; publish after successful commit.
- `store/event_store_test.go` — verify publish-on-write behavior.
- `cli/root.go` — register `watch` from the root command.
- `tests/integration/invoke_test.go` — replace ad hoc event assertions with `store.AssertGoldenEvents`.

### Runtime flow

1. `store.EventStore.WriteEventThenRow` commits the event + projection transaction.
2. After commit succeeds, `EventStore` publishes the same in-memory `*core.Event` to `core.EventBus` when configured.
3. `coding/eventbus.SSEHandler` subscribes a buffered channel to the bus, filters by `run_id` / `kind`, and writes `data: {json}\n\n`.
4. `coworker watch` connects to `/events`, parses `data:` lines, decodes `core.Event`, and prints a concise human-readable line.
5. Snapshot tests continue asserting the durable event log from SQLite, not the transient bus, so observability never becomes the source of truth.

### Key design points

1. `core.EventBus` must live in `core/` so `store/` can depend on the abstraction without importing `coding/`.

```go
type EventBus interface {
	Publish(event *Event)
	Subscribe(ch chan<- *Event)
	Unsubscribe(ch chan<- *Event)
}
```

2. `coding/eventbus.InMemoryBus` uses a mutex-protected subscriber set and publishes with `select { case ch <- event: default: }` so one slow subscriber never stalls runtime writes.

3. `coding/eventbus.SSEHandler` is a plain `net/http` handler:
   - sets `Content-Type: text/event-stream`
   - subscribes to the bus
   - emits `data: {json}\n\n`
   - filters by `?run_id=X` and `?kind=Y`
   - flushes after every event with `http.Flusher`
   - unsubscribes on `r.Context().Done()`

4. `coworker watch` is a read-only SSE client:
   - connects to `http://localhost:<port>/events`
   - forwards `--run` as `run_id` and `--kind` as `kind`
   - parses SSE `data:` frames
   - decodes `core.Event`
   - prints `timestamp kind run=<id> payload=<compact summary>`

5. `store.EventStore` gets an optional `Bus core.EventBus` field. Publication happens only after `tx.Commit()` succeeds; failed transactions never publish.

6. The snapshot helper must normalize volatile IDs while preserving event ordering, kinds, schema versions, and meaningful payload fields. It intentionally omits volatile timestamps from the golden snapshot so Plan 100 integration tests stay deterministic without rewriting the runtime clock first.

7. This plan does not add a replay buffer or server bootstrap. The reusable SSE handler is the production contract; the process that owns the HTTP server mounts it at `/events`. Tests use `httptest.NewServer` to verify the contract end-to-end before a daemon command exists.

---

## Task 1: Core EventBus interface + InMemoryBus implementation + tests

**Files:**
- Create: `core/bus.go`
- Create: `coding/eventbus/bus.go`
- Create: `coding/eventbus/bus_test.go`

### Step 1.1: Write the core interface first

- [ ] Create `core/bus.go`:

```go
package core

// EventBus is the live fan-out surface for already-committed runtime events.
// SQLite remains the source of truth; the bus is used for real-time observers.
type EventBus interface {
	Publish(event *Event)
	Subscribe(ch chan<- *Event)
	Unsubscribe(ch chan<- *Event)
}
```

### Step 1.2: Write the failing unit tests before the bus implementation

- [ ] Create `coding/eventbus/bus_test.go`:

```go
package eventbus

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func TestInMemoryBus_SubscribePublishUnsubscribe(t *testing.T) {
	t.Parallel()

	bus := NewInMemoryBus()
	sub := make(chan *core.Event, 1)
	bus.Subscribe(sub)

	event := &core.Event{
		ID:            "evt_subscribe",
		RunID:         "run_subscribe",
		Sequence:      1,
		Kind:          core.EventRunCreated,
		SchemaVersion: 1,
		Payload:       `{"run_id":"run_subscribe"}`,
		CreatedAt:     time.Unix(1, 0).UTC(),
	}

	bus.Publish(event)

	select {
	case got := <-sub:
		if got != event {
			t.Fatalf("received event pointer %p, want %p", got, event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published event")
	}

	bus.Unsubscribe(sub)
	bus.Publish(&core.Event{ID: "evt_after_unsubscribe"})

	select {
	case got := <-sub:
		t.Fatalf("received event after unsubscribe: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestInMemoryBus_SlowSubscriberDoesNotBlock(t *testing.T) {
	t.Parallel()

	bus := NewInMemoryBus()
	slow := make(chan *core.Event)
	fast := make(chan *core.Event, 1)

	bus.Subscribe(slow)
	bus.Subscribe(fast)

	done := make(chan struct{})
	event := &core.Event{
		ID:            "evt_non_blocking",
		RunID:         "run_non_blocking",
		Sequence:      1,
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		Payload:       `{"job_id":"job_1"}`,
		CreatedAt:     time.Unix(2, 0).UTC(),
	}

	go func() {
		bus.Publish(event)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Publish blocked on a slow subscriber")
	}

	select {
	case got := <-fast:
		if got != event {
			t.Fatalf("fast subscriber received %p, want %p", got, event)
		}
	case <-time.After(time.Second):
		t.Fatal("fast subscriber did not receive the event")
	}
}

func TestInMemoryBus_ConcurrentPublishIsSafe(t *testing.T) {
	t.Parallel()

	const (
		publishers         = 8
		eventsPerPublisher = 25
		totalEvents        = publishers * eventsPerPublisher
	)

	bus := NewInMemoryBus()
	sub := make(chan *core.Event, totalEvents)
	bus.Subscribe(sub)

	var wg sync.WaitGroup
	for publisher := 0; publisher < publishers; publisher++ {
		publisher := publisher
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < eventsPerPublisher; i++ {
				bus.Publish(&core.Event{
					ID:            fmt.Sprintf("evt_%d_%d", publisher, i),
					RunID:         "run_concurrent",
					Sequence:      publisher*eventsPerPublisher + i + 1,
					Kind:          core.EventJobCompleted,
					SchemaVersion: 1,
					Payload:       fmt.Sprintf(`{"publisher":%d,"index":%d}`, publisher, i),
					CreatedAt:     time.Unix(int64(i+1), 0).UTC(),
				})
			}
		}()
	}

	wg.Wait()

	received := 0
	deadline := time.After(2 * time.Second)
	for received < totalEvents {
		select {
		case <-sub:
			received++
		case <-deadline:
			t.Fatalf("received %d events, want %d", received, totalEvents)
		}
	}
}
```

### Step 1.3: Run the new tests and confirm they fail on missing implementation

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./coding/eventbus -run TestInMemoryBus -count=1
```

Expected: build failure for missing `NewInMemoryBus`.

### Step 1.4: Implement the in-memory bus

- [ ] Create `coding/eventbus/bus.go`:

```go
package eventbus

import (
	"sync"

	"github.com/chris/coworker/core"
)

// InMemoryBus fan-outs committed runtime events to live subscribers.
// It intentionally keeps no replay buffer; the SQLite event log is authoritative.
type InMemoryBus struct {
	mu          sync.RWMutex
	subscribers map[chan<- *core.Event]struct{}
}

// NewInMemoryBus creates an empty in-memory event bus.
func NewInMemoryBus() *InMemoryBus {
	return &InMemoryBus{
		subscribers: make(map[chan<- *core.Event]struct{}),
	}
}

// Publish sends the event to all current subscribers without blocking.
func (b *InMemoryBus) Publish(event *core.Event) {
	if event == nil {
		return
	}

	b.mu.RLock()
	subscribers := make([]chan<- *core.Event, 0, len(b.subscribers))
	for ch := range b.subscribers {
		subscribers = append(subscribers, ch)
	}
	b.mu.RUnlock()

	for _, ch := range subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

// Subscribe registers a subscriber channel for future published events.
func (b *InMemoryBus) Subscribe(ch chan<- *core.Event) {
	if ch == nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribers[ch] = struct{}{}
}

// Unsubscribe removes a subscriber channel.
func (b *InMemoryBus) Unsubscribe(ch chan<- *core.Event) {
	if ch == nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subscribers, ch)
}

var _ core.EventBus = (*InMemoryBus)(nil)
```

### Step 1.5: Run the package tests, then the race detector

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./coding/eventbus -run TestInMemoryBus -count=1
```

Expected: all three tests pass.

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./coding/eventbus -run TestInMemoryBus -race -count=1
```

Expected: pass under the race detector.

### Step 1.6: Commit Task 1

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && git add core/bus.go coding/eventbus/bus.go coding/eventbus/bus_test.go && git commit -m "Plan 102 Task 1: add core event bus and in-memory implementation"
```

Expected: one commit containing the interface, implementation, and tests.

---

## Task 2: SSE HTTP handler + tests

**Files:**
- Create: `coding/eventbus/sse.go`
- Create: `coding/eventbus/sse_test.go`

### Step 2.1: Write the failing SSE handler tests first

- [ ] Create `coding/eventbus/sse_test.go`:

```go
package eventbus

import (
	"context"
	"strings"
	"testing"
	"time"
	"net/http/httptest"

	"github.com/chris/coworker/core"
)

func TestSSEHandler_StreamsEvents(t *testing.T) {
	t.Parallel()

	bus := NewInMemoryBus()
	handler := SSEHandler(bus)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(rec, req)
		close(done)
	}()

	time.Sleep(25 * time.Millisecond)

	bus.Publish(&core.Event{
		ID:            "evt_stream",
		RunID:         "run_stream",
		Sequence:      1,
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		Payload:       `{"job_id":"job_stream"}`,
		CreatedAt:     time.Unix(10, 0).UTC(),
	})

	deadline := time.Now().Add(time.Second)
	for !strings.Contains(rec.Body.String(), "data: ") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not exit after context cancellation")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Fatalf("expected SSE frame, body=%q", body)
	}
	if !strings.Contains(body, `"run_stream"`) {
		t.Fatalf("expected run_id in payload, body=%q", body)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want %q", got, "text/event-stream")
	}
}

func TestSSEHandler_FiltersByRunIDAndKind(t *testing.T) {
	t.Parallel()

	bus := NewInMemoryBus()
	handler := SSEHandler(bus)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/events?run_id=run_filter&kind=job.completed", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(rec, req)
		close(done)
	}()

	time.Sleep(25 * time.Millisecond)

	bus.Publish(&core.Event{
		ID:            "evt_wrong_run",
		RunID:         "run_other",
		Sequence:      1,
		Kind:          core.EventJobCompleted,
		SchemaVersion: 1,
		Payload:       `{"job_id":"job_other"}`,
		CreatedAt:     time.Unix(11, 0).UTC(),
	})
	bus.Publish(&core.Event{
		ID:            "evt_wrong_kind",
		RunID:         "run_filter",
		Sequence:      2,
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		Payload:       `{"job_id":"job_wrong_kind"}`,
		CreatedAt:     time.Unix(12, 0).UTC(),
	})
	bus.Publish(&core.Event{
		ID:            "evt_match",
		RunID:         "run_filter",
		Sequence:      3,
		Kind:          core.EventJobCompleted,
		SchemaVersion: 1,
		Payload:       `{"job_id":"job_match"}`,
		CreatedAt:     time.Unix(13, 0).UTC(),
	})

	deadline := time.Now().Add(time.Second)
	for !strings.Contains(rec.Body.String(), "job_match") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not exit after context cancellation")
	}

	body := rec.Body.String()
	if strings.Contains(body, "job_other") {
		t.Fatalf("body should not contain wrong run event: %q", body)
	}
	if strings.Contains(body, "job_wrong_kind") {
		t.Fatalf("body should not contain wrong kind event: %q", body)
	}
	if !strings.Contains(body, "job_match") {
		t.Fatalf("body should contain matching event: %q", body)
	}
}
```

### Step 2.2: Run the SSE tests and confirm the expected failure

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./coding/eventbus -run TestSSEHandler -count=1
```

Expected: build failure for missing `SSEHandler`.

### Step 2.3: Implement the SSE handler

- [ ] Create `coding/eventbus/sse.go`:

```go
package eventbus

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/chris/coworker/core"
)

// SSEHandler streams committed events from the live event bus.
func SSEHandler(bus core.EventBus) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bus == nil {
			http.Error(w, "event bus unavailable", http.StatusServiceUnavailable)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		runIDFilter := r.URL.Query().Get("run_id")
		kindFilter := r.URL.Query().Get("kind")

		sub := make(chan *core.Event, 32)
		bus.Subscribe(sub)
		defer bus.Unsubscribe(sub)

		flusher.Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case event := <-sub:
				if event == nil {
					continue
				}
				if runIDFilter != "" && event.RunID != runIDFilter {
					continue
				}
				if kindFilter != "" && string(event.Kind) != kindFilter {
					continue
				}

				data, err := json.Marshal(event)
				if err != nil {
					continue
				}
				if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})
}
```

### Step 2.4: Re-run the SSE tests

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./coding/eventbus -run TestSSEHandler -count=1
```

Expected: pass.

### Step 2.5: Run the whole eventbus package

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./coding/eventbus -count=1
```

Expected: all bus + SSE tests pass together.

### Step 2.6: Commit Task 2

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && git add coding/eventbus/sse.go coding/eventbus/sse_test.go && git commit -m "Plan 102 Task 2: add SSE event stream handler"
```

Expected: one commit for the handler and its tests.

---

## Task 3: EventStore bus integration (publish-on-write) + tests

**Files:**
- Modify: `store/event_store.go`
- Modify: `store/event_store_test.go`

### Step 3.1: Add the failing publish-on-write test first

- [ ] Append this test to `store/event_store_test.go`:

```go
func TestWriteEventThenRow_PublishesCommittedEvent(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run_publish")

	bus := eventbus.NewInMemoryBus()
	es := NewEventStore(db)
	es.Bus = bus

	sub := make(chan *core.Event, 1)
	bus.Subscribe(sub)
	t.Cleanup(func() {
		bus.Unsubscribe(sub)
	})

	ctx := context.Background()
	event := &core.Event{
		ID:            "evt_publish",
		RunID:         "run_publish",
		Kind:          core.EventRunCreated,
		SchemaVersion: 1,
		Payload:       `{"run_id":"run_publish"}`,
		CreatedAt:     time.Unix(20, 0).UTC(),
	}

	if err := es.WriteEventThenRow(ctx, event, nil); err != nil {
		t.Fatalf("WriteEventThenRow: %v", err)
	}

	select {
	case got := <-sub:
		if got.ID != event.ID {
			t.Fatalf("published event ID = %q, want %q", got.ID, event.ID)
		}
		if got.Sequence != 1 {
			t.Fatalf("published event sequence = %d, want 1", got.Sequence)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published event")
	}
}
```

- [ ] Update the imports in `store/event_store_test.go` to include `github.com/chris/coworker/coding/eventbus`:

```go
import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/chris/coworker/coding/eventbus"
	"github.com/chris/coworker/core"
)
```

### Step 3.2: Run the focused store tests and confirm the expected failure

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./store -run TestWriteEventThenRow_PublishesCommittedEvent -count=1
```

Expected: build failure because `EventStore` has no `Bus` field yet.

### Step 3.3: Modify EventStore to publish after commit

- [ ] Update `store/event_store.go`:

```go
package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/chris/coworker/core"
)

// EventStore handles event persistence with the event-log-before-state invariant.
type EventStore struct {
	db  *DB
	Bus core.EventBus
}

// NewEventStore creates an EventStore backed by the given DB.
func NewEventStore(db *DB) *EventStore {
	return &EventStore{db: db}
}

// WriteEventThenRow writes the event first, then calls applyFn within
// the same transaction to update projection tables. This enforces the
// event-log-before-state invariant from the spec.
//
// The sequence number is auto-assigned as MAX(sequence)+1 for the run.
// If applyFn is nil, only the event is written.
func (s *EventStore) WriteEventThenRow(ctx context.Context, event *core.Event, applyFn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var seq int
	err = tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(sequence), 0) + 1 FROM events WHERE run_id = ?",
		event.RunID,
	).Scan(&seq)
	if err != nil {
		return fmt.Errorf("compute sequence: %w", err)
	}
	event.Sequence = seq

	_, err = tx.ExecContext(ctx,
		`INSERT INTO events (id, run_id, sequence, kind, schema_version,
			idempotency_key, causation_id, correlation_id, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID,
		event.RunID,
		event.Sequence,
		string(event.Kind),
		event.SchemaVersion,
		nullableString(event.IdempotencyKey),
		nullableString(event.CausationID),
		nullableString(event.CorrelationID),
		event.Payload,
		event.CreatedAt.Format("2006-01-02T15:04:05Z"),
	)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}

	if applyFn != nil {
		if err := applyFn(tx); err != nil {
			return fmt.Errorf("apply projection: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	if s.Bus != nil {
		s.Bus.Publish(event)
	}

	return nil
}
```

No other `EventStore` methods need behavior changes; `WriteEventIdempotent` already routes through `WriteEventThenRow`.

### Step 3.4: Re-run the focused test and then the full store package

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./store -run TestWriteEventThenRow_PublishesCommittedEvent -count=1
```

Expected: pass.

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./store -count=1
```

Expected: all existing Plan 100 store tests still pass.

### Step 3.5: Commit Task 3

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && git add store/event_store.go store/event_store_test.go && git commit -m "Plan 102 Task 3: publish committed events from EventStore"
```

Expected: one commit containing publish-on-write integration.

---

## Task 4: `coworker watch` CLI command + integration test

**Files:**
- Create: `cli/watch.go`
- Create: `cli/watch_test.go`
- Modify: `cli/root.go`

### Step 4.1: Write the failing watch command test first

- [ ] Create `cli/watch_test.go`:

```go
package cli

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chris/coworker/coding/eventbus"
	"github.com/chris/coworker/core"
)

func TestWatchStream_PrintsMatchingEvents(t *testing.T) {
	t.Parallel()

	bus := eventbus.NewInMemoryBus()
	server := httptest.NewServer(eventbus.SSEHandler(bus))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out bytes.Buffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- watchStream(ctx, server.Client(), server.URL+"?run_id=run_live&kind=job.created", &out)
	}()

	time.Sleep(25 * time.Millisecond)

	bus.Publish(&core.Event{
		ID:            "evt_skip_run",
		RunID:         "run_other",
		Sequence:      1,
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		Payload:       `{"job_id":"job_skip_run"}`,
		CreatedAt:     time.Unix(30, 0).UTC(),
	})
	bus.Publish(&core.Event{
		ID:            "evt_skip_kind",
		RunID:         "run_live",
		Sequence:      2,
		Kind:          core.EventRunCreated,
		SchemaVersion: 1,
		Payload:       `{"run_id":"run_live"}`,
		CreatedAt:     time.Unix(31, 0).UTC(),
	})
	bus.Publish(&core.Event{
		ID:            "evt_match_watch",
		RunID:         "run_live",
		Sequence:      3,
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		Payload:       `{"job_id":"job_live","role":"reviewer.arch"}`,
		CreatedAt:     time.Unix(32, 0).UTC(),
	})

	deadline := time.Now().Add(time.Second)
	for !strings.Contains(out.String(), "job.created") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	if err := <-errCh; err != nil {
		t.Fatalf("watchStream: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "job.created") {
		t.Fatalf("expected kind in output, got %q", got)
	}
	if !strings.Contains(got, "run_live") {
		t.Fatalf("expected run ID in output, got %q", got)
	}
	if !strings.Contains(got, "job_live") {
		t.Fatalf("expected payload summary in output, got %q", got)
	}
	if strings.Contains(got, "job_skip_run") {
		t.Fatalf("unexpected output for filtered run: %q", got)
	}
	if strings.Contains(got, "run.created") {
		t.Fatalf("unexpected output for filtered kind: %q", got)
	}
}
```

### Step 4.2: Run the focused CLI test and confirm the expected failure

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./cli -run TestWatchStream_PrintsMatchingEvents -count=1
```

Expected: build failure for missing `watchStream`.

### Step 4.3: Implement the command and SSE client

- [ ] Create `cli/watch.go`:

```go
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chris/coworker/core"
	"github.com/spf13/cobra"
)

type watchOptions struct {
	runID string
	kind  string
	port  int
}

func newWatchCmd() *cobra.Command {
	opts := &watchOptions{}

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Stream live runtime events over SSE.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return watchStream(
				cmd.Context(),
				&http.Client{},
				buildEventsURL(opts.port, opts.runID, opts.kind),
				cmd.OutOrStdout(),
			)
		},
	}

	cmd.Flags().StringVar(&opts.runID, "run", "", "Filter to one run ID")
	cmd.Flags().StringVar(&opts.kind, "kind", "", "Filter to one event kind")
	cmd.Flags().IntVar(&opts.port, "port", 7700, "Port for the local coworker SSE server")

	return cmd
}

func buildEventsURL(port int, runID, kind string) string {
	query := url.Values{}
	if runID != "" {
		query.Set("run_id", runID)
	}
	if kind != "" {
		query.Set("kind", kind)
	}

	u := url.URL{
		Scheme:   "http",
		Host:     fmt.Sprintf("localhost:%d", port),
		Path:     "/events",
		RawQuery: query.Encode(),
	}

	return u.String()
}

func watchStream(ctx context.Context, client *http.Client, streamURL string, out io.Writer) error {
	if client == nil {
		client = &http.Client{}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return fmt.Errorf("build watch request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("connect to %s: %w", streamURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("watch %s returned %s: %s", streamURL, resp.Status, strings.TrimSpace(string(body)))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		var event core.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			return fmt.Errorf("decode SSE event: %w", err)
		}

		if _, err := fmt.Fprintln(out, formatWatchEvent(&event)); err != nil {
			return fmt.Errorf("write watch output: %w", err)
		}
	}

	if ctx.Err() != nil {
		return nil
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("read SSE stream: %w", err)
	}

	return nil
}

func formatWatchEvent(event *core.Event) string {
	ts := event.CreatedAt.UTC().Format(time.RFC3339)
	if event.CreatedAt.IsZero() {
		ts = "-"
	}

	return fmt.Sprintf(
		"%s %-18s run=%s payload=%s",
		ts,
		string(event.Kind),
		event.RunID,
		summarizePayload(event.Payload),
	)
}

func summarizePayload(payload string) string {
	if strings.TrimSpace(payload) == "" {
		return "{}"
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(payload)); err == nil {
		payload = compact.String()
	}

	if len(payload) > 120 {
		return payload[:117] + "..."
	}

	return payload
}
```

### Step 4.4: Register `watch` from the root command

- [ ] Update `cli/root.go`:

```go
// Package cli contains cobra command definitions for the coworker binary.
// Subpackages are avoided at this stage to keep the command surface
// discoverable; split when the command set grows unwieldy.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// rootCmd is the coworker binary's root command. Subcommands register
// themselves via init() in their own files.
var rootCmd = &cobra.Command{
	Use:           "coworker",
	Short:         "Local-first runtime that coordinates CLI coding agents as role-typed workers.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.AddCommand(newWatchCmd())
}

// Execute runs the root command. Called from cmd/coworker/main.go.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "coworker:", err)
		os.Exit(1)
	}
}
```

### Step 4.5: Re-run the CLI tests

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./cli -run TestWatchStream_PrintsMatchingEvents -count=1
```

Expected: pass.

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./cli -count=1
```

Expected: all CLI tests pass, including existing `invoke` coverage.

### Step 4.6: Commit Task 4

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && git add cli/root.go cli/watch.go cli/watch_test.go && git commit -m "Plan 102 Task 4: add coworker watch SSE client command"
```

Expected: one commit for the CLI surface.

---

## Task 5: Event-log snapshot testing helper + retrofit Plan 100 tests

**Files:**
- Create: `store/snapshot_test_helper.go`
- Modify: `tests/integration/invoke_test.go`
- Create: `testdata/events/invoke_reviewer_arch.golden.json`

### Step 5.1: Add the reusable snapshot helper

- [ ] Create `store/snapshot_test_helper.go`:

This helper is intentionally aimed at the current integration-test shape: one runtime run per test database. It snapshots the full `events` table ordered by `sequence`, which matches the Plan 100 end-to-end test and the spec's "sequence defines strict ordering within a run" rule.

```go
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chris/coworker/core"
)

type eventSnapshot struct {
	Sequence       int            `json:"sequence"`
	Kind           core.EventKind `json:"kind"`
	SchemaVersion  int            `json:"schema_version"`
	RunID          string         `json:"run_id,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	CausationID    string         `json:"causation_id,omitempty"`
	CorrelationID  string         `json:"correlation_id,omitempty"`
	Payload        any            `json:"payload,omitempty"`
}

type tokenMapper struct {
	prefix string
	next   int
	seen   map[string]string
}

func newTokenMapper(prefix string) *tokenMapper {
	return &tokenMapper{
		prefix: prefix,
		seen:   make(map[string]string),
	}
}

func (m *tokenMapper) Normalize(raw string) string {
	if raw == "" {
		return ""
	}
	if normalized, ok := m.seen[raw]; ok {
		return normalized
	}
	m.next++
	normalized := fmt.Sprintf("%s_%d", m.prefix, m.next)
	m.seen[raw] = normalized
	return normalized
}

type snapshotNormalizers struct {
	runs      *tokenMapper
	jobs      *tokenMapper
	findings  *tokenMapper
	artifacts *tokenMapper
	generic   *tokenMapper
}

func newSnapshotNormalizers() *snapshotNormalizers {
	return &snapshotNormalizers{
		runs:      newTokenMapper("run"),
		jobs:      newTokenMapper("job"),
		findings:  newTokenMapper("finding"),
		artifacts: newTokenMapper("artifact"),
		generic:   newTokenMapper("id"),
	}
}

func (n *snapshotNormalizers) lookup(raw string) (string, bool) {
	for _, mapper := range []*tokenMapper{n.runs, n.jobs, n.findings, n.artifacts, n.generic} {
		if normalized, ok := mapper.seen[raw]; ok {
			return normalized, true
		}
	}
	return "", false
}

func (n *snapshotNormalizers) normalizeID(field, raw string) string {
	if raw == "" {
		return ""
	}
	if normalized, ok := n.lookup(raw); ok {
		return normalized
	}

	switch field {
	case "run_id":
		return n.runs.Normalize(raw)
	case "job_id", "resolved_by_job_id":
		return n.jobs.Normalize(raw)
	case "finding_id":
		return n.findings.Normalize(raw)
	case "artifact_id":
		return n.artifacts.Normalize(raw)
	default:
		return n.generic.Normalize(raw)
	}
}

func (n *snapshotNormalizers) normalizeValue(field string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, child := range typed {
			normalized[key] = n.normalizeValue(key, child)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for i, child := range typed {
			normalized[i] = n.normalizeValue(field, child)
		}
		return normalized
	case string:
		if field == "id" || strings.HasSuffix(field, "_id") {
			return n.normalizeID(field, typed)
		}
		return typed
	default:
		return typed
	}
}

func loadEventSnapshot(db *DB) ([]eventSnapshot, error) {
	rows, err := db.QueryContext(context.Background(),
		`SELECT run_id, sequence, kind, schema_version,
			COALESCE(idempotency_key, ''), COALESCE(causation_id, ''), COALESCE(correlation_id, ''),
			payload
		FROM events
		ORDER BY sequence ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query events for snapshot: %w", err)
	}
	defer rows.Close()

	normalizers := newSnapshotNormalizers()
	var snapshots []eventSnapshot

	for rows.Next() {
		var (
			runID          string
			sequence       int
			kind           string
			schemaVersion  int
			idempotencyKey string
			causationID    string
			correlationID  string
			payload        string
		)

		if err := rows.Scan(
			&runID,
			&sequence,
			&kind,
			&schemaVersion,
			&idempotencyKey,
			&causationID,
			&correlationID,
			&payload,
		); err != nil {
			return nil, fmt.Errorf("scan event snapshot row: %w", err)
		}

		var payloadValue any
		if payload != "" {
			if err := json.Unmarshal([]byte(payload), &payloadValue); err != nil {
				payloadValue = payload
			}
		}
		payloadValue = normalizers.normalizeValue("payload", payloadValue)

		snapshots = append(snapshots, eventSnapshot{
			Sequence:       sequence,
			Kind:           core.EventKind(kind),
			SchemaVersion:  schemaVersion,
			RunID:          normalizers.normalizeID("run_id", runID),
			IdempotencyKey: idempotencyKey,
			CausationID:    normalizers.normalizeID("causation_id", causationID),
			CorrelationID:  normalizers.normalizeID("correlation_id", correlationID),
			Payload:        payloadValue,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event snapshot rows: %w", err)
	}

	return snapshots, nil
}

// AssertGoldenEvents compares the normalized events table against a golden file.
// Set GOLDEN_UPDATE=1 to rewrite the golden snapshot in place.
func AssertGoldenEvents(t *testing.T, db *DB, goldenFile string) {
	t.Helper()

	snapshots, err := loadEventSnapshot(db)
	if err != nil {
		t.Fatalf("load event snapshot: %v", err)
	}

	got, err := json.MarshalIndent(snapshots, "", "  ")
	if err != nil {
		t.Fatalf("marshal event snapshot: %v", err)
	}
	got = append(got, '\n')

	if os.Getenv("GOLDEN_UPDATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenFile), 0755); err != nil {
			t.Fatalf("create golden directory: %v", err)
		}
		if err := os.WriteFile(goldenFile, got, 0644); err != nil {
			t.Fatalf("write golden file: %v", err)
		}
		return
	}

	want, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatalf("read golden file %q: %v", goldenFile, err)
	}

	if !bytes.Equal(want, got) {
		t.Fatalf("event snapshot mismatch for %s\nwant:\n%s\ngot:\n%s", goldenFile, want, got)
	}
}
```

### Step 5.2: Retrofit the existing Plan 100 integration test

- [ ] Replace the manual event sequence block near the end of `tests/integration/invoke_test.go` with the golden helper:

```go
	// Verify the durable event log against the normalized golden snapshot.
	goldenFile := filepath.Join(repoRoot, "testdata", "events", "invoke_reviewer_arch.golden.json")
	store.AssertGoldenEvents(t, db, goldenFile)
```

The surrounding imports in `tests/integration/invoke_test.go` already include `path/filepath` and `github.com/chris/coworker/store`, so no extra import change is required.

### Step 5.3: Create the initial golden snapshot

- [ ] Create `testdata/events/invoke_reviewer_arch.golden.json`:

```json
[
  {
    "sequence": 1,
    "kind": "run.created",
    "schema_version": 1,
    "run_id": "run_1",
    "payload": {
      "mode": "interactive",
      "run_id": "run_1"
    }
  },
  {
    "sequence": 2,
    "kind": "job.created",
    "schema_version": 1,
    "run_id": "run_1",
    "correlation_id": "job_1",
    "payload": {
      "cli": "codex",
      "job_id": "job_1",
      "role": "reviewer.arch",
      "run_id": "run_1"
    }
  },
  {
    "sequence": 3,
    "kind": "job.leased",
    "schema_version": 1,
    "run_id": "run_1",
    "correlation_id": "job_1",
    "payload": {
      "job_id": "job_1",
      "state": "dispatched"
    }
  },
  {
    "sequence": 4,
    "kind": "finding.created",
    "schema_version": 1,
    "run_id": "run_1",
    "correlation_id": "job_1",
    "payload": {
      "finding_id": "finding_1",
      "fingerprint": "55eb886424455398387affa472545e19",
      "job_id": "job_1",
      "line": 42,
      "path": "main.go",
      "severity": "important"
    }
  },
  {
    "sequence": 5,
    "kind": "finding.created",
    "schema_version": 1,
    "run_id": "run_1",
    "correlation_id": "job_1",
    "payload": {
      "finding_id": "finding_2",
      "fingerprint": "c1e7112ce44f290140cb5f7f78719b9d",
      "job_id": "job_1",
      "line": 17,
      "path": "store.go",
      "severity": "minor"
    }
  },
  {
    "sequence": 6,
    "kind": "job.completed",
    "schema_version": 1,
    "run_id": "run_1",
    "correlation_id": "job_1",
    "payload": {
      "job_id": "job_1",
      "state": "complete"
    }
  },
  {
    "sequence": 7,
    "kind": "run.completed",
    "schema_version": 1,
    "run_id": "run_1",
    "payload": {
      "run_id": "run_1",
      "state": "completed"
    }
  }
]
```

### Step 5.4: Run the retrofitted integration test

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./tests/integration -run TestInvokeReviewerArch_EndToEnd -count=1
```

Expected: pass, with the golden snapshot assertion replacing manual event-kind checks.

### Step 5.5: Verify the update workflow for intentional event changes

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && GOLDEN_UPDATE=1 go test ./tests/integration -run TestInvokeReviewerArch_EndToEnd -count=1
```

Expected: pass and rewrite `testdata/events/invoke_reviewer_arch.golden.json` if the event snapshot intentionally changes in a future plan.

### Step 5.6: Commit Task 5

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && git add store/snapshot_test_helper.go tests/integration/invoke_test.go testdata/events/invoke_reviewer_arch.golden.json && git commit -m "Plan 102 Task 5: add event log golden snapshot helper"
```

Expected: one commit that locks in event-log snapshot testing from Plan 102 onward.

---

## Verification

- [ ] Run the new eventbus unit tests:

```bash
cd /home/chris/workshop/coworker && go test ./coding/eventbus -count=1 -race
```

- [ ] Run the store package:

```bash
cd /home/chris/workshop/coworker && go test ./store -count=1
```

- [ ] Run the CLI package:

```bash
cd /home/chris/workshop/coworker && go test ./cli -count=1
```

- [ ] Run the retrofitted integration test:

```bash
cd /home/chris/workshop/coworker && go test ./tests/integration -run TestInvokeReviewerArch_EndToEnd -count=1
```

- [ ] Run the full test suite before shipping:

```bash
cd /home/chris/workshop/coworker && go test ./... -count=1 -timeout 60s
```

- [ ] Smoke-test the watch client against a mounted SSE server once the runtime exposes `/events`:

```bash
cd /home/chris/workshop/coworker && go run ./cmd/coworker watch --port 7700 --run run_123 --kind job.completed
```

Expected: one line per matching event, formatted as `timestamp kind run=<id> payload=<summary>`.
