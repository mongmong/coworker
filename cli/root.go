// Package cli contains cobra command definitions for the coworker binary.
// Subpackages are avoided at this stage to keep the command surface
// discoverable; split when the command set grows unwieldy.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// rootCmd is the coworker binary's root command. Subcommands register
// themselves via init() in their own files.
var rootCmd = &cobra.Command{
	Use:           "coworker",
	Short:         "Local-first runtime that coordinates CLI coding agents as role-typed workers.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command. Called from cmd/coworker/main.go.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "coworker:", err)
		os.Exit(1)
	}
}
