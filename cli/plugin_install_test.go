package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ---------- mergeMCPConfig ----------

// TestMergeMCPConfig_NoExistingFile verifies that when the project .mcp.json
// does not exist, mergeMCPConfig creates a new file containing the plugin's
// mcpServers entries.
func TestMergeMCPConfig_NoExistingFile(t *testing.T) {
	t.Parallel()

	pluginDir := t.TempDir()
	projectDir := t.TempDir()

	// Write a plugin .mcp.json with one server entry.
	pluginMCP := map[string]any{
		"mcpServers": map[string]any{
			"coworker": map[string]any{
				"command": "coworker",
				"args":    []string{"daemon"},
				"type":    "stdio",
			},
		},
	}
	writeJSON(t, filepath.Join(pluginDir, ".mcp.json"), pluginMCP)

	projectMCPPath := filepath.Join(projectDir, ".mcp.json")
	if err := mergeMCPConfig(projectMCPPath, pluginDir); err != nil {
		t.Fatalf("mergeMCPConfig: %v", err)
	}

	data, err := os.ReadFile(projectMCPPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	if _, ok := result["mcpServers"]; !ok {
		t.Fatal("expected mcpServers key in merged output")
	}

	var servers map[string]json.RawMessage
	if err := json.Unmarshal(result["mcpServers"], &servers); err != nil {
		t.Fatalf("parse mcpServers: %v", err)
	}
	if _, ok := servers["coworker"]; !ok {
		t.Errorf("expected coworker server in merged mcpServers, got keys: %v", mapKeys(servers))
	}
}

// TestMergeMCPConfig_ExistingFile verifies that an existing .mcp.json is
// merged: pre-existing servers are kept and new plugin servers are added.
func TestMergeMCPConfig_ExistingFile(t *testing.T) {
	t.Parallel()

	pluginDir := t.TempDir()
	projectDir := t.TempDir()

	// Pre-existing project .mcp.json with a different server.
	existing := map[string]any{
		"mcpServers": map[string]any{
			"other-tool": map[string]any{
				"command": "other",
				"args":    []string{"serve"},
				"type":    "stdio",
			},
		},
	}
	projectMCPPath := filepath.Join(projectDir, ".mcp.json")
	writeJSON(t, projectMCPPath, existing)

	// Plugin adds the coworker server.
	pluginMCP := map[string]any{
		"mcpServers": map[string]any{
			"coworker": map[string]any{
				"command": "coworker",
				"args":    []string{"daemon"},
				"type":    "stdio",
			},
		},
	}
	writeJSON(t, filepath.Join(pluginDir, ".mcp.json"), pluginMCP)

	if err := mergeMCPConfig(projectMCPPath, pluginDir); err != nil {
		t.Fatalf("mergeMCPConfig: %v", err)
	}

	data, err := os.ReadFile(projectMCPPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	var servers map[string]json.RawMessage
	if err := json.Unmarshal(result["mcpServers"], &servers); err != nil {
		t.Fatalf("parse mcpServers: %v", err)
	}

	if _, ok := servers["other-tool"]; !ok {
		t.Error("pre-existing server 'other-tool' was removed during merge")
	}
	if _, ok := servers["coworker"]; !ok {
		t.Error("plugin server 'coworker' was not added during merge")
	}
}

// TestMergeMCPConfig_MalformedProjectJSON verifies that a malformed project
// .mcp.json causes mergeMCPConfig to return an error instead of silently
// overwriting the file.
func TestMergeMCPConfig_MalformedProjectJSON(t *testing.T) {
	t.Parallel()

	pluginDir := t.TempDir()
	projectDir := t.TempDir()

	pluginMCP := map[string]any{
		"mcpServers": map[string]any{},
	}
	writeJSON(t, filepath.Join(pluginDir, ".mcp.json"), pluginMCP)

	// Write syntactically invalid JSON to the project file.
	projectMCPPath := filepath.Join(projectDir, ".mcp.json")
	if err := os.WriteFile(projectMCPPath, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := mergeMCPConfig(projectMCPPath, pluginDir)
	if err == nil {
		t.Fatal("expected error for malformed project .mcp.json, got nil")
	}
}

// TestMergeMCPConfig_NoPluginMCPFile verifies that when the plugin directory
// contains no .mcp.json, the function is a no-op (no error, no file created).
func TestMergeMCPConfig_NoPluginMCPFile(t *testing.T) {
	t.Parallel()

	pluginDir := t.TempDir()
	projectDir := t.TempDir()
	projectMCPPath := filepath.Join(projectDir, ".mcp.json")

	if err := mergeMCPConfig(projectMCPPath, pluginDir); err != nil {
		t.Fatalf("unexpected error when plugin has no .mcp.json: %v", err)
	}

	if _, err := os.Stat(projectMCPPath); !os.IsNotExist(err) {
		t.Error("project .mcp.json should not have been created when plugin has none")
	}
}

// ---------- copyDir ----------

// TestCopyDir verifies that copyDir recursively copies files with their
// permissions into a destination directory.
func TestCopyDir(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// Create a nested structure in src.
	writeFileMode(t, filepath.Join(srcDir, "top.txt"), "hello", 0o644)
	if err := os.Mkdir(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	writeFileMode(t, filepath.Join(srcDir, "sub", "nested.txt"), "world", 0o755)

	if err := copyDir(srcDir, dstDir); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	// Verify top-level file.
	assertFileContent(t, filepath.Join(dstDir, "top.txt"), "hello")

	// Verify nested file.
	assertFileContent(t, filepath.Join(dstDir, "sub", "nested.txt"), "world")

	// Verify permissions are preserved on the nested file.
	info, err := os.Stat(filepath.Join(dstDir, "sub", "nested.txt"))
	if err != nil {
		t.Fatalf("stat nested: %v", err)
	}
	if info.Mode()&0o755 != 0o755 {
		t.Errorf("expected permission bits 0o755, got %o", info.Mode())
	}
}

// ---------- findPluginSource ----------

// TestFindPluginSource_FallbackToCWD verifies that findPluginSource finds a
// plugin directory when it exists relative to the working directory. This
// exercises the "development / repo-root" fallback path without requiring the
// binary to be installed.
func TestFindPluginSource_FallbackToCWD(t *testing.T) {
	// findPluginSource uses os.Getwd(), so we change into a temp dir that
	// contains a plugins/<name>/ directory.
	tmpRoot := t.TempDir()
	pluginPath := filepath.Join(tmpRoot, "plugins", "coworker-test")
	if err := os.MkdirAll(pluginPath, 0o755); err != nil {
		t.Fatalf("setup plugin dir: %v", err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		// Restore the original working directory after the test.
		_ = os.Chdir(orig)
	})

	got, err := findPluginSource("coworker-test")
	if err != nil {
		t.Fatalf("findPluginSource: %v", err)
	}

	// Resolve both paths to catch symlink differences.
	gotAbs, _ := filepath.EvalSymlinks(got)
	wantAbs, _ := filepath.EvalSymlinks(pluginPath)
	if gotAbs != wantAbs {
		t.Errorf("findPluginSource returned %q; want %q", gotAbs, wantAbs)
	}
}

// TestFindPluginSource_NotFound verifies that findPluginSource returns an
// error when no candidate directory exists.
func TestFindPluginSource_NotFound(t *testing.T) {
	t.Parallel()

	// Change to a temp dir that has no plugins/ subdirectory.
	tmpRoot := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	_, err = findPluginSource("nonexistent-plugin")
	if err == nil {
		t.Fatal("expected error for missing plugin directory, got nil")
	}
}

// ---------- helpers ----------

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func writeFileMode(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	if string(data) != want {
		t.Errorf("file %q content = %q; want %q", path, data, want)
	}
}

func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
