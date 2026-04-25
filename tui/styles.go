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
