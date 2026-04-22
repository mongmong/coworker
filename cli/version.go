package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version is overridden at build time via -ldflags
// "-X 'github.com/chris/coworker/cli.Version=<value>'".
// Defaults to a dev marker when built without ldflags.
var Version = "0.0.0-dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the coworker version.",
	Run: func(cmd *cobra.Command, _ []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "coworker %s\n", Version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
	rootCmd.Version = Version
	rootCmd.SetVersionTemplate("coworker {{.Version}}\n")
}
