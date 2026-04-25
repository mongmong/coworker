package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/chris/coworker/core"
)

// connectedMsg signals that the SSE connection was established.
type connectedMsg struct{}

// eventMsg carries a single runtime event from the SSE stream.
type eventMsg struct{ event *core.Event }

// errMsg carries a non-fatal connection error (triggers SSE retry with backoff).
type errMsg struct{ err error }

// retrySSEMsg is sent by the backoff tea.Tick to trigger a reconnect attempt.
type retrySSEMsg struct{}

// snapshotMsg carries the initial REST snapshot of runs, jobs, and attention items.
type snapshotMsg struct {
	runs      []core.Run
	jobs      []core.Job
	attention []core.AttentionItem
}

// jobLogMsg carries the log lines loaded from a job's .jsonl file.
type jobLogMsg struct {
	jobID string
	lines []string
}

// subscribeSSE returns a tea.Cmd that reads one event from the SSE endpoint.
// After Update processes the returned eventMsg, it re-issues subscribeSSE to
// keep the stream open (one-event-per-Cmd pattern, standard for Bubble Tea).
func subscribeSSE(baseURL, runID string) tea.Cmd {
	return func() tea.Msg {
		eventsURL := buildTUIEventsURL(baseURL, runID)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, eventsURL, nil)
		if err != nil {
			return errMsg{fmt.Errorf("build SSE request: %w", err)}
		}
		req.Header.Set("Accept", "text/event-stream")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return errMsg{fmt.Errorf("SSE connect: %w", err)}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return errMsg{fmt.Errorf("SSE server returned %d", resp.StatusCode)}
		}

		// Parse SSE lines and return the first event as a message.
		// Bubble Tea will call this Cmd again after each message so that the
		// subscription stays open: each invocation reads and returns one event,
		// then re-subscribes.
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			raw := strings.TrimPrefix(line, "data: ")
			var ev core.Event
			if err := json.Unmarshal([]byte(raw), &ev); err != nil {
				continue
			}
			return eventMsg{event: &ev}
		}
		if err := scanner.Err(); err != nil {
			return errMsg{fmt.Errorf("SSE read: %w", err)}
		}
		// Stream closed; signal reconnect after brief pause.
		time.Sleep(2 * time.Second)
		return connectedMsg{}
	}
}

// buildTUIEventsURL constructs the SSE endpoint URL.
func buildTUIEventsURL(baseURL, runID string) string {
	query := url.Values{}
	if runID != "" {
		query.Set("run_id", runID)
	}
	u := strings.TrimRight(baseURL, "/") + "/events"
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

// submitAnswer posts an answer for an attention item to the daemon REST API.
// Returns a tea.Cmd that fires an errMsg if the POST fails (non-fatal; logged).
func submitAnswer(baseURL, itemID, answer string) tea.Cmd {
	return func() tea.Msg {
		endpoint := fmt.Sprintf("%s/attention/%s/answer", strings.TrimRight(baseURL, "/"), itemID)
		body, _ := json.Marshal(map[string]string{"answer": answer, "answered_by": "tui"})
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body)) //nolint:noctx
		if err != nil {
			return errMsg{fmt.Errorf("build submit request: %w", err)}
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req) //nolint:noctx
		if err != nil {
			return errMsg{fmt.Errorf("submit answer: %w", err)}
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return errMsg{fmt.Errorf("submit answer: daemon returned %d", resp.StatusCode)}
		}
		return nil // success; attention item will disappear via next event
	}
}

// fetchSnapshot fetches current runs, jobs, and attention items from the daemon
// REST API and returns a snapshotMsg to hydrate the initial model state.
func fetchSnapshot(baseURL, runID string) tea.Cmd {
	return func() tea.Msg {
		client := &http.Client{Timeout: 10 * time.Second}
		base := strings.TrimRight(baseURL, "/")

		var snap snapshotMsg

		// GET /runs
		if runs, err := getJSON[[]core.Run](client, base+"/runs"); err == nil {
			if runID != "" {
				for _, r := range runs {
					if r.ID == runID {
						snap.runs = []core.Run{r}
						break
					}
				}
			} else {
				snap.runs = runs
			}
		}

		// GET /jobs (optionally filtered by run_id)
		jobsURL := base + "/jobs"
		if runID != "" {
			jobsURL += "?run_id=" + url.QueryEscape(runID)
		}
		if jobs, err := getJSON[[]core.Job](client, jobsURL); err == nil {
			snap.jobs = jobs
		}

		// GET /attention (pending items only)
		attURL := base + "/attention"
		if runID != "" {
			attURL += "?run_id=" + url.QueryEscape(runID)
		}
		if items, err := getJSON[[]core.AttentionItem](client, attURL); err == nil {
			snap.attention = items
		}

		return snap
	}
}

// getJSON performs a GET request and decodes the JSON response body into T.
func getJSON[T any](client *http.Client, u string) (T, error) {
	var zero T
	resp, err := client.Get(u) //nolint:noctx
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("GET %s: status %d", u, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&zero); err != nil {
		return zero, err
	}
	return zero, nil
}

// loadJobLog reads the tail of a job's event log file
// (.coworker/runs/<runID>/jobs/<jobID>.jsonl) and returns a jobLogMsg.
// Returns an empty slice (not an error) if the file does not yet exist.
func loadJobLog(runID, jobID string) tea.Cmd {
	return func() tea.Msg {
		path := filepath.Join(".coworker", "runs", runID, "jobs", jobID+".jsonl")
		data, err := os.ReadFile(path)
		if err != nil {
			// File may not exist yet; return empty lines gracefully.
			return jobLogMsg{jobID: jobID, lines: nil}
		}
		raw := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		// Tail the last 500 lines to keep memory bounded.
		const maxLines = 500
		if len(raw) > maxLines {
			raw = raw[len(raw)-maxLines:]
		}
		return jobLogMsg{jobID: jobID, lines: raw}
	}
}
