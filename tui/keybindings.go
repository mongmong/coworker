package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/chris/coworker/core"
)

// handleKey is the top-level key dispatcher. It routes to one of three sub-handlers
// depending on the current UI mode, keeping each function's cyclomatic complexity low.
func handleKey(m Model, msg tea.KeyMsg) (Model, tea.Cmd) {
	key := msg.String()

	// Esc exits any special mode; Backspace exits job-detail only.
	if key == "esc" {
		return handleEscape(m)
	}
	if key == "backspace" && m.jobDetailMode {
		return exitJobDetail(m), nil
	}

	if m.jobDetailMode {
		return handleJobDetailKey(m, key)
	}
	if m.inputMode {
		return handleInputModeKey(m, msg)
	}
	return handleNormalKey(m, key)
}

// handleEscape exits job-detail or input mode, whichever is active.
func handleEscape(m Model) (Model, tea.Cmd) {
	if m.jobDetailMode {
		return exitJobDetail(m), nil
	}
	if m.inputMode {
		m.inputMode = false
		m.inputBuffer = ""
	}
	return m, nil
}

// exitJobDetail clears job-detail state without touching other fields.
func exitJobDetail(m Model) Model {
	m.jobDetailMode = false
	m.jobDetailJobID = ""
	m.jobDetailLines = nil
	return m
}

// handleJobDetailKey processes keys while in the full-screen job detail view.
func handleJobDetailKey(m Model, key string) (Model, tea.Cmd) {
	switch key {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "up", "k":
		if m.jobDetailScroll > 0 {
			m.jobDetailScroll--
		}
	case "down", "j":
		maxScroll := len(m.jobDetailLines) - 1
		if maxScroll < 0 {
			maxScroll = 0
		}
		if m.jobDetailScroll < maxScroll {
			m.jobDetailScroll++
		}
	}
	return m, nil
}

// handleInputModeKey processes keystrokes while the user is typing a freeform answer.
func handleInputModeKey(m Model, msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		return submitInputAnswer(m)
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

// submitInputAnswer finalises the freeform input and submits it.
func submitInputAnswer(m Model) (Model, tea.Cmd) {
	if len(m.attention) == 0 {
		m.inputMode = false
		return m, nil
	}
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

// handleNormalKey processes keys in the normal (non-modal) dashboard state.
func handleNormalKey(m Model, key string) (Model, tea.Cmd) {
	switch key {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "?":
		m.showHelp = !m.showHelp
	case "tab":
		m.activePane = (m.activePane + 1) % paneCount
	case "shift+tab":
		m.activePane = (m.activePane - 1 + paneCount) % paneCount
	case "up", "k":
		m = scrollUp(m)
	case "down", "j":
		m = scrollDown(m)
	case "enter":
		return handleEnterKey(m)
	case "a", "r", "p":
		return handleAttentionAnswer(m, key)
	case "i":
		return handleInputStart(m)
	}
	return m, nil
}

// handleEnterKey focuses a run or opens job detail depending on the active pane.
func handleEnterKey(m Model) (Model, tea.Cmd) {
	switch {
	case m.activePane == PaneRuns && len(m.runs) > 0:
		m.focusRunID = m.runs[m.runCursor].id
	case m.activePane == PaneJobs && len(m.jobs) > 0:
		job := m.jobs[m.jobCursor]
		m.jobDetailMode = true
		m.jobDetailJobID = job.id
		m.jobDetailScroll = 0
		return m, loadJobLog(job.runID, job.id)
	}
	return m, nil
}

// handleAttentionAnswer records a pending answer and submits it.
func handleAttentionAnswer(m Model, key string) (Model, tea.Cmd) {
	if m.activePane != PaneAttention || len(m.attention) == 0 {
		return m, nil
	}
	answerMap := map[string]string{"a": "yes", "r": "no", "p": "pass"}
	answer := answerMap[key]
	item := m.attention[m.attentionCursor]
	if m.pendingAnswers == nil {
		m.pendingAnswers = make(map[string]string)
	}
	m.pendingAnswers[item.id] = answer
	return m, submitAnswer(m.cfg.BaseURL, item.id, answer)
}

// handleInputStart enters freeform input mode for question/subprocess items.
func handleInputStart(m Model) (Model, tea.Cmd) {
	if m.activePane != PaneAttention || len(m.attention) == 0 {
		return m, nil
	}
	item := m.attention[m.attentionCursor]
	if item.kind == core.AttentionQuestion || item.kind == core.AttentionSubprocess {
		m.inputMode = true
		m.inputBuffer = ""
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
