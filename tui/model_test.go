package tui

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/chris/coworker/core"
)

// makeEvent creates a synthetic *core.Event for testing.
func makeEvent(kind core.EventKind, runID string, payload any) *core.Event {
	p, _ := json.Marshal(payload)
	return &core.Event{
		ID:        "evt-1",
		RunID:     runID,
		Kind:      kind,
		Payload:   string(p),
		CreatedAt: time.Now(),
	}
}

// ---------------------------------------------------------------------------
// applyEvent unit tests
// ---------------------------------------------------------------------------

func TestApplyEvent_RunCreated(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	ev := makeEvent(core.EventRunCreated, "run-abc", map[string]string{"mode": "autopilot"})
	m2 := applyEvent(m, ev)

	if len(m2.runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(m2.runs))
	}
	if m2.runs[0].id != "run-abc" {
		t.Errorf("unexpected run ID: %s", m2.runs[0].id)
	}
	if m2.runs[0].state != core.RunStateActive {
		t.Errorf("unexpected state: %s", m2.runs[0].state)
	}
	if len(m2.events) != 1 {
		t.Errorf("expected 1 event row, got %d", len(m2.events))
	}
}

func TestApplyEvent_RunCompleted(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	ev1 := makeEvent(core.EventRunCreated, "run-1", nil)
	m = applyEvent(m, ev1)

	ev2 := makeEvent(core.EventRunCompleted, "run-1", nil)
	m = applyEvent(m, ev2)

	if m.runs[0].state != core.RunStateCompleted {
		t.Errorf("expected completed, got %s", m.runs[0].state)
	}
}

func TestApplyEvent_JobCreatedAndCompleted(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})

	ev1 := makeEvent(core.EventJobCreated, "run-1", nil)
	ev1.ID = "job-1"
	m = applyEvent(m, ev1)

	if len(m.jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(m.jobs))
	}
	if m.jobs[0].id != "job-1" {
		t.Errorf("unexpected job ID: %s", m.jobs[0].id)
	}

	ev2 := makeEvent(core.EventJobCompleted, "run-1", nil)
	ev2.CausationID = "job-1"
	m = applyEvent(m, ev2)

	if m.jobs[0].state != core.JobStateComplete {
		t.Errorf("expected complete, got %s", m.jobs[0].state)
	}
}

func TestApplyEvent_JobLeased(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	ev1 := makeEvent(core.EventJobCreated, "run-1", nil)
	ev1.ID = "job-2"
	m = applyEvent(m, ev1)

	ev2 := makeEvent(core.EventJobLeased, "run-1", nil)
	ev2.CausationID = "job-2"
	m = applyEvent(m, ev2)

	if m.jobs[0].state != core.JobStateRunning {
		t.Errorf("expected running, got %s", m.jobs[0].state)
	}
}

func TestApplyEvent_AttentionCreatedAndResolved(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	item := core.AttentionItem{
		ID:       "att-1",
		RunID:    "run-1",
		Kind:     core.AttentionCheckpoint,
		Question: "approve phase-clean?",
	}
	ev := makeEvent(core.EventAttentionCreated, "run-1", item)
	ev.CausationID = "att-1"
	m = applyEvent(m, ev)

	if len(m.attention) != 1 {
		t.Fatalf("expected 1 attention item, got %d", len(m.attention))
	}
	if m.attention[0].id != "att-1" {
		t.Errorf("unexpected attention ID: %s", m.attention[0].id)
	}
	if m.attention[0].question != "approve phase-clean?" {
		t.Errorf("unexpected question: %s", m.attention[0].question)
	}

	ev2 := makeEvent(core.EventAttentionResolved, "run-1", nil)
	ev2.CausationID = "att-1"
	m = applyEvent(m, ev2)

	if len(m.attention) != 0 {
		t.Errorf("expected 0 attention items after resolve, got %d", len(m.attention))
	}
}

func TestApplyEvent_AttentionCursorClamped(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	// Add two items
	for _, id := range []string{"att-1", "att-2"} {
		item := core.AttentionItem{ID: id, RunID: "run-1", Kind: core.AttentionCheckpoint}
		ev := makeEvent(core.EventAttentionCreated, "run-1", item)
		ev.CausationID = id
		m = applyEvent(m, ev)
	}
	// Move cursor to second item
	m.attentionCursor = 1

	// Resolve second item
	ev := makeEvent(core.EventAttentionResolved, "run-1", nil)
	ev.CausationID = "att-2"
	m = applyEvent(m, ev)

	if m.attentionCursor != 0 {
		t.Errorf("expected cursor clamped to 0, got %d", m.attentionCursor)
	}
}

func TestApplyEvent_CostDelta(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	cp := CostPayload{
		RunID:      "run-1",
		Cumulative: 0.0042,
		BudgetUSD:  5.0,
	}
	ev := makeEvent(core.EventCostDelta, "run-1", cp)
	m = applyEvent(m, ev)

	if m.costByRun["run-1"] != 0.0042 {
		t.Errorf("unexpected cost: %f", m.costByRun["run-1"])
	}
	if m.budgetUSD != 5.0 {
		t.Errorf("unexpected budget: %f", m.budgetUSD)
	}
}

func TestEventRingCap(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	for i := 0; i < 250; i++ {
		ev := makeEvent(core.EventRunCreated, "run-x", nil)
		m = applyEvent(m, ev)
	}
	if len(m.events) > 200 {
		t.Errorf("event ring exceeded 200: got %d", len(m.events))
	}
}

// ---------------------------------------------------------------------------
// Scroll / pane navigation tests
// ---------------------------------------------------------------------------

func TestScrollBounds(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	// Scroll on empty list should not panic.
	m = scrollUp(m)
	m = scrollDown(m)

	ev := makeEvent(core.EventRunCreated, "run-1", nil)
	m = applyEvent(m, ev)
	m.runCursor = 0
	m = scrollDown(m)
	if m.runCursor != 0 {
		t.Errorf("cursor exceeded list length: %d", m.runCursor)
	}
	m = scrollUp(m)
	if m.runCursor != 0 {
		t.Errorf("cursor went negative: %d", m.runCursor)
	}
}

func TestScrollAllPanes(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})

	// Add one entry to each pane's list.
	runEv := makeEvent(core.EventRunCreated, "run-1", nil)
	m = applyEvent(m, runEv)

	jobEv := makeEvent(core.EventJobCreated, "run-1", nil)
	jobEv.ID = "job-1"
	m = applyEvent(m, jobEv)

	for _, pane := range []Pane{PaneRuns, PaneJobs, PaneEvents, PaneAttention} {
		m.activePane = pane
		// scrollUp on cursor 0 must not go negative.
		m = scrollUp(m)
		switch pane {
		case PaneRuns:
			if m.runCursor < 0 {
				t.Errorf("PaneRuns cursor went negative")
			}
		case PaneJobs:
			if m.jobCursor < 0 {
				t.Errorf("PaneJobs cursor went negative")
			}
		case PaneEvents:
			if m.eventCursor < 0 {
				t.Errorf("PaneEvents cursor went negative")
			}
		case PaneAttention:
			if m.attentionCursor < 0 {
				t.Errorf("PaneAttention cursor went negative")
			}
		}
	}
}

func TestKeyTabCyclesPanes(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	if m.activePane != PaneRuns {
		t.Fatalf("initial pane should be PaneRuns")
	}
	msg := tea.KeyMsg{Type: tea.KeyTab}
	updated, _ := m.Update(msg)
	m2 := updated.(Model)
	if m2.activePane != PaneJobs {
		t.Errorf("expected PaneJobs after Tab, got %d", m2.activePane)
	}
	// Cycling all the way around should return to PaneRuns.
	for i := 0; i < int(paneCount)-1; i++ {
		updated, _ = m2.Update(msg)
		m2 = updated.(Model)
	}
	if m2.activePane != PaneRuns {
		t.Errorf("expected PaneRuns after full cycle, got %d", m2.activePane)
	}
}

func TestKeyShiftTabCyclesPanesBackward(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	msg := tea.KeyMsg{Type: tea.KeyShiftTab}
	updated, _ := m.Update(msg)
	m2 := updated.(Model)
	if m2.activePane != PaneAttention {
		t.Errorf("expected PaneAttention after Shift+Tab from PaneRuns, got %d", m2.activePane)
	}
}

// ---------------------------------------------------------------------------
// Checkpoint approval tests
// ---------------------------------------------------------------------------

func TestCheckpointPendingAnswer(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.attention = []attentionRow{{
		id:       "att-1",
		kind:     core.AttentionCheckpoint,
		question: "approve?",
	}}
	m.activePane = PaneAttention

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}
	updated, cmd := m.Update(msg)
	m2 := updated.(Model)

	if m2.pendingAnswers["att-1"] != "yes" {
		t.Errorf("expected pendingAnswers[att-1]=yes, got %q", m2.pendingAnswers["att-1"])
	}
	if cmd == nil {
		t.Error("expected a non-nil cmd for submitAnswer")
	}
}

func TestCheckpointRejectAnswer(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.attention = []attentionRow{{
		id:       "att-2",
		kind:     core.AttentionCheckpoint,
		question: "approve?",
	}}
	m.activePane = PaneAttention

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}
	updated, cmd := m.Update(msg)
	m2 := updated.(Model)

	if m2.pendingAnswers["att-2"] != "no" {
		t.Errorf("expected pendingAnswers[att-2]=no, got %q", m2.pendingAnswers["att-2"])
	}
	if cmd == nil {
		t.Error("expected a non-nil cmd for submitAnswer")
	}
}

func TestCheckpointPassAnswer(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.attention = []attentionRow{{
		id:       "att-3",
		kind:     core.AttentionCheckpoint,
		question: "approve?",
	}}
	m.activePane = PaneAttention

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")}
	updated, cmd := m.Update(msg)
	m2 := updated.(Model)

	if m2.pendingAnswers["att-3"] != "pass" {
		t.Errorf("expected pendingAnswers[att-3]=pass, got %q", m2.pendingAnswers["att-3"])
	}
	if cmd == nil {
		t.Error("expected a non-nil cmd for submitAnswer")
	}
}

func TestNoAnswerWithoutAttentionItems(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.activePane = PaneAttention
	// No attention items; pressing a/r/p should return nil cmd.
	for _, key := range []string{"a", "r", "p"} {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
		_, cmd := m.Update(msg)
		if cmd != nil {
			t.Errorf("expected nil cmd for key %q with no attention items, got non-nil", key)
		}
	}
}

// ---------------------------------------------------------------------------
// Job detail tests
// ---------------------------------------------------------------------------

func TestJobDetailEnter(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.jobs = []jobRow{{
		id:    "job-1",
		runID: "run-1",
		role:  "coder",
		state: core.JobStateRunning,
	}}
	m.activePane = PaneJobs

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	updated, cmd := m.Update(msg)
	m2 := updated.(Model)

	if !m2.jobDetailMode {
		t.Error("expected jobDetailMode=true after Enter on Jobs pane")
	}
	if m2.jobDetailJobID != "job-1" {
		t.Errorf("unexpected jobDetailJobID: %s", m2.jobDetailJobID)
	}
	if cmd == nil {
		t.Error("expected non-nil cmd (loadJobLog)")
	}
}

func TestJobDetailEsc(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.jobDetailMode = true
	m.jobDetailJobID = "job-1"

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	updated, _ := m.Update(msg)
	m2 := updated.(Model)

	if m2.jobDetailMode {
		t.Error("expected jobDetailMode=false after Esc")
	}
	if m2.jobDetailJobID != "" {
		t.Errorf("expected jobDetailJobID empty, got %s", m2.jobDetailJobID)
	}
}

func TestJobDetailBackspace(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.jobDetailMode = true
	m.jobDetailJobID = "job-1"

	msg := tea.KeyMsg{Type: tea.KeyBackspace}
	updated, _ := m.Update(msg)
	m2 := updated.(Model)

	if m2.jobDetailMode {
		t.Error("expected jobDetailMode=false after Backspace")
	}
}

// ---------------------------------------------------------------------------
// Terminal size guard
// ---------------------------------------------------------------------------

func TestSmallTerminalView(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.width = 40
	m.height = 10

	out := m.View()
	if out != "Terminal too small (min 60×15)\n" {
		t.Errorf("unexpected small-terminal view: %q", out)
	}
}

func TestQuitView(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.quitting = true
	if out := m.View(); out != "" {
		t.Errorf("expected empty view when quitting, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// Input mode tests
// ---------------------------------------------------------------------------

func TestInputModeEnterAndSubmit(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.attention = []attentionRow{{
		id:   "att-q",
		kind: core.AttentionQuestion,
	}}
	m.activePane = PaneAttention

	// Enter input mode with "i".
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m2 := updated.(Model)
	if !m2.inputMode {
		t.Fatal("expected inputMode=true after 'i'")
	}

	// Type "hello".
	for _, ch := range "hello" {
		updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		m2 = updated.(Model)
	}
	if m2.inputBuffer != "hello" {
		t.Errorf("unexpected inputBuffer: %q", m2.inputBuffer)
	}

	// Press Enter to submit.
	updated, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m3 := updated.(Model)
	if m3.inputMode {
		t.Error("expected inputMode=false after Enter")
	}
	if m3.inputBuffer != "" {
		t.Errorf("expected empty buffer after Enter, got %q", m3.inputBuffer)
	}
	if m3.pendingAnswers["att-q"] != "hello" {
		t.Errorf("expected pendingAnswers[att-q]=hello, got %q", m3.pendingAnswers["att-q"])
	}
	if cmd == nil {
		t.Error("expected non-nil submitAnswer cmd")
	}
}

func TestInputModeEscapeClears(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.attention = []attentionRow{{
		id:   "att-q",
		kind: core.AttentionQuestion,
	}}
	m.activePane = PaneAttention
	m.inputMode = true
	m.inputBuffer = "partial"

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := updated.(Model)
	if m2.inputMode {
		t.Error("expected inputMode=false after Esc")
	}
	if m2.inputBuffer != "" {
		t.Errorf("expected empty buffer after Esc, got %q", m2.inputBuffer)
	}
}

func TestInputModeBackspace(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.attention = []attentionRow{{id: "att-q", kind: core.AttentionQuestion}}
	m.activePane = PaneAttention
	m.inputMode = true
	m.inputBuffer = "ab"

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m2 := updated.(Model)
	if m2.inputBuffer != "a" {
		t.Errorf("expected inputBuffer=a after backspace, got %q", m2.inputBuffer)
	}
}

// ---------------------------------------------------------------------------
// Help toggle
// ---------------------------------------------------------------------------

func TestHelpToggle(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	if m.showHelp {
		t.Fatal("showHelp should be false initially")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	m2 := updated.(Model)
	if !m2.showHelp {
		t.Error("expected showHelp=true after '?'")
	}
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	m3 := updated.(Model)
	if m3.showHelp {
		t.Error("expected showHelp=false after second '?'")
	}
}

// ---------------------------------------------------------------------------
// View rendering smoke tests (no terminal required)
// ---------------------------------------------------------------------------

func TestViewWithSufficientTerminal(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.width = 120
	m.height = 30

	out := m.View()
	// With sufficient terminal, should not show size-guard message.
	if out == "Terminal too small (min 60×15)\n" {
		t.Error("unexpected small-terminal view with large terminal")
	}
	// Should contain pane titles.
	for _, want := range []string{"Runs", "Jobs", "Events", "Attention"} {
		if !bytes.Contains(stripANSI([]byte(out)), []byte(want)) {
			t.Errorf("expected %q in view output", want)
		}
	}
}

func TestViewWithRun(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.width = 120
	m.height = 30
	ev := makeEvent(core.EventRunCreated, "run-abc", nil)
	m = applyEvent(m, ev)

	out := m.View()
	if !bytes.Contains(stripANSI([]byte(out)), []byte("run-abc")) {
		t.Errorf("expected run-abc in view output")
	}
}

func TestViewWithAttention(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.width = 120
	m.height = 30
	item := core.AttentionItem{
		ID:       "att-1",
		RunID:    "run-1",
		Kind:     core.AttentionCheckpoint,
		Question: "approve-this?",
	}
	ev := makeEvent(core.EventAttentionCreated, "run-1", item)
	ev.CausationID = "att-1"
	m = applyEvent(m, ev)

	out := m.View()
	stripped := stripANSI([]byte(out))
	// With w=120 each pane is 60 wide; w-4=56, w-30=26, so "approve-this?" (13 chars) fits.
	if !bytes.Contains(stripped, []byte("approve-this")) {
		t.Errorf("expected attention question prefix in view output, got:\n%s", stripped)
	}
}

// ---------------------------------------------------------------------------
// Golden output tests (fragment-based, ANSI-stripped)
// ---------------------------------------------------------------------------

// ansiEscape matches ANSI colour/style escape sequences.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// stripANSI removes ANSI escape codes from b.
func stripANSI(b []byte) []byte {
	return ansiEscape.ReplaceAll(b, nil)
}

// goldenAssert writes or compares a golden file.
// Set UPDATE_GOLDEN=1 to regenerate.
func goldenAssert(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("..", "testdata", "tui", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		// If the golden file doesn't exist, skip rather than fail.
		t.Skipf("golden file %s not found; run with UPDATE_GOLDEN=1 to create", path)
		return
	}
	if !bytes.Equal(want, got) {
		t.Errorf("golden mismatch for %s\nwant:\n%s\ngot:\n%s", name, want, got)
	}
}

// TestGoldenInitialView verifies that the empty dashboard renders all four pane titles.
// Golden file comparison is skipped when the golden file is absent or contains
// time-dependent data; use UPDATE_GOLDEN=1 to regenerate.
func TestGoldenInitialView(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	// Use fixed 80×24 so rendering is deterministic in CI.
	m.width = 80
	m.height = 24

	out := stripANSI([]byte(m.View()))
	// Fragment assertions: verify structural content without time-sensitive data.
	for _, want := range []string{"Runs", "Jobs", "Events", "Attention", "no runs yet", "no jobs", "no events"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("expected %q in initial view output:\n%s", want, out)
		}
	}
	// Golden file comparison (skipped if file absent).
	goldenAssert(t, "golden_initial.txt", out)
}

// TestGoldenViewWithRun verifies that a run ID appears in the Runs pane.
func TestGoldenViewWithRun(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.width = 80
	m.height = 24
	ev := makeEvent(core.EventRunCreated, "run-abc", nil)
	// Use a fixed timestamp to avoid golden-file churn from time.Now().
	ev.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m = applyEvent(m, ev)

	out := stripANSI([]byte(m.View()))
	if !bytes.Contains(out, []byte("run-abc")) {
		t.Errorf("expected run-abc in view output:\n%s", out)
	}
	if !bytes.Contains(out, []byte("active")) {
		t.Errorf("expected 'active' state in view output:\n%s", out)
	}
}

// TestGoldenViewWithAttention verifies that a checkpoint item appears in the Attention pane.
func TestGoldenViewWithAttention(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.width = 80
	m.height = 24
	item := core.AttentionItem{
		ID:       "att-1",
		RunID:    "run-1",
		Kind:     core.AttentionCheckpoint,
		Question: "approve-phase-clean",
	}
	ev := makeEvent(core.EventAttentionCreated, "run-1", item)
	ev.CausationID = "att-1"
	m = applyEvent(m, ev)

	out := stripANSI([]byte(m.View()))
	// The question is truncated in the 80-wide layout — at least "approv" should appear.
	if !bytes.Contains(out, []byte("approv")) {
		t.Errorf("expected 'approv' (from approve-phase-clean) in view output:\n%s", out)
	}
	// The checkpoint kind and action hints should appear.
	if !bytes.Contains(out, []byte("checkpoint")) {
		t.Errorf("expected 'checkpoint' kind in view output:\n%s", out)
	}
	if !bytes.Contains(out, []byte("[a]pprove")) {
		t.Errorf("expected '[a]pprove' hint in view output:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Snapshot hydration tests
// ---------------------------------------------------------------------------

func TestSnapshotMsg_HydratesRuns(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	snap := snapshotMsg{
		runs: []core.Run{
			{ID: "snap-run-1", Mode: "autopilot", State: core.RunStateActive, StartedAt: time.Now()},
		},
	}
	updated, _ := m.Update(snap)
	m2 := updated.(Model)

	if len(m2.runs) != 1 {
		t.Fatalf("expected 1 run after snapshot, got %d", len(m2.runs))
	}
	if m2.runs[0].id != "snap-run-1" {
		t.Errorf("unexpected run ID: %s", m2.runs[0].id)
	}
}

func TestSnapshotMsg_DeduplicatesRuns(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	// Pre-populate a run via event.
	ev := makeEvent(core.EventRunCreated, "snap-run-1", nil)
	m = applyEvent(m, ev)

	snap := snapshotMsg{
		runs: []core.Run{
			{ID: "snap-run-1", Mode: "autopilot", State: core.RunStateActive, StartedAt: time.Now()},
		},
	}
	updated, _ := m.Update(snap)
	m2 := updated.(Model)

	// Should not duplicate.
	if len(m2.runs) != 1 {
		t.Errorf("expected 1 run (no duplicate), got %d", len(m2.runs))
	}
}

// ---------------------------------------------------------------------------
// Focus run filter test
// ---------------------------------------------------------------------------

func TestFocusRunFiltersJobs(t *testing.T) {
	m := New(Config{BaseURL: "http://localhost:7700"})
	m.width = 120
	m.height = 30
	m.runs = []runRow{{id: "run-A", state: core.RunStateActive}}
	m.jobs = []jobRow{
		{id: "job-1", runID: "run-A", role: "coder", state: core.JobStateRunning},
		{id: "job-2", runID: "run-B", role: "reviewer", state: core.JobStatePending},
	}
	m.focusRunID = "run-A"

	out := stripANSI([]byte(m.View()))
	if !bytes.Contains(out, []byte("coder")) {
		t.Error("expected coder job to appear when focusRunID=run-A")
	}
	if bytes.Contains(out, []byte("reviewer")) {
		t.Error("expected reviewer job to be filtered when focusRunID=run-A")
	}
}

// ---------------------------------------------------------------------------
// truncate helper test
// ---------------------------------------------------------------------------

func TestTruncate(t *testing.T) {
	cases := []struct {
		in  string
		n   int
		out string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello…"},
		{"line\nnewline", 20, "line newline"},
	}
	for _, c := range cases {
		got := truncate(c.in, c.n)
		if got != c.out {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.out)
		}
	}
}
