package eventbus

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
