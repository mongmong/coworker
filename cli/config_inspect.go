package cli

import (
	"fmt"
	"os"
	"path/filepath"

	policypkg "github.com/chris/coworker/coding/policy"
	"github.com/spf13/cobra"
)

var configInspectCmd = &cobra.Command{
	Use:   "config inspect",
	Short: "Show the effective configuration with source annotations.",
	Long: `Print the effective configuration, showing where each setting
came from (built-in defaults, global config, or repo config).

Example:
  coworker config inspect`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigInspect(cmd)
	},
}

func init() {
	rootCmd.AddCommand(configInspectCmd)
}

func runConfigInspect(cmd *cobra.Command) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve HOME directory: %w", err)
	}

	loader := &policypkg.PolicyLoader{
		GlobalConfigPath: filepath.Join(home, ".config", "coworker", "policy.yaml"),
		RepoConfigPath:   filepath.Join(".coworker", "policy.yaml"),
	}

	p, err := loader.LoadPolicy()
	if err != nil {
		return fmt.Errorf("load policy: %w", err)
	}

	output := policypkg.InspectString(p)
	_, err = fmt.Fprint(cmd.OutOrStdout(), output)
	return err
}
