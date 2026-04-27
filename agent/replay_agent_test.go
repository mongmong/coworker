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
	// Some findings may be present; what matters is that Wait returned promptly.
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
	if res.Stderr == "" {
		t.Error("stderr should mention parse error")
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
