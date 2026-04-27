package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func writeTranscript(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestReplayAgent_HappyPath(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "developer.jsonl", strings.Join([]string{
		`{"type":"finding","path":"main.go","line":42,"severity":"important","body":"missing close"}`,
		`{"type":"finding","path":"util.go","line":7,"severity":"minor","body":"trailing space"}`,
		`{"type":"done","exit_code":0}`,
	}, "\n"))

	a := &ReplayAgent{TranscriptDir: dir}
	h, err := a.Dispatch(context.Background(), &core.Job{Role: "developer"}, "ignored")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	res, err := h.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(res.Findings) != 2 {
		t.Errorf("findings = %d, want 2", len(res.Findings))
	}
	if res.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", res.ExitCode)
	}
	if res.Findings[0].Path != "main.go" || res.Findings[1].Path != "util.go" {
		t.Errorf("findings paths wrong: %+v", res.Findings)
	}
	if res.Findings[0].Severity != core.Severity("important") {
		t.Errorf("findings[0] severity = %q", res.Findings[0].Severity)
	}
}

func TestReplayAgent_MissingTranscript(t *testing.T) {
	a := &ReplayAgent{TranscriptDir: t.TempDir()}
	_, err := a.Dispatch(context.Background(), &core.Job{Role: "ghost"}, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ghost.jsonl") {
		t.Errorf("error %q should mention ghost.jsonl", err)
	}
}

func TestReplayAgent_RoleWithDots(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "reviewer_arch.jsonl", `{"type":"done","exit_code":0}`+"\n")
	a := &ReplayAgent{TranscriptDir: dir}
	h, err := a.Dispatch(context.Background(), &core.Job{Role: "reviewer.arch"}, "")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if _, err := h.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestReplayAgent_ContextCancel(t *testing.T) {
	dir := t.TempDir()
	// Several lines + delay so the cancel fires mid-stream.
	writeTranscript(t, dir, "developer.jsonl", strings.Join([]string{
		`{"type":"finding","path":"a.go","line":1,"severity":"minor","body":"x"}`,
		`{"type":"finding","path":"b.go","line":2,"severity":"minor","body":"y"}`,
		`{"type":"finding","path":"c.go","line":3,"severity":"minor","body":"z"}`,
		`{"type":"done","exit_code":0}`,
	}, "\n"))

	a := &ReplayAgent{TranscriptDir: dir, LineDelay: 100 * time.Millisecond}
	h, err := a.Dispatch(context.Background(), &core.Job{Role: "developer"}, "")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	res, err := h.Wait(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if res == nil {
		t.Fatal("res is nil")
	}
	// Partial result should contain the first finding parsed before
	// cancellation fired — proves cancel returns *partial* state, not
	// throws away parsed findings.
	if len(res.Findings) == 0 {
		t.Errorf("partial result should include at least 1 finding parsed before cancel; got 0")
	}
	if len(res.Findings) >= 4 {
		t.Errorf("partial result should be partial; got all %d findings", len(res.Findings))
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 (done line not yet reached)", res.ExitCode)
	}
}

func TestReplayAgent_HandleCancel(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "developer.jsonl", strings.Join([]string{
		`{"type":"finding","path":"a.go","line":1,"severity":"minor","body":"x"}`,
		`{"type":"finding","path":"b.go","line":2,"severity":"minor","body":"y"}`,
		`{"type":"finding","path":"c.go","line":3,"severity":"minor","body":"z"}`,
		`{"type":"done","exit_code":0}`,
	}, "\n"))

	a := &ReplayAgent{TranscriptDir: dir, LineDelay: 100 * time.Millisecond}
	h, err := a.Dispatch(context.Background(), &core.Job{Role: "developer"}, "")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = h.Cancel()
	}()
	res, err := h.Wait(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if res == nil {
		t.Fatal("res is nil")
	}
	if len(res.Findings) == 0 {
		t.Errorf("partial result should include at least 1 finding before Cancel; got 0")
	}
	if len(res.Findings) >= 4 {
		t.Errorf("partial result should be partial; got all %d findings", len(res.Findings))
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 (done line not yet reached)", res.ExitCode)
	}
}

func TestReplayAgent_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "developer.jsonl", strings.Join([]string{
		`{"type":"finding","path":"a.go","line":1,"severity":"minor","body":"ok"}`,
		`not valid json`,
		`{"type":"done","exit_code":0}`,
	}, "\n"))
	a := &ReplayAgent{TranscriptDir: dir}
	h, err := a.Dispatch(context.Background(), &core.Job{Role: "developer"}, "")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	res, err := h.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Errorf("findings = %d, want 1 (parsed before malformed line)", len(res.Findings))
	}
	// Mirror cli_handle.go behavior: malformed JSON does NOT populate Stderr.
	// Remaining bytes are appended to Stdout instead.
	if res.Stderr != "" {
		t.Errorf("stderr = %q, want empty (parser parity with cli_handle.go)", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "not valid json") {
		t.Errorf("stdout = %q; expected to contain remaining 'not valid json' bytes", res.Stdout)
	}
}

func TestReplayAgent_LineDelay(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "developer.jsonl", strings.Join([]string{
		`{"type":"finding","path":"a.go","line":1,"severity":"minor","body":"x"}`,
		`{"type":"finding","path":"b.go","line":2,"severity":"minor","body":"y"}`,
		`{"type":"finding","path":"c.go","line":3,"severity":"minor","body":"z"}`,
		`{"type":"done","exit_code":0}`,
	}, "\n"))
	a := &ReplayAgent{TranscriptDir: dir, LineDelay: 30 * time.Millisecond}
	h, err := a.Dispatch(context.Background(), &core.Job{Role: "developer"}, "")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	start := time.Now()
	if _, err := h.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	elapsed := time.Since(start)
	// 4 lines × 30ms = 120ms minimum, allow margin.
	if elapsed < 90*time.Millisecond {
		t.Errorf("elapsed = %v; expected >= 90ms with LineDelay=30ms × 4 lines", elapsed)
	}
}

func TestReplayAgent_CostFromResult(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "developer.jsonl", strings.Join([]string{
		`{"type":"finding","path":"x.go","line":1,"severity":"minor","body":"y"}`,
		`{"type":"done","exit_code":0}`,
		`{"type":"result","total_cost_usd":0.0123,"usage":{"input_tokens":10,"output_tokens":5},"modelUsage":{"claude-opus-4-7":{"inputTokens":10,"outputTokens":5,"costUSD":0.0123}}}`,
	}, "\n"))
	a := &ReplayAgent{TranscriptDir: dir}
	h, err := a.Dispatch(context.Background(), &core.Job{Role: "developer"}, "")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	res, err := h.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Cost == nil {
		t.Fatal("Cost was not populated from `result` event")
	}
	if res.Cost.Provider != "anthropic" || res.Cost.USD != 0.0123 ||
		res.Cost.Model != "claude-opus-4-7" {
		t.Errorf("Cost = %+v", res.Cost)
	}
}

func TestReplayAgent_CostFromTurnCompleted(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "developer.jsonl", strings.Join([]string{
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":50,"output_tokens":20}}`,
		`{"type":"done","exit_code":0}`,
	}, "\n"))
	a := &ReplayAgent{TranscriptDir: dir}
	h, err := a.Dispatch(context.Background(), &core.Job{Role: "developer"}, "")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	res, err := h.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Cost == nil {
		t.Fatal("Cost was not populated from `turn.completed` event")
	}
	if res.Cost.Provider != "openai" || res.Cost.USD != 0 ||
		res.Cost.TokensIn != 150 || res.Cost.TokensOut != 20 {
		t.Errorf("Cost = %+v", res.Cost)
	}
}

func TestReplayAgent_EmptyTranscript(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "developer.jsonl", "")
	a := &ReplayAgent{TranscriptDir: dir}
	h, err := a.Dispatch(context.Background(), &core.Job{Role: "developer"}, "")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	res, err := h.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(res.Findings) != 0 || res.ExitCode != 0 {
		t.Errorf("res = %+v, want empty", res)
	}
}
