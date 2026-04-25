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
                   or open job detail view when Jobs pane is active
  a                approve selected attention item
  r                reject selected attention item
  p                pass / skip selected attention item
  i                input freeform answer for question/subprocess items
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
