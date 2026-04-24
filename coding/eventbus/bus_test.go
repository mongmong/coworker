package eventbus

import (
	"context"
	"fmt"
	"log/slog"
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

func TestInMemoryBus_PublishLogsDroppedEvents(t *testing.T) {
	bus := NewInMemoryBus()
	slow := make(chan *core.Event)
	bus.Subscribe(slow)

	handler := &captureHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(previous)

	bus.Publish(&core.Event{
		ID:            "evt_dropped",
		RunID:         "run_dropped",
		Sequence:      1,
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		Payload:       `{"job_id":"job_dropped"}`,
		CreatedAt:     time.Unix(40, 0).UTC(),
	})

	records := handler.Records()
	if len(records) != 1 {
		t.Fatalf("captured %d log records, want 1", len(records))
	}
	if records[0].message != "event dropped for slow subscriber" {
		t.Fatalf("log message = %q, want %q", records[0].message, "event dropped for slow subscriber")
	}
	if got := records[0].attrs["event_kind"]; got != string(core.EventJobCreated) {
		t.Fatalf("event_kind = %v, want %q", got, core.EventJobCreated)
	}
	if got := records[0].attrs["run_id"]; got != "run_dropped" {
		t.Fatalf("run_id = %v, want %q", got, "run_dropped")
	}
}

type capturedRecord struct {
	message string
	attrs   map[string]any
}

type captureHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *captureHandler) Handle(_ context.Context, record slog.Record) error {
	captured := capturedRecord{
		message: record.Message,
		attrs:   make(map[string]any),
	}
	record.Attrs(func(attr slog.Attr) bool {
		captured.attrs[attr.Key] = attr.Value.Any()
		return true
	})

	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, captured)
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *captureHandler) WithGroup(string) slog.Handler {
	return h
}

func (h *captureHandler) Records() []capturedRecord {
	h.mu.Lock()
	defer h.mu.Unlock()

	records := make([]capturedRecord, len(h.records))
	copy(records, h.records)
	return records
}
