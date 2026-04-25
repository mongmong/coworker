package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderJobDetail renders the full-screen job detail view.
func renderJobDetail(m Model) string {
	if len(m.jobs) == 0 || m.jobDetailJobID == "" {
		return "No job selected. Press Esc to return.\n"
	}

	// Find the job row.
	var job *jobRow
	for i := range m.jobs {
		if m.jobs[i].id == m.jobDetailJobID {
			job = &m.jobs[i]
			break
		}
	}
	if job == nil {
		return "Job not found. Press Esc to return.\n"
	}

	width := m.width
	if width < 1 {
		width = 80
	}

	header := styleTitle.Render(fmt.Sprintf("Job Detail: %s", job.id)) + "\n" +
		styleSubtle.Render(fmt.Sprintf("Run: %s  Role: %s  State: %s  CLI: %s",
			job.runID, job.role, string(job.state), job.cli)) + "\n" +
		strings.Repeat("─", width) + "\n"

	// Render log lines with viewport scrolling.
	lines := m.jobDetailLines
	maxVisible := m.height - 6 // header (3) + footer (2) + border
	if maxVisible < 1 {
		maxVisible = 1
	}
	start := m.jobDetailScroll
	if start < 0 {
		start = 0
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := start + maxVisible
	if end > len(lines) {
		end = len(lines)
	}
	visible := lines[start:end]

	var logBody string
	if len(lines) == 0 {
		logBody = styleSubtle.Render("(no log lines yet)")
	} else {
		logBody = lipgloss.JoinVertical(lipgloss.Left, visible...)
	}

	startDisp := start + 1
	if len(lines) == 0 {
		startDisp = 0
	}
	footer := strings.Repeat("─", width) + "\n" +
		styleHelp.Render(fmt.Sprintf(" ↑/↓: scroll  Esc/Backspace: return  [%d-%d of %d]",
			startDisp, end, len(lines)))

	return header + logBody + "\n" + footer
}
