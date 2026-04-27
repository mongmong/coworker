package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chris/coworker/core"
)

// ReplayAgent is a core.Agent implementation that streams recorded
// stream-json output from a transcript file. It mirrors CliAgent's
// behavior exactly: Dispatch returns a JobHandle whose Wait parses the
// transcript using the same streamMessage schema and produces a real
// core.JobResult with parsed Findings, ExitCode, Stdout, Stderr.
//
// Used by replay tests to exercise the full dispatch pipeline
// (supervisor, dedupe, finding persistence) without running a real CLI.
//
// Per-role transcript routing: ReplayAgent looks for
// "<TranscriptDir>/<role-with-dots-replaced-by-underscores>.jsonl"
// (matching the role-file naming convention). Missing transcripts are a
// loud error.
type ReplayAgent struct {
	TranscriptDir string

	// LineDelay throttles between transcript lines. Zero == as fast as
	// the parser consumes.
	LineDelay time.Duration
}

// Dispatch opens the transcript matching job.Role and returns a JobHandle
// that streams it. If the transcript does not exist, Dispatch returns an
// error with the constructed path.
func (a *ReplayAgent) Dispatch(_ context.Context, job *core.Job, _ string) (core.JobHandle, error) {
	roleName := strings.ReplaceAll(job.Role, ".", "_")
	path := filepath.Join(a.TranscriptDir, roleName+".jsonl")
	f, err := os.Open(path) //nolint:gosec // G304: path constructed from controlled inputs in tests only
	if err != nil {
		return nil, fmt.Errorf("replay agent: open transcript %q: %w", path, err)
	}
	return &replayHandle{
		f:     f,
		delay: a.LineDelay,
	}, nil
}

type replayHandle struct {
	f     *os.File
	delay time.Duration

	mu        sync.Mutex
	cancelled bool
}

func (h *replayHandle) isCancelled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cancelled
}

// Wait reads the transcript line-by-line, parses each line as a stream-json
// event, and assembles a core.JobResult. Returns the partial result with
// ctx.Err() if the context is cancelled or Cancel() is called before the
// transcript completes.
func (h *replayHandle) Wait(ctx context.Context) (*core.JobResult, error) {
	defer func() { _ = h.f.Close() }()

	result := &core.JobResult{}
	decoder := json.NewDecoder(h.f)

	for decoder.More() {
		if h.isCancelled() {
			return result, context.Canceled
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		var msg streamMessage
		if err := decoder.Decode(&msg); err != nil {
			// Mirror cli_handle.go: on decode error, accumulate
			// remaining bytes into Stdout and stop. Do NOT set Stderr
			// here — Stderr is reserved for the agent's own stderr in
			// CliAgent (no equivalent in replay; left empty).
			_ = err
			rest, _ := io.ReadAll(decoder.Buffered())
			extra, _ := io.ReadAll(h.f)
			result.Stdout = string(rest) + string(extra)
			return result, nil
		}

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
		// Plan 121: extract cost from result/turn.completed events.
		populateCost(msg, result)

		if h.delay > 0 {
			select {
			case <-time.After(h.delay):
			case <-ctx.Done():
				return result, ctx.Err()
			}
		}
	}
	return result, nil
}

// Cancel marks the handle as cancelled so a concurrent Wait will return
// context.Canceled at the next loop iteration.
func (h *replayHandle) Cancel() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancelled = true
	return nil
}

// Compile-time assertion that ReplayAgent satisfies core.Agent.
var _ core.Agent = (*ReplayAgent)(nil)
