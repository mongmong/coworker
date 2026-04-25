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

		// Cost line under the run row (suppressed when no data).
		if cost, ok := m.costByRun[r.id]; ok && cost > 0 {
			budget := ""
			if m.budgetUSD > 0 {
				budget = fmt.Sprintf(" / $%.2f", m.budgetUSD)
			}
			costLine := styleCost.Render(fmt.Sprintf("    cost: $%.4f%s", cost, budget))
			lines = append(lines, costLine)
		}

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
		filtered := make([]jobRow, 0, len(jobs))
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
		filtered := make([]eventRow, 0, len(events))
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
		payloadWidth := w - 40
		if payloadWidth < 0 {
			payloadWidth = 0
		}
		line := fmt.Sprintf("%s  %s  %s", styleSubtle.Render(ts), kind, truncate(e.raw, payloadWidth))
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

		// Show pending answer if submitted.
		pending := ""
		if ans, ok := m.pendingAnswers[item.id]; ok {
			pending = styleStateWaiting.Render(fmt.Sprintf("  [answering: %s]", ans))
		}

		line := fmt.Sprintf("%s%s  %s%s", cursor, kindStr, truncate(item.question, w-30), pending)

		if i == m.attentionCursor {
			line = styleSelected.Width(w).Render(line)

			// Show freeform input prompt when in input mode.
			if m.inputMode {
				prompt := fmt.Sprintf("  > %s_", m.inputBuffer)
				lines = append(lines, line)
				lines = append(lines, styleSelected.Width(w).Render(prompt))
			} else {
				lines = append(lines, line)

				// Show checkpoint-specific numbered options.
				if item.kind == core.AttentionCheckpoint && len(item.options) > 0 {
					for n, opt := range item.options {
						lines = append(lines, styleHelp.Render(fmt.Sprintf("         [%d] %s", n+1, opt)))
					}
				}

				// Kind-appropriate affordance hint.
				var hint string
				switch item.kind {
				case core.AttentionCheckpoint:
					hint = "[a]pprove  [r]eject  [p]pass"
				case core.AttentionPermission:
					hint = "[a]llow  [r]eject"
				case core.AttentionQuestion, core.AttentionSubprocess:
					hint = "[i]nput answer  [p]ass"
				}
				if hint != "" {
					lines = append(lines, styleHelp.Render("         "+hint))
				}
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
