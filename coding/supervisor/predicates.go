package supervisor

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/chris/coworker/core"
)

// EvalContext provides the data needed to evaluate predicates.
// This struct is passed to every predicate function. The supervisor
// package does NOT import store/ — all data is pre-assembled by the caller.
type EvalContext struct {
	Job    *core.Job
	Result *core.JobResult
	Role   *core.Role
	RunID  string
	// WorkDir is the working directory for git commands.
	// If empty, git predicates use the current working directory.
	WorkDir string
}

// PredicateFn is a function that evaluates a condition against the context.
// It returns (passed bool, err error). An error indicates the predicate
// could not be evaluated (not that it failed).
type PredicateFn func(ctx *EvalContext, args []string) (bool, error)

// predicateRegistry maps predicate function names to their implementations.
var predicateRegistry = map[string]PredicateFn{
	"all_findings_have":          allFindingsHave,
	"exit_code_is":               exitCodeIs,
	"git_current_branch_matches": gitCurrentBranchMatches,
	"last_commit_msg_contains":   lastCommitMsgContains,
}

// LookupPredicate returns the predicate function for the given name,
// or an error if no such predicate is registered.
func LookupPredicate(name string) (PredicateFn, error) {
	fn, ok := predicateRegistry[name]
	if !ok {
		return nil, fmt.Errorf("unknown predicate %q", name)
	}
	return fn, nil
}

// ParseCheck parses a check expression like:
//
//	all_findings_have(["path", "line"])
//	exit_code_is(0)
//	git_current_branch_matches("^feature/plan-\\d+-")
//
// Returns the function name and a slice of string arguments.
func ParseCheck(check string) (funcName string, args []string, err error) {
	// Match: funcName(argContent)
	re := regexp.MustCompile(`^(\w+)\((.+)\)$`)
	m := re.FindStringSubmatch(strings.TrimSpace(check))
	if m == nil {
		return "", nil, fmt.Errorf("invalid check syntax %q: expected funcName(args...)", check)
	}
	funcName = m[1]
	argContent := strings.TrimSpace(m[2])

	args, err = parseArgs(argContent)
	if err != nil {
		return "", nil, fmt.Errorf("parse args for %q: %w", check, err)
	}

	return funcName, args, nil
}

// parseArgs parses the argument content inside the parentheses.
// Supports:
//   - Array syntax: ["path", "line"] -> ["path", "line"]
//   - Quoted string: "^feature/" -> ["^feature/"]
//   - Bare integer: 0 -> ["0"]
func parseArgs(s string) ([]string, error) {
	s = strings.TrimSpace(s)

	// Array syntax: ["a", "b", ...]
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		inner := strings.TrimSpace(s[1 : len(s)-1])
		if inner == "" {
			return nil, nil
		}
		parts := strings.Split(inner, ",")
		var args []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			p = stripQuotes(p)
			if p != "" {
				args = append(args, p)
			}
		}
		return args, nil
	}

	// Single quoted string: "value"
	if (strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"")) ||
		(strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'")) {
		return []string{stripQuotes(s)}, nil
	}

	// Bare value (integer, identifier, etc.)
	return []string{s}, nil
}

// stripQuotes removes surrounding double or single quotes.
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '\'' && s[len(s)-1] == '\'') {
			if unquoted, err := strconv.Unquote(s); err == nil {
				return unquoted
			}
			return s[1 : len(s)-1]
		}
	}
	return s
}

// --- Built-in predicate implementations ---

// allFindingsHave checks that every finding in the job result has
// non-empty values for all the specified fields.
// Usage: all_findings_have(["path", "line"])
// Supported fields: "path", "line", "severity", "body"
func allFindingsHave(ctx *EvalContext, args []string) (bool, error) {
	if len(args) == 0 {
		return false, fmt.Errorf("all_findings_have requires at least one field name")
	}

	if ctx.Result == nil {
		return false, fmt.Errorf("no job result available")
	}

	// If there are no findings, the check passes vacuously.
	if len(ctx.Result.Findings) == 0 {
		return true, nil
	}

	for i, f := range ctx.Result.Findings {
		for _, field := range args {
			switch field {
			case "path":
				if f.Path == "" {
					return false, nil
				}
			case "line":
				if f.Line == 0 {
					return false, nil
				}
			case "severity":
				if f.Severity == "" {
					return false, nil
				}
			case "body":
				if f.Body == "" {
					return false, nil
				}
			default:
				return false, fmt.Errorf("all_findings_have: unknown field %q at finding[%d]", field, i)
			}
		}
	}

	return true, nil
}

// exitCodeIs checks that the job exit code matches the expected value.
// Usage: exit_code_is(0)
func exitCodeIs(ctx *EvalContext, args []string) (bool, error) {
	if len(args) != 1 {
		return false, fmt.Errorf("exit_code_is requires exactly one argument")
	}

	if ctx.Result == nil {
		return false, fmt.Errorf("no job result available")
	}

	var expected int
	_, err := fmt.Sscanf(args[0], "%d", &expected)
	if err != nil {
		return false, fmt.Errorf("exit_code_is: invalid integer %q: %w", args[0], err)
	}

	return ctx.Result.ExitCode == expected, nil
}

// gitCurrentBranchMatches checks that the current git branch matches
// the given regex pattern.
// Usage: git_current_branch_matches("^feature/plan-\\d+-")
func gitCurrentBranchMatches(ctx *EvalContext, args []string) (bool, error) {
	if len(args) != 1 {
		return false, fmt.Errorf("git_current_branch_matches requires exactly one argument")
	}

	pattern := args[0]
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, fmt.Errorf("git_current_branch_matches: invalid regex %q: %w", pattern, err)
	}

	branch, err := gitCurrentBranch(ctx.WorkDir)
	if err != nil {
		return false, fmt.Errorf("git_current_branch_matches: %w", err)
	}

	return re.MatchString(branch), nil
}

// lastCommitMsgContains checks that the most recent commit message
// matches the given regex pattern.
// Usage: last_commit_msg_contains("Phase \\d+:")
func lastCommitMsgContains(ctx *EvalContext, args []string) (bool, error) {
	if len(args) != 1 {
		return false, fmt.Errorf("last_commit_msg_contains requires exactly one argument")
	}

	pattern := args[0]
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, fmt.Errorf("last_commit_msg_contains: invalid regex %q: %w", pattern, err)
	}

	msg, err := gitLastCommitMsg(ctx.WorkDir)
	if err != nil {
		return false, fmt.Errorf("last_commit_msg_contains: %w", err)
	}

	return re.MatchString(msg), nil
}

// --- Git helpers ---

// gitCurrentBranch returns the current git branch name.
func gitCurrentBranch(workDir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	if workDir != "" {
		cmd.Dir = workDir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --abbrev-ref HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitLastCommitMsg returns the subject line of the most recent commit.
func gitLastCommitMsg(workDir string) (string, error) {
	cmd := exec.Command("git", "log", "-1", "--format=%s")
	if workDir != "" {
		cmd.Dir = workDir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git log -1 --format=%%s: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
