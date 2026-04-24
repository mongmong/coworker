package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chris/coworker/coding/eventbus"
	"github.com/chris/coworker/core"
)

func TestWatchStream_PrintsMatchingEvents(t *testing.T) {
	t.Parallel()

	bus := eventbus.NewInMemoryBus()
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return roundTripHandler(req, eventbus.SSEHandler(bus))
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out bytes.Buffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- watchStream(ctx, client, "http://coworker.test/events?run_id=run_live&kind=job.created", &out)
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

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type pipeResponseWriter struct {
	header http.Header
	body   *io.PipeWriter
	ready  chan struct{}
	once   sync.Once
	status int
}

func (w *pipeResponseWriter) Header() http.Header {
	return w.header
}

func (w *pipeResponseWriter) WriteHeader(statusCode int) {
	w.status = statusCode
	w.markReady()
}

func (w *pipeResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.markReady()
	return w.body.Write(p)
}

func (w *pipeResponseWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.markReady()
}

func (w *pipeResponseWriter) markReady() {
	w.once.Do(func() {
		close(w.ready)
	})
}

func roundTripHandler(req *http.Request, handler http.Handler) (*http.Response, error) {
	pr, pw := io.Pipe()
	writer := &pipeResponseWriter{
		header: make(http.Header),
		body:   pw,
		ready:  make(chan struct{}),
	}

	go func() {
		defer pw.Close()
		handler.ServeHTTP(writer, req)
		writer.markReady()
	}()

	select {
	case <-writer.ready:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}

	statusCode := writer.status
	if statusCode == 0 {
		statusCode = http.StatusOK
	}

	return &http.Response{
		StatusCode: statusCode,
		Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		Header:     writer.header.Clone(),
		Body:       pr,
		Request:    req,
	}, nil
}
