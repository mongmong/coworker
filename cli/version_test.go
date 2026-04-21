package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionSubcommand(t *testing.T) {
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"version"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute version: %v", err)
	}

	got := buf.String()
	want := "coworker " + Version + "\n"
	if got != want {
		t.Errorf("version output mismatch:\n  got:  %q\n  want: %q", got, want)
	}
}

func TestVersionDefaultIsDev(t *testing.T) {
	if !strings.HasSuffix(Version, "-dev") {
		t.Fatalf("Version should default to a -dev marker when built without ldflags, got %q", Version)
	}
}
