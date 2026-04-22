package cli

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestInvokeCommand_Help(t *testing.T) {
	// Use the command's UsageString/Help directly to avoid flag state pollution
	// that occurs when cobra's --help flag is parsed and not reset between tests.
	buf := &bytes.Buffer{}
	invokeCmd.SetOut(buf)
	t.Cleanup(func() {
		invokeCmd.SetOut(nil)
	})

	if err := invokeCmd.Help(); err != nil {
		t.Fatalf("invoke Help(): %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Invoke a role") {
		t.Errorf("help output should contain 'Invoke a role', got:\n%s", output)
	}
	if !strings.Contains(output, "--diff") {
		t.Errorf("help output should contain '--diff', got:\n%s", output)
	}
	if !strings.Contains(output, "--spec") {
		t.Errorf("help output should contain '--spec', got:\n%s", output)
	}
	if !strings.Contains(output, "--db") {
		t.Errorf("help output should contain '--db', got:\n%s", output)
	}
	if !strings.Contains(output, "--cli-binary") {
		t.Errorf("help output should contain '--cli-binary', got:\n%s", output)
	}
	if !strings.Contains(output, "--role-dir") {
		t.Errorf("help output should contain '--role-dir', got:\n%s", output)
	}
	if !strings.Contains(output, "--prompt-dir") {
		t.Errorf("help output should contain '--prompt-dir', got:\n%s", output)
	}
}

func TestInvokeCommand_MissingRole(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on windows")
	}

	buf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(errBuf)
	rootCmd.SetArgs([]string{"invoke"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("expected error when no role is specified, got nil")
	}
}
