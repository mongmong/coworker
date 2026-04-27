package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/chris/coworker/core"
)

// runStreamScript runs `sh -c <script>` and returns the resulting
// JobResult after parsing stdout via cliJobHandle.Wait. Used to drive
// the parser with controlled input. Plan 128 (I5).
func runStreamScript(t *testing.T, script string) (*core.JobResult, error) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "sh", "-c", script)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &cliJobHandle{
		cmd:    cmd,
		stdout: stdout,
		stderr: stderr,
		job:    &core.Job{ID: "test-job", RunID: "test-run", Role: "test"},
	}
	return h.Wait(context.Background())
}

func TestCliHandle_ParsesFinding(t *testing.T) {
	res, err := runStreamScript(t, `cat <<'EOF'
{"type":"finding","path":"main.go","line":42,"severity":"important","body":"missing close"}
{"type":"done","exit_code":0}
EOF`)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(res.Findings))
	}
	f := res.Findings[0]
	if f.Path != "main.go" || f.Line != 42 ||
		f.Severity != core.Severity("important") || f.Body != "missing close" {
		t.Errorf("finding = %+v", f)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", res.ExitCode)
	}
}

func TestCliHandle_ParsesMultipleFindings(t *testing.T) {
	res, err := runStreamScript(t, `cat <<'EOF'
{"type":"finding","path":"a.go","line":1,"severity":"minor","body":"a"}
{"type":"finding","path":"b.go","line":2,"severity":"important","body":"b"}
{"type":"finding","path":"c.go","line":3,"severity":"critical","body":"c"}
{"type":"done","exit_code":0}
EOF`)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(res.Findings) != 3 {
		t.Fatalf("findings = %d, want 3", len(res.Findings))
	}
	if res.Findings[2].Severity != core.Severity("critical") {
		t.Errorf("third severity = %q, want critical", res.Findings[2].Severity)
	}
}

func TestCliHandle_DoneNonZeroExitCode(t *testing.T) {
	res, err := runStreamScript(t, `cat <<'EOF'
{"type":"done","exit_code":2}
EOF`)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 2 {
		t.Errorf("exit_code = %d, want 2", res.ExitCode)
	}
}

func TestCliHandle_MalformedJSONFallsBackToStdout(t *testing.T) {
	// The parser's contract (per cli_handle.go:51-57) is: on decode
	// error, accumulate remaining bytes into Stdout and break the loop.
	// stderr stays empty.
	res, err := runStreamScript(t, `cat <<'EOF'
{"type":"finding","path":"a.go","line":1,"severity":"minor","body":"ok"}
this is not valid JSON
EOF`)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Errorf("findings = %d, want 1 (parsed before malformed line)", len(res.Findings))
	}
	if !strings.Contains(res.Stdout, "not valid JSON") {
		t.Errorf("stdout should include malformed bytes; got %q", res.Stdout)
	}
}

func TestCliHandle_EmptyOutput(t *testing.T) {
	res, err := runStreamScript(t, `:`) // no-op produces no stdout
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("findings = %d, want 0", len(res.Findings))
	}
	if res.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0 (process exited 0)", res.ExitCode)
	}
}

func TestCliHandle_StderrCaptured(t *testing.T) {
	res, err := runStreamScript(t, `echo "boom" >&2; echo '{"type":"done","exit_code":0}'`)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !strings.Contains(res.Stderr, "boom") {
		t.Errorf("stderr = %q; expected 'boom'", res.Stderr)
	}
}

func TestCliHandle_NonZeroExitFromShell(t *testing.T) {
	// When the subprocess exits non-zero (without a `done` line), the
	// parser sets result.ExitCode from the exec.ExitError.
	res, err := runStreamScript(t, `exit 7`)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("exit_code = %d, want 7", res.ExitCode)
	}
}

func TestCliHandle_MissingFieldsTreatedAsZero(t *testing.T) {
	// streamMessage is unmarshaled with omitempty fields; missing fields
	// in the JSON come back as zero-values (empty string / 0).
	res, err := runStreamScript(t, `cat <<'EOF'
{"type":"finding"}
{"type":"done","exit_code":0}
EOF`)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(res.Findings))
	}
	f := res.Findings[0]
	if f.Path != "" || f.Line != 0 || f.Severity != core.Severity("") || f.Body != "" {
		t.Errorf("expected zero-valued finding, got %+v", f)
	}
}

func TestCliHandle_UnknownTypeIgnored(t *testing.T) {
	// Unknown event types are silently dropped (no findings produced).
	// Cost-bearing types (`result`, `turn.completed`) populate Cost via
	// populateCost — covered separately in cost_helpers_test.go.
	res, err := runStreamScript(t, `cat <<'EOF'
{"type":"thinking","content":"hmm"}
{"type":"finding","path":"x.go","line":1,"severity":"minor","body":"x"}
{"type":"done","exit_code":0}
EOF`)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Errorf("findings = %d, want 1 (unknown type dropped)", len(res.Findings))
	}
}
