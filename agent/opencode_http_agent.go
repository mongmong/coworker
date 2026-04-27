package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/chris/coworker/core"
)

// DefaultOpenCodeServerURL is the default URL for the OpenCode HTTP server.
const DefaultOpenCodeServerURL = "http://127.0.0.1:7777"

// OpenCodeHTTPAgent dispatches jobs to an OpenCode server via its REST/SSE API.
// It implements core.Agent.
//
// The dispatch lifecycle:
//  1. POST /session → get session ID
//  2. Subscribe to GET /event SSE stream (goroutine)
//  3. POST /session/{id}/message → start the LLM response
//  4. SSE goroutine collects events; session.idle signals completion
//  5. DELETE /session/{id} cleanup
type OpenCodeHTTPAgent struct {
	// ServerURL is the base URL of the OpenCode server
	// (e.g. "http://127.0.0.1:7777"). Defaults to DefaultOpenCodeServerURL
	// when empty.
	ServerURL string

	// HTTPClient is the HTTP client used for all requests. When nil the
	// package-level http.DefaultClient is used.
	HTTPClient *http.Client
}

// httpClient returns the effective HTTP client.
func (a *OpenCodeHTTPAgent) httpClient() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return http.DefaultClient
}

// serverURL returns the effective server URL without a trailing slash.
func (a *OpenCodeHTTPAgent) serverURL() string {
	u := a.ServerURL
	if u == "" {
		u = DefaultOpenCodeServerURL
	}
	return strings.TrimRight(u, "/")
}

// openCodeSessionResponse is the shape returned by POST /session.
type openCodeSessionResponse struct {
	ID string `json:"id"`
}

// openCodeMessageRequest is the shape sent to POST /session/{id}/message.
type openCodeMessageRequest struct {
	Parts []openCodeMessagePart `json:"parts"`
}

// openCodeMessagePart is a single part of a message request.
type openCodeMessagePart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// openCodeMessageResponse is the shape returned by POST /session/{id}/message.
type openCodeMessageResponse struct {
	Info struct {
		ID     string `json:"id"`
		Role   string `json:"role"`
		Finish string `json:"finish"`
	} `json:"info"`
	Parts []openCodeMessageResponsePart `json:"parts"`
}

// openCodeMessageResponsePart is one part of the message response.
type openCodeMessageResponsePart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// openCodeSSEEvent is the envelope for an SSE event from OpenCode.
type openCodeSSEEvent struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

// openCodeSessionIDHolder is used to extract sessionID from SSE properties.
type openCodeSessionIDHolder struct {
	SessionID string `json:"sessionID"`
}

// openCodeSessionIdleProps is the shape of session.idle properties.
type openCodeSessionIdleProps = openCodeSessionIDHolder

// openCodeSessionErrorProps is the shape of session.error properties.
type openCodeSessionErrorProps struct {
	SessionID string `json:"sessionID"`
	Error     struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

// openCodeMessagePartUpdatedProps is the shape of message.part.updated properties.
type openCodeMessagePartUpdatedProps struct {
	SessionID string `json:"sessionID"`
	Part      struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"part"`
}

// openCodeJobHandle implements core.JobHandle for an HTTP-dispatched OpenCode job.
type openCodeJobHandle struct {
	sessionID string
	agent     *OpenCodeHTTPAgent

	// resultCh receives exactly one *core.JobResult from the SSE goroutine.
	resultCh <-chan *core.JobResult
	// cancel cancels the SSE goroutine context.
	cancel context.CancelFunc
}

// Dispatch starts a job by:
//  1. Creating an OpenCode session
//  2. Starting an SSE subscription goroutine
//  3. Posting the prompt as a message to the session
//
// Returns a JobHandle to wait for the result.
func (a *OpenCodeHTTPAgent) Dispatch(ctx context.Context, job *core.Job, prompt string) (core.JobHandle, error) {
	base := a.serverURL()
	client := a.httpClient()

	// 1. Create a session.
	sessionID, err := a.createSession(ctx, client, base, job.ID)
	if err != nil {
		return nil, fmt.Errorf("opencode: create session: %w", err)
	}

	// 2. Start SSE subscription goroutine before posting the message so we
	//    don't miss events that arrive before the POST returns.
	sseCtx, sseCancel := context.WithCancel(ctx)
	resultCh := make(chan *core.JobResult, 1)

	go func() { //nolint:gosec // G118: goroutine intentionally creates a context.Background for DELETE cleanup that must outlive the caller's context
		result := a.sseLoop(sseCtx, client, base, sessionID)
		// Best-effort session cleanup uses context.Background so the DELETE
		// is sent even when the caller's context has been cancelled.
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer deleteCancel()
		a.deleteSession(deleteCtx, client, base, sessionID) //nolint:errcheck
		resultCh <- result
	}()

	// 3. Post the message. This blocks until OpenCode finishes responding.
	//    We send it in a goroutine so context cancellation can interrupt it.
	msgErrCh := make(chan error, 1)
	go func() {
		msgErrCh <- a.sendMessage(ctx, client, base, sessionID, prompt)
	}()

	// Wait for the message to be sent (or context to be cancelled). We return
	// the handle immediately; the SSE goroutine will signal completion.
	select {
	case msgErr := <-msgErrCh:
		if msgErr != nil {
			sseCancel()
			return nil, fmt.Errorf("opencode: send message: %w", msgErr)
		}
	case <-ctx.Done():
		sseCancel()
		return nil, ctx.Err()
	}

	handle := &openCodeJobHandle{
		sessionID: sessionID,
		agent:     a,
		resultCh:  resultCh,
		cancel:    sseCancel,
	}
	return handle, nil
}

// Wait blocks until the SSE goroutine signals completion or ctx is cancelled.
func (h *openCodeJobHandle) Wait(ctx context.Context) (*core.JobResult, error) {
	select {
	case result := <-h.resultCh:
		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Cancel aborts the running session by cancelling the SSE goroutine context
// and posting to /session/{id}/abort.
func (h *openCodeJobHandle) Cancel() error {
	h.cancel()

	abortCtx, abortCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer abortCancel()

	return h.agent.abortSession(abortCtx, h.agent.httpClient(), h.agent.serverURL(), h.sessionID)
}

// createSession posts to /session and returns the new session ID.
func (a *OpenCodeHTTPAgent) createSession(ctx context.Context, client *http.Client, base, title string) (string, error) {
	body, _ := json.Marshal(map[string]string{"title": title})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/session", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("POST /session returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}

	var session openCodeSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return "", fmt.Errorf("decode session response: %w", err)
	}
	if session.ID == "" {
		return "", fmt.Errorf("server returned session with empty ID")
	}
	return session.ID, nil
}

// sendMessage posts the prompt to /session/{id}/message and returns when
// OpenCode has finished processing the request.
func (a *OpenCodeHTTPAgent) sendMessage(ctx context.Context, client *http.Client, base, sessionID, prompt string) error {
	reqBody := openCodeMessageRequest{
		Parts: []openCodeMessagePart{
			{Type: "text", Text: prompt},
		},
	}
	body, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/session/"+sessionID+"/message", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("POST /session/%s/message returned %s: %s",
			sessionID, resp.Status, strings.TrimSpace(string(raw)))
	}

	// Drain the response body so the connection is returned to the pool.
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// abortSession posts to /session/{id}/abort. Best-effort; error is non-fatal.
func (a *OpenCodeHTTPAgent) abortSession(ctx context.Context, client *http.Client, base, sessionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/session/"+sessionID+"/abort", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// deleteSession sends DELETE /session/{id}. Best-effort; error is logged but
// does not affect the job result.
func (a *OpenCodeHTTPAgent) deleteSession(ctx context.Context, client *http.Client, base, sessionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		base+"/session/"+sessionID, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// sseLoop subscribes to /event and processes events until session.idle is
// received for the target session or ctx is cancelled. It returns the
// accumulated JobResult. On transient disconnects it reconnects with backoff.
func (a *OpenCodeHTTPAgent) sseLoop(ctx context.Context, client *http.Client, base, sessionID string) *core.JobResult {
	var assistantParts []string
	var sessionErrMsg string
	reconnects := 0
	backoff := 250 * time.Millisecond

	for ctx.Err() == nil {
		complete, newParts, newErrMsg, streamErr := a.sseStream(ctx, client, base, sessionID)
		// Merge accumulated text.
		assistantParts = append(assistantParts, newParts...)
		if newErrMsg != "" {
			sessionErrMsg = newErrMsg
		}

		if complete {
			break
		}

		// No transient error, or context cancelled — stop retrying.
		if streamErr == nil || ctx.Err() != nil {
			break
		}

		// Transient error — reconnect with backoff.
		reconnects++
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		if backoff < 5*time.Second {
			backoff *= 2
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
		}
	}

	assistantText := strings.Join(assistantParts, "")
	result := parseAssistantText(assistantText)
	result.Stderr = sessionErrMsg
	return result
}

// sseStream connects to /event once and reads events until:
//   - session.idle is received for the target session → complete=true
//   - the SSE connection is closed or errors → complete=false, streamErr set
//   - ctx is cancelled → complete=false
//
// Returns the text parts accumulated in this connection and any session error.
func (a *OpenCodeHTTPAgent) sseStream(
	ctx context.Context,
	client *http.Client,
	base, sessionID string,
) (complete bool, parts []string, sessionErrMsg string, streamErr error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/event", nil)
	if err != nil {
		return false, nil, "", err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, nil, "", nil
		}
		return false, nil, "", fmt.Errorf("connect to /event: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, nil, "", fmt.Errorf("GET /event returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}

	reader := bufio.NewReader(resp.Body)
	var dataLines []string

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if ctx.Err() != nil {
				return false, parts, sessionErrMsg, nil
			}
			if len(dataLines) > 0 {
				// Process the last accumulated event before returning.
				isDone, textPart, errMsg := processSSEEvent(strings.Join(dataLines, "\n"), sessionID)
				if textPart != "" {
					parts = append(parts, textPart)
				}
				if errMsg != "" {
					sessionErrMsg = errMsg
				}
				if isDone {
					return true, parts, sessionErrMsg, nil
				}
			}
			return false, parts, sessionErrMsg, fmt.Errorf("read SSE: %w", err)
		}

		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			// Empty line = event boundary.
			if len(dataLines) == 0 {
				continue
			}
			isDone, textPart, errMsg := processSSEEvent(strings.Join(dataLines, "\n"), sessionID)
			dataLines = dataLines[:0]
			if textPart != "" {
				parts = append(parts, textPart)
			}
			if errMsg != "" {
				sessionErrMsg = errMsg
			}
			if isDone {
				return true, parts, sessionErrMsg, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // SSE comment
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		value := strings.TrimPrefix(line, "data:")
		value = strings.TrimPrefix(value, " ")
		dataLines = append(dataLines, value)
	}
}

// processSSEEvent decodes one SSE data payload and returns:
//   - done: true when session.idle is received for the target session
//   - textPart: assistant text accumulated from message.part.updated
//   - sessionErrMsg: error text from session.error
func processSSEEvent(raw, sessionID string) (done bool, textPart, sessionErrMsg string) {
	var env openCodeSSEEvent
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return false, "", ""
	}

	switch env.Type {
	case "session.idle":
		var props openCodeSessionIdleProps
		if err := json.Unmarshal(env.Properties, &props); err == nil {
			if props.SessionID == sessionID {
				return true, "", ""
			}
		}

	case "message.part.updated":
		var props openCodeMessagePartUpdatedProps
		if err := json.Unmarshal(env.Properties, &props); err == nil {
			if props.SessionID == sessionID && props.Part.Type == "text" && props.Part.Text != "" {
				return false, props.Part.Text, ""
			}
		}

	case "session.error":
		var props openCodeSessionErrorProps
		if err := json.Unmarshal(env.Properties, &props); err == nil {
			if props.SessionID == sessionID {
				msg := props.Error.Name
				if props.Error.Data.Message != "" {
					msg += ": " + props.Error.Data.Message
				}
				return false, "", msg
			}
		}
	}

	return false, "", ""
}

// parseAssistantText tries to decode the assistant's text output as JSONL
// stream-message records (same format as CliAgent). If the text does not
// contain valid JSONL, it is placed in result.Stdout unchanged.
func parseAssistantText(text string) *core.JobResult {
	result := &core.JobResult{}
	if text == "" {
		return result
	}

	decoder := json.NewDecoder(strings.NewReader(text))
	foundAny := false
	for decoder.More() {
		var msg streamMessage
		if err := decoder.Decode(&msg); err != nil {
			break
		}
		foundAny = true
		switch msg.Type {
		case "finding":
			result.Findings = append(result.Findings, core.Finding{
				ID:       core.NewID(),
				Path:     msg.Path,
				Line:     msg.Line,
				Severity: core.Severity(msg.Severity),
				Body:     msg.Body,
			})
		case "done":
			result.ExitCode = msg.ExitCode
		}
	}

	if !foundAny {
		result.Stdout = text
	}
	return result
}
