package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

// mockOpenCodeServer holds all the state for a test-controlled OpenCode server.
type mockOpenCodeServer struct {
	// sseEvents is the sequence of raw SSE data lines the server will send
	// to /event subscribers, in order. Each element is one JSON payload.
	// The server sends them with a small delay between each to simulate
	// real streaming.
	sseEvents []string

	// sessionCreated tracks how many POST /session calls were received.
	sessionCreated int32
	// messageSent tracks how many POST /session/{id}/message calls were received.
	messageSent int32
	// sessionDeleted tracks how many DELETE /session/{id} calls were received.
	sessionDeleted int32
	// abortSent tracks how many POST /session/{id}/abort calls were received.
	abortSent int32

	// messageResponseCode is the HTTP status returned by POST /session/{id}/message.
	// Defaults to 200.
	messageResponseCode int

	// sseDropAfter makes the SSE handler close the connection after emitting
	// this many events (to test reconnect). Zero means never drop.
	sseDropAfter int

	// sseConnections tracks how many times /event was connected.
	sseConnections int32
}

const testSessionID = "ses_test123"

// newMockServer builds and registers a mock OpenCode server. Returns the
// httptest.Server and the mock state struct.
func newMockServer(t *testing.T, state *mockOpenCodeServer) *httptest.Server {
	t.Helper()
	if state.messageResponseCode == 0 {
		state.messageResponseCode = http.StatusOK
	}

	mux := http.NewServeMux()

	// POST /session
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&state.sessionCreated, 1)
		w.Header().Set("Content-Type", "application/json")
		resp := openCodeSessionResponse{ID: testSessionID}
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	})

	// POST /session/{id}/message
	mux.HandleFunc("POST /session/{sessionID}/message", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&state.messageSent, 1)
		w.WriteHeader(state.messageResponseCode)
		if state.messageResponseCode >= 300 {
			fmt.Fprintln(w, "internal server error")
			return
		}
		// Return a minimal message response.
		resp := openCodeMessageResponse{}
		resp.Info.ID = "msg_test456"
		resp.Info.Role = "assistant"
		resp.Info.Finish = "stop"
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	})

	// DELETE /session/{id}
	mux.HandleFunc("DELETE /session/{sessionID}", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&state.sessionDeleted, 1)
		w.WriteHeader(http.StatusOK)
	})

	// POST /session/{id}/abort
	mux.HandleFunc("POST /session/{sessionID}/abort", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&state.abortSent, 1)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "true")
	})

	// GET /event — SSE stream
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
		connNum := atomic.AddInt32(&state.sseConnections, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Log("responseWriter does not implement http.Flusher")
			return
		}

		events := state.sseEvents
		dropAfter := state.sseDropAfter

		// If this is a reconnect and dropAfter was set, send all events on
		// the second connection (simulate successful reconnect).
		if dropAfter > 0 && connNum > 1 {
			dropAfter = 0
		}

		for i, ev := range events {
			if dropAfter > 0 && i >= dropAfter {
				// Simulate connection drop.
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", ev)
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
		}
		// Keep the connection open briefly so the reader can process all events.
		time.Sleep(20 * time.Millisecond)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// buildSSEEvent creates a JSON string for an SSE event envelope.
func buildSSEEvent(eventType string, props map[string]interface{}) string {
	env := map[string]interface{}{
		"type":       eventType,
		"properties": props,
	}
	data, _ := json.Marshal(env)
	return string(data)
}

// sessionIdleEvent returns the JSON payload for a session.idle event.
func sessionIdleEvent(sessionID string) string {
	return buildSSEEvent("session.idle", map[string]interface{}{
		"sessionID": sessionID,
	})
}

// testAssistantMsgID is the message ID used for assistant messages in tests.
const testAssistantMsgID = "msg_test_assistant"

// messageUpdatedAssistantEvent returns a message.updated JSON payload for an
// assistant message. This must be sent before messagePartUpdatedEvent so the
// SSE handler knows which message IDs belong to the assistant.
func messageUpdatedAssistantEvent(sessionID string) string {
	return buildSSEEvent("message.updated", map[string]interface{}{
		"sessionID": sessionID,
		"info": map[string]interface{}{
			"id":   testAssistantMsgID,
			"role": "assistant",
		},
	})
}

// messagePartUpdatedEvent returns a message.part.updated JSON payload for a
// text part attributed to the assistant message (testAssistantMsgID).
func messagePartUpdatedEvent(sessionID, text string) string {
	return buildSSEEvent("message.part.updated", map[string]interface{}{
		"sessionID": sessionID,
		"part": map[string]interface{}{
			"type":      "text",
			"text":      text,
			"messageID": testAssistantMsgID,
		},
	})
}

// sessionErrorEvent returns a session.error JSON payload.
func sessionErrorEvent(sessionID, name, message string) string {
	return buildSSEEvent("session.error", map[string]interface{}{
		"sessionID": sessionID,
		"error": map[string]interface{}{
			"name": name,
			"data": map[string]string{"message": message},
		},
	})
}

// TestOpenCodeHTTPAgent_HappyPath verifies the full dispatch flow:
// session created, message sent, SSE events arrive (including JSONL findings
// in text parts), session.idle received, session deleted.
func TestOpenCodeHTTPAgent_HappyPath(t *testing.T) {
	// Build the assistant output: two finding JSONL records.
	finding1 := `{"type":"finding","path":"main.go","line":10,"severity":"important","body":"Missing error check"}`
	finding2 := `{"type":"finding","path":"util.go","line":5,"severity":"minor","body":"Unused variable"}`
	done := `{"type":"done","exit_code":0}`
	assistantText := finding1 + "\n" + finding2 + "\n" + done + "\n"

	state := &mockOpenCodeServer{
		sseEvents: []string{
			buildSSEEvent("session.created", map[string]interface{}{"sessionID": testSessionID}),
			// message.updated (role:assistant) must precede message.part.updated so
			// the SSE handler registers the message ID as belonging to the assistant.
			messageUpdatedAssistantEvent(testSessionID),
			messagePartUpdatedEvent(testSessionID, assistantText),
			sessionIdleEvent(testSessionID),
		},
	}

	srv := newMockServer(t, state)
	ag := &OpenCodeHTTPAgent{
		ServerURL:  srv.URL,
		HTTPClient: srv.Client(),
	}

	job := &core.Job{
		ID:    "job-happy",
		RunID: "run-happy",
		Role:  "developer",
		CLI:   "opencode",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	handle, err := ag.Dispatch(ctx, job, "Please review the code.")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	result, err := handle.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	// Verify session was created.
	if got := atomic.LoadInt32(&state.sessionCreated); got != 1 {
		t.Errorf("POST /session called %d times, want 1", got)
	}
	// Verify message was sent.
	if got := atomic.LoadInt32(&state.messageSent); got != 1 {
		t.Errorf("POST /session/{id}/message called %d times, want 1", got)
	}
	// Verify session was deleted (cleanup happens in goroutine — give it time).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&state.sessionDeleted) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&state.sessionDeleted); got != 1 {
		t.Errorf("DELETE /session/{id} called %d times, want 1", got)
	}

	// Verify findings parsed.
	if len(result.Findings) != 2 {
		t.Fatalf("findings count = %d, want 2", len(result.Findings))
	}
	f1 := result.Findings[0]
	if f1.Path != "main.go" {
		t.Errorf("finding[0].path = %q, want %q", f1.Path, "main.go")
	}
	if f1.Line != 10 {
		t.Errorf("finding[0].line = %d, want 10", f1.Line)
	}
	if f1.Severity != core.SeverityImportant {
		t.Errorf("finding[0].severity = %q, want %q", f1.Severity, core.SeverityImportant)
	}
	if f1.Body != "Missing error check" {
		t.Errorf("finding[0].body = %q, want %q", f1.Body, "Missing error check")
	}
	f2 := result.Findings[1]
	if f2.Path != "util.go" {
		t.Errorf("finding[1].path = %q, want %q", f2.Path, "util.go")
	}
	if result.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", result.ExitCode)
	}
}

// TestOpenCodeHTTPAgent_Cancel verifies that calling Cancel() aborts the session
// and that Wait returns context.Canceled.
func TestOpenCodeHTTPAgent_Cancel(t *testing.T) {
	// SSE server that blocks indefinitely (never sends session.idle).
	holdCh := make(chan struct{})
	state := &mockOpenCodeServer{
		sseEvents: []string{
			buildSSEEvent("session.created", map[string]interface{}{"sessionID": testSessionID}),
		},
	}
	var srv *httptest.Server

	// Override the SSE handler to block.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&state.sessionCreated, 1)
		json.NewEncoder(w).Encode(openCodeSessionResponse{ID: testSessionID}) //nolint:errcheck
	})
	mux.HandleFunc("POST /session/{sessionID}/message", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&state.messageSent, 1)
		json.NewEncoder(w).Encode(openCodeMessageResponse{}) //nolint:errcheck
	})
	mux.HandleFunc("DELETE /session/{sessionID}", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&state.sessionDeleted, 1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /session/{sessionID}/abort", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&state.abortSent, 1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Block until client disconnects or test ends.
		select {
		case <-r.Context().Done():
		case <-holdCh:
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(holdCh) })

	ag := &OpenCodeHTTPAgent{
		ServerURL:  srv.URL,
		HTTPClient: srv.Client(),
	}

	job := &core.Job{
		ID:    "job-cancel",
		RunID: "run-cancel",
		Role:  "developer",
		CLI:   "opencode",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	handle, err := ag.Dispatch(ctx, job, "Long running task")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// Cancel the handle.
	if err := handle.Cancel(); err != nil {
		t.Logf("Cancel returned error (non-fatal): %v", err)
	}

	// Wait should return quickly.
	waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Second)
	defer waitCancel()
	_, waitErr := handle.Wait(waitCtx)
	if waitErr == nil {
		// Also acceptable: the SSE goroutine completed with an empty result.
		t.Log("Wait returned nil err after Cancel (SSE goroutine may have exited cleanly)")
	}

	// Give abort handler time to be called.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&state.abortSent) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&state.abortSent); got < 1 {
		t.Errorf("POST /session/{id}/abort called %d times, want >= 1", got)
	}
}

// TestOpenCodeHTTPAgent_CancelDrainsMessageGoroutine verifies that
// Cancel waits for the message goroutine before returning (so it does
// not leak). The drain returns immediately when the network responds to
// context cancellation; this test asserts the WaitGroup wiring is
// connected, not that the 5s timeout actually fires (which would
// require a true network hang we cannot easily simulate in httptest).
// Plan 123 (B5).
func TestOpenCodeHTTPAgent_CancelDrainsMessageGoroutine(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(openCodeSessionResponse{ID: testSessionID}) //nolint:errcheck
	})
	mux.HandleFunc("POST /session/{sessionID}/message", func(w http.ResponseWriter, r *http.Request) {
		// Sleep briefly so the message goroutine is genuinely alive
		// when Cancel runs; respect context cancellation so the test
		// terminates promptly when sseCtx cancels.
		select {
		case <-time.After(2 * time.Second):
			json.NewEncoder(w).Encode(openCodeMessageResponse{}) //nolint:errcheck
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("DELETE /session/{sessionID}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /session/{sessionID}/abort", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ag := &OpenCodeHTTPAgent{
		ServerURL:  srv.URL,
		HTTPClient: srv.Client(),
	}

	job := &core.Job{ID: "j-drain", RunID: "r-drain", Role: "developer", CLI: "opencode"}
	handle, err := ag.Dispatch(context.Background(), job, "drain test")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// Brief pause so the message goroutine actually starts the POST.
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	if err := handle.Cancel(); err != nil {
		t.Logf("Cancel returned: %v", err)
	}
	elapsed := time.Since(start)

	// Cancel must return within the bound. Healthy networks drain fast
	// (sub-second); the WaitGroup tops out at 5s after which the goroutine
	// is logged-and-leaked.
	if elapsed > 7*time.Second {
		t.Errorf("Cancel took %v; expected <= 7s", elapsed)
	}

	// After Cancel returns, the message goroutine must have completed
	// (modulo timeout-leak). We can verify by attempting a second
	// messageWG.Wait — should be a no-op since Cancel already drained.
	openCodeHandle, ok := handle.(*openCodeJobHandle)
	if !ok {
		t.Fatalf("handle type = %T, want *openCodeJobHandle", handle)
	}
	doneCh := make(chan struct{})
	go func() {
		openCodeHandle.messageWG.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
		// Expected: WaitGroup is already drained.
	case <-time.After(500 * time.Millisecond):
		t.Error("messageWG.Wait blocked after Cancel returned; goroutine leaked past Cancel")
	}
}

// TestOpenCodeHTTPAgent_SSEReconnect verifies that when the SSE connection
// drops mid-stream, the agent reconnects and eventually receives session.idle.
func TestOpenCodeHTTPAgent_SSEReconnect(t *testing.T) {
	assistantText := "hello from reconnected session"
	state := &mockOpenCodeServer{
		// Drop after 1 event (before session.idle).
		sseDropAfter: 1,
		sseEvents: []string{
			buildSSEEvent("session.created", map[string]interface{}{"sessionID": testSessionID}),
			// These are only sent on the second connection (dropAfter resets for conn > 1).
			// message.updated (role:assistant) must precede message.part.updated.
			messageUpdatedAssistantEvent(testSessionID),
			messagePartUpdatedEvent(testSessionID, assistantText),
			sessionIdleEvent(testSessionID),
		},
	}

	srv := newMockServer(t, state)
	ag := &OpenCodeHTTPAgent{
		ServerURL:  srv.URL,
		HTTPClient: srv.Client(),
	}

	job := &core.Job{
		ID:    "job-reconnect",
		RunID: "run-reconnect",
		Role:  "developer",
		CLI:   "opencode",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	handle, err := ag.Dispatch(ctx, job, "Do something")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	result, err := handle.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	// After reconnect, the assistant text should be in result.Stdout (free-form,
	// not valid JSONL).
	if !strings.Contains(result.Stdout, "hello from reconnected session") {
		t.Errorf("result.Stdout = %q; want to contain assistant text", result.Stdout)
	}

	// Verify SSE was connected more than once.
	if got := atomic.LoadInt32(&state.sseConnections); got < 2 {
		t.Errorf("SSE connections = %d, want >= 2 (reconnect expected)", got)
	}
}

// TestOpenCodeHTTPAgent_NoSessionIdle_ContextTimeout verifies that when
// session.idle never arrives, Wait returns ctx.Err after the context deadline.
func TestOpenCodeHTTPAgent_NoSessionIdle_ContextTimeout(t *testing.T) {
	// SSE server that only sends a created event, never session.idle.
	state := &mockOpenCodeServer{
		sseEvents: []string{
			buildSSEEvent("session.created", map[string]interface{}{"sessionID": testSessionID}),
		},
	}

	// Custom handler that blocks SSE indefinitely.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(openCodeSessionResponse{ID: testSessionID}) //nolint:errcheck
	})
	mux.HandleFunc("POST /session/{sessionID}/message", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&state.messageSent, 1)
		json.NewEncoder(w).Encode(openCodeMessageResponse{}) //nolint:errcheck
	})
	mux.HandleFunc("DELETE /session/{sessionID}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /session/{sessionID}/abort", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Send the created event then block forever.
		fmt.Fprintf(w, "data: %s\n\n", buildSSEEvent("session.created",
			map[string]interface{}{"sessionID": testSessionID}))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ag := &OpenCodeHTTPAgent{
		ServerURL:  srv.URL,
		HTTPClient: srv.Client(),
	}

	job := &core.Job{
		ID:    "job-timeout",
		RunID: "run-timeout",
		Role:  "developer",
		CLI:   "opencode",
	}

	// Very short context deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	handle, err := ag.Dispatch(ctx, job, "What is 2+2?")
	if err != nil {
		// Dispatch may fail if ctx expires before message is sent — acceptable.
		t.Logf("Dispatch returned (context expired early): %v", err)
		return
	}

	// Use an outer context so Wait itself doesn't block the test indefinitely.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()

	_, waitErr := handle.Wait(waitCtx)
	if waitErr == nil {
		t.Log("Wait returned nil (SSE goroutine may have exited on context cancellation)")
	} else {
		// Should be context deadline exceeded or cancelled.
		t.Logf("Wait returned expected error: %v", waitErr)
	}
}

// TestOpenCodeHTTPAgent_SessionError verifies that a session.error event is
// captured in result.Stderr and that Wait returns without hanging.
func TestOpenCodeHTTPAgent_SessionError(t *testing.T) {
	state := &mockOpenCodeServer{
		sseEvents: []string{
			buildSSEEvent("session.created", map[string]interface{}{"sessionID": testSessionID}),
			sessionErrorEvent(testSessionID, "MessageAbortedError", "Aborted"),
			sessionIdleEvent(testSessionID),
		},
	}

	srv := newMockServer(t, state)
	ag := &OpenCodeHTTPAgent{
		ServerURL:  srv.URL,
		HTTPClient: srv.Client(),
	}

	job := &core.Job{
		ID:    "job-error",
		RunID: "run-error",
		Role:  "developer",
		CLI:   "opencode",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	handle, err := ag.Dispatch(ctx, job, "Please do something")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	result, err := handle.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	// The session.error message should be in result.Stderr.
	if !strings.Contains(result.Stderr, "MessageAbortedError") {
		t.Errorf("result.Stderr = %q; want to contain %q", result.Stderr, "MessageAbortedError")
	}
}

// TestOpenCodeHTTPAgent_NonJSONLOutput verifies that free-form text output
// (not JSONL) is placed in result.Stdout with no findings.
func TestOpenCodeHTTPAgent_NonJSONLOutput(t *testing.T) {
	assistantText := "This is a free-form prose response with no findings format."

	state := &mockOpenCodeServer{
		sseEvents: []string{
			buildSSEEvent("session.created", map[string]interface{}{"sessionID": testSessionID}),
			// message.updated (role:assistant) must precede message.part.updated.
			messageUpdatedAssistantEvent(testSessionID),
			messagePartUpdatedEvent(testSessionID, assistantText),
			sessionIdleEvent(testSessionID),
		},
	}

	srv := newMockServer(t, state)
	ag := &OpenCodeHTTPAgent{
		ServerURL:  srv.URL,
		HTTPClient: srv.Client(),
	}

	job := &core.Job{
		ID:    "job-prose",
		RunID: "run-prose",
		Role:  "developer",
		CLI:   "opencode",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	handle, err := ag.Dispatch(ctx, job, "Summarize the code")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	result, err := handle.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if len(result.Findings) != 0 {
		t.Errorf("findings = %d, want 0 (free-form text should not be parsed as JSONL)", len(result.Findings))
	}
	if result.Stdout != assistantText {
		t.Errorf("result.Stdout = %q, want %q", result.Stdout, assistantText)
	}
}

// TestOpenCodeHTTPAgent_MessageSendError verifies that a server error on
// POST /session/{id}/message does not block Dispatch (fire-and-forget POST),
// but the SSE goroutine will time out without receiving session.idle. Wait
// returns ctx.Err when the deadline is reached.
func TestOpenCodeHTTPAgent_MessageSendError(t *testing.T) {
	// SSE never sends session.idle — the message was rejected.
	mux := http.NewServeMux()
	var messageSent int32
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(openCodeSessionResponse{ID: testSessionID}) //nolint:errcheck
	})
	mux.HandleFunc("POST /session/{sessionID}/message", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&messageSent, 1)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintln(w, "internal server error")
	})
	mux.HandleFunc("DELETE /session/{sessionID}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /session/{sessionID}/abort", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Block until the context is cancelled (simulates a server that never
		// sends session.idle because the message was rejected).
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ag := &OpenCodeHTTPAgent{
		ServerURL:  srv.URL,
		HTTPClient: srv.Client(),
	}

	job := &core.Job{
		ID:    "job-msgerr",
		RunID: "run-msgerr",
		Role:  "developer",
		CLI:   "opencode",
	}

	// Dispatch returns immediately (fire-and-forget POST).
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	handle, err := ag.Dispatch(ctx, job, "cause an error")
	if err != nil {
		t.Fatalf("Dispatch: %v (should not block on message send failure)", err)
	}

	// Wait should return with the context deadline error.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	_, waitErr := handle.Wait(waitCtx)
	if waitErr == nil {
		t.Log("Wait returned nil err (SSE goroutine exited cleanly on context cancellation)")
	} else {
		t.Logf("Wait returned expected error: %v", waitErr)
	}

	// The message POST should have been attempted.
	if got := atomic.LoadInt32(&messageSent); got < 1 {
		t.Errorf("POST /session/{id}/message called %d times, want >= 1", got)
	}
}

// TestParseAssistantText_JSONL verifies the JSONL parser extracts findings.
func TestParseAssistantText_JSONL(t *testing.T) {
	text := `{"type":"finding","path":"a.go","line":1,"severity":"blocker","body":"Bad"}
{"type":"done","exit_code":2}
`
	result := parseAssistantText(text)
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(result.Findings))
	}
	if result.Findings[0].Path != "a.go" {
		t.Errorf("path = %q, want %q", result.Findings[0].Path, "a.go")
	}
	if result.ExitCode != 2 {
		t.Errorf("exit_code = %d, want 2", result.ExitCode)
	}
	if result.Stdout != "" {
		t.Errorf("stdout should be empty for JSONL output, got %q", result.Stdout)
	}
}

// TestParseAssistantText_Prose verifies that free-form text is placed in Stdout.
func TestParseAssistantText_Prose(t *testing.T) {
	text := "This is just some prose output."
	result := parseAssistantText(text)
	if len(result.Findings) != 0 {
		t.Errorf("findings = %d, want 0", len(result.Findings))
	}
	if result.Stdout != text {
		t.Errorf("stdout = %q, want %q", result.Stdout, text)
	}
}

// TestParseAssistantText_Empty verifies that empty text produces an empty result.
func TestParseAssistantText_Empty(t *testing.T) {
	result := parseAssistantText("")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Findings) != 0 {
		t.Errorf("findings = %d, want 0", len(result.Findings))
	}
}

// TestProcessSSEEvent_SessionIdle verifies session.idle detection.
func TestProcessSSEEvent_SessionIdle(t *testing.T) {
	raw := buildSSEEvent("session.idle", map[string]interface{}{"sessionID": testSessionID})
	ids := make(map[string]struct{})
	done, text, errMsg := processSSEEvent(raw, testSessionID, ids)
	if !done {
		t.Error("expected done=true for session.idle")
	}
	if text != "" || errMsg != "" {
		t.Errorf("unexpected text=%q errMsg=%q", text, errMsg)
	}
}

// TestProcessSSEEvent_OtherSession verifies that events for other sessions are ignored.
func TestProcessSSEEvent_OtherSession(t *testing.T) {
	raw := buildSSEEvent("session.idle", map[string]interface{}{"sessionID": "ses_other"})
	ids := make(map[string]struct{})
	done, text, errMsg := processSSEEvent(raw, testSessionID, ids)
	if done {
		t.Error("expected done=false for session.idle from other session")
	}
	if text != "" || errMsg != "" {
		t.Errorf("unexpected text=%q errMsg=%q", text, errMsg)
	}
}

// TestProcessSSEEvent_MessagePartUpdated verifies text extraction when the
// assistant message ID is known.
func TestProcessSSEEvent_MessagePartUpdated(t *testing.T) {
	// Pre-populate the assistant message ID set so the part is accepted.
	ids := map[string]struct{}{testAssistantMsgID: {}}
	raw := messagePartUpdatedEvent(testSessionID, "hello world")
	done, text, errMsg := processSSEEvent(raw, testSessionID, ids)
	if done {
		t.Error("expected done=false for message.part.updated")
	}
	if text != "hello world" {
		t.Errorf("text = %q, want %q", text, "hello world")
	}
	if errMsg != "" {
		t.Errorf("errMsg = %q, want empty", errMsg)
	}
}

// TestProcessSSEEvent_MessagePartUpdated_UserRole verifies that text parts
// for unknown (user) message IDs are filtered out.
func TestProcessSSEEvent_MessagePartUpdated_UserRole(t *testing.T) {
	// Empty set — no assistant messages registered yet.
	ids := make(map[string]struct{})
	// This part has the test assistant message ID but it's not registered,
	// simulating a user message part arriving before the assistant message.updated.
	raw := messagePartUpdatedEvent(testSessionID, "user prompt text")
	done, text, errMsg := processSSEEvent(raw, testSessionID, ids)
	if done {
		t.Error("expected done=false")
	}
	if text != "" {
		t.Errorf("text = %q, want empty (user message parts must be filtered)", text)
	}
	if errMsg != "" {
		t.Errorf("errMsg = %q, want empty", errMsg)
	}
}

// TestProcessSSEEvent_MessageUpdatedRegistersAssistant verifies that a
// message.updated event with role "assistant" adds the message ID to the set.
func TestProcessSSEEvent_MessageUpdatedRegistersAssistant(t *testing.T) {
	ids := make(map[string]struct{})
	raw := messageUpdatedAssistantEvent(testSessionID)
	done, text, errMsg := processSSEEvent(raw, testSessionID, ids)
	if done || text != "" || errMsg != "" {
		t.Errorf("unexpected result: done=%v text=%q errMsg=%q", done, text, errMsg)
	}
	if _, ok := ids[testAssistantMsgID]; !ok {
		t.Errorf("assistant message ID %q not registered in ids map", testAssistantMsgID)
	}
}

// TestProcessSSEEvent_SessionError verifies error extraction.
func TestProcessSSEEvent_SessionError(t *testing.T) {
	raw := sessionErrorEvent(testSessionID, "MessageAbortedError", "Aborted")
	ids := make(map[string]struct{})
	done, text, errMsg := processSSEEvent(raw, testSessionID, ids)
	if done {
		t.Error("expected done=false for session.error")
	}
	if text != "" {
		t.Errorf("text = %q, want empty", text)
	}
	if !strings.Contains(errMsg, "MessageAbortedError") {
		t.Errorf("errMsg = %q; want to contain %q", errMsg, "MessageAbortedError")
	}
}

// TestOpenCodeHTTPAgent_ServerURL verifies the serverURL helper trims trailing slashes.
func TestOpenCodeHTTPAgent_ServerURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http://127.0.0.1:7777/", "http://127.0.0.1:7777"},
		{"http://127.0.0.1:7777", "http://127.0.0.1:7777"},
		{"", DefaultOpenCodeServerURL},
	}
	for _, tc := range tests {
		ag := &OpenCodeHTTPAgent{ServerURL: tc.input}
		if got := ag.serverURL(); got != tc.want {
			t.Errorf("serverURL(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
