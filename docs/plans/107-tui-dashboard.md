# Plan 107 — TUI Dashboard

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Live Bubble Tea dashboard for coworker state visualization and checkpoint approvals. Subscribes to the SSE event stream from Plan 102, renders four panes (runs, jobs, events, attention), and lets users approve or reject checkpoints without leaving the terminal.

**Architecture:** New `tui/` package with Model/Update/View, Lipgloss styles, and a `tea.Cmd` SSE subscriber. New `cli/dashboard.go` cobra command. No new DB access — the TUI reads live state from the SSE stream plus an initial REST snapshot of runs/jobs/attention from the daemon API.

**Tech Stack:** Go 1.25+, `charmbracelet/bubbletea`, `charmbracelet/lipgloss`, `charmbracelet/bubbles`, `charmbracelet/x/teatest`, `net/http` (stdlib SSE client from Plan 102's `watch.go`).

**Blocks on:** Plan 102 (SSE endpoint live), Plan 103 (attention types), Plan 104 (daemon HTTP server).

**Parallel-safe with:** 103, 104, 105, 106, 108, 109, 110.

**Branch:** `feature/plan-107-tui-dashboard`.

**Manifest entry:** `docs/specs/001-plan-manifest.md` §107.

---

## Architecture Overview

The TUI is read-mostly: it subscribes to the SSE `/events` endpoint and applies
each incoming event to its in-memory projection. It never writes to SQLite
directly. Checkpoint approvals and attention answers are submitted via HTTP POST
to the daemon's REST API (the same endpoint the MCP `orch.attention.answer` tool
calls internally).

### Layout (2x2 grid, fixed proportions)

```
┌──────────────────────┬──────────────────────┐
│  Runs                │  Jobs                │
│  [active run list]   │  [job list for run]  │
│  cost: $0.12 / $5.00 │  role  state  age    │
├──────────────────────┼──────────────────────┤
│  Events              │  Attention           │
│  run.created …       │  ◆ checkpoint        │
│  job.leased …        │  Approve phase-clean?│
│  job.completed …     │  [p]ass [r]eject     │
└──────────────────────┴──────────────────────┘
```

Active pane is highlighted with a border colour. `Tab` / `Shift+Tab` cycles panes.
Arrow keys scroll within the focused pane.

### Key bindings

| Key | Scope | Action |
|-----|-------|--------|
| `Tab` / `Shift+Tab` | Global | Cycle active pane |
| `↑` / `↓` | Any pane | Scroll / select row |
| `Enter` | Runs pane | Focus run → filter Jobs+Events to that run |
| `a` | Attention pane | Approve selected item (send "yes") |
| `r` | Attention pane | Reject selected item (send "no") |
| `p` | Attention pane | Pass / skip (send "pass") |
| `q` / `Ctrl+C` | Global | Quit |
| `?` | Global | Toggle help overlay |

### File layout after Plan 107

```
tui/
├── model.go          # Main Model struct; Init, Update, View
├── model_test.go     # teatest golden-output tests
├── views.go          # Per-pane render functions (Lipgloss)
├── keybindings.go    # Key message handlers
├── events.go         # SSE tea.Cmd + tea.Msg types
└── styles.go         # Lipgloss style definitions

cli/
└── dashboard.go      # `coworker dashboard` cobra command
```

No new packages are created outside `tui/` and `cli/`.

---

## Task 1: Bubble Tea skeleton + Lipgloss layout

**Files:**
- Create: `tui/styles.go`
- Create: `tui/model.go`
- Create: `tui/views.go`
- Create: `tui/keybindings.go`
- Create: `cli/dashboard.go`
- Modify: `go.mod` / `go.sum` (add three Charmbracelet deps)

### Step 1.1 — Add dependencies

- [ ] Run `go get github.com/charmbracelet/bubbletea@latest github.com/charmbracelet/lipgloss@latest github.com/charmbracelet/bubbles@latest`
- [ ] Verify `go.mod` lists all three as direct requires.
- [ ] Run `go mod tidy` to clean up.

### Step 1.2 — Lipgloss style definitions

- [ ] Create `tui/styles.go`:

```go
package tui

import "github.com/charmbracelet/lipgloss"

// Colour palette — calm, terminal-safe 256-colour values.
var (
    colourBorder       = lipgloss.Color("240") // dim grey
    colourBorderFocus  = lipgloss.Color("39")  // bright blue
    colourTitle        = lipgloss.Color("254") // near-white
    colourSubtle       = lipgloss.Color("243") // mid grey
    colourStateActive  = lipgloss.Color("46")  // green
    colourStateFailed  = lipgloss.Color("196") // red
    colourStateWaiting = lipgloss.Color("214") // orange
    colourCost         = lipgloss.Color("220") // yellow
    colourEventKind    = lipgloss.Color("75")  // light blue
    colourAttention    = lipgloss.Color("214") // orange
)

var (
    stylePaneBase = lipgloss.NewStyle().
            Border(lipgloss.RoundedBorder()).
            BorderForeground(colourBorder).
            Padding(0, 1)

    stylePaneFocus = stylePaneBase.
            BorderForeground(colourBorderFocus)

    styleTitle = lipgloss.NewStyle().
            Foreground(colourTitle).
            Bold(true)

    styleSubtle = lipgloss.NewStyle().
            Foreground(colourSubtle)

    styleStateActive = lipgloss.NewStyle().
            Foreground(colourStateActive)

    styleStateFailed = lipgloss.NewStyle().
            Foreground(colourStateFailed)

    styleStateWaiting = lipgloss.NewStyle().
            Foreground(colourStateWaiting)

    styleCost = lipgloss.NewStyle().
            Foreground(colourCost)

    styleEventKind = lipgloss.NewStyle().
            Foreground(colourEventKind)

    styleAttentionKind = lipgloss.NewStyle().
            Foreground(colourAttention).
            Bold(true)

    styleSelected = lipgloss.NewStyle().
            Background(lipgloss.Color("236")).
            Bold(true)

    styleHelp = lipgloss.NewStyle().
            Foreground(colourSubtle).
            Italic(true)
)

// paneStyle returns the border style for a pane depending on whether it is active.
func paneStyle(active bool) lipgloss.Style {
    if active {
        return stylePaneFocus
    }
    return stylePaneBase
}
```

### Step 1.3 — Core model types

- [ ] Create `tui/model.go`:

```go
package tui

import (
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

    attention      []attentionRow
    attentionCursor int

    // Terminal dimensions
    width  int
    height int

    // UI state
    showHelp bool
    err      error
    quitting bool
}

// New creates a new Model with the given configuration.
func New(cfg Config) Model {
    return Model{
        cfg:        cfg,
        activePane: PaneRuns,
    }
}

// Init starts the SSE subscription command.
func (m Model) Init() tea.Cmd {
    return subscribeSSE(m.cfg.BaseURL, m.cfg.RunID)
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
        m.err = msg.err
        return m, nil

    case eventMsg:
        m = applyEvent(m, msg.event)
        return m, nil

    case connectedMsg:
        // SSE connected; no state change needed.
        return m, nil
    }

    return m, nil
}

// View renders the full dashboard.
func (m Model) View() string {
    if m.quitting {
        return ""
    }
    if m.err != nil {
        return fmt.Sprintf("error: %v\nPress q to quit.\n", m.err)
    }

    half := m.width / 2

    // Pane inner heights: subtract 2 for border + 1 for title row.
    topH := (m.height - 3) / 2
    botH := (m.height - 3) - topH

    topLeft  := renderRunsPane(m, half, topH)
    topRight := renderJobsPane(m, half, topH)
    botLeft  := renderEventsPane(m, half, botH)
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
        if r.id == target || r.runID == ev.RunID {
            rows[i].state = state
            return rows
        }
    }
    return rows
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
        " Tab: pane  ↑↓: scroll  Enter: focus run  a: approve  r: reject  p: pass  q: quit  ?: help",
    )
}
```

### Step 1.4 — Keybindings handler

- [ ] Create `tui/keybindings.go`:

```go
package tui

import (
    tea "github.com/charmbracelet/bubbletea"
)

// handleKey processes keyboard input and returns the updated model + next cmd.
func handleKey(m Model, msg tea.KeyMsg) (Model, tea.Cmd) {
    switch msg.String() {
    case "q", "ctrl+c":
        m.quitting = true
        return m, tea.Quit

    case "?":
        m.showHelp = !m.showHelp
        return m, nil

    case "tab":
        m.activePane = (m.activePane + 1) % paneCount
        return m, nil

    case "shift+tab":
        m.activePane = (m.activePane - 1 + paneCount) % paneCount
        return m, nil

    case "up", "k":
        m = scrollUp(m)
        return m, nil

    case "down", "j":
        m = scrollDown(m)
        return m, nil

    case "enter":
        if m.activePane == PaneRuns && len(m.runs) > 0 {
            m.focusRunID = m.runs[m.runCursor].id
        }
        return m, nil

    case "a":
        if m.activePane == PaneAttention && len(m.attention) > 0 {
            item := m.attention[m.attentionCursor]
            return m, submitAnswer(m.cfg.BaseURL, item.id, "yes")
        }
        return m, nil

    case "r":
        if m.activePane == PaneAttention && len(m.attention) > 0 {
            item := m.attention[m.attentionCursor]
            return m, submitAnswer(m.cfg.BaseURL, item.id, "no")
        }
        return m, nil

    case "p":
        if m.activePane == PaneAttention && len(m.attention) > 0 {
            item := m.attention[m.attentionCursor]
            return m, submitAnswer(m.cfg.BaseURL, item.id, "pass")
        }
        return m, nil
    }

    return m, nil
}

// scrollUp moves the cursor up in the active pane.
func scrollUp(m Model) Model {
    switch m.activePane {
    case PaneRuns:
        if m.runCursor > 0 {
            m.runCursor--
        }
    case PaneJobs:
        if m.jobCursor > 0 {
            m.jobCursor--
        }
    case PaneEvents:
        if m.eventCursor > 0 {
            m.eventCursor--
        }
    case PaneAttention:
        if m.attentionCursor > 0 {
            m.attentionCursor--
        }
    }
    return m
}

// scrollDown moves the cursor down in the active pane.
func scrollDown(m Model) Model {
    switch m.activePane {
    case PaneRuns:
        if m.runCursor < len(m.runs)-1 {
            m.runCursor++
        }
    case PaneJobs:
        if m.jobCursor < len(m.jobs)-1 {
            m.jobCursor++
        }
    case PaneEvents:
        if m.eventCursor < len(m.events)-1 {
            m.eventCursor++
        }
    case PaneAttention:
        if m.attentionCursor < len(m.attention)-1 {
            m.attentionCursor++
        }
    }
    return m
}
```

### Step 1.5 — View functions for each pane

- [ ] Create `tui/views.go`:

```go
package tui

import (
    "fmt"
    "time"

    "github.com/charmbracelet/lipgloss"

    "github.com/chris/coworker/core"
)

// renderRunsPane renders the top-left pane.
func renderRunsPane(m Model, w, h int) string {
    inner := renderRunsContent(m, w-4, h-2) // 4 = border+padding, 2 = title line
    title := styleTitle.Render("Runs")
    content := lipgloss.JoinVertical(lipgloss.Left, title, inner)
    return paneStyle(m.activePane == PaneRuns).
        Width(w).Height(h).
        Render(content)
}

func renderRunsContent(m Model, w, h int) string {
    if len(m.runs) == 0 {
        return styleSubtle.Render("no runs yet")
    }
    var lines []string
    for i, r := range m.runs {
        var stateStyle lipgloss.Style
        switch r.state {
        case core.RunStateActive:
            stateStyle = styleStateActive
        case core.RunStateFailed, core.RunStateAborted:
            stateStyle = styleStateFailed
        default:
            stateStyle = styleSubtle
        }

        cursor := "  "
        if i == m.runCursor {
            cursor = "> "
        }
        focused := ""
        if r.id == m.focusRunID {
            focused = " *"
        }
        age := time.Since(r.since).Truncate(time.Second)
        line := fmt.Sprintf("%s%s%s  %s  %s  %s",
            cursor,
            truncate(r.id, 12),
            focused,
            r.mode,
            stateStyle.Render(string(r.state)),
            styleSubtle.Render(age.String()),
        )
        if i == m.runCursor {
            line = styleSelected.Width(w).Render(line)
        }
        lines = append(lines, line)
        if len(lines) >= h {
            break
        }
    }
    return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// renderJobsPane renders the top-right pane.
func renderJobsPane(m Model, w, h int) string {
    inner := renderJobsContent(m, w-4, h-2)
    title := styleTitle.Render("Jobs")
    content := lipgloss.JoinVertical(lipgloss.Left, title, inner)
    return paneStyle(m.activePane == PaneJobs).
        Width(w).Height(h).
        Render(content)
}

func renderJobsContent(m Model, w, h int) string {
    jobs := m.jobs
    if m.focusRunID != "" {
        filtered := jobs[:0]
        for _, j := range jobs {
            if j.runID == m.focusRunID {
                filtered = append(filtered, j)
            }
        }
        jobs = filtered
    }
    if len(jobs) == 0 {
        return styleSubtle.Render("no jobs")
    }

    var lines []string
    for i, j := range jobs {
        var stateStyle lipgloss.Style
        switch j.state {
        case core.JobStateRunning:
            stateStyle = styleStateActive
        case core.JobStateFailed, core.JobStateCancelled:
            stateStyle = styleStateFailed
        case core.JobStateDispatched:
            stateStyle = styleStateWaiting
        default:
            stateStyle = styleSubtle
        }

        cursor := "  "
        if i == m.jobCursor {
            cursor = "> "
        }
        age := time.Since(j.since).Truncate(time.Second)
        line := fmt.Sprintf("%s%-14s  %-10s  %s  %s",
            cursor,
            truncate(j.role, 14),
            stateStyle.Render(string(j.state)),
            styleSubtle.Render(j.cli),
            styleSubtle.Render(age.String()),
        )
        if i == m.jobCursor {
            line = styleSelected.Width(w).Render(line)
        }
        lines = append(lines, line)
        if len(lines) >= h {
            break
        }
    }
    return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// renderEventsPane renders the bottom-left pane.
func renderEventsPane(m Model, w, h int) string {
    inner := renderEventsContent(m, w-4, h-2)
    title := styleTitle.Render("Events")
    content := lipgloss.JoinVertical(lipgloss.Left, title, inner)
    return paneStyle(m.activePane == PaneEvents).
        Width(w).Height(h).
        Render(content)
}

func renderEventsContent(m Model, w, h int) string {
    events := m.events
    if m.focusRunID != "" {
        filtered := events[:0]
        for _, e := range events {
            if e.runID == m.focusRunID {
                filtered = append(filtered, e)
            }
        }
        events = filtered
    }
    if len(events) == 0 {
        return styleSubtle.Render("no events")
    }

    // Show most recent at the bottom; display only last h lines.
    start := 0
    if len(events) > h {
        start = len(events) - h
    }
    display := events[start:]

    var lines []string
    for i, e := range display {
        ts := e.ts.Format("15:04:05")
        kind := styleEventKind.Render(fmt.Sprintf("%-26s", string(e.kind)))
        line := fmt.Sprintf("%s  %s  %s", styleSubtle.Render(ts), kind, truncate(e.raw, w-40))
        idx := start + i
        if idx == m.eventCursor {
            line = styleSelected.Width(w).Render(line)
        }
        lines = append(lines, line)
    }
    return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// renderAttentionPane renders the bottom-right pane.
func renderAttentionPane(m Model, w, h int) string {
    inner := renderAttentionContent(m, w-4, h-2)
    var countStr string
    if n := len(m.attention); n > 0 {
        countStr = styleAttentionKind.Render(fmt.Sprintf(" (%d)", n))
    }
    title := styleTitle.Render("Attention") + countStr
    content := lipgloss.JoinVertical(lipgloss.Left, title, inner)
    return paneStyle(m.activePane == PaneAttention).
        Width(w).Height(h).
        Render(content)
}

func renderAttentionContent(m Model, w, h int) string {
    if len(m.attention) == 0 {
        return styleSubtle.Render("no pending items")
    }

    var lines []string
    for i, item := range m.attention {
        cursor := "  "
        if i == m.attentionCursor {
            cursor = "> "
        }
        kindStr := styleAttentionKind.Render(fmt.Sprintf("%-12s", string(item.kind)))
        line := fmt.Sprintf("%s%s  %s", cursor, kindStr, truncate(item.question, w-20))
        if i == m.attentionCursor {
            line = styleSelected.Width(w).Render(line)
            // Show options on the line below the selected item.
            if len(item.options) > 0 {
                optLine := styleHelp.Render(fmt.Sprintf("         options: %v", item.options))
                lines = append(lines, line, optLine)
            } else {
                lines = append(lines, line, styleHelp.Render("         [a]pprove  [r]eject  [p]ass"))
            }
        } else {
            lines = append(lines, line)
        }
        if len(lines) >= h {
            break
        }
    }
    return lipgloss.JoinVertical(lipgloss.Left, lines...)
}
```

### Step 1.6 — `coworker dashboard` cobra command

- [ ] Create `cli/dashboard.go`:

```go
package cli

import (
    "fmt"

    tea "github.com/charmbracelet/bubbletea"
    "github.com/spf13/cobra"

    "github.com/chris/coworker/tui"
)

type dashboardOptions struct {
    port  int
    runID string
}

func init() {
    rootCmd.AddCommand(newDashboardCmd())
}

func newDashboardCmd() *cobra.Command {
    opts := &dashboardOptions{}

    cmd := &cobra.Command{
        Use:   "dashboard",
        Short: "Open the live TUI dashboard.",
        Long: `Open the Bubble Tea TUI dashboard.

The dashboard subscribes to the coworker daemon's SSE event stream and renders
four panes: active runs, jobs, live events, and pending attention items.

Keyboard shortcuts:
  Tab / Shift+Tab  cycle panes
  ↑ / ↓           scroll / select within a pane
  Enter            focus the selected run (filters Jobs and Events panes)
  a                approve selected attention item
  r                reject selected attention item
  p                pass / skip selected attention item
  q / Ctrl+C       quit
  ?                toggle help`,
        RunE: func(cmd *cobra.Command, _ []string) error {
            baseURL := fmt.Sprintf("http://localhost:%d", opts.port)
            m := tui.New(tui.Config{
                BaseURL: baseURL,
                RunID:   opts.runID,
            })
            p := tea.NewProgram(m, tea.WithAltScreen())
            if _, err := p.Run(); err != nil {
                return fmt.Errorf("dashboard: %w", err)
            }
            return nil
        },
    }

    cmd.Flags().IntVar(&opts.port, "port", 7700, "Port for the local coworker daemon")
    cmd.Flags().StringVar(&opts.runID, "run", "", "Pre-filter dashboard to one run ID")

    return cmd
}
```

---

## Task 2: SSE subscription tea.Cmd + live model updates

**Files:**
- Create: `tui/events.go`
- Modify: `tui/model.go` (wire `subscribeSSE` and `submitAnswer` stubs)

### Step 2.1 — SSE subscription tea.Cmd

- [ ] Create `tui/events.go`:

```go
package tui

import (
    "bufio"
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "net/url"
    "strings"
    "time"

    tea "github.com/charmbracelet/bubbletea"

    "github.com/chris/coworker/core"
)

// connectedMsg signals that the SSE connection was established.
type connectedMsg struct{}

// eventMsg carries a single runtime event from the SSE stream.
type eventMsg struct{ event *core.Event }

// errMsg carries a non-fatal connection error.
type errMsg struct{ err error }

// subscribeSSE returns a tea.Cmd that reads from the SSE endpoint until the
// program quits. Each received event becomes an eventMsg delivered to Update.
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
        // Stream closed; reconnect after a short delay.
        time.Sleep(2 * time.Second)
        return subscribeSSE(baseURL, runID)()
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
        resp, err := http.Post(endpoint, "application/json", bytes.NewReader(body)) //nolint:noctx
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
```

### Step 2.2 — Wire re-subscription in Update

The `subscribeSSE` cmd returns exactly one message per call. After `Update`
processes an `eventMsg`, it must return `subscribeSSE` as the next `tea.Cmd`
to keep the stream open. Update `tui/model.go`'s `Update` method:

- [ ] Modify the `eventMsg` case in `Update`:

```go
case eventMsg:
    m = applyEvent(m, msg.event)
    return m, subscribeSSE(m.cfg.BaseURL, m.cfg.RunID)

case connectedMsg:
    return m, subscribeSSE(m.cfg.BaseURL, m.cfg.RunID)
```

This one-event-per-Cmd pattern is the standard Bubble Tea approach for
long-lived streams: each Cmd fires once, delivers its message, and the Update
loop schedules the next read.

### Step 2.3 — Attention events

The SSE stream does not yet emit `attention.*` events (those come in Plan 103).
For now, attention items are populated optimistically from `job.failed` events
(which may produce checkpoints), and the panel shows a placeholder when empty.
The full wiring happens once the daemon emits `attention.created` / `attention.resolved` events.

- [ ] Add `attention.*` EventKind constants to `core/event.go` if not already present:

```go
EventAttentionCreated  EventKind = "attention.created"
EventAttentionResolved EventKind = "attention.resolved"
```

- [ ] Add a case to `applyEvent` in `tui/model.go` for `EventAttentionCreated`:

```go
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
```

- [ ] Add `removeAttentionItem` helper to `tui/model.go`:

```go
func removeAttentionItem(rows []attentionRow, id string) []attentionRow {
    out := rows[:0]
    for _, r := range rows {
        if r.id != id {
            out = append(out, r)
        }
    }
    return out
}
```

---

## Task 3: Checkpoint approval UI

**Files:**
- Modify: `tui/keybindings.go` (already handles `a`/`r`/`p`; refine for checkpoint kind)
- Modify: `tui/views.go` (render checkpoint-specific prompt)
- Modify: `tui/model.go` (add `pendingAnswer` field for optimistic UI)

### Step 3.1 — Optimistic "answering…" state

When the user presses `a`, `r`, or `p`, the UI should immediately show
"answering…" next to the item rather than waiting for the SSE to confirm.

- [ ] Add `pendingAnswers map[string]string` to `Model`:

```go
type Model struct {
    // ... existing fields ...
    pendingAnswers map[string]string // itemID → submitted answer, cleared on attention.resolved
}
```

- [ ] In `handleKey` for `a`, `r`, `p`:

```go
case "a":
    if m.activePane == PaneAttention && len(m.attention) > 0 {
        item := m.attention[m.attentionCursor]
        if m.pendingAnswers == nil {
            m.pendingAnswers = make(map[string]string)
        }
        m.pendingAnswers[item.id] = "yes"
        return m, submitAnswer(m.cfg.BaseURL, item.id, "yes")
    }
```

- [ ] In `renderAttentionContent`, show "answering…" suffix when the item has a pending answer:

```go
pending := ""
if ans, ok := m.pendingAnswers[item.id]; ok {
    pending = styleStateWaiting.Render(fmt.Sprintf("  [answering: %s]", ans))
}
line := fmt.Sprintf("%s%s  %s%s", cursor, kindStr, truncate(item.question, w-30), pending)
```

### Step 3.2 — Checkpoint-specific rendering

Checkpoints (`AttentionCheckpoint`) are the most common interactive item.
They should render their options as numbered choices.

- [ ] In `renderAttentionContent`, when `item.kind == core.AttentionCheckpoint`:

```go
if item.kind == core.AttentionCheckpoint && i == m.attentionCursor && len(item.options) > 0 {
    for n, opt := range item.options {
        lines = append(lines, styleHelp.Render(fmt.Sprintf("         [%d] %s", n+1, opt)))
    }
}
```

### Step 3.3 — Tests for checkpoint flow

- [ ] In `tui/model_test.go` (Task 6), add a test that:
  1. Constructs a Model with one `AttentionCheckpoint` attention item.
  2. Sends a `tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}` via `Update`.
  3. Asserts `pendingAnswers[itemID] == "yes"`.
  4. Asserts `submitAnswer` Cmd was returned (non-nil).

---

## Task 4: Cost ledger view

**Files:**
- Modify: `tui/model.go` (add `CostEntry` type + slice)
- Modify: `tui/views.go` (render cost summary in runs pane header)
- Modify: `core/event.go` (add `cost.*` EventKind constants if absent)

### Step 4.1 — Cost event types

- [ ] Add to `core/event.go` if not present:

```go
EventCostDelta EventKind = "cost.delta"
```

- [ ] Define `CostPayload` in `tui/model.go`:

```go
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
```

### Step 4.2 — Cost state in Model

- [ ] Add to `Model`:

```go
costByRun map[string]float64 // runID → cumulative USD
budgetUSD float64
```

- [ ] In `applyEvent`, handle `EventCostDelta`:

```go
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
```

### Step 4.3 — Cost display in runs pane

- [ ] In `renderRunsContent`, after the run row, if `m.costByRun[r.id] > 0`:

```go
if cost, ok := m.costByRun[r.id]; ok && cost > 0 {
    budget := ""
    if m.budgetUSD > 0 {
        budget = fmt.Sprintf(" / $%.2f", m.budgetUSD)
    }
    costLine := styleCost.Render(fmt.Sprintf("    cost: $%.4f%s", cost, budget))
    lines = append(lines, costLine)
}
```

---

## Task 5: Attention-queue panel with answer affordance

Task 5 extends Task 3's checkpoint UI to support all four attention kinds
(`permission`, `subprocess`, `question`, `checkpoint`) with kind-appropriate
prompts and freeform text input for `question` items.

**Files:**
- Modify: `tui/model.go` (add `inputMode` + `inputBuffer` fields)
- Modify: `tui/keybindings.go` (input mode handling)
- Modify: `tui/views.go` (render text input prompt)

### Step 5.1 — Input mode for freeform answers

Some attention items (`question`, `subprocess`) need a freeform text response.

- [ ] Add to `Model`:

```go
inputMode   bool   // true when typing a freeform answer
inputBuffer string // accumulated keystrokes
```

- [ ] In `handleKey`, when `inputMode == true`, capture runes and handle Enter/Escape:

```go
if m.inputMode {
    switch msg.String() {
    case "enter":
        if len(m.attention) > 0 {
            item := m.attention[m.attentionCursor]
            answer := m.inputBuffer
            m.inputBuffer = ""
            m.inputMode = false
            if m.pendingAnswers == nil {
                m.pendingAnswers = make(map[string]string)
            }
            m.pendingAnswers[item.id] = answer
            return m, submitAnswer(m.cfg.BaseURL, item.id, answer)
        }
        m.inputMode = false
        return m, nil
    case "esc":
        m.inputMode = false
        m.inputBuffer = ""
        return m, nil
    case "backspace":
        if len(m.inputBuffer) > 0 {
            m.inputBuffer = m.inputBuffer[:len(m.inputBuffer)-1]
        }
        return m, nil
    default:
        if len(msg.Runes) > 0 {
            m.inputBuffer += string(msg.Runes)
        }
        return m, nil
    }
}
```

- [ ] Add `i` binding in normal mode to enter input mode for `question` / `subprocess` kinds:

```go
case "i":
    if m.activePane == PaneAttention && len(m.attention) > 0 {
        item := m.attention[m.attentionCursor]
        if item.kind == core.AttentionQuestion || item.kind == core.AttentionSubprocess {
            m.inputMode = true
            m.inputBuffer = ""
        }
    }
    return m, nil
```

### Step 5.2 — Render the input prompt

- [ ] In `renderAttentionContent`, when `m.inputMode && i == m.attentionCursor`:

```go
if m.inputMode && i == m.attentionCursor {
    prompt := fmt.Sprintf("  > %s_", m.inputBuffer)
    lines = append(lines, styleSelected.Width(w).Render(prompt))
}
```

### Step 5.3 — Kind-appropriate affordance rendering

- [ ] In `renderAttentionContent`, choose the hint line by kind:

```go
var hint string
switch item.kind {
case core.AttentionCheckpoint:
    hint = "[a]pprove  [r]eject  [p]pass"
case core.AttentionPermission:
    hint = "[a]llow  [r]eject"
case core.AttentionQuestion, core.AttentionSubprocess:
    hint = "[i]nput answer  [p]ass"
}
if i == m.attentionCursor && hint != "" {
    lines = append(lines, styleHelp.Render("         "+hint))
}
```

### Update global key hint list

- [ ] Update `renderHelp()` in `tui/model.go` to include `i: input answer`.

---

## Task 6: Snapshot tests (teatest golden output)

**Files:**
- Create: `tui/model_test.go`
- Create: `testdata/tui/golden_initial.txt`
- Create: `testdata/tui/golden_with_run.txt`
- Create: `testdata/tui/golden_with_attention.txt`
- Modify: `go.mod` / `go.sum` (add `github.com/charmbracelet/x/teatest`)

### Step 6.1 — Add teatest dependency

- [ ] Run `go get github.com/charmbracelet/x/teatest@latest`
- [ ] Run `go mod tidy`.

### Step 6.2 — Unit tests for Update logic (no golden needed)

These run fast and have no terminal dependency.

- [ ] Create `tui/model_test.go`:

```go
package tui

import (
    "encoding/json"
    "testing"
    "time"

    tea "github.com/charmbracelet/bubbletea"
    "github.com/chris/coworker/core"
)

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

func TestApplyEvent_JobCreatedAndCompleted(t *testing.T) {
    m := New(Config{BaseURL: "http://localhost:7700"})
    ev1 := makeEvent(core.EventJobCreated, "run-1", nil)
    ev1.CausationID = "job-1"
    m = applyEvent(m, ev1)

    if len(m.jobs) != 1 {
        t.Fatalf("expected 1 job, got %d", len(m.jobs))
    }

    ev2 := makeEvent(core.EventJobCompleted, "run-1", nil)
    ev2.CausationID = "job-1"
    m = applyEvent(m, ev2)
    if m.jobs[0].state != core.JobStateComplete {
        t.Errorf("expected complete, got %s", m.jobs[0].state)
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

    ev2 := makeEvent(core.EventAttentionResolved, "run-1", nil)
    ev2.CausationID = "att-1"
    m = applyEvent(m, ev2)
    if len(m.attention) != 0 {
        t.Errorf("expected 0 attention items after resolve, got %d", len(m.attention))
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

func TestScrollBounds(t *testing.T) {
    m := New(Config{BaseURL: "http://localhost:7700"})
    // scroll up on empty list should not panic
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
}
```

Note: `TestCheckpointPendingAnswer` uses a local import alias to avoid circular
dependency. In the actual file, use the import path directly; do not use an alias.

### Step 6.3 — teatest golden-output test

teatest drives a real Bubble Tea program headlessly and captures its output.
These tests guard against layout regressions.

- [ ] Add to `tui/model_test.go`:

```go
func TestGoldenInitialView(t *testing.T) {
    m := New(Config{BaseURL: "http://localhost:7700"})
    // Override Init to return nil (no SSE connection in test).
    p := teatest.NewTestProgram(t, m, teatest.WithInitialTermSize(120, 30))
    teatest.WaitFor(t, p.Output(), func(bts []byte) bool {
        return bytes.Contains(bts, []byte("Runs"))
    }, teatest.WithDuration(time.Second))
    p.Quit()
    p.WaitFinished(t, teatest.WithFinalTimeout(time.Second))
    out := p.FinalOutput(t)
    golden.RequireEqual(t, out)
}
```

- [ ] The `golden.RequireEqual` helper can use `charmbracelet/x/exp/teatest`'s
  built-in golden file support, or a simple custom helper:

```go
// goldenAssert writes or compares a golden file.
// Set UPDATE_GOLDEN=1 to regenerate.
func goldenAssert(t *testing.T, name string, got []byte) {
    t.Helper()
    path := filepath.Join("..", "testdata", "tui", name)
    if os.Getenv("UPDATE_GOLDEN") == "1" {
        _ = os.MkdirAll(filepath.Dir(path), 0o755)
        if err := os.WriteFile(path, got, 0o644); err != nil {
            t.Fatalf("write golden: %v", err)
        }
        return
    }
    want, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("read golden %s: %v (run with UPDATE_GOLDEN=1 to create)", path, err)
    }
    if !bytes.Equal(want, got) {
        t.Errorf("golden mismatch for %s\ndiff:\n%s", name,
            cmp.Diff(string(want), string(got)))
    }
}
```

- [ ] Create stub golden files (empty) so `go test ./...` passes before
  `UPDATE_GOLDEN=1` has been run; or gate with `t.Skip` if file is absent.

### Step 6.4 — Makefile target

- [ ] Add to `Makefile`:

```makefile
.PHONY: golden-update
golden-update:
    UPDATE_GOLDEN=1 go test ./tui/... -run TestGolden -count=1
```

---

## Acceptance Criteria

- [ ] `go build ./...` succeeds with the three Charmbracelet deps present.
- [ ] `go test ./tui/...` passes: all unit tests green, golden files stable.
- [ ] `go test ./cli/...` passes: dashboard command registered, no import cycle.
- [ ] `golangci-lint run ./tui/... ./cli/dashboard.go` passes with zero findings.
- [ ] Manual smoke: `coworker dashboard --port 7700` renders the four-pane layout,
  Tab cycles panes, `q` quits cleanly.
- [ ] `coworker dashboard --help` shows keybinding documentation.

---

## Post-Execution Report

_To be filled in after implementation._

---

## Code Review

_To be filled in before PR is raised._
