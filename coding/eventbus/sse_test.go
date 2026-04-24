package eventbus

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func TestSSEHandler_StreamsEvents(t *testing.T) {
	t.Parallel()

	bus := NewInMemoryBus()
	handler := SSEHandler(bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	rec := newStreamingRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer rec.Close()
		handler.ServeHTTP(rec, req)
	}()

	rec.WaitReady(t)

	bus.Publish(&core.Event{
		ID:            "evt_stream",
		RunID:         "run_stream",
		Sequence:      1,
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		Payload:       `{"job_id":"job_stream"}`,
		CreatedAt:     time.Unix(10, 0).UTC(),
	})

	body := readSSEFrame(t, rec.Body())
	if !strings.Contains(body, "data: ") {
		t.Fatalf("expected SSE frame, body=%q", body)
	}
	if !strings.Contains(body, `"run_stream"`) {
		t.Fatalf("expected run_id in payload, body=%q", body)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not exit after context cancellation")
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
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/events?run_id=run_filter&kind=job.completed", nil).WithContext(ctx)
	rec := newStreamingRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer rec.Close()
		handler.ServeHTTP(rec, req)
	}()

	rec.WaitReady(t)

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

	body := readSSEFrame(t, rec.Body())
	if strings.Contains(body, "job_other") {
		t.Fatalf("body should not contain wrong run event: %q", body)
	}
	if strings.Contains(body, "job_wrong_kind") {
		t.Fatalf("body should not contain wrong kind event: %q", body)
	}
	if !strings.Contains(body, "job_match") {
		t.Fatalf("body should contain matching event: %q", body)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not exit after context cancellation")
	}
}

type streamingRecorder struct {
	header http.Header
	body   *io.PipeReader
	writer *io.PipeWriter
	ready  chan struct{}
	once   sync.Once
}

func newStreamingRecorder() *streamingRecorder {
	body, writer := io.Pipe()
	return &streamingRecorder{
		header: make(http.Header),
		body:   body,
		writer: writer,
		ready:  make(chan struct{}),
	}
}

func (r *streamingRecorder) Header() http.Header {
	return r.header
}

func (r *streamingRecorder) WriteHeader(int) {
	r.markReady()
}

func (r *streamingRecorder) Write(p []byte) (int, error) {
	r.markReady()
	return r.writer.Write(p)
}

func (r *streamingRecorder) Flush() {
	r.markReady()
}

func (r *streamingRecorder) Body() io.Reader {
	return r.body
}

func (r *streamingRecorder) Close() {
	r.markReady()
	_ = r.writer.Close()
	_ = r.body.Close()
}

func (r *streamingRecorder) WaitReady(t *testing.T) {
	t.Helper()

	select {
	case <-r.ready:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for response headers")
	}
}

func (r *streamingRecorder) markReady() {
	r.once.Do(func() {
		close(r.ready)
	})
}

func readSSEFrame(t *testing.T, body io.Reader) string {
	t.Helper()

	type result struct {
		frame string
		err   error
	}

	results := make(chan result, 1)
	go func() {
		reader := bufio.NewReader(body)
		var frame strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				results <- result{err: err}
				return
			}

			frame.WriteString(line)
			if line == "\n" {
				results <- result{frame: frame.String()}
				return
			}
		}
	}()

	select {
	case res := <-results:
		if res.err != nil {
			t.Fatalf("read SSE frame: %v", res.err)
		}
		return res.frame
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SSE frame")
		return ""
	}
}
