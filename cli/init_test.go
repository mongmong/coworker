package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// ---- helpers ----

// makeCmd returns a *cobra.Command with output captured into a strings.Builder.
func makeCmd(buf *strings.Builder) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	return cmd
}

// assertDirExists fails the test if the directory at path does not exist.
func assertDirExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected directory %q to exist: %v", path, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %q to be a directory, got file", path)
	}
}

// assertFileExists fails the test if the file at path does not exist.
func assertFileExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected file %q to exist: %v", path, err)
	}
	if info.IsDir() {
		t.Fatalf("expected %q to be a file, got directory", path)
	}
}

// assertFileContains fails if the file does not contain the given substring.
func assertFileContains(t *testing.T, path, substr string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	if !strings.Contains(string(data), substr) {
		t.Errorf("file %q does not contain %q\ncontent:\n%s", path, substr, string(data))
	}
}

// runInitInDir changes into tmpDir, runs runInit, then restores cwd.
func runInitInDir(t *testing.T, tmpDir string, opts *initOptions) error {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir %q: %v", tmpDir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	var buf strings.Builder
	cmd := makeCmd(&buf)
	return runInit(cmd, opts)
}

// ---- tests ----

// TestRunInit_BasicStructure verifies that coworker init creates all expected
// directories and writes config.yaml, policy.yaml, and .version.
func TestRunInit_BasicStructure(t *testing.T) {
	// Not parallel — uses os.Chdir (process-wide).
	tmpDir := t.TempDir()
	if err := runInitInDir(t, tmpDir, &initOptions{}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	coworkerDir := filepath.Join(tmpDir, ".coworker")

	// Directories.
	assertDirExists(t, coworkerDir)
	assertDirExists(t, filepath.Join(coworkerDir, "roles"))
	assertDirExists(t, filepath.Join(coworkerDir, "prompts"))
	assertDirExists(t, filepath.Join(coworkerDir, "rules"))
	assertDirExists(t, filepath.Join(coworkerDir, "runs"))

	// config.yaml
	assertFileExists(t, filepath.Join(coworkerDir, "config.yaml"))
	assertFileContains(t, filepath.Join(coworkerDir, "config.yaml"), "bind: local_socket")
	assertFileContains(t, filepath.Join(coworkerDir, "config.yaml"), "rate_limit_concurrent")

	// policy.yaml
	assertFileExists(t, filepath.Join(coworkerDir, "policy.yaml"))
	assertFileContains(t, filepath.Join(coworkerDir, "policy.yaml"), "spec-approved: block")
	assertFileContains(t, filepath.Join(coworkerDir, "policy.yaml"), "max_retries_per_job: 3")

	// .version
	assertFileExists(t, filepath.Join(coworkerDir, ".version"))
	data, _ := os.ReadFile(filepath.Join(coworkerDir, ".version"))
	if strings.TrimSpace(string(data)) == "" {
		t.Error(".version should not be empty")
	}
}

// TestRunInit_RolesPromptsRulesCopied verifies that role YAMLs, prompt MDs, and
// rule YAMLs are copied from the coding/ source tree into .coworker/.
// This test only runs when the coding/ directory is accessible from cwd, which
// is true when running tests from the repo root.
func TestRunInit_RolesPromptsRulesCopied(t *testing.T) {
	// Not parallel — uses os.Chdir (process-wide).

	// Skip if coding/ is not available in the repo root.
	if _, err := os.Stat("../coding/roles"); err != nil {
		t.Skip("coding/ source directory not accessible from test cwd — skipping asset copy test")
	}

	// We need to be in the repo root so findInitAssets can locate coding/.
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	tmpDir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Change to repoRoot so coding/ can be found, but redirect .coworker output
	// to tmpDir by setting up a symlink — actually easier to use a subdir approach.
	// Instead: cd to tmpDir, but put coding/ accessible via binary-path lookup.
	// Simplest: create a symlink coding -> ../coding in tmpDir.
	if err := os.Symlink(filepath.Join(repoRoot, "coding"), filepath.Join(tmpDir, "coding")); err != nil {
		t.Fatalf("symlink coding: %v", err)
	}

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	var buf strings.Builder
	cmd := makeCmd(&buf)
	if err := runInit(cmd, &initOptions{}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	coworkerDir := filepath.Join(tmpDir, ".coworker")

	// Check that at least one role YAML was copied.
	entries, err := os.ReadDir(filepath.Join(coworkerDir, "roles"))
	if err != nil {
		t.Fatalf("read roles dir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected at least one role YAML in .coworker/roles/")
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yaml") {
			t.Errorf("unexpected non-YAML file in roles dir: %q", e.Name())
		}
	}

	// Check that at least one prompt was copied.
	pEntries, err := os.ReadDir(filepath.Join(coworkerDir, "prompts"))
	if err != nil {
		t.Fatalf("read prompts dir: %v", err)
	}
	if len(pEntries) == 0 {
		t.Error("expected at least one prompt MD in .coworker/prompts/")
	}

	// Check supervisor-contract.yaml was copied.
	assertFileExists(t, filepath.Join(coworkerDir, "rules", "supervisor-contract.yaml"))
	assertFileContains(t, filepath.Join(coworkerDir, "rules", "supervisor-contract.yaml"), "applies_to")

	// Check quality.yaml was copied.
	assertFileExists(t, filepath.Join(coworkerDir, "rules", "quality.yaml"))
	assertFileContains(t, filepath.Join(coworkerDir, "rules", "quality.yaml"), "missing_required_tests")
}

// TestRunInit_GitignoreAugmented verifies that .gitignore receives the expected
// coworker entries when none exist yet.
func TestRunInit_GitignoreAugmented(t *testing.T) {
	// Not parallel — uses os.Chdir (process-wide).
	tmpDir := t.TempDir()
	if err := runInitInDir(t, tmpDir, &initOptions{}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	gitignorePath := filepath.Join(tmpDir, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	content := string(data)

	for _, entry := range gitignoreEntries {
		if !strings.Contains(content, entry) {
			t.Errorf(".gitignore missing entry %q\ncontent:\n%s", entry, content)
		}
	}
}

// TestRunInit_GitignoreIdempotent verifies that running init twice does not
// duplicate .gitignore entries.
func TestRunInit_GitignoreIdempotent(t *testing.T) {
	// Not parallel — uses os.Chdir (process-wide).
	tmpDir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	opts := &initOptions{}
	var buf strings.Builder

	// First run.
	if err := runInit(makeCmd(&buf), opts); err != nil {
		t.Fatalf("first runInit: %v", err)
	}

	// Second run.
	buf.Reset()
	if err := runInit(makeCmd(&buf), opts); err != nil {
		t.Fatalf("second runInit: %v", err)
	}

	gitignorePath := filepath.Join(tmpDir, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	content := string(data)

	for _, entry := range gitignoreEntries {
		count := strings.Count(content, entry)
		if count != 1 {
			t.Errorf(".gitignore entry %q appears %d times (want 1)\ncontent:\n%s", entry, count, content)
		}
	}
}

// TestRunInit_IdempotentSkipsExisting verifies that a second init without
// --force does not overwrite config.yaml (reads the file, modifies it in-place,
// and checks the modification survives the re-run).
func TestRunInit_IdempotentSkipsExisting(t *testing.T) {
	// Not parallel — uses os.Chdir (process-wide).
	tmpDir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	opts := &initOptions{}

	// First run to create files.
	if err := runInit(makeCmd(&strings.Builder{}), opts); err != nil {
		t.Fatalf("first runInit: %v", err)
	}

	// Overwrite config.yaml with a sentinel value.
	configPath := filepath.Join(tmpDir, ".coworker", "config.yaml")
	sentinel := "# SENTINEL - do not overwrite\n"
	if err := os.WriteFile(configPath, []byte(sentinel), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// Second run without --force: sentinel should survive.
	if err := runInit(makeCmd(&strings.Builder{}), opts); err != nil {
		t.Fatalf("second runInit: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if string(data) != sentinel {
		t.Errorf("config.yaml was overwritten without --force\ngot:\n%s\nwant:\n%s", string(data), sentinel)
	}
}

// TestRunInit_ForceOverwrites verifies that --force causes existing files to be
// overwritten with fresh defaults.
func TestRunInit_ForceOverwrites(t *testing.T) {
	// Not parallel — uses os.Chdir (process-wide).
	tmpDir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	// First run.
	if err := runInit(makeCmd(&strings.Builder{}), &initOptions{}); err != nil {
		t.Fatalf("first runInit: %v", err)
	}

	// Overwrite config.yaml with sentinel.
	configPath := filepath.Join(tmpDir, ".coworker", "config.yaml")
	sentinel := "# SENTINEL\n"
	if err := os.WriteFile(configPath, []byte(sentinel), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// Second run with --force: sentinel should be gone.
	if err := runInit(makeCmd(&strings.Builder{}), &initOptions{Force: true}); err != nil {
		t.Fatalf("second runInit with --force: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if string(data) == sentinel {
		t.Error("config.yaml was NOT overwritten even with --force")
	}
	if !strings.Contains(string(data), "bind: local_socket") {
		t.Errorf("config.yaml after --force does not contain expected content\ngot:\n%s", string(data))
	}
}

// TestRunInit_WithPlugins_TriggersInstall verifies that --with-plugins attempts
// plugin installation for all three CLIs. Because plugin sources may not be
// available in the test environment, we accept either success or a graceful
// "not found" message (non-fatal). The test ensures no panic and the command
// completes.
func TestRunInit_WithPlugins_TriggersInstall(t *testing.T) {
	// Not parallel — uses os.Chdir (process-wide).
	tmpDir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(orig)
	})
	t.Setenv("HOME", tmpDir)

	var buf strings.Builder
	cmd := makeCmd(&buf)

	// runInit with --with-plugins should not panic; it logs plugin errors gracefully.
	err = runInit(cmd, &initOptions{WithPlugins: true})
	if err != nil {
		t.Fatalf("runInit --with-plugins returned error: %v", err)
	}

	out := buf.String()
	// Should mention plugins in the output.
	if !strings.Contains(out, "plugin") && !strings.Contains(out, "Plugin") {
		t.Errorf("expected output to mention plugins, got:\n%s", out)
	}
}

// ---- augmentGitignore unit tests ----

// TestAugmentGitignore_NewFile verifies that augmentGitignore creates a
// .gitignore when none exists and adds all requested entries.
func TestAugmentGitignore_NewFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".gitignore")

	added, err := augmentGitignore(path, []string{"foo/", "bar.db"})
	if err != nil {
		t.Fatalf("augmentGitignore: %v", err)
	}
	if len(added) != 2 {
		t.Errorf("expected 2 entries added, got %d: %v", len(added), added)
	}

	data, _ := os.ReadFile(path)
	for _, e := range []string{"foo/", "bar.db"} {
		if !strings.Contains(string(data), e) {
			t.Errorf(".gitignore missing entry %q", e)
		}
	}
}

// TestAugmentGitignore_ExistingEntries verifies that entries already present
// in .gitignore are not duplicated.
func TestAugmentGitignore_ExistingEntries(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".gitignore")

	// Pre-populate with one of the entries.
	initial := "*.log\n.coworker/state.db\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	entries := []string{".coworker/state.db", ".coworker/runs/"}
	added, err := augmentGitignore(path, entries)
	if err != nil {
		t.Fatalf("augmentGitignore: %v", err)
	}

	// Only .coworker/runs/ should have been added.
	if len(added) != 1 || added[0] != ".coworker/runs/" {
		t.Errorf("expected only .coworker/runs/ to be added; got %v", added)
	}

	data, _ := os.ReadFile(path)
	count := strings.Count(string(data), ".coworker/state.db")
	if count != 1 {
		t.Errorf(".coworker/state.db appears %d times (want 1)", count)
	}
}

// TestAugmentGitignore_AllPresent verifies that when all entries already exist,
// augmentGitignore returns nil without modifying the file.
func TestAugmentGitignore_AllPresent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".gitignore")

	initial := ".coworker/state.db\n.coworker/runs/\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	statBefore, _ := os.Stat(path)
	added, err := augmentGitignore(path, []string{".coworker/state.db", ".coworker/runs/"})
	if err != nil {
		t.Fatalf("augmentGitignore: %v", err)
	}
	if len(added) != 0 {
		t.Errorf("expected no entries added, got %v", added)
	}

	statAfter, _ := os.Stat(path)
	if statBefore.Size() != statAfter.Size() {
		t.Error("file was modified even though all entries were already present")
	}
}

// TestWriteInitFile_SkipsWhenExists verifies that writeInitFile does not
// overwrite an existing file when force is false.
func TestWriteInitFile_SkipsWhenExists(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.yaml")
	original := "original content"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("write original: %v", err)
	}

	if err := writeInitFile(path, "new content", false); err != nil {
		t.Fatalf("writeInitFile: %v", err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Errorf("file was overwritten without force: got %q, want %q", string(data), original)
	}
}

// TestWriteInitFile_ForceOverwrites verifies that writeInitFile overwrites an
// existing file when force is true.
func TestWriteInitFile_ForceOverwrites(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.yaml")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatalf("write original: %v", err)
	}

	newContent := "updated content"
	if err := writeInitFile(path, newContent, true); err != nil {
		t.Fatalf("writeInitFile: %v", err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != newContent {
		t.Errorf("file not updated with force: got %q, want %q", string(data), newContent)
	}
}
