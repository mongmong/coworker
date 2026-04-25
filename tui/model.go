package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/chris/coworker/core"
)

// Pane identifies one of the four dashboard panes.
type Pane int

const (
	PaneRuns Pane = iota
	PaneJobs
	PaneEvents
	PaneAttention
	paneCount // sentinel for modular cycling
)

// Config holds runtime parameters for the TUI.
type Config struct {
	// BaseURL is the HTTP base URL of the coworker daemon (e.g. "http://localhost:7700").
	BaseURL string
	// RunID, when non-empty, pre-filters all panes to one run.
	RunID string
}

// runRow is the TUI's in-memory projection of a run.
type runRow struct {
	id    string
	mode  string
	state core.RunState
	since time.Time
}

// jobRow is the TUI's in-memory projection of a job.
type jobRow struct {
	id    string
	runID string
	role  string
	state core.JobState
	cli   string
	since time.Time
}

// eventRow is a line in the events pane.
type eventRow struct {
	kind  core.EventKind
	runID string
	ts    time.Time
	raw   string // truncated payload preview
}

// attentionRow is the TUI's view of a pending attention item.
type attentionRow struct {
	id       string
	runID    string
	kind     core.AttentionKind
	question string
	options  []string
}

// CostPayload is the JSON payload of a cost.delta event.
type CostPayload struct {
	RunID      string  `json:"run_id"`
	JobID      string  `json:"job_id"`
	InputTok   int     `json:"input_tok"`
	OutputTok  int     `json:"output_tok"`
	CostUSD    float64 `json:"cost_usd"`
	Cumulative float64 `json:"cumulative_usd"`
	BudgetUSD  float64 `json:"budget_usd"`
}

// Model is the root Bubble Tea model.
type Model struct {
	cfg        Config
	activePane Pane

	// Per-pane state
	runs       []runRow
	runCursor  int
	focusRunID string // empty = all runs

	jobs      []jobRow
	jobCursor int

	events      []eventRow
	eventCursor int

	attention       []attentionRow
	attentionCursor int

	// Terminal dimensions
	width  int
	height int

	// SSE reconnection state
	sseRetryDelay time.Duration // current backoff delay; 0 = first connect

	// UI state
	showHelp bool
	err      error
	quitting bool

	// Job detail / log view state
	jobDetailMode   bool     // true when showing full-screen job detail
	jobDetailJobID  string   // ID of the job being inspected
	jobDetailLines  []string // cached log lines (tail of .coworker/runs/<run>/<job>.jsonl)
	jobDetailScroll int      // viewport scroll offset

	// Checkpoint approval state
	pendingAnswers map[string]string // itemID → submitted answer, cleared on attention.resolved

	// Cost tracking
	costByRun map[string]float64 // runID → cumulative USD
	budgetUSD float64

	// Freeform input mode for question/subprocess attention items
	inputMode   bool   // true when typing a freeform answer
	inputBuffer string // accumulated keystrokes
}

// New creates a new Model with the given configuration.
func New(cfg Config) Model {
	return Model{
		cfg:        cfg,
		activePane: PaneRuns,
	}
}

// Init fires the initial REST snapshot fetch and the SSE subscription in parallel.
// The snapshot hydrates state before the first SSE event arrives.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		fetchSnapshot(m.cfg.BaseURL, m.cfg.RunID),
		subscribeSSE(m.cfg.BaseURL, m.cfg.RunID),
	)
}

// Update processes incoming messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		return handleKey(m, msg)
	case errMsg:
		return m, m.sseBackoffCmd(msg.err)
	case retrySSEMsg:
		return m, subscribeSSE(m.cfg.BaseURL, m.cfg.RunID)
	case snapshotMsg:
		m = applySnapshot(m, msg)
		return m, nil
	case eventMsg:
		m = applyEvent(m, msg.event)
		return m, subscribeSSE(m.cfg.BaseURL, m.cfg.RunID)
	case connectedMsg:
		m.sseRetryDelay = 0
		m.err = nil
		return m, subscribeSSE(m.cfg.BaseURL, m.cfg.RunID)
	case jobLogMsg:
		m = applyJobLog(m, msg)
		return m, nil
	}
	return m, nil
}

// sseBackoffCmd records the error and returns a tea.Cmd that retries SSE after
// an exponential back-off delay (1s → 2s → 4s → max 30s).
func (m *Model) sseBackoffCmd(err error) tea.Cmd {
	m.err = err
	delay := m.sseRetryDelay
	if delay == 0 {
		delay = time.Second
	} else {
		delay *= 2
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
	}
	m.sseRetryDelay = delay
	return tea.Tick(delay, func(time.Time) tea.Msg { return retrySSEMsg{} })
}

// applySnapshot hydrates model state from the initial REST snapshot.
func applySnapshot(m Model, snap snapshotMsg) Model {
	for _, r := range snap.runs {
		found := false
		for i, row := range m.runs {
			if row.id == r.ID {
				m.runs[i].state = r.State
				m.runs[i].mode = r.Mode
				found = true
				break
			}
		}
		if !found {
			m.runs = append(m.runs, runRow{
				id: r.ID, mode: r.Mode, state: r.State, since: r.StartedAt,
			})
		}
	}
	for _, j := range snap.jobs {
		if !containsJob(m.jobs, j.ID) {
			m.jobs = append(m.jobs, jobRow{
				id: j.ID, runID: j.RunID, role: j.Role,
				state: j.State, cli: j.CLI, since: j.StartedAt,
			})
		}
	}
	for _, a := range snap.attention {
		if !containsAttention(m.attention, a.ID) {
			m.attention = append(m.attention, attentionRow{
				id: a.ID, runID: a.RunID, kind: a.Kind,
				question: a.Question, options: a.Options,
			})
		}
	}
	return m
}

func containsJob(rows []jobRow, id string) bool {
	for _, r := range rows {
		if r.id == id {
			return true
		}
	}
	return false
}

func containsAttention(rows []attentionRow, id string) bool {
	for _, r := range rows {
		if r.id == id {
			return true
		}
	}
	return false
}

// applyJobLog updates the job detail log lines and scroll position.
func applyJobLog(m Model, msg jobLogMsg) Model {
	if m.jobDetailJobID != msg.jobID {
		return m
	}
	m.jobDetailLines = msg.lines
	scroll := len(msg.lines) - m.height + 4
	if scroll < 0 {
		scroll = 0
	}
	m.jobDetailScroll = scroll
	return m
}

// View renders the full dashboard.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width < 60 || m.height < 15 {
		return "Terminal too small (min 60×15)\n"
	}
	if m.err != nil {
		return fmt.Sprintf("error: %v\nPress q to quit.\n", m.err)
	}

	// Job detail view takes over the full screen when active.
	if m.jobDetailMode {
		return renderJobDetail(m)
	}

	half := m.width / 2

	// Pane inner heights: subtract 2 for border + 1 for title row.
	topH := (m.height - 3) / 2
	botH := (m.height - 3) - topH

	topLeft := renderRunsPane(m, half, topH)
	topRight := renderJobsPane(m, half, topH)
	botLeft := renderEventsPane(m, half, botH)
	botRight := renderAttentionPane(m, half, botH)

	top := lipgloss.JoinHorizontal(lipgloss.Top, topLeft, topRight)
	bot := lipgloss.JoinHorizontal(lipgloss.Top, botLeft, botRight)
	body := lipgloss.JoinVertical(lipgloss.Left, top, bot)

	if m.showHelp {
		body = lipgloss.JoinVertical(lipgloss.Left, body, renderHelp())
	}

	return body
}

// applyEvent mutates model state based on the incoming runtime event.
func applyEvent(m Model, ev *core.Event) Model {
	// Always append to events ring (cap at 200 lines).
	row := eventRow{
		kind:  ev.Kind,
		runID: ev.RunID,
		ts:    ev.CreatedAt,
		raw:   truncate(ev.Payload, 60),
	}
	m.events = append(m.events, row)
	if len(m.events) > 200 {
		m.events = m.events[len(m.events)-200:]
	}

	switch ev.Kind {
	case core.EventRunCreated:
		m.runs = appendOrUpdateRun(m.runs, ev)
	case core.EventRunCompleted:
		m.runs = updateRunState(m.runs, ev.RunID, core.RunStateCompleted)
	case core.EventJobCreated:
		m.jobs = appendOrUpdateJob(m.jobs, ev)
	case core.EventJobLeased:
		m.jobs = updateJobState(m.jobs, ev, core.JobStateRunning)
	case core.EventJobCompleted:
		m.jobs = updateJobState(m.jobs, ev, core.JobStateComplete)
	case core.EventJobFailed:
		m.jobs = updateJobState(m.jobs, ev, core.JobStateFailed)
	case core.EventAttentionCreated:
		var item core.AttentionItem
		if err := json.Unmarshal([]byte(ev.Payload), &item); err == nil {
			m.attention = append(m.attention, attentionRow{
				id:       item.ID,
				runID:    item.RunID,
				kind:     item.Kind,
				question: item.Question,
				options:  item.Options,
			})
		}
	case core.EventAttentionResolved:
		m.attention = removeAttentionItem(m.attention, ev.CausationID)
		if m.attentionCursor >= len(m.attention) && m.attentionCursor > 0 {
			m.attentionCursor--
		}
	case core.EventCostDelta:
		var cp CostPayload
		if err := json.Unmarshal([]byte(ev.Payload), &cp); err == nil {
			if m.costByRun == nil {
				m.costByRun = make(map[string]float64)
			}
			m.costByRun[ev.RunID] = cp.Cumulative
			if cp.BudgetUSD > 0 {
				m.budgetUSD = cp.BudgetUSD
			}
		}
	}

	return m
}

// appendOrUpdateRun inserts a new run or refreshes an existing one.
func appendOrUpdateRun(rows []runRow, ev *core.Event) []runRow {
	for i, r := range rows {
		if r.id == ev.RunID {
			rows[i].state = core.RunStateActive
			return rows
		}
	}
	return append(rows, runRow{
		id:    ev.RunID,
		state: core.RunStateActive,
		since: ev.CreatedAt,
	})
}

// updateRunState marks a run as completed/failed/aborted.
func updateRunState(rows []runRow, runID string, state core.RunState) []runRow {
	for i, r := range rows {
		if r.id == runID {
			rows[i].state = state
			return rows
		}
	}
	return rows
}

// appendOrUpdateJob inserts or updates a job row.
func appendOrUpdateJob(rows []jobRow, ev *core.Event) []jobRow {
	for _, r := range rows {
		if r.id == ev.ID {
			return rows
		}
	}
	return append(rows, jobRow{
		id:    ev.ID,
		runID: ev.RunID,
		state: core.JobStatePending,
		since: ev.CreatedAt,
	})
}

// updateJobState updates the state of a job identified by the event's causation ID
// (the job ID that caused a leased/completed/failed event).
func updateJobState(rows []jobRow, ev *core.Event, state core.JobState) []jobRow {
	target := ev.CausationID
	if target == "" {
		target = ev.RunID // fallback
	}
	for i, r := range rows {
		if r.id == target {
			rows[i].state = state
			return rows
		}
	}
	return rows
}

// removeAttentionItem removes an attention item by ID.
func removeAttentionItem(rows []attentionRow, id string) []attentionRow {
	out := rows[:0]
	for _, r := range rows {
		if r.id != id {
			out = append(out, r)
		}
	}
	return out
}

// truncate shortens a string to at most n characters, appending "…" if cut.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// renderHelp returns the one-line help strip.
func renderHelp() string {
	return styleHelp.Render(
		" Tab: pane  ↑↓: scroll  Enter: focus/detail  a: approve  r: reject  p: pass  i: input  q: quit  ?: help",
	)
}
