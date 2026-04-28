package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func saveAndRestoreEditFlags(t *testing.T) {
	t.Helper()
	origDB := editDBPath
	origEditor := os.Getenv("EDITOR")
	t.Cleanup(func() {
		editDBPath = origDB
		_ = os.Setenv("EDITOR", origEditor)
	})
}

func runEditForTest(t *testing.T, dbPath, target string) (string, error) {
	t.Helper()
	saveAndRestoreEditFlags(t)
	editDBPath = dbPath
	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetIn(bytes.NewReader(nil))
	cmd.SetContext(context.Background())
	err := runEdit(cmd, []string{target})
	return buf.String(), err
}

func TestEdit_NoActiveSession(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "state.db")
	target := filepath.Join(tmp, "x.txt")
	if err := os.WriteFile(target, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runEditForTest(t, dbPath, target)
	if err == nil {
		t.Fatal("expected error when no active session")
	}
	if !strings.Contains(err.Error(), "no active session") {
		t.Errorf("err = %q; expected 'no active session'", err.Error())
	}
}

func TestEdit_ArtifactMissing(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "state.db")
	_, err := runEditForTest(t, dbPath, filepath.Join(tmp, "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for missing artifact")
	}
	if !strings.Contains(err.Error(), "artifact not found") {
		t.Errorf("err = %q; expected 'artifact not found'", err.Error())
	}
}

// seedActiveSession sets up a temp DB with an active run and writes the
// session lock file so session.Manager.CurrentSession() succeeds.
func seedActiveSession(t *testing.T) (dbPath, runID string) {
	t.Helper()
	dbPath, runID, _ = advanceTestEnv(t, false)
	return
}

func TestEdit_EditorRunsAndExitsCleanly(t *testing.T) {
	dbPath, _ := seedActiveSession(t)
	tmp := t.TempDir()
	target := filepath.Join(tmp, "doc.md")
	if err := os.WriteFile(target, []byte("# hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Use `true` as the "editor" — exits 0 immediately, doesn't open a TTY.
	saveAndRestoreEditFlags(t)
	editDBPath = dbPath
	if err := os.Setenv("EDITOR", "true"); err != nil {
		t.Fatal(err)
	}

	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetIn(bytes.NewReader(nil))
	cmd.SetContext(context.Background())
	if err := runEdit(cmd, []string{target}); err != nil {
		t.Fatalf("runEdit: %v", err)
	}
	// Output may include the dirty-hint or nothing — the workspace under
	// tmp isn't a git repo so isPathDirtyInGit returns false → no hint.
	// Verify the command at least succeeded.
	_ = buf.String()
}

func TestEdit_EditorFailureSurfaces(t *testing.T) {
	dbPath, _ := seedActiveSession(t)
	tmp := t.TempDir()
	target := filepath.Join(tmp, "doc.md")
	if err := os.WriteFile(target, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}

	saveAndRestoreEditFlags(t)
	editDBPath = dbPath
	// `false` is the canonical "exit non-zero" command on Unix.
	if err := os.Setenv("EDITOR", "false"); err != nil {
		t.Fatal(err)
	}

	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetIn(bytes.NewReader(nil))
	cmd.SetContext(context.Background())
	err := runEdit(cmd, []string{target})
	if err == nil {
		t.Fatal("expected error when editor exits non-zero")
	}
	if !strings.Contains(err.Error(), "run editor") {
		t.Errorf("err = %q; expected 'run editor'", err.Error())
	}
}

func TestEdit_DirtyHintAppears(t *testing.T) {
	// Initialize a temp git repo + commit a file, then "edit" it (modify
	// + run a no-op editor) so the path is dirty when isPathDirtyInGit
	// checks it.
	dbPath, _ := seedActiveSession(t)
	tmp := t.TempDir()
	if err := runGit(t, tmp, "init", "--quiet"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	if err := runGit(t, tmp, "config", "user.email", "test@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(t, tmp, "config", "user.name", "test"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(tmp, "doc.md")
	if err := os.WriteFile(target, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runGit(t, tmp, "add", "doc.md"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(t, tmp, "commit", "-m", "init"); err != nil {
		t.Fatal(err)
	}
	// Modify the file so that after the no-op editor, git status is dirty.
	if err := os.WriteFile(target, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}

	saveAndRestoreEditFlags(t)
	editDBPath = dbPath
	if err := os.Setenv("EDITOR", "true"); err != nil {
		t.Fatal(err)
	}

	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetIn(bytes.NewReader(nil))
	cmd.SetContext(context.Background())
	// Run from tmp so git status sees the repo.
	origWD, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	if err := runEdit(cmd, []string{"doc.md"}); err != nil {
		t.Fatalf("runEdit: %v", err)
	}
	if !strings.Contains(buf.String(), "is dirty") {
		t.Errorf("output = %q; expected 'is dirty' hint", buf.String())
	}
}

func runGit(t *testing.T, dir string, args ...string) error {
	t.Helper()
	cmd := newGitCmd(dir, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %v: %v\n%s", args, err, out)
	}
	return nil
}

func newGitCmd(dir string, args ...string) *exec.Cmd {
	c := exec.Command("git", args...)
	c.Dir = dir
	return c
}
