package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var pluginCLI string

var pluginCmd = &cobra.Command{
	Use:   "plugin",
	Short: "Manage coworker CLI plugins.",
}

var pluginInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install a coworker plugin into the current project.",
	Long: `Install a coworker plugin into the current project.

Copies the plugin files from the coworker installation into the project's CLI
plugin directory and merges the MCP server configuration into .mcp.json.

Supported CLIs:
  claude   — installs into .claude/plugins/coworker/

Example:
  coworker plugin install --cli claude`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runPluginInstall(cmd)
	},
}

func init() {
	pluginInstallCmd.Flags().StringVar(&pluginCLI, "cli", "claude", "Target CLI to install the plugin for (default: claude)")
	pluginCmd.AddCommand(pluginInstallCmd)
	rootCmd.AddCommand(pluginCmd)
}

func runPluginInstall(cmd *cobra.Command) error {
	switch pluginCLI {
	case "claude":
		return installClaudePlugin(cmd)
	default:
		return fmt.Errorf("unsupported CLI %q: only 'claude' is supported in this release", pluginCLI)
	}
}

// installClaudePlugin copies plugins/coworker-claude/ into .claude/plugins/coworker/
// and merges the MCP server entry into .mcp.json at the project root.
func installClaudePlugin(cmd *cobra.Command) error {
	// Locate the source plugin directory relative to the coworker binary.
	// For development, fall back to looking in common locations.
	srcDir, err := findPluginSource("coworker-claude")
	if err != nil {
		return fmt.Errorf("locate plugin source: %w", err)
	}

	// Destination: .claude/plugins/coworker/ in the current working directory.
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	destDir := filepath.Join(cwd, ".claude", "plugins", "coworker")

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create destination directory %q: %w", destDir, err)
	}

	// Copy all plugin files.
	if err := copyDir(srcDir, destDir); err != nil {
		return fmt.Errorf("copy plugin files: %w", err)
	}

	// Merge MCP server config into .mcp.json at the project root.
	mcpConfigPath := filepath.Join(cwd, ".mcp.json")
	if err := mergeMCPConfig(mcpConfigPath, srcDir); err != nil {
		return fmt.Errorf("merge .mcp.json: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "coworker-claude plugin installed\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  plugin dir:  %s\n", destDir)
	fmt.Fprintf(cmd.OutOrStdout(), "  mcp config:  %s\n", mcpConfigPath)
	fmt.Fprintf(cmd.OutOrStdout(), "\nStart Claude Code in this project to activate the coworker-orchy skill.\n")
	return nil
}

// findPluginSource returns the path to the named plugin directory.
// It checks, in order:
//  1. <binary-dir>/plugins/<name>/
//  2. <cwd>/plugins/<name>/     (for development / running from repo root)
func findPluginSource(name string) (string, error) {
	candidates := []string{}

	// Option 1: sibling of the binary.
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "plugins", name))
	}

	// Option 2: relative to cwd (development).
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "plugins", name))
	}

	for _, dir := range candidates {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, nil
		}
	}

	return "", fmt.Errorf("plugin directory %q not found in any of %v", name, candidates)
}

// copyDir recursively copies src into dst, preserving file permissions.
// Existing destination files are overwritten.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		destPath := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(destPath, 0o755)
		}

		return copyFile(path, destPath)
	})
}

// copyFile copies a single file from src to dst.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source %q: %w", src, err)
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("stat source %q: %w", src, err)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create parent directory for %q: %w", dst, err)
	}

	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return fmt.Errorf("create destination %q: %w", dst, err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copy %q -> %q: %w", src, dst, err)
	}
	return nil
}

// mergeMCPConfig reads the plugin's .mcp.json and merges its mcpServers entries
// into the project-root .mcp.json. If the project file does not exist, it is
// created. Other keys in the existing project file are preserved.
func mergeMCPConfig(projectMCPPath, pluginSrcDir string) error {
	pluginMCPPath := filepath.Join(pluginSrcDir, ".mcp.json")
	pluginData, err := os.ReadFile(pluginMCPPath)
	if err != nil {
		// Plugin has no .mcp.json — nothing to merge.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read plugin .mcp.json: %w", err)
	}

	var pluginConfig map[string]json.RawMessage
	if err := json.Unmarshal(pluginData, &pluginConfig); err != nil {
		return fmt.Errorf("parse plugin .mcp.json: %w", err)
	}

	// Load or initialise the project .mcp.json.
	projectConfig := make(map[string]json.RawMessage)
	if existing, err := os.ReadFile(projectMCPPath); err == nil {
		if err := json.Unmarshal(existing, &projectConfig); err != nil {
			return fmt.Errorf("parse existing .mcp.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read .mcp.json: %w", err)
	}

	// Merge mcpServers — add plugin servers without removing existing ones.
	merged, err := mergeMapKey(projectConfig["mcpServers"], pluginConfig["mcpServers"])
	if err != nil {
		return fmt.Errorf("merge mcpServers: %w", err)
	}
	projectConfig["mcpServers"] = merged

	out, err := json.MarshalIndent(projectConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal merged .mcp.json: %w", err)
	}
	out = append(out, '\n')

	if err := os.WriteFile(projectMCPPath, out, 0o600); err != nil {
		return fmt.Errorf("write .mcp.json: %w", err)
	}
	return nil
}

// mergeMapKey merges the keys from incoming into existing (both are JSON
// objects). Returns the merged object as raw JSON. incoming keys overwrite
// existing keys with the same name.
func mergeMapKey(existing, incoming json.RawMessage) (json.RawMessage, error) {
	base := make(map[string]json.RawMessage)
	if existing != nil {
		if err := json.Unmarshal(existing, &base); err != nil {
			return nil, fmt.Errorf("unmarshal existing: %w", err)
		}
	}

	overlay := make(map[string]json.RawMessage)
	if incoming != nil {
		if err := json.Unmarshal(incoming, &overlay); err != nil {
			return nil, fmt.Errorf("unmarshal incoming: %w", err)
		}
	}

	for k, v := range overlay {
		base[k] = v
	}

	out, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("marshal merged map: %w", err)
	}
	return out, nil
}
