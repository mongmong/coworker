# Plan 000 — Project Bootstrap

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Scaffold the Go project so every later plan has a stable module, package layout, tooling, CI, and architectural-invariant test to build on.

**Architecture:** Single Go module at `github.com/chris/coworker`. Top-level packages `core/` (domain-neutral) and `coding/` (coding-specific) with enforced one-way import flow. Binary entry `cmd/coworker/` dispatching through a cobra CLI. Bootstrap tests live in `tests/architecture/` to enforce cross-cutting invariants.

**Tech Stack:** Go 1.23+, `spf13/cobra` for CLI, `golangci-lint` for linting, GitHub Actions for CI, `go test` stdlib. No runtime dependencies beyond cobra at this stage.

**Manifest entry:** `docs/specs/001-plan-manifest.md` §000 — Project bootstrap. Flavor: Runtime (small). `blocks_on: []`.

**Branch:** `feature/plan-000-project-bootstrap` (already created off `main`).

---

## File structure after Plan 000

```
coworker/
├── go.mod
├── go.sum
├── .gitignore
├── .golangci.yml
├── Makefile
├── CLAUDE.md                          (existing)
├── docs/                              (existing)
├── .github/
│   └── workflows/
│       └── ci.yml
├── cmd/
│   └── coworker/
│       └── main.go                    (package main, dispatches to cli)
├── cli/
│   ├── root.go                        (cobra root command)
│   ├── version.go                     (version subcommand + Version var)
│   └── version_test.go                (unit test)
├── core/
│   └── doc.go                         (package comment only for now)
├── coding/
│   └── doc.go
├── tui/
│   └── doc.go
├── mcp/
│   └── doc.go
├── store/
│   └── doc.go
├── agent/
│   └── doc.go
├── internal/                          (created empty; first subpackage lands in Plan 100)
├── testdata/                          (created empty; fixtures land in Plan 100)
└── tests/
    └── architecture/
        └── imports_test.go            (enforces core ∤ coding)
```

Each of the empty-but-registered packages ships a `doc.go` with a package comment describing its purpose (matching the manifest). Later plans fill them in.

---

## Task 1: Initialize Go module and .gitignore

**Files:**
- Create: `go.mod`
- Create: `.gitignore`

- [ ] **Step 1: Initialize the Go module**

Run:
```bash
cd /home/chris/workshop/coworker
go mod init github.com/chris/coworker
```

Expected: creates `go.mod` with contents similar to:
```
module github.com/chris/coworker

go 1.23
```

(If the installed Go toolchain is newer, the `go` directive may show a higher minor. That's fine. If Go is older than 1.23, upgrade first — do NOT pin to an older version without telling the user.)

- [ ] **Step 2: Verify go.mod**

Run:
```bash
cat go.mod
```

Expected: the file exists and declares the module as `github.com/chris/coworker`.

- [ ] **Step 3: Write .gitignore**

Create `.gitignore`:
```gitignore
# Binary
/coworker
/bin/
*.exe

# Test output
*.test
*.out
coverage.*
*.prof

# Editor
.vscode/
.idea/
*.swp
*~

# OS
.DS_Store
Thumbs.db

# Go
vendor/
```

- [ ] **Step 4: Commit**

```bash
git add go.mod .gitignore
git commit -m "Plan 000 Phase 1: initialize Go module and .gitignore"
```

---

## Task 2: Create package directory layout with doc.go files

**Files:**
- Create: `core/doc.go`
- Create: `coding/doc.go`
- Create: `tui/doc.go`
- Create: `mcp/doc.go`
- Create: `store/doc.go`
- Create: `agent/doc.go`
- Create: `internal/.gitkeep`
- Create: `testdata/.gitkeep`

- [ ] **Step 1: Write `core/doc.go`**

```go
// Package core contains domain-neutral primitives: runs, jobs, events,
// supervisor framework, attention queue, worker registry, cost ledger, and
// the Agent protocol. It is the foundation layer that coding/ builds on.
//
// Import discipline: core packages must not import any coding/ package.
// Enforced by tests/architecture/imports_test.go.
package core
```

- [ ] **Step 2: Write `coding/doc.go`**

```go
// Package coding contains coding-specific roles, rules, workflows, and
// plugin adapters. It builds on core/ primitives.
package coding
```

- [ ] **Step 3: Write `tui/doc.go`**

```go
// Package tui holds the Bubble Tea dashboard that renders live runtime
// state from the event stream. Introduced in Plan 107.
package tui
```

- [ ] **Step 4: Write `mcp/doc.go`**

```go
// Package mcp exposes the runtime as an MCP server, offering the orch.*
// tool family to registered CLI workers and user-facing panes.
// Introduced in Plan 104.
package mcp
```

- [ ] **Step 5: Write `store/doc.go`**

```go
// Package store is the SQLite persistence layer: schema, migrations,
// typed DAO helpers, and the event-log-before-state-update invariant.
// Introduced in Plan 100.
package store
```

- [ ] **Step 6: Write `agent/doc.go`**

```go
// Package agent provides concrete Agent implementations (CliAgent for
// subprocess-backed CLI binaries; future HttpAgent/LibraryAgent). The
// Agent protocol itself lives in core/ to avoid circular imports.
// Introduced in Plan 100.
package agent
```

- [ ] **Step 7: Create `internal/.gitkeep` and `testdata/.gitkeep`**

```bash
mkdir -p internal testdata
touch internal/.gitkeep testdata/.gitkeep
```

(We don't put `doc.go` under `internal/` or `testdata/` because they don't yet hold a package. First subpackage under `internal/` lands in Plan 100; `testdata/` is treated as data by Go and doesn't need a package.)

- [ ] **Step 8: Verify packages compile**

Run:
```bash
go build ./...
```

Expected: no output, exit code 0. (Empty packages with just doc.go still compile cleanly.)

- [ ] **Step 9: Commit**

```bash
git add core/ coding/ tui/ mcp/ store/ agent/ internal/.gitkeep testdata/.gitkeep
git commit -m "Plan 000 Phase 1: create package layout with doc.go files"
```

---

## Task 3: Cobra CLI skeleton with version command

**Files:**
- Add dependency: `github.com/spf13/cobra`
- Create: `cli/root.go`
- Create: `cli/version.go`
- Create: `cli/version_test.go`
- Create: `cmd/coworker/main.go`

- [ ] **Step 1: Add cobra dependency**

Run:
```bash
go get github.com/spf13/cobra@latest
```

Expected: cobra and its transitive deps added to `go.mod` / `go.sum`.

- [ ] **Step 2: Write `cli/root.go`**

```go
// Package cli contains cobra command definitions for the coworker binary.
// Subpackages are avoided at this stage to keep the command surface
// discoverable; split when the command set grows unwieldy.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// rootCmd is the coworker binary's root command. Subcommands register
// themselves via init() in their own files.
var rootCmd = &cobra.Command{
	Use:           "coworker",
	Short:         "Local-first runtime that coordinates CLI coding agents as role-typed workers.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command. Called from cmd/coworker/main.go.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "coworker:", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 3: Write `cli/version.go`**

```go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version is overridden at build time via -ldflags
// "-X 'github.com/chris/coworker/cli.Version=<value>'".
// Defaults to a dev marker when built without ldflags.
var Version = "0.0.0-dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the coworker version.",
	Run: func(cmd *cobra.Command, _ []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "coworker %s\n", Version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
	rootCmd.Version = Version
	rootCmd.SetVersionTemplate("coworker {{.Version}}\n")
}
```

- [ ] **Step 4: Write `cli/version_test.go`**

```go
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
```

- [ ] **Step 5: Write `cmd/coworker/main.go`**

```go
// Command coworker is the entry point for the coworker runtime binary.
// It defers all command dispatch to the cli package.
package main

import "github.com/chris/coworker/cli"

func main() {
	cli.Execute()
}
```

- [ ] **Step 6: Verify build**

Run:
```bash
go build -o /tmp/coworker ./cmd/coworker
```

Expected: exit code 0. Produces a `/tmp/coworker` binary.

- [ ] **Step 7: Verify `--version` flag**

Run:
```bash
/tmp/coworker --version
```

Expected: `coworker 0.0.0-dev` followed by a newline.

- [ ] **Step 8: Verify `version` subcommand**

Run:
```bash
/tmp/coworker version
```

Expected: `coworker 0.0.0-dev` followed by a newline.

- [ ] **Step 9: Run the unit test**

Run:
```bash
go test ./cli/... -v -count=1
```

Expected:
```
=== RUN   TestVersionSubcommand
--- PASS: TestVersionSubcommand (0.00s)
=== RUN   TestVersionDefaultIsDev
--- PASS: TestVersionDefaultIsDev (0.00s)
PASS
ok  	github.com/chris/coworker/cli	...
```

- [ ] **Step 10: Clean up the temp binary**

```bash
rm /tmp/coworker
```

- [ ] **Step 11: Commit**

```bash
git add go.mod go.sum cli/ cmd/
git commit -m "Plan 000 Phase 3: cobra CLI skeleton with version command"
```

---

## Task 4: Makefile

**Files:**
- Create: `Makefile`

- [ ] **Step 1: Write `Makefile`**

```makefile
.PHONY: help test lint build clean tidy

BINARY := coworker
MODULE := github.com/chris/coworker

# Inject version from git when available; fall back to dev marker.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")
LDFLAGS := -ldflags "-X '$(MODULE)/cli.Version=$(VERSION)'"

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

test: ## Run all Go tests.
	go test ./... -count=1 -timeout 60s

lint: ## Run golangci-lint.
	golangci-lint run ./...

build: ## Build the coworker binary with version injected.
	go build $(LDFLAGS) -o $(BINARY) ./cmd/coworker

clean: ## Remove built artifacts and test cache.
	rm -f $(BINARY)
	go clean -testcache

tidy: ## Tidy go.mod / go.sum.
	go mod tidy
```

- [ ] **Step 2: Verify `make test` works**

Run:
```bash
make test
```

Expected: the `cli` test package reports PASS; exit code 0.

- [ ] **Step 3: Verify `make build` works and injects version**

Run:
```bash
make build
./coworker --version
```

Expected: version output reflects `git describe` (e.g., `coworker 05b670c` or similar short SHA) rather than `0.0.0-dev`.

- [ ] **Step 4: Clean the built binary**

```bash
make clean
```

- [ ] **Step 5: Commit**

```bash
git add Makefile
git commit -m "Plan 000 Phase 2: Makefile with test/lint/build/clean targets"
```

---

## Task 5: golangci-lint config

**Files:**
- Create: `.golangci.yml`

- [ ] **Step 1: Write `.golangci.yml`**

```yaml
# golangci-lint v1 configuration. If using v2, run `golangci-lint migrate`
# to convert to the new schema.

run:
  timeout: 5m
  tests: true

linters:
  disable-all: true
  enable:
    - govet
    - staticcheck
    - errcheck
    - gosec
    - gocyclo
    - gofmt
    - goimports
    - ineffassign
    - unused
    - misspell
    - revive

linters-settings:
  gocyclo:
    min-complexity: 20
  gosec:
    exclude-generated: true
  revive:
    rules:
      - name: package-comments
      - name: exported

issues:
  max-issues-per-linter: 0
  max-same-issues: 0
  exclude-rules:
    # Allow shorter comments on tests.
    - path: _test\.go
      linters:
        - revive
```

- [ ] **Step 2: Check local golangci-lint installation**

Run:
```bash
golangci-lint --version
```

If not installed, install via:
```bash
# macOS: brew install golangci-lint
# linux:  curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin
```

If installed version is v2.x, migrate the config:
```bash
golangci-lint migrate
```

(This replaces `.golangci.yml` with the v2 schema in place.)

- [ ] **Step 3: Run the linter**

Run:
```bash
make lint
```

Expected: zero lint errors on the scaffolded code. If any errors surface (e.g., missing package comment), fix them in the offending file before continuing. The scaffolded `doc.go` files should satisfy `revive`'s `package-comments` rule, and all `cli` code is straightforward enough to pass.

- [ ] **Step 4: Commit**

```bash
git add .golangci.yml
git commit -m "Plan 000 Phase 2: golangci-lint config"
```

---

## Task 6: Import-discipline test (core ∤ coding)

**Files:**
- Create: `tests/architecture/imports_test.go`

- [ ] **Step 1: Write the failing test**

Create `tests/architecture/imports_test.go`:
```go
// Package architecture contains cross-cutting tests that enforce
// project-wide architectural invariants. No non-test source files live
// here; Go supports test-only packages.
package architecture

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/chris/coworker"

// TestCoreDoesNotImportCoding enforces the architecture posture from
// docs/specs/001-plan-manifest.md: imports flow core → coding, never
// the reverse. A violation means the core/ package has reached into
// coding/, which would couple the domain-neutral layer to the
// coding-specific one.
func TestCoreDoesNotImportCoding(t *testing.T) {
	cmd := exec.Command("go", "list",
		"-f", "{{.ImportPath}}: {{.Imports}}",
		modulePath+"/core/...")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, out)
	}

	forbidden := modulePath + "/coding"
	var violations []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, forbidden) {
			violations = append(violations, line)
		}
	}
	if len(violations) > 0 {
		t.Errorf("core imports coding (forbidden):\n  %s",
			strings.Join(violations, "\n  "))
	}
}
```

- [ ] **Step 2: Run the test — it should PASS (core doesn't yet import anything)**

Run:
```bash
go test ./tests/architecture/... -v -count=1
```

Expected:
```
=== RUN   TestCoreDoesNotImportCoding
--- PASS: TestCoreDoesNotImportCoding (...)
PASS
ok  	github.com/chris/coworker/tests/architecture	...
```

Note: this test currently "passes by absence" — `core/` has no imports yet. The test becomes load-bearing once `core/` has real code. To prove the test actually works, deliberately break it next:

- [ ] **Step 3: Prove the test detects violations (temporary)**

Add a forbidden import to `core/doc.go` temporarily:

Replace the contents of `core/doc.go` with:
```go
// Package core contains domain-neutral primitives.
package core

import _ "github.com/chris/coworker/coding"
```

Run the test:
```bash
go test ./tests/architecture/... -v -count=1
```

Expected: the test FAILS with output mentioning `github.com/chris/coworker/core: [github.com/chris/coworker/coding]`.

- [ ] **Step 4: Revert `core/doc.go`**

Restore the original content:
```go
// Package core contains domain-neutral primitives: runs, jobs, events,
// supervisor framework, attention queue, worker registry, cost ledger, and
// the Agent protocol. It is the foundation layer that coding/ builds on.
//
// Import discipline: core packages must not import any coding/ package.
// Enforced by tests/architecture/imports_test.go.
package core
```

Run the test again:
```bash
go test ./tests/architecture/... -v -count=1
```

Expected: PASS. The test is verified to both detect and allow correctly.

- [ ] **Step 5: Run the full suite to ensure nothing else broke**

Run:
```bash
make test
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add tests/architecture/
git commit -m "Plan 000 Phase 5: import-discipline test (core ∤ coding)"
```

---

## Task 7: GitHub Actions CI workflow

**Files:**
- Create: `.github/workflows/ci.yml`

- [ ] **Step 1: Write `.github/workflows/ci.yml`**

```yaml
name: CI

on:
  push:
    branches: [main]
  pull_request:

permissions:
  contents: read

jobs:
  lint:
    name: Lint
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true

      - name: golangci-lint
        uses: golangci/golangci-lint-action@v6
        with:
          version: latest

  test:
    name: Test
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true

      - name: Test
        run: make test

      - name: Build
        run: make build
```

- [ ] **Step 2: Validate YAML locally (best-effort)**

If `yamllint` is available:
```bash
yamllint .github/workflows/ci.yml
```

Otherwise rely on GitHub's parser when the workflow first runs. Minimum local check:
```bash
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/ci.yml'))" && echo OK
```

Expected: `OK`.

- [ ] **Step 3: Verify the workflow will find `go.mod`**

The workflow uses `go-version-file: go.mod`. Confirm the file is at the repo root:
```bash
test -f go.mod && echo "go.mod present"
```

Expected: `go.mod present`.

- [ ] **Step 4: Commit**

```bash
git add .github/
git commit -m "Plan 000 Phase 4: GitHub Actions CI (lint + test + build)"
```

---

## Verification (run before claiming done)

- [ ] **Full test suite passes**

```bash
make test
```

Expected: all tests pass, zero failures.

- [ ] **Lint passes**

```bash
make lint
```

Expected: zero issues.

- [ ] **Binary builds with injected version**

```bash
make build
./coworker --version
```

Expected: version reflects current git state (short SHA plus optional `-dirty`).

- [ ] **Import discipline test is load-bearing**

Re-run Task 6 Step 3/4 if any doubt remains. The test must both detect and allow.

- [ ] **Package layout matches the file-structure diagram at the top of this plan**

```bash
find . -type d -not -path './.git*' -not -path './docs*' | sort
```

Expected entries (at least):
```
.
./.github
./.github/workflows
./agent
./cli
./cmd
./cmd/coworker
./coding
./core
./internal
./mcp
./store
./testdata
./tests
./tests/architecture
./tui
```

- [ ] **Clean up any built artifacts**

```bash
make clean
git status
```

Expected: `nothing to commit, working tree clean` (ignoring any untracked files the engineer created during exploration).

---

## Post-Execution Report

**Implementation details**
- Go toolchain: 1.23.5 installed; `go.mod` declares `go 1.23.5`.
- Cobra: v1.10.2 (latest at time of execution).
- golangci-lint: v1.62.2 installed via `go install`. Config is v1 format; no migration needed.
- All 7 tasks completed across 10 commits (3 fixup commits: `go mod tidy`, `t.Cleanup`, parallel CI+import-test).

**Deviations from plan**
- Task 2 commit message says "Phase 2" instead of plan-specified "Phase 1" — cosmetic, not functional.
- Task 3 required a follow-up `go mod tidy` commit after spec review caught cobra marked `// indirect`.
- Task 3 required a follow-up `t.Cleanup` commit after code review caught test state mutation without reset.
- Task 5 implementer committed config before running lint (golangci-lint not on PATH); lint verified post-hoc after manual install. Lint passed with zero findings.
- Tasks 6 and 7 dispatched in parallel (independent directories, no file overlap).

**Known limitations**
- No test coverage gate yet — intentional. Added in a later plan if/when useful.
- `internal/` and `testdata/` are empty placeholders. First real content lands in Plan 100.
- CI doesn't run `make lint` inside the test job; lint is its own job. Matches standard Go project practice.
- `golangci-lint` not on system PATH by default (`~/go/bin/` not in PATH). CI uses the golangci-lint-action which handles its own install.
- `.github/` directory exists but no remote is configured yet — CI won't run until the repo is pushed to GitHub.

**Verification results (post-implementation)**
- `go test ./... -count=1 -timeout 60s` — 2 packages PASS, 7 have no test files yet.
- `make lint` — zero issues.
- `make build && ./coworker --version` — binary built, version `df31c00` injected from git.
- Package layout matches the file-structure diagram.
- Import discipline test verified both allow and detect modes.

**Follow-up work**
- When Plan 100 adds the first real `core/` package, re-verify the import discipline test still passes.
- When a second CLI dependency is added (e.g. viper), re-check lint rules for any needed exclusions.
- Add `~/go/bin` to PATH in shell profile if golangci-lint is needed locally beyond CI.

---

## Code Review

(Append review findings here during Step 5 of `docs/development-workflow.md`.)

### Review 1

- **Date**: YYYY-MM-DD
- **Reviewer**: (tbd)
- **PR**: (tbd)
- **Verdict**: (tbd)
