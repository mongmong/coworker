package quality

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/chris/coworker/internal/executil"
)

// Judge evaluates a single quality rule against a diff and context.
// The judge is always called with one rule at a time so that each verdict
// maps cleanly to a single rule name.
type Judge interface {
	// Evaluate invokes the judge with the given rule, diff, and context.
	// Returns a structured verdict or an error if the judge could not run.
	Evaluate(ctx context.Context, rule *Rule, diff string, jobContext string) (*Verdict, error)
}

// defaultJudgeMaxWallclockMinutes is the default subprocess deadline for
// CLIJudge when MaxWallclockMinutes is zero.
const defaultJudgeMaxWallclockMinutes = 5

// CLIJudge shells out to a Codex-compatible CLI binary to evaluate
// quality rules. It expects the binary to accept a prompt on stdin and
// write a single JSON object to stdout.
//
// The binary is invoked as: <BinaryPath> exec --json
// with the combined prompt written to stdin.
//
// Output format expected on stdout:
//
//	{"pass": bool, "category": "...", "findings": [...], "confidence": float}
type CLIJudge struct {
	// BinaryPath is the path to the Codex (or compatible) binary.
	// If empty, "codex" is used (looked up on PATH).
	BinaryPath string

	// MaxWallclockMinutes is the subprocess deadline in minutes.
	// Zero uses the defaultJudgeMaxWallclockMinutes (5 minutes).
	// Negative disables the deadline.
	MaxWallclockMinutes int

	// Logger is used for debug and error logging. If nil, slog.Default() is used.
	Logger *slog.Logger
}

// Evaluate shells out to the CLI binary with the rendered quality prompt.
func (j *CLIJudge) Evaluate(ctx context.Context, rule *Rule, diff string, jobContext string) (*Verdict, error) {
	logger := j.Logger
	if logger == nil {
		logger = slog.Default()
	}

	binary := j.BinaryPath
	if binary == "" {
		binary = "codex"
	}

	prompt := renderJudgePrompt(rule, diff, jobContext)

	maxMinutes := j.MaxWallclockMinutes
	if maxMinutes == 0 {
		maxMinutes = defaultJudgeMaxWallclockMinutes
	}
	judgeCtx, judgeCancel := executil.BudgetTimeout(ctx, maxMinutes)
	defer judgeCancel()

	cmd := exec.CommandContext(judgeCtx, binary, "exec", "--json")
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = strings.NewReader(prompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	logger.Debug("invoking quality judge", "binary", binary, "rule", rule.Name)

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("quality judge %q for rule %q: %w (stderr: %s)", binary, rule.Name, err, stderr.String())
	}

	var verdict Verdict
	raw := stdout.Bytes()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&verdict); err != nil {
		return nil, fmt.Errorf("parse judge output for rule %q: %w; raw: %s", rule.Name, err, string(raw))
	}

	logger.Debug("quality judge verdict", "rule", rule.Name, "pass", verdict.Pass, "confidence", verdict.Confidence)
	return &verdict, nil
}

// renderJudgePrompt builds the prompt sent to the LLM judge binary.
// It combines the rule's evaluation prompt with the diff and job context
// so the judge has everything it needs to produce a verdict.
func renderJudgePrompt(rule *Rule, diff string, jobContext string) string {
	var sb strings.Builder
	sb.WriteString("You are a code quality evaluator. Evaluate the following criterion and respond with a single JSON object.\n\n")
	sb.WriteString("## Criterion\n\n")
	sb.WriteString(rule.Prompt)
	sb.WriteString("\n\n")

	if diff != "" {
		sb.WriteString("## Diff\n\n```diff\n")
		sb.WriteString(diff)
		sb.WriteString("\n```\n\n")
	}

	if jobContext != "" {
		sb.WriteString("## Context\n\n")
		sb.WriteString(jobContext)
		sb.WriteString("\n\n")
	}

	sb.WriteString("## Required JSON Response Format\n\n")
	sb.WriteString("Respond with exactly one JSON object on a single line:\n")
	sb.WriteString(`{"pass": <bool>, "category": "` + string(rule.Category) + `", "findings": ["<issue1>", ...], "confidence": <0.0-1.0>}`)
	sb.WriteString("\n\nIf the criterion is satisfied, set \"pass\" to true and \"findings\" to []. ")
	sb.WriteString("If not satisfied, set \"pass\" to false and list specific issues in \"findings\".")

	return sb.String()
}

// MockJudge is a test double for Judge.
// Verdicts are keyed by rule name. If a rule name is not found,
// a passing verdict is returned.
type MockJudge struct {
	// Verdicts maps rule names to pre-configured verdicts.
	Verdicts map[string]*Verdict

	// Calls records each (rule, diff, context) triplet that was evaluated,
	// for assertion in tests.
	Calls []MockJudgeCall
}

// MockJudgeCall records a single call to the mock judge.
type MockJudgeCall struct {
	RuleName   string
	Diff       string
	JobContext string
}

// Evaluate returns the pre-configured verdict for the rule, or a passing
// verdict if the rule name is not in the Verdicts map.
func (m *MockJudge) Evaluate(_ context.Context, rule *Rule, diff string, jobContext string) (*Verdict, error) {
	m.Calls = append(m.Calls, MockJudgeCall{
		RuleName:   rule.Name,
		Diff:       diff,
		JobContext: jobContext,
	})

	if v, ok := m.Verdicts[rule.Name]; ok {
		return v, nil
	}

	// Default: passing verdict.
	return &Verdict{
		Pass:       true,
		Category:   string(rule.Category),
		Findings:   nil,
		Confidence: 1.0,
	}, nil
}
