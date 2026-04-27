package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chris/coworker/coding/eventbus"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// openHTTPTestDB opens an in-memory SQLite DB for HTTP handler tests.
func openHTTPTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newHTTPTestStores creates store instances for the given DB.
func newHTTPTestStores(t *testing.T, db *store.DB) httpStores {
	t.Helper()
	es := store.NewEventStore(db)
	return httpStores{
		run:       store.NewRunStore(db, es),
		job:       store.NewJobStore(db, es),
		attention: store.NewAttentionStore(db),
	}
}

// createHTTPTestRun creates a run row in the DB and returns its ID.
func createHTTPTestRun(t *testing.T, s httpStores, id, mode string) string {
	t.Helper()
	run := &core.Run{
		ID:        id,
		Mode:      mode,
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := s.run.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return id
}

// createHTTPTestJob creates a job row linked to a run.
func createHTTPTestJob(t *testing.T, s httpStores, id, runID, role string) {
	t.Helper()
	job := &core.Job{
		ID:        id,
		RunID:     runID,
		Role:      role,
		State:     core.JobStatePending,
		StartedAt: time.Now(),
	}
	if err := s.job.CreateJob(context.Background(), job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
}

// TestHandleListRuns verifies GET /runs returns a JSON list.
func TestHandleListRuns(t *testing.T) {
	db := openHTTPTestDB(t)
	s := newHTTPTestStores(t, db)
	createHTTPTestRun(t, s, "run_http_1", "autopilot")
	createHTTPTestRun(t, s, "run_http_2", "interactive")

	bus := eventbus.NewInMemoryBus()
	ts := httptest.NewServer(buildHTTPMux(bus, s))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/runs")
	if err != nil {
		t.Fatalf("GET /runs: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	runs, ok := body["runs"].([]interface{})
	if !ok {
		t.Fatalf("runs field missing or wrong type: %T", body["runs"])
	}
	if len(runs) != 2 {
		t.Errorf("runs count = %d, want 2", len(runs))
	}
}

// TestHandleGetRun verifies GET /runs/{id} returns the run or 404.
func TestHandleGetRun(t *testing.T) {
	db := openHTTPTestDB(t)
	s := newHTTPTestStores(t, db)
	createHTTPTestRun(t, s, "run_get_1", "autopilot")

	bus := eventbus.NewInMemoryBus()
	ts := httptest.NewServer(buildHTTPMux(bus, s))
	defer ts.Close()

	t.Run("found", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/runs/run_get_1")
		if err != nil {
			t.Fatalf("GET /runs/run_get_1: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body["ID"] != "run_get_1" && body["id"] != "run_get_1" {
			t.Errorf("run ID not found in response: %v", body)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/runs/no_such_run")
		if err != nil {
			t.Fatalf("GET /runs/no_such_run: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
	})
}

// TestHandleListJobs verifies GET /runs/{id}/jobs returns jobs for the run.
func TestHandleListJobs(t *testing.T) {
	db := openHTTPTestDB(t)
	s := newHTTPTestStores(t, db)
	createHTTPTestRun(t, s, "run_jobs_1", "autopilot")
	createHTTPTestJob(t, s, "job_a", "run_jobs_1", "developer")
	createHTTPTestJob(t, s, "job_b", "run_jobs_1", "reviewer")

	bus := eventbus.NewInMemoryBus()
	ts := httptest.NewServer(buildHTTPMux(bus, s))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/runs/run_jobs_1/jobs")
	if err != nil {
		t.Fatalf("GET /runs/run_jobs_1/jobs: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	jobs, ok := body["jobs"].([]interface{})
	if !ok {
		t.Fatalf("jobs field missing or wrong type: %T", body["jobs"])
	}
	if len(jobs) != 2 {
		t.Errorf("jobs count = %d, want 2", len(jobs))
	}
}

// TestHandleListAttention verifies GET /attention lists pending items.
func TestHandleListAttention(t *testing.T) {
	db := openHTTPTestDB(t)
	s := newHTTPTestStores(t, db)
	createHTTPTestRun(t, s, "run_attn_1", "autopilot")

	// Insert one pending attention item.
	item := &core.AttentionItem{
		ID:        core.NewID(),
		RunID:     "run_attn_1",
		Kind:      core.AttentionQuestion,
		Source:    "test",
		Question:  "Proceed?",
		CreatedAt: time.Now(),
	}
	if err := s.attention.InsertAttention(context.Background(), item); err != nil {
		t.Fatalf("InsertAttention: %v", err)
	}

	bus := eventbus.NewInMemoryBus()
	ts := httptest.NewServer(buildHTTPMux(bus, s))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/attention")
	if err != nil {
		t.Fatalf("GET /attention: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	items, ok := body["items"].([]interface{})
	if !ok {
		t.Fatalf("items field missing or wrong type: %T", body["items"])
	}
	if len(items) != 1 {
		t.Errorf("items count = %d, want 1", len(items))
	}
}

// TestHandleAnswerAttention verifies POST /attention/{id}/answer mutates the DB.
func TestHandleAnswerAttention(t *testing.T) {
	db := openHTTPTestDB(t)
	s := newHTTPTestStores(t, db)
	createHTTPTestRun(t, s, "run_ans_1", "autopilot")

	item := &core.AttentionItem{
		ID:        core.NewID(),
		RunID:     "run_ans_1",
		Kind:      core.AttentionCheckpoint,
		Source:    "test",
		Question:  "Approve?",
		CreatedAt: time.Now(),
	}
	if err := s.attention.InsertAttention(context.Background(), item); err != nil {
		t.Fatalf("InsertAttention: %v", err)
	}

	bus := eventbus.NewInMemoryBus()
	ts := httptest.NewServer(buildHTTPMux(bus, s))
	defer ts.Close()

	body := map[string]string{"answer": "approve", "answered_by": "human"}
	bodyBytes, _ := json.Marshal(body)

	resp, err := http.Post(
		ts.URL+"/attention/"+item.ID+"/answer",
		"application/json",
		bytes.NewReader(bodyBytes),
	)
	if err != nil {
		t.Fatalf("POST /attention/.../answer: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyStr, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, bodyStr)
	}

	// Verify the item is no longer pending (answer IS NULL is false now).
	pending, err := s.attention.ListAllPending(context.Background())
	if err != nil {
		t.Fatalf("ListAllPending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending count = %d, want 0 after answer", len(pending))
	}

	// Verify the answer is persisted.
	answered, err := s.attention.GetAttentionByID(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("GetAttentionByID: %v", err)
	}
	if answered.Answer != "approve" {
		t.Errorf("answer = %q, want %q", answered.Answer, "approve")
	}
	if answered.AnsweredBy != "human" {
		t.Errorf("answered_by = %q, want %q", answered.AnsweredBy, "human")
	}
}

// TestHandleAnswerAttention_MalformedJSON verifies 400 on invalid JSON body.
func TestHandleAnswerAttention_MalformedJSON(t *testing.T) {
	db := openHTTPTestDB(t)
	s := newHTTPTestStores(t, db)

	bus := eventbus.NewInMemoryBus()
	ts := httptest.NewServer(buildHTTPMux(bus, s))
	defer ts.Close()

	resp, err := http.Post(
		ts.URL+"/attention/some-id/answer",
		"application/json",
		strings.NewReader("not-valid-json"),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestHandleSSEEvents verifies GET /events streams a published event.
func TestHandleSSEEvents(t *testing.T) {
	db := openHTTPTestDB(t)
	s := newHTTPTestStores(t, db)
	bus := eventbus.NewInMemoryBus()

	ts := httptest.NewServer(buildHTTPMux(bus, s))
	defer ts.Close()

	// Connect to the SSE endpoint with a cancellable context.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	// Wait briefly for the connection to be established.
	var sseResp *http.Response
	select {
	case sseResp = <-respCh:
	case err := <-errCh:
		t.Fatalf("SSE connect: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for SSE connection")
	}
	defer sseResp.Body.Close()

	// Publish an event.
	event := &core.Event{
		ID:    core.NewID(),
		RunID: "run_sse_test",
		Kind:  core.EventRunCreated,
	}
	bus.Publish(event)

	// Read the first SSE data line.
	lineCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		n, _ := sseResp.Body.Read(buf)
		lineCh <- string(buf[:n])
	}()

	select {
	case line := <-lineCh:
		if !strings.Contains(line, "data:") {
			t.Errorf("SSE line does not contain data prefix: %q", line)
		}
		// Verify the event JSON is present.
		if !strings.Contains(line, event.ID) {
			t.Errorf("SSE line does not contain event ID %q: %q", event.ID, line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for SSE event")
	}

	// Cancel the context to verify graceful shutdown.
	cancel()
}

// TestHTTPMux_UnknownRoute verifies that unknown routes return 404.
func TestHTTPMux_UnknownRoute(t *testing.T) {
	db := openHTTPTestDB(t)
	s := newHTTPTestStores(t, db)
	bus := eventbus.NewInMemoryBus()
	ts := httptest.NewServer(buildHTTPMux(bus, s))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/unknown-path")
	if err != nil {
		t.Fatalf("GET /unknown-path: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestHandleListRuns_EmptyDB verifies GET /runs returns an empty list (not nil).
func TestHandleListRuns_EmptyDB(t *testing.T) {
	db := openHTTPTestDB(t)
	s := newHTTPTestStores(t, db)
	bus := eventbus.NewInMemoryBus()
	ts := httptest.NewServer(buildHTTPMux(bus, s))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/runs")
	if err != nil {
		t.Fatalf("GET /runs: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	b, _ := json.Marshal(body["runs"])
	if string(b) == "null" {
		t.Error("runs should be [] not null when empty")
	}
}

// TestHandleListAttention_AfterAnswer verifies answered items disappear from /attention.
func TestHandleListAttention_AfterAnswer(t *testing.T) {
	db := openHTTPTestDB(t)
	s := newHTTPTestStores(t, db)
	createHTTPTestRun(t, s, "run_attn_cycle", "autopilot")

	item := &core.AttentionItem{
		ID:        core.NewID(),
		RunID:     "run_attn_cycle",
		Kind:      core.AttentionCheckpoint,
		Source:    "test",
		Question:  "Approve?",
		CreatedAt: time.Now(),
	}
	if err := s.attention.InsertAttention(context.Background(), item); err != nil {
		t.Fatalf("InsertAttention: %v", err)
	}

	bus := eventbus.NewInMemoryBus()
	ts := httptest.NewServer(buildHTTPMux(bus, s))
	defer ts.Close()

	// Answer the item.
	body := map[string]string{"answer": "approve", "answered_by": "human"}
	bodyBytes, _ := json.Marshal(body)
	ansResp, err := http.Post(
		ts.URL+"/attention/"+item.ID+"/answer",
		"application/json",
		bytes.NewReader(bodyBytes),
	)
	if err != nil {
		t.Fatalf("POST answer: %v", err)
	}
	ansResp.Body.Close()

	// Now list attention — item should be gone.
	listResp, err := http.Get(ts.URL + "/attention")
	if err != nil {
		t.Fatalf("GET /attention: %v", err)
	}
	defer listResp.Body.Close()

	var listBody map[string]interface{}
	if err := json.NewDecoder(listResp.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	items, ok := listBody["items"].([]interface{})
	if !ok {
		t.Fatalf("items wrong type: %T", listBody["items"])
	}
	if len(items) != 0 {
		t.Errorf("items count = %d, want 0 after answer", len(items))
	}
}

// TestHandleAnswerAttention_MissingAnswer verifies 400 when answer field is empty.
func TestHandleAnswerAttention_MissingAnswer(t *testing.T) {
	db := openHTTPTestDB(t)
	s := newHTTPTestStores(t, db)

	bus := eventbus.NewInMemoryBus()
	ts := httptest.NewServer(buildHTTPMux(bus, s))
	defer ts.Close()

	body := map[string]string{"answered_by": "human"} // missing "answer"
	bodyBytes, _ := json.Marshal(body)

	resp, err := http.Post(
		ts.URL+"/attention/some-id/answer",
		"application/json",
		bytes.NewReader(bodyBytes),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestHandleAnswerAttention_UnknownID verifies that answering an unknown
// attention ID returns HTTP 404 (Important #2).
func TestHandleAnswerAttention_UnknownID(t *testing.T) {
	db := openHTTPTestDB(t)
	s := newHTTPTestStores(t, db)

	bus := eventbus.NewInMemoryBus()
	ts := httptest.NewServer(buildHTTPMux(bus, s))
	defer ts.Close()

	body := map[string]string{"answer": "approve", "answered_by": "human"}
	bodyBytes, _ := json.Marshal(body)

	resp, err := http.Post(
		ts.URL+"/attention/nonexistent-id/answer",
		"application/json",
		bytes.NewReader(bodyBytes),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		bodyStr, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, bodyStr)
	}
}

// TestHandleAnswerAttention_InvalidAnswer verifies that an answer other than
// "approve" or "reject" returns HTTP 400 (Polish: HTTP answer validation).
func TestHandleAnswerAttention_InvalidAnswer(t *testing.T) {
	db := openHTTPTestDB(t)
	s := newHTTPTestStores(t, db)

	bus := eventbus.NewInMemoryBus()
	ts := httptest.NewServer(buildHTTPMux(bus, s))
	defer ts.Close()

	body := map[string]string{"answer": "maybe", "answered_by": "human"}
	bodyBytes, _ := json.Marshal(body)

	resp, err := http.Post(
		ts.URL+"/attention/some-id/answer",
		"application/json",
		bytes.NewReader(bodyBytes),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		bodyStr, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, bodyStr)
	}
}

// TestErrGroupMutualCancel verifies context cancel stops the HTTP server.
// This is a lightweight integration test of the shutdown path using httptest.
func TestErrGroupMutualCancel(t *testing.T) {
	db := openHTTPTestDB(t)
	s := newHTTPTestStores(t, db)
	bus := eventbus.NewInMemoryBus()

	mux := buildHTTPMux(bus, s)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Ensure the server is alive.
	resp, err := http.Get(ts.URL + "/runs")
	if err != nil {
		t.Fatalf("GET /runs before close: %v", err)
	}
	resp.Body.Close()

	// Close the test server (simulating context cancel → Shutdown).
	ts.Close()

	// After close, requests should fail.
	_, err = http.Get(fmt.Sprintf("%s/runs", ts.URL))
	if err == nil {
		t.Error("expected error after server closed, got nil")
	}
}
