# Plan 101 — Supervisor Contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deterministic rule engine that evaluates job outputs against YAML-configured contract rules, retries with feedback on failure, and escalates to compliance-breach after max retries.

**Architecture:** Rules are YAML predicates loaded from `coding/supervisor/rules.yaml` (built-in seed set) or `.coworker/rules/supervisor-contract.yaml` (per-repo overrides). After every job, the `RuleEngine` evaluates applicable rules (filtered by role glob match). On failure, the `Dispatcher` retries the job with rule violation messages injected into the prompt. Max-retry ceiling (default 3) prevents oscillation; exhaustion emits a `compliance-breach` event.

**Tech Stack:** Go 1.25+, `gopkg.in/yaml.v3` for rule YAML, `os/exec` for git predicates, `regexp` for pattern matching. Builds on existing `store.EventStore`, `core.Job`, `core.Role`.

**Manifest entry:** `docs/specs/001-plan-manifest.md` section 101.

**Branch:** `feature/plan-101-supervisor-contract`.

---

## File structure after Plan 101

New and modified files:

```
coworker/
├── core/
│   └── supervisor.go              # NEW: SupervisorVerdict, RuleResult, new EventKinds
├── coding/
│   ├── supervisor/
│   │   ├── engine.go              # NEW: RuleEngine — load rules, evaluate against job outputs
│   │   ├── engine_test.go         # NEW
│   │   ├── predicates.go          # NEW: Built-in predicate functions + registry
│   │   ├── predicates_test.go     # NEW
│   │   ├── loader.go              # NEW: YAML rule file loader
│   │   ├── loader_test.go         # NEW
│   │   └── rules.yaml             # NEW: Seed rule catalog for reviewer.*
│   ├── dispatch.go                # MODIFY: add after-job supervisor hook + retry loop
│   └── dispatch_test.go           # MODIFY: add supervisor integration tests
└── testdata/
    └── mocks/
        ├── codex                  # (existing, unchanged)
        ├── codex-bad-findings     # NEW: mock that emits findings missing path/line
        └── codex-retry-then-pass  # NEW: mock that fails first call, passes second
```

---

## Task 1: Core supervisor types

**Goal:** Add `SupervisorVerdict`, `RuleResult`, and new `EventKind` constants to `core/`. Pure data types with no dependencies.

**Files:**
- Create: `core/supervisor.go`

### Step 1.1: Write `core/supervisor.go`

- [ ] Create `core/supervisor.go`:

```go
package core

// Supervisor EventKinds.
const (
	// EventSupervisorVerdict is emitted after every job evaluation.
	// Payload: {"job_id": "...", "pass": true/false, "results": [...]}
	EventSupervisorVerdict EventKind = "supervisor.verdict"

	// EventSupervisorRetry is emitted when a job is retried with feedback.
	// Payload: {"job_id": "...", "retry_job_id": "...", "attempt": N, "feedback": "..."}
	EventSupervisorRetry EventKind = "supervisor.retry"

	// EventComplianceBreach is emitted when max retries are exhausted.
	// Payload: {"job_id": "...", "role": "...", "failed_rules": [...], "attempts": N}
	EventComplianceBreach EventKind = "compliance-breach"
)

// SupervisorVerdict is the result of evaluating all applicable rules
// against a job's outputs.
type SupervisorVerdict struct {
	Pass    bool
	Results []RuleResult
}

// RuleResult is the evaluation outcome of a single rule.
type RuleResult struct {
	RuleName string
	Passed   bool
	Message  string
}

// FailedMessages returns the messages from all failed rules,
// suitable for injecting into a retry prompt.
func (v *SupervisorVerdict) FailedMessages() []string {
	var msgs []string
	for _, r := range v.Results {
		if !r.Passed {
			msgs = append(msgs, r.Message)
		}
	}
	return msgs
}
```

### Step 1.2: Verify the core package compiles

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go build ./core/...
```

Expected: clean compilation, no errors.

### Step 1.3: Run all existing core tests

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./core/... -v -count=1
```

Expected: all existing tests pass (TestNewID_*, TestComputeFingerprint*).

---

## Task 2: Rule YAML loader

**Goal:** Parse YAML rule files into Go structs. Support role glob matching (e.g., `reviewer.*` matches `reviewer.arch`).

**Files:**
- Create: `coding/supervisor/loader.go`
- Create: `coding/supervisor/loader_test.go`

### Step 2.1: Write `coding/supervisor/loader.go`

- [ ] Create `coding/supervisor/loader.go`:

```go
// Package supervisor implements the deterministic contract rule engine
// that evaluates job outputs against YAML-configured rules.
package supervisor

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Rule is a single contract rule parsed from YAML.
type Rule struct {
	Name      string   `yaml:"-"`       // populated from the map key
	AppliesTo []string `yaml:"applies_to"`
	Check     string   `yaml:"check"`
	Message   string   `yaml:"message"`
}

// RuleSet is the top-level structure of a rules YAML file.
type RuleSet struct {
	Rules map[string]Rule `yaml:"rules"`
}

// RuleList is a flattened, validated list of rules ready for evaluation.
type RuleList struct {
	Rules []Rule
}

// LoadRulesFromFile reads and parses a YAML rule file.
func LoadRulesFromFile(path string) (*RuleList, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rules file %q: %w", path, err)
	}
	return LoadRulesFromBytes(data)
}

// LoadRulesFromBytes parses YAML rule bytes into a RuleList.
func LoadRulesFromBytes(data []byte) (*RuleList, error) {
	var rs RuleSet
	if err := yaml.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("parse rules YAML: %w", err)
	}

	if len(rs.Rules) == 0 {
		return nil, fmt.Errorf("rules file contains no rules")
	}

	var rules []Rule
	for name, r := range rs.Rules {
		r.Name = name
		if err := validateRule(&r); err != nil {
			return nil, fmt.Errorf("rule %q: %w", name, err)
		}
		rules = append(rules, r)
	}

	return &RuleList{Rules: rules}, nil
}

// validateRule checks that required fields are present.
func validateRule(r *Rule) error {
	if len(r.AppliesTo) == 0 {
		return fmt.Errorf("applies_to must not be empty")
	}
	if r.Check == "" {
		return fmt.Errorf("check must not be empty")
	}
	if r.Message == "" {
		return fmt.Errorf("message must not be empty")
	}
	return nil
}

// RulesForRole returns the subset of rules that apply to the given role name.
// Matching uses glob semantics: "reviewer.*" matches "reviewer.arch",
// "developer" matches exactly "developer".
func (rl *RuleList) RulesForRole(roleName string) []Rule {
	var matched []Rule
	for _, r := range rl.Rules {
		for _, pattern := range r.AppliesTo {
			if roleGlobMatches(pattern, roleName) {
				matched = append(matched, r)
				break
			}
		}
	}
	return matched
}

// roleGlobMatches converts a role glob pattern to a regex and tests the name.
// "reviewer.*" -> matches "reviewer.arch", "reviewer.frontend"
// "developer" -> matches only "developer" exactly
// "*" -> matches everything
func roleGlobMatches(pattern, roleName string) bool {
	// Escape regex meta-characters except *.
	// First escape dots (they are literal in role names).
	escaped := strings.ReplaceAll(pattern, ".", "\\.")
	// Then convert glob * to regex .*
	escaped = strings.ReplaceAll(escaped, "*", ".*")
	// Anchor the pattern.
	re, err := regexp.Compile("^" + escaped + "$")
	if err != nil {
		return false
	}
	return re.MatchString(roleName)
}
```

### Step 2.2: Write `coding/supervisor/loader_test.go`

- [ ] Create `coding/supervisor/loader_test.go`:

```go
package supervisor

import (
	"testing"
)

func TestLoadRulesFromBytes_ValidYAML(t *testing.T) {
	yaml := []byte(`
rules:
  reviewer_findings_line_anchored:
    applies_to: [reviewer.*]
    check: all_findings_have(["path", "line"])
    message: "All findings must have path and line references"
  dev_branch_check:
    applies_to: [developer]
    check: git_current_branch_matches("^feature/plan-\\d+-")
    message: "Developer must commit on a feature branch"
`)

	rl, err := LoadRulesFromBytes(yaml)
	if err != nil {
		t.Fatalf("LoadRulesFromBytes: %v", err)
	}
	if len(rl.Rules) != 2 {
		t.Fatalf("rules count = %d, want 2", len(rl.Rules))
	}

	// Verify rule names are populated from map keys.
	names := make(map[string]bool)
	for _, r := range rl.Rules {
		names[r.Name] = true
	}
	if !names["reviewer_findings_line_anchored"] {
		t.Error("missing rule: reviewer_findings_line_anchored")
	}
	if !names["dev_branch_check"] {
		t.Error("missing rule: dev_branch_check")
	}
}

func TestLoadRulesFromBytes_EmptyRules(t *testing.T) {
	yaml := []byte(`rules: {}`)
	_, err := LoadRulesFromBytes(yaml)
	if err == nil {
		t.Error("expected error for empty rules, got nil")
	}
}

func TestLoadRulesFromBytes_InvalidYAML(t *testing.T) {
	_, err := LoadRulesFromBytes([]byte(`{not valid yaml`))
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

func TestLoadRulesFromBytes_MissingCheck(t *testing.T) {
	yaml := []byte(`
rules:
  bad_rule:
    applies_to: [developer]
    message: "some message"
`)
	_, err := LoadRulesFromBytes(yaml)
	if err == nil {
		t.Error("expected error for missing check field, got nil")
	}
}

func TestLoadRulesFromBytes_MissingAppliesTo(t *testing.T) {
	yaml := []byte(`
rules:
  bad_rule:
    check: exit_code_is(0)
    message: "some message"
`)
	_, err := LoadRulesFromBytes(yaml)
	if err == nil {
		t.Error("expected error for missing applies_to, got nil")
	}
}

func TestLoadRulesFromBytes_MissingMessage(t *testing.T) {
	yaml := []byte(`
rules:
  bad_rule:
    applies_to: [developer]
    check: exit_code_is(0)
`)
	_, err := LoadRulesFromBytes(yaml)
	if err == nil {
		t.Error("expected error for missing message, got nil")
	}
}

func TestRulesForRole_ExactMatch(t *testing.T) {
	rl := &RuleList{
		Rules: []Rule{
			{Name: "dev_rule", AppliesTo: []string{"developer"}, Check: "exit_code_is(0)", Message: "m1"},
			{Name: "rev_rule", AppliesTo: []string{"reviewer.arch"}, Check: "exit_code_is(0)", Message: "m2"},
		},
	}

	matched := rl.RulesForRole("developer")
	if len(matched) != 1 {
		t.Fatalf("matched = %d, want 1", len(matched))
	}
	if matched[0].Name != "dev_rule" {
		t.Errorf("matched rule = %q, want %q", matched[0].Name, "dev_rule")
	}
}

func TestRulesForRole_GlobMatch(t *testing.T) {
	rl := &RuleList{
		Rules: []Rule{
			{Name: "rev_rule", AppliesTo: []string{"reviewer.*"}, Check: "exit_code_is(0)", Message: "m1"},
		},
	}

	tests := []struct {
		role  string
		match bool
	}{
		{"reviewer.arch", true},
		{"reviewer.frontend", true},
		{"reviewer", false},
		{"developer", false},
	}
	for _, tt := range tests {
		matched := rl.RulesForRole(tt.role)
		got := len(matched) > 0
		if got != tt.match {
			t.Errorf("RulesForRole(%q): matched=%v, want %v", tt.role, got, tt.match)
		}
	}
}

func TestRulesForRole_WildcardAll(t *testing.T) {
	rl := &RuleList{
		Rules: []Rule{
			{Name: "all_rule", AppliesTo: []string{"*"}, Check: "exit_code_is(0)", Message: "m1"},
		},
	}

	for _, role := range []string{"developer", "reviewer.arch", "shipper"} {
		matched := rl.RulesForRole(role)
		if len(matched) != 1 {
			t.Errorf("RulesForRole(%q): matched=%d, want 1", role, len(matched))
		}
	}
}

func TestRulesForRole_MultipleAppliesTo(t *testing.T) {
	rl := &RuleList{
		Rules: []Rule{
			{Name: "multi_rule", AppliesTo: []string{"developer", "shipper"}, Check: "exit_code_is(0)", Message: "m1"},
		},
	}

	if len(rl.RulesForRole("developer")) != 1 {
		t.Error("should match developer")
	}
	if len(rl.RulesForRole("shipper")) != 1 {
		t.Error("should match shipper")
	}
	if len(rl.RulesForRole("reviewer.arch")) != 0 {
		t.Error("should not match reviewer.arch")
	}
}

func TestRoleGlobMatches(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"developer", "developer", true},
		{"developer", "developer.sub", false},
		{"reviewer.*", "reviewer.arch", true},
		{"reviewer.*", "reviewer.frontend", true},
		{"reviewer.*", "reviewer", false},
		{"reviewer.*", "xreviewer.arch", false},
		{"*", "anything", true},
		{"*.*", "a.b", true},
		{"*.*", "abc", false},
	}
	for _, tt := range tests {
		got := roleGlobMatches(tt.pattern, tt.name)
		if got != tt.want {
			t.Errorf("roleGlobMatches(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
		}
	}
}
```

### Step 2.3: Run loader tests

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./coding/supervisor/... -v -count=1 -run "TestLoadRules|TestRulesForRole|TestRoleGlobMatches"
```

Expected: all tests pass.

---

## Task 3: Predicate functions

**Goal:** Implement a registry of predicate functions that can be referenced by name in YAML `check` fields. Parse the `check` field syntax (e.g., `all_findings_have(["path", "line"])`) into function name + arguments. Evaluate predicates against an `EvalContext`.

**Files:**
- Create: `coding/supervisor/predicates.go`
- Create: `coding/supervisor/predicates_test.go`

### Step 3.1: Write `coding/supervisor/predicates.go`

- [ ] Create `coding/supervisor/predicates.go`:

```go
package supervisor

import (
	"fmt"
	"os/exec"
	"regexp"
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
	"all_findings_have":           allFindingsHave,
	"exit_code_is":                exitCodeIs,
	"git_current_branch_matches":  gitCurrentBranchMatches,
	"last_commit_msg_contains":    lastCommitMsgContains,
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
```

### Step 3.2: Write `coding/supervisor/predicates_test.go`

- [ ] Create `coding/supervisor/predicates_test.go`:

```go
package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/chris/coworker/core"
)

// --- ParseCheck tests ---

func TestParseCheck_ArrayArgs(t *testing.T) {
	fn, args, err := ParseCheck(`all_findings_have(["path", "line"])`)
	if err != nil {
		t.Fatalf("ParseCheck: %v", err)
	}
	if fn != "all_findings_have" {
		t.Errorf("funcName = %q, want %q", fn, "all_findings_have")
	}
	if len(args) != 2 || args[0] != "path" || args[1] != "line" {
		t.Errorf("args = %v, want [path, line]", args)
	}
}

func TestParseCheck_QuotedString(t *testing.T) {
	fn, args, err := ParseCheck(`git_current_branch_matches("^feature/plan-\\d+-")`)
	if err != nil {
		t.Fatalf("ParseCheck: %v", err)
	}
	if fn != "git_current_branch_matches" {
		t.Errorf("funcName = %q, want %q", fn, "git_current_branch_matches")
	}
	if len(args) != 1 || args[0] != `^feature/plan-\d+-` {
		t.Errorf("args = %v, want [^feature/plan-\\d+-]", args)
	}
}

func TestParseCheck_BareInteger(t *testing.T) {
	fn, args, err := ParseCheck(`exit_code_is(0)`)
	if err != nil {
		t.Fatalf("ParseCheck: %v", err)
	}
	if fn != "exit_code_is" {
		t.Errorf("funcName = %q, want %q", fn, "exit_code_is")
	}
	if len(args) != 1 || args[0] != "0" {
		t.Errorf("args = %v, want [0]", args)
	}
}

func TestParseCheck_InvalidSyntax(t *testing.T) {
	tests := []string{
		"no_parens",
		"",
		"bad(",
		"bad)",
	}
	for _, check := range tests {
		_, _, err := ParseCheck(check)
		if err == nil {
			t.Errorf("ParseCheck(%q): expected error, got nil", check)
		}
	}
}

// --- allFindingsHave tests ---

func TestAllFindingsHave_AllPresent(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{
			Findings: []core.Finding{
				{Path: "main.go", Line: 10, Severity: "important", Body: "fix this"},
				{Path: "store.go", Line: 20, Severity: "minor", Body: "nit"},
			},
		},
	}

	passed, err := allFindingsHave(ctx, []string{"path", "line"})
	if err != nil {
		t.Fatalf("allFindingsHave: %v", err)
	}
	if !passed {
		t.Error("expected pass, got fail")
	}
}

func TestAllFindingsHave_MissingPath(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{
			Findings: []core.Finding{
				{Path: "main.go", Line: 10},
				{Path: "", Line: 20}, // missing path
			},
		},
	}

	passed, err := allFindingsHave(ctx, []string{"path"})
	if err != nil {
		t.Fatalf("allFindingsHave: %v", err)
	}
	if passed {
		t.Error("expected fail, got pass")
	}
}

func TestAllFindingsHave_MissingLine(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{
			Findings: []core.Finding{
				{Path: "main.go", Line: 0}, // line 0 = missing
			},
		},
	}

	passed, err := allFindingsHave(ctx, []string{"line"})
	if err != nil {
		t.Fatalf("allFindingsHave: %v", err)
	}
	if passed {
		t.Error("expected fail for line=0, got pass")
	}
}

func TestAllFindingsHave_NoFindings(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{
			Findings: nil,
		},
	}

	passed, err := allFindingsHave(ctx, []string{"path", "line"})
	if err != nil {
		t.Fatalf("allFindingsHave: %v", err)
	}
	if !passed {
		t.Error("expected vacuous pass for empty findings, got fail")
	}
}

func TestAllFindingsHave_UnknownField(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{
			Findings: []core.Finding{
				{Path: "main.go", Line: 10},
			},
		},
	}

	_, err := allFindingsHave(ctx, []string{"nonexistent"})
	if err == nil {
		t.Error("expected error for unknown field, got nil")
	}
}

func TestAllFindingsHave_NoArgs(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{},
	}

	_, err := allFindingsHave(ctx, nil)
	if err == nil {
		t.Error("expected error for no args, got nil")
	}
}

func TestAllFindingsHave_SeverityAndBody(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{
			Findings: []core.Finding{
				{Path: "a.go", Line: 1, Severity: "critical", Body: "bad"},
				{Path: "b.go", Line: 2, Severity: "", Body: "ok"}, // missing severity
			},
		},
	}

	passed, err := allFindingsHave(ctx, []string{"severity"})
	if err != nil {
		t.Fatalf("allFindingsHave: %v", err)
	}
	if passed {
		t.Error("expected fail for missing severity, got pass")
	}
}

// --- exitCodeIs tests ---

func TestExitCodeIs_Match(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{ExitCode: 0},
	}
	passed, err := exitCodeIs(ctx, []string{"0"})
	if err != nil {
		t.Fatalf("exitCodeIs: %v", err)
	}
	if !passed {
		t.Error("expected pass")
	}
}

func TestExitCodeIs_NoMatch(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{ExitCode: 1},
	}
	passed, err := exitCodeIs(ctx, []string{"0"})
	if err != nil {
		t.Fatalf("exitCodeIs: %v", err)
	}
	if passed {
		t.Error("expected fail")
	}
}

func TestExitCodeIs_InvalidArg(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{ExitCode: 0},
	}
	_, err := exitCodeIs(ctx, []string{"not_a_number"})
	if err == nil {
		t.Error("expected error for non-integer arg, got nil")
	}
}

func TestExitCodeIs_WrongArgCount(t *testing.T) {
	ctx := &EvalContext{
		Result: &core.JobResult{ExitCode: 0},
	}
	_, err := exitCodeIs(ctx, []string{"0", "1"})
	if err == nil {
		t.Error("expected error for wrong arg count, got nil")
	}
}

// --- gitCurrentBranchMatches tests ---

func TestGitCurrentBranchMatches(t *testing.T) {
	// Create a temporary git repo.
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "checkout", "-b", "feature/plan-101-supervisor")
	// Need at least one commit for HEAD to exist.
	writeFile(t, filepath.Join(tmpDir, "dummy.txt"), "x")
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "init")

	ctx := &EvalContext{
		WorkDir: tmpDir,
		Result:  &core.JobResult{},
	}

	// Should match.
	passed, err := gitCurrentBranchMatches(ctx, []string{`^feature/plan-\d+-`})
	if err != nil {
		t.Fatalf("gitCurrentBranchMatches: %v", err)
	}
	if !passed {
		t.Error("expected match for feature/plan-101-supervisor")
	}

	// Should not match.
	passed, err = gitCurrentBranchMatches(ctx, []string{`^main$`})
	if err != nil {
		t.Fatalf("gitCurrentBranchMatches: %v", err)
	}
	if passed {
		t.Error("expected no match for ^main$")
	}
}

func TestGitCurrentBranchMatches_InvalidRegex(t *testing.T) {
	ctx := &EvalContext{
		WorkDir: t.TempDir(),
		Result:  &core.JobResult{},
	}
	_, err := gitCurrentBranchMatches(ctx, []string{"[invalid"})
	if err == nil {
		t.Error("expected error for invalid regex, got nil")
	}
}

// --- lastCommitMsgContains tests ---

func TestLastCommitMsgContains(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	writeFile(t, filepath.Join(tmpDir, "dummy.txt"), "x")
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "Phase 2: implement supervisor")

	ctx := &EvalContext{
		WorkDir: tmpDir,
		Result:  &core.JobResult{},
	}

	passed, err := lastCommitMsgContains(ctx, []string{`Phase \d+:`})
	if err != nil {
		t.Fatalf("lastCommitMsgContains: %v", err)
	}
	if !passed {
		t.Error("expected match for Phase 2:")
	}

	passed, err = lastCommitMsgContains(ctx, []string{`^BREAKING:`})
	if err != nil {
		t.Fatalf("lastCommitMsgContains: %v", err)
	}
	if passed {
		t.Error("expected no match for ^BREAKING:")
	}
}

// --- LookupPredicate tests ---

func TestLookupPredicate_Known(t *testing.T) {
	known := []string{"all_findings_have", "exit_code_is", "git_current_branch_matches", "last_commit_msg_contains"}
	for _, name := range known {
		fn, err := LookupPredicate(name)
		if err != nil {
			t.Errorf("LookupPredicate(%q): %v", name, err)
		}
		if fn == nil {
			t.Errorf("LookupPredicate(%q) returned nil", name)
		}
	}
}

func TestLookupPredicate_Unknown(t *testing.T) {
	_, err := LookupPredicate("nonexistent_predicate")
	if err == nil {
		t.Error("expected error for unknown predicate, got nil")
	}
}

// --- test helpers ---

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Set git author for commits.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}
```

### Step 3.3: Run predicate tests

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./coding/supervisor/... -v -count=1 -run "TestParseCheck|TestAllFindings|TestExitCode|TestGitCurrent|TestLastCommit|TestLookup"
```

Expected: all predicate tests pass.

---

## Task 4: Rule engine (evaluate)

**Goal:** Implement `RuleEngine` that loads rules, filters by role, evaluates predicates, and returns a `SupervisorVerdict`. The engine is pure logic — it does NOT write events (the dispatcher does that).

**Files:**
- Create: `coding/supervisor/engine.go`
- Create: `coding/supervisor/engine_test.go`

### Step 4.1: Write `coding/supervisor/engine.go`

- [ ] Create `coding/supervisor/engine.go`:

```go
package supervisor

import (
	"fmt"

	"github.com/chris/coworker/core"
)

// RuleEngine evaluates contract rules against job outputs.
// It is pure logic — event writing is the caller's responsibility.
type RuleEngine struct {
	rules *RuleList
}

// NewRuleEngine creates an engine from a loaded rule list.
func NewRuleEngine(rules *RuleList) *RuleEngine {
	return &RuleEngine{rules: rules}
}

// NewRuleEngineFromFile loads rules from a YAML file and creates an engine.
func NewRuleEngineFromFile(path string) (*RuleEngine, error) {
	rules, err := LoadRulesFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("load rules: %w", err)
	}
	return NewRuleEngine(rules), nil
}

// NewRuleEngineFromBytes loads rules from YAML bytes and creates an engine.
func NewRuleEngineFromBytes(data []byte) (*RuleEngine, error) {
	rules, err := LoadRulesFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("load rules: %w", err)
	}
	return NewRuleEngine(rules), nil
}

// Evaluate runs all applicable rules for the given role against the
// job result and returns a SupervisorVerdict. All rules are evaluated
// even if some fail (no short-circuit) so the feedback includes all
// violations.
func (e *RuleEngine) Evaluate(ctx *EvalContext) (*core.SupervisorVerdict, error) {
	if ctx.Role == nil {
		return nil, fmt.Errorf("EvalContext.Role must not be nil")
	}

	applicable := e.rules.RulesForRole(ctx.Role.Name)

	// If no rules apply, verdict is pass.
	if len(applicable) == 0 {
		return &core.SupervisorVerdict{Pass: true}, nil
	}

	verdict := &core.SupervisorVerdict{Pass: true}

	for _, rule := range applicable {
		funcName, args, err := ParseCheck(rule.Check)
		if err != nil {
			verdict.Pass = false
			verdict.Results = append(verdict.Results, core.RuleResult{
				RuleName: rule.Name,
				Passed:   false,
				Message:  fmt.Sprintf("failed to parse check %q: %v", rule.Check, err),
			})
			continue
		}

		predFn, err := LookupPredicate(funcName)
		if err != nil {
			verdict.Pass = false
			verdict.Results = append(verdict.Results, core.RuleResult{
				RuleName: rule.Name,
				Passed:   false,
				Message:  fmt.Sprintf("unknown predicate %q in rule %q", funcName, rule.Name),
			})
			continue
		}

		passed, err := predFn(ctx, args)
		if err != nil {
			verdict.Pass = false
			verdict.Results = append(verdict.Results, core.RuleResult{
				RuleName: rule.Name,
				Passed:   false,
				Message:  fmt.Sprintf("predicate %q error: %v", funcName, err),
			})
			continue
		}

		result := core.RuleResult{
			RuleName: rule.Name,
			Passed:   passed,
			Message:  rule.Message,
		}
		if !passed {
			verdict.Pass = false
		}
		verdict.Results = append(verdict.Results, result)
	}

	return verdict, nil
}

// RuleCount returns the total number of loaded rules.
func (e *RuleEngine) RuleCount() int {
	return len(e.rules.Rules)
}

// RulesForRole returns the rules that apply to the given role name.
func (e *RuleEngine) RulesForRole(roleName string) []Rule {
	return e.rules.RulesForRole(roleName)
}
```

### Step 4.2: Write `coding/supervisor/engine_test.go`

- [ ] Create `coding/supervisor/engine_test.go`:

```go
package supervisor

import (
	"testing"

	"github.com/chris/coworker/core"
)

func makeEngine(t *testing.T, yaml string) *RuleEngine {
	t.Helper()
	engine, err := NewRuleEngineFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("NewRuleEngineFromBytes: %v", err)
	}
	return engine
}

func TestEngine_AllRulesPass(t *testing.T) {
	engine := makeEngine(t, `
rules:
  findings_have_path_line:
    applies_to: [reviewer.*]
    check: all_findings_have(["path", "line"])
    message: "All findings must have path and line"
  exit_zero:
    applies_to: [reviewer.*]
    check: exit_code_is(0)
    message: "Job must exit with code 0"
`)

	ctx := &EvalContext{
		Role:   &core.Role{Name: "reviewer.arch"},
		Result: &core.JobResult{
			ExitCode: 0,
			Findings: []core.Finding{
				{Path: "main.go", Line: 10, Severity: "important", Body: "fix"},
			},
		},
		Job:   &core.Job{ID: "j1", RunID: "r1", Role: "reviewer.arch"},
		RunID: "r1",
	}

	verdict, err := engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !verdict.Pass {
		t.Error("expected pass")
		for _, r := range verdict.Results {
			t.Logf("  %s: passed=%v msg=%s", r.RuleName, r.Passed, r.Message)
		}
	}
	if len(verdict.Results) != 2 {
		t.Errorf("results count = %d, want 2", len(verdict.Results))
	}
}

func TestEngine_OneRuleFails(t *testing.T) {
	engine := makeEngine(t, `
rules:
  findings_have_path_line:
    applies_to: [reviewer.*]
    check: all_findings_have(["path", "line"])
    message: "All findings must have path and line"
  exit_zero:
    applies_to: [reviewer.*]
    check: exit_code_is(0)
    message: "Job must exit with code 0"
`)

	ctx := &EvalContext{
		Role:   &core.Role{Name: "reviewer.arch"},
		Result: &core.JobResult{
			ExitCode: 0,
			Findings: []core.Finding{
				{Path: "", Line: 10}, // missing path -> rule fails
			},
		},
		Job:   &core.Job{ID: "j1", RunID: "r1", Role: "reviewer.arch"},
		RunID: "r1",
	}

	verdict, err := engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if verdict.Pass {
		t.Error("expected fail (missing path in finding)")
	}

	// exit_code_is(0) should still pass.
	var failCount, passCount int
	for _, r := range verdict.Results {
		if r.Passed {
			passCount++
		} else {
			failCount++
		}
	}
	if failCount != 1 {
		t.Errorf("failCount = %d, want 1", failCount)
	}
	if passCount != 1 {
		t.Errorf("passCount = %d, want 1", passCount)
	}
}

func TestEngine_NoApplicableRules(t *testing.T) {
	engine := makeEngine(t, `
rules:
  dev_rule:
    applies_to: [developer]
    check: exit_code_is(0)
    message: "exit 0"
`)

	ctx := &EvalContext{
		Role:   &core.Role{Name: "reviewer.arch"},
		Result: &core.JobResult{ExitCode: 1},
		Job:    &core.Job{ID: "j1", RunID: "r1", Role: "reviewer.arch"},
		RunID:  "r1",
	}

	verdict, err := engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !verdict.Pass {
		t.Error("expected pass when no rules apply")
	}
	if len(verdict.Results) != 0 {
		t.Errorf("results count = %d, want 0", len(verdict.Results))
	}
}

func TestEngine_UnknownPredicate(t *testing.T) {
	engine := makeEngine(t, `
rules:
  bad_check:
    applies_to: [reviewer.*]
    check: nonexistent_predicate("arg")
    message: "should fail"
`)

	ctx := &EvalContext{
		Role:   &core.Role{Name: "reviewer.arch"},
		Result: &core.JobResult{},
		Job:    &core.Job{ID: "j1", RunID: "r1", Role: "reviewer.arch"},
		RunID:  "r1",
	}

	verdict, err := engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// Unknown predicate -> rule fails, verdict fails.
	if verdict.Pass {
		t.Error("expected fail for unknown predicate")
	}
	if len(verdict.Results) != 1 {
		t.Fatalf("results count = %d, want 1", len(verdict.Results))
	}
	if verdict.Results[0].Passed {
		t.Error("expected rule result to be failed")
	}
}

func TestEngine_NilRoleErrors(t *testing.T) {
	engine := makeEngine(t, `
rules:
  r1:
    applies_to: [developer]
    check: exit_code_is(0)
    message: "m"
`)

	_, err := engine.Evaluate(&EvalContext{Role: nil})
	if err == nil {
		t.Error("expected error for nil Role, got nil")
	}
}

func TestEngine_FailedMessages(t *testing.T) {
	verdict := &core.SupervisorVerdict{
		Pass: false,
		Results: []core.RuleResult{
			{RuleName: "r1", Passed: true, Message: "ok"},
			{RuleName: "r2", Passed: false, Message: "findings need path"},
			{RuleName: "r3", Passed: false, Message: "wrong exit code"},
		},
	}

	msgs := verdict.FailedMessages()
	if len(msgs) != 2 {
		t.Fatalf("FailedMessages() len = %d, want 2", len(msgs))
	}
	if msgs[0] != "findings need path" {
		t.Errorf("msgs[0] = %q, want %q", msgs[0], "findings need path")
	}
	if msgs[1] != "wrong exit code" {
		t.Errorf("msgs[1] = %q, want %q", msgs[1], "wrong exit code")
	}
}

func TestEngine_GlobMatchMultipleRoles(t *testing.T) {
	engine := makeEngine(t, `
rules:
  rev_findings:
    applies_to: [reviewer.*]
    check: all_findings_have(["path", "line"])
    message: "findings must have path and line"
  all_exit:
    applies_to: ["*"]
    check: exit_code_is(0)
    message: "must exit 0"
`)

	// reviewer.arch should get both rules.
	ctx := &EvalContext{
		Role:   &core.Role{Name: "reviewer.arch"},
		Result: &core.JobResult{
			ExitCode: 0,
			Findings: []core.Finding{
				{Path: "a.go", Line: 1},
			},
		},
		Job:   &core.Job{ID: "j1", RunID: "r1", Role: "reviewer.arch"},
		RunID: "r1",
	}

	verdict, err := engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(verdict.Results) != 2 {
		t.Errorf("results count = %d, want 2", len(verdict.Results))
	}

	// developer should only get the wildcard rule.
	ctx.Role = &core.Role{Name: "developer"}
	ctx.Job.Role = "developer"
	verdict, err = engine.Evaluate(ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(verdict.Results) != 1 {
		t.Errorf("results count = %d, want 1 (wildcard only)", len(verdict.Results))
	}
}

func TestEngine_RuleCount(t *testing.T) {
	engine := makeEngine(t, `
rules:
  r1:
    applies_to: [developer]
    check: exit_code_is(0)
    message: "m1"
  r2:
    applies_to: [reviewer.*]
    check: all_findings_have(["path"])
    message: "m2"
`)
	if engine.RuleCount() != 2 {
		t.Errorf("RuleCount() = %d, want 2", engine.RuleCount())
	}
}
```

### Step 4.3: Run engine tests

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./coding/supervisor/... -v -count=1 -run "TestEngine"
```

Expected: all engine tests pass.

---

## Task 5: Seed rule catalog + dispatcher retry integration

**Goal:** Create the built-in rule YAML for reviewer roles. Modify `Dispatcher.Orchestrate` to evaluate rules after each job, retry with feedback on failure, and emit `supervisor.verdict` and `supervisor.retry` events.

**Files:**
- Create: `coding/supervisor/rules.yaml`
- Modify: `coding/dispatch.go`
- Modify: `coding/dispatch_test.go`
- Create: `testdata/mocks/codex-bad-findings`
- Create: `testdata/mocks/codex-retry-then-pass`

### Step 5.1: Write the seed rule catalog

- [ ] Create `coding/supervisor/rules.yaml`:

```yaml
# Seed rule catalog — built-in contract rules for the supervisor.
# Per-repo overrides go in .coworker/rules/supervisor-contract.yaml.
rules:
  reviewer_findings_line_anchored:
    applies_to: [reviewer.*]
    check: all_findings_have(["path", "line"])
    message: "All reviewer findings must have both path and line references"

  reviewer_findings_have_severity:
    applies_to: [reviewer.*]
    check: all_findings_have(["severity"])
    message: "All reviewer findings must include a severity level"

  reviewer_findings_have_body:
    applies_to: [reviewer.*]
    check: all_findings_have(["body"])
    message: "All reviewer findings must include a body description"
```

### Step 5.2: Add `SupervisorConfig` to `Dispatcher` and modify `Orchestrate`

- [ ] Modify `coding/dispatch.go` to add the supervisor hook. Replace the entire file with:

```go
package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chris/coworker/coding/roles"
	"github.com/chris/coworker/coding/supervisor"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// DefaultMaxRetries is the default maximum number of supervisor retries
// before escalating to a compliance-breach event.
const DefaultMaxRetries = 3

// Dispatcher orchestrates the end-to-end flow: load role -> create run/job
// -> render prompt -> dispatch agent -> capture result -> supervisor check
// -> persist findings (with retry loop on contract failure).
type Dispatcher struct {
	RoleDir    string // path to directory containing role YAML files
	PromptDir  string // path to directory containing prompt template files
	Agent      core.Agent
	DB         *store.DB
	Logger     *slog.Logger

	// Supervisor is the optional rule engine. If nil, no contract
	// checks are performed (equivalent to all-pass).
	Supervisor *supervisor.RuleEngine

	// MaxRetries is the maximum number of supervisor retries per job.
	// Zero means use DefaultMaxRetries. Negative means no retries.
	MaxRetries int

	// WorkDir is the working directory for git-based predicates.
	// If empty, git predicates use the current working directory.
	WorkDir string
}

// DispatchInput contains the inputs for a dispatch operation.
type DispatchInput struct {
	RoleName string
	Inputs   map[string]string // required inputs (e.g., "diff_path", "spec_path")
}

// DispatchResult contains the output of a dispatch operation.
type DispatchResult struct {
	RunID    string
	JobID    string
	Findings []core.Finding
	ExitCode int

	// SupervisorVerdict is the final verdict from the last evaluation.
	// Nil if no supervisor engine was configured.
	SupervisorVerdict *core.SupervisorVerdict

	// RetryCount is the number of supervisor retries that occurred.
	RetryCount int
}

// Orchestrate runs the full dispatch pipeline for an ephemeral job.
func (d *Dispatcher) Orchestrate(ctx context.Context, input *DispatchInput) (*DispatchResult, error) {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// 1. Load the role.
	role, err := roles.LoadRole(d.RoleDir, input.RoleName)
	if err != nil {
		return nil, fmt.Errorf("load role: %w", err)
	}
	logger.Info("loaded role", "name", role.Name, "cli", role.CLI)

	// 2. Validate required inputs.
	for _, req := range role.Inputs.Required {
		if _, ok := input.Inputs[req]; !ok {
			return nil, fmt.Errorf("missing required input %q for role %q", req, role.Name)
		}
	}

	// 3. Create the stores.
	eventStore := store.NewEventStore(d.DB)
	runStore := store.NewRunStore(d.DB, eventStore)
	jobStore := store.NewJobStore(d.DB, eventStore)
	findingStore := store.NewFindingStore(d.DB, eventStore)

	// 4. Create a run.
	runID := core.NewID()
	run := &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := runStore.CreateRun(ctx, run); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	logger.Info("created run", "id", runID)

	// 6. Render the prompt template.
	tmpl, err := roles.LoadPromptTemplate(d.PromptDir, role.PromptTemplate)
	if err != nil {
		return nil, fmt.Errorf("load prompt template: %w", err)
	}

	// Build template data from inputs.
	tmplData := make(map[string]string)
	for k, v := range input.Inputs {
		tmplData[snakeToPascal(k)] = v
	}

	originalPrompt, err := roles.RenderPrompt(tmpl, tmplData)
	if err != nil {
		return nil, fmt.Errorf("render prompt: %w", err)
	}

	maxRetries := d.maxRetries()
	var lastVerdict *core.SupervisorVerdict
	var lastJobID string
	var lastResult *core.JobResult
	retryCount := 0
	prompt := originalPrompt

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// 5. Create a job.
		jobID := core.NewID()
		dispatchedBy := "user"
		if attempt > 0 {
			dispatchedBy = "supervisor-retry"
		}
		job := &core.Job{
			ID:           jobID,
			RunID:        runID,
			Role:         role.Name,
			State:        core.JobStatePending,
			DispatchedBy: dispatchedBy,
			CLI:          role.CLI,
			StartedAt:    time.Now(),
		}
		if err := jobStore.CreateJob(ctx, job); err != nil {
			return nil, fmt.Errorf("create job: %w", err)
		}
		logger.Info("created job", "id", jobID, "role", role.Name, "attempt", attempt)

		// 7. Update job state to dispatched.
		if err := jobStore.UpdateJobState(ctx, jobID, core.JobStateDispatched); err != nil {
			return nil, fmt.Errorf("update job to dispatched: %w", err)
		}

		// 8. Dispatch to the agent.
		handle, err := d.Agent.Dispatch(ctx, job, prompt)
		if err != nil {
			jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed) //nolint:errcheck
			return nil, fmt.Errorf("dispatch agent: %w", err)
		}
		logger.Info("dispatched to agent", "cli", role.CLI, "attempt", attempt)

		// 9. Wait for result.
		result, err := handle.Wait(ctx)
		if err != nil {
			jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed) //nolint:errcheck
			return nil, fmt.Errorf("wait for agent: %w", err)
		}
		logger.Info("agent completed", "findings", len(result.Findings), "exit_code", result.ExitCode, "attempt", attempt)

		lastJobID = jobID
		lastResult = result

		// 10. Supervisor evaluation (if configured).
		if d.Supervisor != nil {
			evalCtx := &supervisor.EvalContext{
				Job:     job,
				Result:  result,
				Role:    role,
				RunID:   runID,
				WorkDir: d.WorkDir,
			}

			verdict, evalErr := d.Supervisor.Evaluate(evalCtx)
			if evalErr != nil {
				logger.Error("supervisor evaluation error", "error", evalErr)
				// Treat evaluation error as a pass — don't block the job
				// for engine bugs.
				verdict = &core.SupervisorVerdict{Pass: true}
			}
			lastVerdict = verdict

			// Emit supervisor.verdict event.
			verdictPayload := d.marshalVerdictPayload(jobID, verdict)
			verdictEvent := &core.Event{
				ID:            core.NewID(),
				RunID:         runID,
				Kind:          core.EventSupervisorVerdict,
				SchemaVersion: 1,
				CorrelationID: jobID,
				Payload:       verdictPayload,
				CreatedAt:     time.Now(),
			}
			if writeErr := eventStore.WriteEventThenRow(ctx, verdictEvent, nil); writeErr != nil {
				logger.Error("failed to write supervisor.verdict event", "error", writeErr)
			}

			if !verdict.Pass && attempt < maxRetries {
				// Retry: update current job to failed, emit retry event.
				jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed) //nolint:errcheck

				feedback := d.buildRetryFeedback(verdict)
				prompt = feedback + "\n\n" + originalPrompt
				retryCount++

				// Emit supervisor.retry event.
				retryPayload, _ := json.Marshal(map[string]interface{}{
					"job_id":       jobID,
					"attempt":      attempt + 1,
					"feedback":     feedback,
					"failed_rules": verdict.FailedMessages(),
				})
				retryEvent := &core.Event{
					ID:            core.NewID(),
					RunID:         runID,
					Kind:          core.EventSupervisorRetry,
					SchemaVersion: 1,
					CorrelationID: jobID,
					Payload:       string(retryPayload),
					CreatedAt:     time.Now(),
				}
				if writeErr := eventStore.WriteEventThenRow(ctx, retryEvent, nil); writeErr != nil {
					logger.Error("failed to write supervisor.retry event", "error", writeErr)
				}

				logger.Info("supervisor retry", "attempt", attempt+1, "failed_rules", len(verdict.FailedMessages()))
				continue
			}

			if !verdict.Pass && attempt >= maxRetries {
				// Max retries exhausted — escalate.
				logger.Warn("supervisor max retries exhausted, emitting compliance-breach",
					"job_id", jobID, "attempts", attempt+1)

				breachPayload, _ := json.Marshal(map[string]interface{}{
					"job_id":       jobID,
					"role":         role.Name,
					"failed_rules": verdict.FailedMessages(),
					"attempts":     attempt + 1,
				})
				breachEvent := &core.Event{
					ID:            core.NewID(),
					RunID:         runID,
					Kind:          core.EventComplianceBreach,
					SchemaVersion: 1,
					CorrelationID: jobID,
					Payload:       string(breachPayload),
					CreatedAt:     time.Now(),
				}
				if writeErr := eventStore.WriteEventThenRow(ctx, breachEvent, nil); writeErr != nil {
					logger.Error("failed to write compliance-breach event", "error", writeErr)
				}
			}
		}

		// Supervisor passed (or not configured) — persist findings and finalize.
		break
	}

	// 11. Persist findings from the final attempt.
	for i := range lastResult.Findings {
		f := &lastResult.Findings[i]
		f.RunID = runID
		f.JobID = lastJobID
		if f.ID == "" {
			f.ID = core.NewID()
		}
		if err := findingStore.InsertFinding(ctx, f); err != nil {
			logger.Error("failed to persist finding", "error", err, "path", f.Path, "line", f.Line)
		}
	}

	// 12. Update job state to complete (or failed).
	finalState := core.JobStateComplete
	if lastResult.ExitCode != 0 {
		finalState = core.JobStateFailed
	}
	// If supervisor verdict failed after max retries, mark as failed.
	if lastVerdict != nil && !lastVerdict.Pass {
		finalState = core.JobStateFailed
	}
	if err := jobStore.UpdateJobState(ctx, lastJobID, finalState); err != nil {
		return nil, fmt.Errorf("update job to %s: %w", finalState, err)
	}

	// 13. Complete the run.
	runState := core.RunStateCompleted
	if finalState == core.JobStateFailed {
		runState = core.RunStateFailed
	}
	if err := runStore.CompleteRun(ctx, runID, runState); err != nil {
		return nil, fmt.Errorf("complete run: %w", err)
	}

	return &DispatchResult{
		RunID:             runID,
		JobID:             lastJobID,
		Findings:          lastResult.Findings,
		ExitCode:          lastResult.ExitCode,
		SupervisorVerdict: lastVerdict,
		RetryCount:        retryCount,
	}, nil
}

// maxRetries returns the effective max retry count.
func (d *Dispatcher) maxRetries() int {
	if d.MaxRetries < 0 {
		return 0
	}
	if d.MaxRetries == 0 {
		return DefaultMaxRetries
	}
	return d.MaxRetries
}

// buildRetryFeedback constructs the supervisor feedback string
// prepended to the prompt on retry.
func (d *Dispatcher) buildRetryFeedback(verdict *core.SupervisorVerdict) string {
	msgs := verdict.FailedMessages()
	var sb strings.Builder
	sb.WriteString("SUPERVISOR FEEDBACK: The following contract rules were violated:\n")
	for i, msg := range msgs {
		sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, msg))
	}
	sb.WriteString("Please fix these issues and try again.")
	return sb.String()
}

// marshalVerdictPayload serializes a verdict to JSON for the event payload.
func (d *Dispatcher) marshalVerdictPayload(jobID string, verdict *core.SupervisorVerdict) string {
	type resultJSON struct {
		RuleName string `json:"rule_name"`
		Passed   bool   `json:"passed"`
		Message  string `json:"message"`
	}
	var results []resultJSON
	for _, r := range verdict.Results {
		results = append(results, resultJSON{
			RuleName: r.RuleName,
			Passed:   r.Passed,
			Message:  r.Message,
		})
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"job_id":  jobID,
		"pass":    verdict.Pass,
		"results": results,
	})
	return string(payload)
}

// snakeToPascal converts "diff_path" to "DiffPath".
func snakeToPascal(s string) string {
	parts := splitOn(s, '_')
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = string(upper(p[0])) + p[1:]
		}
	}
	result := ""
	for _, p := range parts {
		result += p
	}
	return result
}

func splitOn(s string, sep byte) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func upper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 32
	}
	return b
}
```

### Step 5.3: Create mock that emits findings without path/line

- [ ] Create `testdata/mocks/codex-bad-findings`:

```bash
#!/bin/bash
# Mock codex CLI that emits findings missing path and line fields.
# Used to test supervisor contract failure.

cat > /dev/null

echo '{"type":"finding","path":"","line":0,"severity":"important","body":"Missing error check"}'
echo '{"type":"finding","path":"","line":0,"severity":"","body":""}'
echo '{"type":"done","exit_code":0}'
```

- [ ] Make it executable:

```bash
chmod +x /home/chris/workshop/coworker/testdata/mocks/codex-bad-findings
```

### Step 5.4: Create mock that fails on first call, passes on second

- [ ] Create `testdata/mocks/codex-retry-then-pass`:

```bash
#!/bin/bash
# Mock codex CLI that returns bad findings on first call and good
# findings on second call. Uses a state file to track attempts.
#
# The state file path is passed via the COWORKER_MOCK_STATE env var.
# If not set, defaults to /tmp/codex-retry-state.

cat > /dev/null

STATE_FILE="${COWORKER_MOCK_STATE:-/tmp/codex-retry-state}"

if [ ! -f "$STATE_FILE" ]; then
    # First call: emit bad findings (missing path/line).
    echo '{"type":"finding","path":"","line":0,"severity":"important","body":"Missing error check"}'
    echo '{"type":"done","exit_code":0}'
    echo "1" > "$STATE_FILE"
else
    # Subsequent calls: emit good findings.
    echo '{"type":"finding","path":"main.go","line":42,"severity":"important","body":"Missing error check on Close()"}'
    echo '{"type":"finding","path":"store.go","line":17,"severity":"minor","body":"Consider using prepared statement"}'
    echo '{"type":"done","exit_code":0}'
fi
```

- [ ] Make it executable:

```bash
chmod +x /home/chris/workshop/coworker/testdata/mocks/codex-retry-then-pass
```

### Step 5.5: Update `coding/dispatch_test.go` with supervisor tests

- [ ] Replace `coding/dispatch_test.go` with:

```go
package coding

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding/supervisor"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// coding/ is one level below the repo root.
	return filepath.Dir(wd)
}

func TestOrchestrate_WithMockCodex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex")
	if _, err := os.Stat(mockBin); err != nil {
		t.Fatalf("mock binary not found: %v", err)
	}

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	d := &Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     agent.NewCliAgent(mockBin),
		DB:        db,
	}

	ctx := context.Background()
	result, err := d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	if result.RunID == "" {
		t.Error("run ID should not be empty")
	}
	if result.JobID == "" {
		t.Error("job ID should not be empty")
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}
	if len(result.Findings) != 2 {
		t.Fatalf("findings count = %d, want 2", len(result.Findings))
	}

	// Verify findings were persisted.
	findingStore := store.NewFindingStore(db, store.NewEventStore(db))
	findings, err := findingStore.ListFindings(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(findings) != 2 {
		t.Errorf("persisted findings = %d, want 2", len(findings))
	}

	// Verify run was completed.
	runStore := store.NewRunStore(db, store.NewEventStore(db))
	run, err := runStore.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.State != core.RunStateCompleted {
		t.Errorf("run state = %q, want %q", run.State, core.RunStateCompleted)
	}

	// Verify job was completed.
	jobStore := store.NewJobStore(db, store.NewEventStore(db))
	job, err := jobStore.GetJob(ctx, result.JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.State != core.JobStateComplete {
		t.Errorf("job state = %q, want %q", job.State, core.JobStateComplete)
	}

	// Verify event log.
	events, err := store.NewEventStore(db).ListEvents(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	// Expected events: run.created, job.created, job.leased (dispatched),
	// finding.created x2, job.completed, run.completed = 7
	if len(events) != 7 {
		t.Errorf("event count = %d, want 7", len(events))
		for i, e := range events {
			t.Logf("  event[%d]: seq=%d kind=%s", i, e.Sequence, e.Kind)
		}
	}
}

func TestOrchestrate_WithSupervisor_AllPass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex")

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	// The mock codex emits findings with path and line, so
	// all_findings_have(["path", "line"]) should pass.
	engine, err := supervisor.NewRuleEngineFromBytes([]byte(`
rules:
  findings_have_path_line:
    applies_to: [reviewer.*]
    check: all_findings_have(["path", "line"])
    message: "findings must have path and line"
`))
	if err != nil {
		t.Fatalf("NewRuleEngineFromBytes: %v", err)
	}

	d := &Dispatcher{
		RoleDir:    filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:  filepath.Join(repoRoot, "coding"),
		Agent:      agent.NewCliAgent(mockBin),
		DB:         db,
		Supervisor: engine,
	}

	ctx := context.Background()
	result, err := d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	if result.SupervisorVerdict == nil {
		t.Fatal("expected supervisor verdict, got nil")
	}
	if !result.SupervisorVerdict.Pass {
		t.Error("expected supervisor pass")
	}
	if result.RetryCount != 0 {
		t.Errorf("retry count = %d, want 0", result.RetryCount)
	}

	// Verify supervisor.verdict event was emitted.
	events, err := store.NewEventStore(db).ListEvents(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var hasVerdictEvent bool
	for _, e := range events {
		if e.Kind == core.EventSupervisorVerdict {
			hasVerdictEvent = true
			break
		}
	}
	if !hasVerdictEvent {
		t.Error("expected supervisor.verdict event in event log")
		for i, e := range events {
			t.Logf("  event[%d]: seq=%d kind=%s", i, e.Sequence, e.Kind)
		}
	}
}

func TestOrchestrate_WithSupervisor_RetryThenPass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex-retry-then-pass")
	if _, err := os.Stat(mockBin); err != nil {
		t.Fatalf("mock binary not found: %v", err)
	}

	// Create a unique state file for this test.
	stateFile := filepath.Join(t.TempDir(), "retry-state")
	t.Setenv("COWORKER_MOCK_STATE", stateFile)
	t.Cleanup(func() { os.Remove(stateFile) })

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	engine, err := supervisor.NewRuleEngineFromBytes([]byte(`
rules:
  findings_have_path:
    applies_to: [reviewer.*]
    check: all_findings_have(["path"])
    message: "findings must have path"
  findings_have_line:
    applies_to: [reviewer.*]
    check: all_findings_have(["line"])
    message: "findings must have line"
`))
	if err != nil {
		t.Fatalf("NewRuleEngineFromBytes: %v", err)
	}

	d := &Dispatcher{
		RoleDir:    filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:  filepath.Join(repoRoot, "coding"),
		Agent:      agent.NewCliAgent(mockBin),
		DB:         db,
		Supervisor: engine,
		MaxRetries: 3,
	}

	ctx := context.Background()
	result, err := d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	// Should have retried once, then passed.
	if result.RetryCount != 1 {
		t.Errorf("retry count = %d, want 1", result.RetryCount)
	}
	if result.SupervisorVerdict == nil {
		t.Fatal("expected supervisor verdict")
	}
	if !result.SupervisorVerdict.Pass {
		t.Error("expected final verdict to pass")
	}
	// Final findings should be the good ones (2 findings with path/line).
	if len(result.Findings) != 2 {
		t.Errorf("findings count = %d, want 2", len(result.Findings))
	}

	// Verify events include supervisor.verdict and supervisor.retry.
	events, err := store.NewEventStore(db).ListEvents(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var verdictCount, retryCount int
	for _, e := range events {
		switch e.Kind {
		case core.EventSupervisorVerdict:
			verdictCount++
		case core.EventSupervisorRetry:
			retryCount++
		}
	}
	// 2 verdicts: one for failed attempt, one for passing attempt.
	if verdictCount != 2 {
		t.Errorf("supervisor.verdict events = %d, want 2", verdictCount)
	}
	// 1 retry event (between attempt 0 and attempt 1).
	if retryCount != 1 {
		t.Errorf("supervisor.retry events = %d, want 1", retryCount)
	}
}

func TestOrchestrate_MissingRequiredInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex")

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	d := &Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     agent.NewCliAgent(mockBin),
		DB:        db,
	}

	ctx := context.Background()
	_, err = d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			// Missing spec_path.
		},
	})
	if err == nil {
		t.Error("expected error for missing required input, got nil")
	}
}

func TestOrchestrate_InvalidRole(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	d := &Dispatcher{
		RoleDir:   "/nonexistent",
		PromptDir: "/nonexistent",
		Agent:     agent.NewCliAgent("/bin/true"),
		DB:        db,
	}

	ctx := context.Background()
	_, err = d.Orchestrate(ctx, &DispatchInput{
		RoleName: "nonexistent.role",
		Inputs:   map[string]string{},
	})
	if err == nil {
		t.Error("expected error for invalid role, got nil")
	}
}

func TestSnakeToPascal(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"diff_path", "DiffPath"},
		{"spec_path", "SpecPath"},
		{"simple", "Simple"},
		{"a_b_c", "ABC"},
	}
	for _, tt := range tests {
		got := snakeToPascal(tt.input)
		if got != tt.want {
			t.Errorf("snakeToPascal(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
```

### Step 5.6: Verify compilation

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go build ./...
```

Expected: clean compilation.

### Step 5.7: Run all tests

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./... -count=1 -timeout 60s
```

Expected: all tests pass including the new supervisor tests.

---

## Task 6: Max-retry escalation + compliance-breach event

**Goal:** Verify that when all retries are exhausted, the dispatcher emits a `compliance-breach` event and marks the job as failed. Add a test for this scenario.

**Files:**
- Modify: `coding/dispatch_test.go` (add max-retry exhaustion test)

### Step 6.1: Add max-retry exhaustion test to `coding/dispatch_test.go`

- [ ] Append this test to `coding/dispatch_test.go`:

```go
func TestOrchestrate_WithSupervisor_MaxRetriesExhausted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex-bad-findings")
	if _, err := os.Stat(mockBin); err != nil {
		t.Fatalf("mock binary not found: %v", err)
	}

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	engine, err := supervisor.NewRuleEngineFromBytes([]byte(`
rules:
  findings_have_path:
    applies_to: [reviewer.*]
    check: all_findings_have(["path"])
    message: "findings must have a non-empty path"
`))
	if err != nil {
		t.Fatalf("NewRuleEngineFromBytes: %v", err)
	}

	// Set max retries to 2 so we exhaust quickly.
	d := &Dispatcher{
		RoleDir:    filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:  filepath.Join(repoRoot, "coding"),
		Agent:      agent.NewCliAgent(mockBin),
		DB:         db,
		Supervisor: engine,
		MaxRetries: 2,
	}

	ctx := context.Background()
	result, err := d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	// Should have retried MaxRetries times (2), so retryCount = 2.
	if result.RetryCount != 2 {
		t.Errorf("retry count = %d, want 2", result.RetryCount)
	}

	// Final verdict should fail.
	if result.SupervisorVerdict == nil {
		t.Fatal("expected supervisor verdict")
	}
	if result.SupervisorVerdict.Pass {
		t.Error("expected final verdict to fail (max retries exhausted)")
	}

	// Verify compliance-breach event was emitted.
	events, err := store.NewEventStore(db).ListEvents(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var hasComplianceBreach bool
	var verdictCount, retryCount int
	for _, e := range events {
		switch e.Kind {
		case core.EventComplianceBreach:
			hasComplianceBreach = true
		case core.EventSupervisorVerdict:
			verdictCount++
		case core.EventSupervisorRetry:
			retryCount++
		}
	}
	if !hasComplianceBreach {
		t.Error("expected compliance-breach event in event log")
		for i, e := range events {
			t.Logf("  event[%d]: seq=%d kind=%s", i, e.Sequence, e.Kind)
		}
	}
	// 3 verdicts: initial + 2 retries.
	if verdictCount != 3 {
		t.Errorf("supervisor.verdict events = %d, want 3", verdictCount)
	}
	// 2 retry events.
	if retryCount != 2 {
		t.Errorf("supervisor.retry events = %d, want 2", retryCount)
	}

	// Verify run state is failed.
	runStore := store.NewRunStore(db, store.NewEventStore(db))
	run, err := runStore.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.State != core.RunStateFailed {
		t.Errorf("run state = %q, want %q", run.State, core.RunStateFailed)
	}

	// Verify final job state is failed.
	jobStore := store.NewJobStore(db, store.NewEventStore(db))
	job, err := jobStore.GetJob(ctx, result.JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.State != core.JobStateFailed {
		t.Errorf("job state = %q, want %q", job.State, core.JobStateFailed)
	}
	// Final job should be dispatched-by supervisor-retry.
	if job.DispatchedBy != "supervisor-retry" {
		t.Errorf("dispatched_by = %q, want %q", job.DispatchedBy, "supervisor-retry")
	}
}

func TestOrchestrate_WithSupervisor_NoApplicableRules(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex")

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	// Rules only apply to developer — should not affect reviewer.arch.
	engine, err := supervisor.NewRuleEngineFromBytes([]byte(`
rules:
  dev_rule:
    applies_to: [developer]
    check: exit_code_is(99)
    message: "developer must exit 99"
`))
	if err != nil {
		t.Fatalf("NewRuleEngineFromBytes: %v", err)
	}

	d := &Dispatcher{
		RoleDir:    filepath.Join(repoRoot, "coding", "roles"),
		PromptDir:  filepath.Join(repoRoot, "coding"),
		Agent:      agent.NewCliAgent(mockBin),
		DB:         db,
		Supervisor: engine,
	}

	ctx := context.Background()
	result, err := d.Orchestrate(ctx, &DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": "/tmp/test.diff",
			"spec_path": "/tmp/spec.md",
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	if result.SupervisorVerdict == nil {
		t.Fatal("expected supervisor verdict")
	}
	if !result.SupervisorVerdict.Pass {
		t.Error("expected pass when no rules apply to role")
	}
	if result.RetryCount != 0 {
		t.Errorf("retry count = %d, want 0", result.RetryCount)
	}
}
```

### Step 6.2: Run the full test suite

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && go test ./... -v -count=1 -timeout 60s
```

Expected: all tests pass, including:
- `TestOrchestrate_WithMockCodex` (no supervisor, backward compat)
- `TestOrchestrate_WithSupervisor_AllPass`
- `TestOrchestrate_WithSupervisor_RetryThenPass`
- `TestOrchestrate_WithSupervisor_MaxRetriesExhausted`
- `TestOrchestrate_WithSupervisor_NoApplicableRules`
- `TestOrchestrate_MissingRequiredInput`
- `TestOrchestrate_InvalidRole`
- All `coding/supervisor/` tests (loader, predicates, engine)
- All existing `core/`, `store/`, `agent/` tests

### Step 6.3: Run the linter

- [ ] Run:

```bash
cd /home/chris/workshop/coworker && make lint
```

Expected: no lint errors. If `golangci-lint` flags issues, fix them before proceeding.

---

## Verification Checklist

After all tasks are complete, verify:

- [ ] `go build ./...` succeeds with no errors
- [ ] `go test ./... -count=1 -timeout 60s` — all tests pass
- [ ] `make lint` — no lint errors
- [ ] `core/supervisor.go` contains `SupervisorVerdict`, `RuleResult`, three new `EventKind` constants
- [ ] `coding/supervisor/loader.go` loads and validates YAML rules with role glob matching
- [ ] `coding/supervisor/predicates.go` has `all_findings_have`, `exit_code_is`, `git_current_branch_matches`, `last_commit_msg_contains`
- [ ] `coding/supervisor/engine.go` evaluates rules and returns verdicts without importing `store/`
- [ ] `coding/supervisor/rules.yaml` has seed rules for `reviewer.*`
- [ ] `coding/dispatch.go` has retry loop with supervisor hook, emits `supervisor.verdict`, `supervisor.retry`, and `compliance-breach` events
- [ ] Backward compatibility: `Dispatcher` with `nil` Supervisor works identically to pre-Plan-101 behavior
- [ ] No import cycles: `coding/supervisor/` imports `core/` but not `store/` or `coding/`

---

## Post-Execution Report

_To be written after implementation is complete._

---

## Code Review

### Review 1

- **Date**: 2026-04-23
- **Reviewer**: Claude (full implementation review)
- **Verdict**: Approved with suggestions

**Should Fix**

1. `[FIXED]` **`roleGlobMatches` compiles a regex on every call — no caching.** `coding/supervisor/loader.go:98-110`.
   → Response: Added `compiled []*regexp.Regexp` field to Rule struct, populated during LoadRulesFromBytes.

2. `[FIXED]` **`roleGlobMatches` only escapes dots, not all regex meta-chars.** `coding/supervisor/loader.go:99-104`.
   → Response: Replaced with `regexp.QuoteMeta` + `\*` → `.*` conversion.

3. `[FIXED]` **Context cancellation not checked between retry iterations.** `coding/dispatch.go:140-156`.
   → Response: Added `select { case <-ctx.Done(): return nil, ctx.Err() default: }` at loop top.

4. `[WONTFIX]` **`ParseCheck` regex `.+` rejects zero-arg predicates.** `coding/supervisor/predicates.go:58`.
   → Response: No zero-arg predicates exist or are planned. Documented as intentional restriction.

**Nice to Have**

5. `[WONTFIX]` `Severity` type comparison with `""` — works, cosmetic only.

6. `[WONTFIX]` `stripQuotes` deviation using `strconv.Unquote` — intentional and correct.

7. `[FIXED]` **Non-deterministic rule iteration order from YAML map.** `coding/supervisor/loader.go:52-59`.
   → Response: Added `sort.Slice(rules, ...)` by Name after loading.

8. `[WONTFIX]` **Missing test for context cancellation mid-retry.** Defer to when retry loop gets more complex.

9. `[WONTFIX]` Intermediate-attempt findings discarded — by design per plan.

10. `[FIXED]` **Architecture test doesn't cover `coding/supervisor/` → `store/` import ban.**
   → Response: Added `TestSupervisorDoesNotImportStore` to `tests/architecture/imports_test.go`.

11. `[WONTFIX]` **Git predicates don't use `exec.CommandContext`.** Defer to Plan 103+ when context threading through EvalContext is designed.

**Strengths noted:** Clean separation (engine is pure logic, no store imports), comprehensive test suite (44+ tests), backward-compatible nil-Supervisor path, well-designed mock scripts with state file pattern.
