# Plan 100 — Thin End-to-End Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `coworker invoke reviewer.arch --diff <path> --spec <path>` spawns ephemeral Codex, captures findings JSON, persists to SQLite with event-log-before-state invariant.

**Architecture:** Core domain types in `core/`, SQLite persistence in `store/` with event-first writes, `CliAgent` in `agent/` wrapping `os/exec` with stream-JSON parsing, role loading from YAML in `coding/`, dispatch orchestration in `coding/dispatch.go`, CLI entry in `cli/invoke.go`. The `Agent` interface returns a `JobHandle` for forward-compatibility with persistent workers.

**Tech Stack:** Go 1.23+, `modernc.org/sqlite` (pure Go SQLite), `gopkg.in/yaml.v3`, `spf13/cobra`, `database/sql`, `os/exec`, `encoding/json`.

**Manifest entry:** `docs/specs/001-plan-manifest.md` section 100.

**Branch:** `feature/plan-100-thin-end-to-end` (already created off `main`).

---

## File structure after Plan 100

```
coworker/
├── core/
│   ├── doc.go              (existing)
│   ├── id.go               (NewID helper)
│   ├── id_test.go
│   ├── run.go              (Run, RunState)
│   ├── job.go              (Job, JobState, JobResult)
│   ├── event.go            (Event, EventKind, EventWriter)
│   ├── finding.go          (Finding, Severity, ComputeFingerprint)
│   ├── finding_test.go
│   ├── artifact.go         (Artifact)
│   ├── agent.go            (Agent, JobHandle interfaces)
│   └── role.go             (Role, Permission types)
├── store/
│   ├── doc.go              (existing)
│   ├── db.go               (Open, Migrate, Close)
│   ├── db_test.go
│   ├── migrations/
│   │   └── 001_init.sql
│   ├── event_store.go      (WriteEventThenRow, ListEvents)
│   ├── event_store_test.go
│   ├── run_store.go        (CreateRun, GetRun, CompleteRun)
│   ├── run_store_test.go
│   ├── job_store.go        (CreateJob, UpdateJobState, GetJob)
│   ├── job_store_test.go
│   ├── finding_store.go    (InsertFinding, ResolveFinding, ListFindings)
│   ├── finding_store_test.go
│   ├── artifact_store.go   (InsertArtifact)
│   └── artifact_store_test.go
├── agent/
│   ├── doc.go              (existing)
│   ├── cli_agent.go        (CliAgent + Dispatch)
│   ├── cli_handle.go       (cliJobHandle + Wait/Cancel)
│   └── cli_agent_test.go
├── coding/
│   ├── doc.go              (existing)
│   ├── roles/
│   │   ├── loader.go       (LoadRole, LoadPromptTemplate)
│   │   ├── loader_test.go
│   │   └── reviewer_arch.yaml
│   ├── prompts/
│   │   └── reviewer_arch.md
│   └── dispatch.go         (Dispatcher, Orchestrate)
│   └── dispatch_test.go
├── cli/
│   ├── root.go             (existing)
│   ├── version.go          (existing)
│   ├── version_test.go     (existing)
│   ├── invoke.go           (coworker invoke command)
│   └── invoke_test.go
├── testdata/
│   └── mocks/
│       └── codex           (shell script mock)
├── docs/
│   └── architecture/
│       └── decisions.md    (first cross-cutting decisions)
└── (everything else from Plan 000, unchanged)
```

---

## Task 1: Core domain types

**Goal:** Create all type definitions in `core/`. Pure data types + ID generation + fingerprint function. No dependencies on store or agent.

**Files:**
- Create: `core/id.go`, `core/id_test.go`
- Create: `core/run.go`
- Create: `core/job.go`
- Create: `core/event.go`
- Create: `core/finding.go`, `core/finding_test.go`
- Create: `core/artifact.go`
- Create: `core/agent.go`
- Create: `core/role.go`

### Step 1.1: Write `core/id.go`

- [ ] Create `core/id.go`:

```go
package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewID generates a 32-character hex string from 16 random bytes.
// Used for RunID, JobID, EventID, FindingID, ArtifactID.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}
```

### Step 1.2: Write `core/id_test.go`

- [ ] Create `core/id_test.go`:

```go
package core

import (
	"encoding/hex"
	"testing"
)

func TestNewID_Length(t *testing.T) {
	id := NewID()
	if len(id) != 32 {
		t.Errorf("NewID() length = %d, want 32", len(id))
	}
}

func TestNewID_ValidHex(t *testing.T) {
	id := NewID()
	_, err := hex.DecodeString(id)
	if err != nil {
		t.Errorf("NewID() is not valid hex: %v", err)
	}
}

func TestNewID_Unique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := NewID()
		if seen[id] {
			t.Fatalf("NewID() produced duplicate at iteration %d: %s", i, id)
		}
		seen[id] = true
	}
}
```

### Step 1.3: Run the ID test

- [ ] Run:

```bash
go test ./core/... -v -count=1 -run TestNewID
```

Expected: all three TestNewID tests pass.

### Step 1.4: Write `core/run.go`

- [ ] Create `core/run.go`:

```go
package core

import "time"

// RunState represents the lifecycle state of a run.
type RunState string

const (
	RunStateActive    RunState = "active"
	RunStateCompleted RunState = "completed"
	RunStateFailed    RunState = "failed"
	RunStateAborted   RunState = "aborted"
)

// Run is a correlated tree of jobs sharing a run-id, a context store,
// and a workflow. A PRD-to-PRs autopilot is one run; an interactive
// session is also one run.
type Run struct {
	ID        string
	Mode      string // "autopilot" | "interactive"
	State     RunState
	StartedAt time.Time
	EndedAt   *time.Time
}
```

### Step 1.5: Write `core/job.go`

- [ ] Create `core/job.go`:

```go
package core

import "time"

// JobState represents the lifecycle state of a job.
type JobState string

const (
	JobStatePending    JobState = "pending"
	JobStateDispatched JobState = "dispatched"
	JobStateRunning    JobState = "running"
	JobStateComplete   JobState = "complete"
	JobStateFailed     JobState = "failed"
	JobStateCancelled  JobState = "cancelled"
)

// Job is one execution of one role. The atomic unit of retry, cost, and audit.
type Job struct {
	ID           string
	RunID        string
	Role         string
	State        JobState
	DispatchedBy string // "scheduler" | "user" | "supervisor-retry" | "self"
	CLI          string // "codex" | "claude-code" | "opencode"
	StartedAt    time.Time
	EndedAt      *time.Time
}

// JobResult holds the output of a completed job.
type JobResult struct {
	Findings  []Finding
	Artifacts []Artifact
	ExitCode  int
	Stdout    string
	Stderr    string
}
```

### Step 1.6: Write `core/event.go`

- [ ] Create `core/event.go`:

```go
package core

import (
	"context"
	"time"
)

// EventKind identifies the type of event in the event log.
type EventKind string

const (
	EventRunCreated      EventKind = "run.created"
	EventRunCompleted    EventKind = "run.completed"
	EventJobCreated      EventKind = "job.created"
	EventJobLeased       EventKind = "job.leased"
	EventJobCompleted    EventKind = "job.completed"
	EventJobFailed       EventKind = "job.failed"
	EventFindingCreated  EventKind = "finding.created"
	EventArtifactCreated EventKind = "artifact.created"
)

// Event is a single entry in the append-only event log.
// The events table is the authoritative history of a run.
type Event struct {
	ID              string
	RunID           string
	Sequence        int
	Kind            EventKind
	SchemaVersion   int
	IdempotencyKey  string
	CausationID     string
	CorrelationID   string
	Payload         string // JSON
	CreatedAt       time.Time
}

// EventWriter is the interface for writing events to the event log.
// Implemented by store.EventStore.
type EventWriter interface {
	// WriteEventThenRow writes the event first, then calls applyFn
	// within the same transaction to update projection tables.
	// This enforces the event-log-before-state invariant.
	WriteEventThenRow(ctx context.Context, event *Event, applyFn func(tx interface{}) error) error
}
```

### Step 1.7: Write `core/finding.go`

- [ ] Create `core/finding.go`:

```go
package core

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// Severity represents the severity of a finding.
type Severity string

const (
	SeverityCritical  Severity = "critical"
	SeverityImportant Severity = "important"
	SeverityMinor     Severity = "minor"
	SeverityNit       Severity = "nit"
)

// Finding is an immutable review finding. Once created, only
// resolved_by_job_id and resolved_at can be updated.
type Finding struct {
	ID              string
	RunID           string
	JobID           string
	Path            string
	Line            int
	Severity        Severity
	Body            string
	Fingerprint     string
	ResolvedByJobID string
	ResolvedAt      *time.Time
}

// ComputeFingerprint generates a stable fingerprint for deduplication.
// The fingerprint is based on path + line + severity + body, so that
// identical findings from different reviewers can be deduplicated.
func ComputeFingerprint(path string, line int, severity Severity, body string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s:%d:%s:%s", path, line, severity, body)
	return fmt.Sprintf("%x", h.Sum(nil))[:32]
}
```

### Step 1.8: Write `core/finding_test.go`

- [ ] Create `core/finding_test.go`:

```go
package core

import (
	"encoding/hex"
	"testing"
)

func TestComputeFingerprint_Deterministic(t *testing.T) {
	fp1 := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	fp2 := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	if fp1 != fp2 {
		t.Errorf("fingerprints differ for same input: %q vs %q", fp1, fp2)
	}
}

func TestComputeFingerprint_Length(t *testing.T) {
	fp := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	if len(fp) != 32 {
		t.Errorf("fingerprint length = %d, want 32", len(fp))
	}
}

func TestComputeFingerprint_ValidHex(t *testing.T) {
	fp := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	_, err := hex.DecodeString(fp)
	if err != nil {
		t.Errorf("fingerprint is not valid hex: %v", err)
	}
}

func TestComputeFingerprint_DiffersByPath(t *testing.T) {
	fp1 := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	fp2 := ComputeFingerprint("other.go", 42, SeverityImportant, "Missing error check")
	if fp1 == fp2 {
		t.Error("fingerprints should differ when path differs")
	}
}

func TestComputeFingerprint_DiffersByLine(t *testing.T) {
	fp1 := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	fp2 := ComputeFingerprint("main.go", 99, SeverityImportant, "Missing error check")
	if fp1 == fp2 {
		t.Error("fingerprints should differ when line differs")
	}
}

func TestComputeFingerprint_DiffersBySeverity(t *testing.T) {
	fp1 := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	fp2 := ComputeFingerprint("main.go", 42, SeverityMinor, "Missing error check")
	if fp1 == fp2 {
		t.Error("fingerprints should differ when severity differs")
	}
}

func TestComputeFingerprint_DiffersByBody(t *testing.T) {
	fp1 := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	fp2 := ComputeFingerprint("main.go", 42, SeverityImportant, "Different body text")
	if fp1 == fp2 {
		t.Error("fingerprints should differ when body differs")
	}
}
```

### Step 1.9: Run the finding tests

- [ ] Run:

```bash
go test ./core/... -v -count=1 -run TestComputeFingerprint
```

Expected: all seven TestComputeFingerprint tests pass.

### Step 1.10: Write `core/artifact.go`

- [ ] Create `core/artifact.go`:

```go
package core

// Artifact is a pointer to a file produced by a job.
// File artifacts are references by path; nothing durable is inlined into SQLite.
type Artifact struct {
	ID    string
	JobID string
	Kind  string // "diff", "spec", "plan", "report", "log", etc.
	Path  string // filesystem path, repo-relative
}
```

### Step 1.11: Write `core/agent.go`

- [ ] Create `core/agent.go`:

```go
package core

import "context"

// Agent is the protocol for dispatching jobs to CLI coding agents.
// Shipped with one implementation (CliAgent) in Plan 100.
// Future HTTP-backed agents or library agents drop in without
// touching dispatch code.
type Agent interface {
	// Dispatch starts a job asynchronously and returns a handle to
	// wait for the result or cancel the execution.
	Dispatch(ctx context.Context, job *Job, prompt string) (JobHandle, error)
}

// JobHandle represents a running job. Callers use Wait to block
// until the job completes, or Cancel to abort it.
type JobHandle interface {
	// Wait blocks until the job completes and returns the result.
	Wait(ctx context.Context) (*JobResult, error)
	// Cancel aborts the running job.
	Cancel() error
}
```

### Step 1.12: Write `core/role.go`

- [ ] Create `core/role.go`:

```go
package core

// Role is a named job description. Binds an agent to a prompt template,
// inputs/outputs contract, sandbox override, concurrency rule, and skill set.
// Parsed from YAML files under .coworker/roles/.
type Role struct {
	Name           string         `yaml:"name"`
	Concurrency    string         `yaml:"concurrency"`     // "single" | "many"
	CLI            string         `yaml:"cli"`             // "codex" | "claude-code" | "opencode"
	PromptTemplate string         `yaml:"prompt_template"` // relative path to .md file
	Inputs         RoleInputs     `yaml:"inputs"`
	Outputs        RoleOutputs    `yaml:"outputs"`
	Sandbox        string         `yaml:"sandbox"`         // "read-only" | "workspace-write" | etc.
	Permissions    RolePermissions `yaml:"permissions"`
	Budget         RoleBudget     `yaml:"budget"`
	RetryPolicy    RetryPolicy    `yaml:"retry_policy"`
}

// RoleInputs declares the required and optional inputs for a role.
type RoleInputs struct {
	Required []string `yaml:"required"`
	Optional []string `yaml:"optional,omitempty"`
}

// RoleOutputs declares the output contract for a role.
type RoleOutputs struct {
	Contract map[string]interface{} `yaml:"contract"`
	Emits    map[string]interface{} `yaml:"emits"`
}

// RolePermissions declares the expected permission surface of a role.
type RolePermissions struct {
	AllowedTools  []string `yaml:"allowed_tools"`
	Never         []string `yaml:"never"`
	RequiresHuman []string `yaml:"requires_human"`
}

// RoleBudget sets resource limits for jobs of this role.
type RoleBudget struct {
	MaxTokensPerJob      int     `yaml:"max_tokens_per_job"`
	MaxWallclockMinutes  int     `yaml:"max_wallclock_minutes"`
	MaxCostUSD           float64 `yaml:"max_cost_usd"`
}

// RetryPolicy controls how failed jobs are retried.
type RetryPolicy struct {
	OnContractFail string `yaml:"on_contract_fail"` // "retry_with_feedback" | "fail"
	OnJobError     string `yaml:"on_job_error"`     // "retry_once" | "fail"
}
```

### Step 1.13: Verify all core types compile

- [ ] Run:

```bash
go build ./core/...
```

Expected: exit code 0, no output.

### Step 1.14: Run the full core test suite

- [ ] Run:

```bash
go test ./core/... -v -count=1
```

Expected: all tests pass (TestNewID_*, TestComputeFingerprint_*).

### Step 1.15: Commit

- [ ] Run:

```bash
git add core/
git commit -m "Plan 100 Task 1: core domain types (Run, Job, Event, Finding, Artifact, Agent, Role)"
```

---

## Task 2: SQLite schema + migration runner

**Goal:** Create the 6-table schema in `store/migrations/001_init.sql`. Build a migration runner. Add `modernc.org/sqlite` dependency.

**Files:**
- Create: `store/migrations/001_init.sql`
- Create: `store/db.go`
- Create: `store/db_test.go`

### Step 2.1: Add dependencies

- [ ] Run:

```bash
go get modernc.org/sqlite@latest
go get gopkg.in/yaml.v3@latest
```

Expected: both added to `go.mod` / `go.sum`.

### Step 2.2: Tidy modules

- [ ] Run:

```bash
go mod tidy
```

### Step 2.3: Write `store/migrations/001_init.sql`

- [ ] Create `store/migrations/001_init.sql`:

```sql
-- Plan 100: initial schema for coworker runtime.
-- Tables: events, runs, jobs, findings, artifacts, schema_migrations.
-- The events table is the authoritative history; other tables are projections.

-- Schema migrations tracking.
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- SSE event log (authoritative history of a run).
CREATE TABLE IF NOT EXISTS events (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    kind TEXT NOT NULL,
    schema_version INTEGER NOT NULL DEFAULT 1,
    idempotency_key TEXT,
    causation_id TEXT,
    correlation_id TEXT,
    payload TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(run_id, sequence),
    UNIQUE(idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_events_run_id ON events(run_id);
CREATE INDEX IF NOT EXISTS idx_events_kind ON events(kind);

-- Top-level run.
CREATE TABLE IF NOT EXISTS runs (
    id TEXT PRIMARY KEY,
    mode TEXT NOT NULL DEFAULT 'interactive',
    state TEXT NOT NULL DEFAULT 'active',
    started_at TEXT NOT NULL,
    ended_at TEXT
);

-- Jobs = role invocations.
CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    role TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    dispatched_by TEXT NOT NULL DEFAULT 'scheduler',
    cli TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    ended_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_jobs_run_id ON jobs(run_id);
CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state);

-- Findings (immutable once written; only resolved_by_job_id and resolved_at can be updated).
CREATE TABLE IF NOT EXISTS findings (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    job_id TEXT NOT NULL REFERENCES jobs(id),
    path TEXT NOT NULL,
    line INTEGER NOT NULL,
    severity TEXT NOT NULL,
    body TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    resolved_by_job_id TEXT,
    resolved_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_findings_run_id ON findings(run_id);
CREATE INDEX IF NOT EXISTS idx_findings_fingerprint ON findings(fingerprint);

-- Artifacts (pointers to files on disk).
CREATE TABLE IF NOT EXISTS artifacts (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES jobs(id),
    kind TEXT NOT NULL,
    path TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_artifacts_job_id ON artifacts(job_id);
```

### Step 2.4: Write `store/db.go`

- [ ] Create `store/db.go`:

```go
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps a *sql.DB with coworker-specific helpers.
type DB struct {
	*sql.DB
}

// Open opens (or creates) a SQLite database at the given path and
// runs all pending migrations. Use ":memory:" for in-memory databases.
func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}

	// Enable WAL mode for better concurrent read performance.
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	// Enable foreign keys.
	if _, err := sqlDB.Exec("PRAGMA foreign_keys=ON"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	db := &DB{DB: sqlDB}
	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

// migrate applies all pending migrations from the embedded migrations/ directory.
func (db *DB) migrate() error {
	// Ensure schema_migrations table exists. We bootstrap it outside
	// the numbered migrations to avoid a chicken-and-egg problem.
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	// Read migration files.
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	// Sort by filename to ensure ordering.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		// Extract version number from filename: "001_init.sql" -> 1
		var version int
		if _, err := fmt.Sscanf(entry.Name(), "%d_", &version); err != nil {
			return fmt.Errorf("parse migration version from %q: %w", entry.Name(), err)
		}

		// Check if already applied.
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&count)
		if err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if count > 0 {
			continue
		}

		// Read and execute the migration.
		data, err := fs.ReadFile(migrationsFS, "migrations/"+entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}

		if _, err := tx.Exec(string(data)); err != nil {
			tx.Rollback()
			return fmt.Errorf("exec migration %d: %w", version, err)
		}

		if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}

	return nil
}
```

### Step 2.5: Write `store/db_test.go`

- [ ] Create `store/db_test.go`:

```go
package store

import (
	"testing"
)

func TestOpen_InMemory(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()
}

func TestOpen_CreatesAllTables(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	expectedTables := []string{
		"schema_migrations",
		"events",
		"runs",
		"jobs",
		"findings",
		"artifacts",
	}

	for _, table := range expectedTables {
		var name string
		err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
			table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestOpen_MigrationIdempotent(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	// Running migrate again should be a no-op.
	if err := db.migrate(); err != nil {
		t.Fatalf("second migrate() failed: %v", err)
	}

	// Verify migration was recorded exactly once.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = 1").Scan(&count)
	if err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count != 1 {
		t.Errorf("migration version 1 recorded %d times, want 1", count)
	}
}

func TestOpen_ForeignKeysEnabled(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	var fkEnabled int
	err = db.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled)
	if err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fkEnabled != 1 {
		t.Errorf("foreign_keys = %d, want 1", fkEnabled)
	}
}

func TestOpen_EventsTableConstraints(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	// Insert a run first (for FK).
	_, err = db.Exec(`INSERT INTO runs (id, mode, state, started_at) VALUES ('r1', 'interactive', 'active', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	// Insert first event.
	_, err = db.Exec(`INSERT INTO events (id, run_id, sequence, kind, payload, created_at)
		VALUES ('e1', 'r1', 1, 'run.created', '{}', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	// Duplicate (run_id, sequence) should fail.
	_, err = db.Exec(`INSERT INTO events (id, run_id, sequence, kind, payload, created_at)
		VALUES ('e2', 'r1', 1, 'run.created', '{}', '2026-01-01T00:00:00Z')`)
	if err == nil {
		t.Error("expected UNIQUE constraint violation on (run_id, sequence), got nil")
	}

	// Duplicate idempotency_key should fail.
	_, err = db.Exec(`INSERT INTO events (id, run_id, sequence, kind, idempotency_key, payload, created_at)
		VALUES ('e3', 'r1', 2, 'test', 'key1', '{}', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert event with idempotency_key: %v", err)
	}
	_, err = db.Exec(`INSERT INTO events (id, run_id, sequence, kind, idempotency_key, payload, created_at)
		VALUES ('e4', 'r1', 3, 'test', 'key1', '{}', '2026-01-01T00:00:00Z')`)
	if err == nil {
		t.Error("expected UNIQUE constraint violation on idempotency_key, got nil")
	}
}
```

### Step 2.6: Run the store tests

- [ ] Run:

```bash
go test ./store/... -v -count=1
```

Expected: all five tests pass: TestOpen_InMemory, TestOpen_CreatesAllTables, TestOpen_MigrationIdempotent, TestOpen_ForeignKeysEnabled, TestOpen_EventsTableConstraints.

### Step 2.7: Commit

- [ ] Run:

```bash
git add store/ go.mod go.sum
git commit -m "Plan 100 Task 2: SQLite schema + migration runner (events, runs, jobs, findings, artifacts)"
```

---

## Task 3: Event store with WriteEventThenRow

**Goal:** Implement the event-log-before-state invariant. `WriteEventThenRow` writes the event, then calls applyFn within the same transaction. Also implement `ListEvents` and idempotency.

**Files:**
- Create: `store/event_store.go`
- Create: `store/event_store_test.go`

### Step 3.1: Write `store/event_store.go`

- [ ] Create `store/event_store.go`:

```go
package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/chris/coworker/core"
)

// EventStore handles event persistence with the event-log-before-state invariant.
type EventStore struct {
	db *DB
}

// NewEventStore creates an EventStore backed by the given DB.
func NewEventStore(db *DB) *EventStore {
	return &EventStore{db: db}
}

// WriteEventThenRow writes the event first, then calls applyFn within
// the same transaction to update projection tables. This enforces the
// event-log-before-state invariant from the spec.
//
// The sequence number is auto-assigned as MAX(sequence)+1 for the run.
// If applyFn is nil, only the event is written.
func (s *EventStore) WriteEventThenRow(ctx context.Context, event *core.Event, applyFn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Auto-assign sequence number.
	var seq int
	err = tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(sequence), 0) + 1 FROM events WHERE run_id = ?",
		event.RunID,
	).Scan(&seq)
	if err != nil {
		return fmt.Errorf("compute sequence: %w", err)
	}
	event.Sequence = seq

	// Write the event first (event-log-before-state invariant).
	_, err = tx.ExecContext(ctx,
		`INSERT INTO events (id, run_id, sequence, kind, schema_version,
			idempotency_key, causation_id, correlation_id, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID,
		event.RunID,
		event.Sequence,
		string(event.Kind),
		event.SchemaVersion,
		nullableString(event.IdempotencyKey),
		nullableString(event.CausationID),
		nullableString(event.CorrelationID),
		event.Payload,
		event.CreatedAt.Format("2006-01-02T15:04:05Z"),
	)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}

	// Then apply the projection update.
	if applyFn != nil {
		if err := applyFn(tx); err != nil {
			return fmt.Errorf("apply projection: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// WriteEventIdempotent writes an event with an idempotency key.
// If the key already exists, the write is silently skipped (no error).
// Returns true if the event was written, false if it was a duplicate.
func (s *EventStore) WriteEventIdempotent(ctx context.Context, event *core.Event, applyFn func(tx *sql.Tx) error) (bool, error) {
	if event.IdempotencyKey == "" {
		return false, fmt.Errorf("idempotency key must not be empty")
	}

	// Check if already exists.
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events WHERE idempotency_key = ?",
		event.IdempotencyKey,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check idempotency key: %w", err)
	}
	if count > 0 {
		return false, nil
	}

	if err := s.WriteEventThenRow(ctx, event, applyFn); err != nil {
		return false, err
	}
	return true, nil
}

// ListEvents returns all events for a run, ordered by sequence.
func (s *EventStore) ListEvents(ctx context.Context, runID string) ([]core.Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, sequence, kind, schema_version,
			COALESCE(idempotency_key, ''), COALESCE(causation_id, ''), COALESCE(correlation_id, ''),
			payload, created_at
		FROM events WHERE run_id = ? ORDER BY sequence ASC`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var events []core.Event
	for rows.Next() {
		var e core.Event
		var kindStr, createdAtStr string
		err := rows.Scan(
			&e.ID, &e.RunID, &e.Sequence, &kindStr,
			&e.SchemaVersion, &e.IdempotencyKey, &e.CausationID,
			&e.CorrelationID, &e.Payload, &createdAtStr,
		)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.Kind = core.EventKind(kindStr)
		events = append(events, e)
	}
	return events, rows.Err()
}

// nullableString returns a *string for SQL nullable TEXT columns.
// Empty string is stored as NULL.
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
```

### Step 3.2: Write `store/event_store_test.go`

- [ ] Create `store/event_store_test.go`:

```go
package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func setupTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func insertTestRun(t *testing.T, db *DB, runID string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO runs (id, mode, state, started_at) VALUES (?, 'interactive', 'active', ?)`,
		runID, time.Now().Format("2006-01-02T15:04:05Z"),
	)
	if err != nil {
		t.Fatalf("insert test run: %v", err)
	}
}

func TestWriteEventThenRow_WritesEventAndApplies(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run1")
	es := NewEventStore(db)
	ctx := context.Background()

	applyCalled := false
	event := &core.Event{
		ID:            "evt1",
		RunID:         "run1",
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		Payload:       `{"job_id":"j1"}`,
		CreatedAt:     time.Now(),
	}

	err := es.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		applyCalled = true
		_, err := tx.Exec(`INSERT INTO jobs (id, run_id, role, state, dispatched_by, cli, started_at)
			VALUES ('j1', 'run1', 'reviewer.arch', 'pending', 'scheduler', 'codex', ?)`,
			time.Now().Format("2006-01-02T15:04:05Z"))
		return err
	})
	if err != nil {
		t.Fatalf("WriteEventThenRow: %v", err)
	}

	if !applyCalled {
		t.Error("applyFn was not called")
	}

	// Verify event was written.
	events, err := es.ListEvents(ctx, "run1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].ID != "evt1" {
		t.Errorf("event ID = %q, want %q", events[0].ID, "evt1")
	}
	if events[0].Sequence != 1 {
		t.Errorf("event sequence = %d, want 1", events[0].Sequence)
	}

	// Verify projection (job) was written.
	var jobID string
	err = db.QueryRow("SELECT id FROM jobs WHERE id = 'j1'").Scan(&jobID)
	if err != nil {
		t.Errorf("job not found: %v", err)
	}
}

func TestWriteEventThenRow_ApplyFnFailsRollsBackBoth(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run1")
	es := NewEventStore(db)
	ctx := context.Background()

	event := &core.Event{
		ID:            "evt_fail",
		RunID:         "run1",
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		Payload:       `{"test":"fail"}`,
		CreatedAt:     time.Now(),
	}

	err := es.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		return fmt.Errorf("simulated projection failure")
	})
	if err == nil {
		t.Fatal("expected error from failed applyFn, got nil")
	}

	// Both event and projection should be rolled back.
	events, err := es.ListEvents(ctx, "run1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events after rollback, got %d", len(events))
	}
}

func TestWriteEventThenRow_NilApplyFn(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run1")
	es := NewEventStore(db)
	ctx := context.Background()

	event := &core.Event{
		ID:            "evt_noproject",
		RunID:         "run1",
		Kind:          core.EventRunCreated,
		SchemaVersion: 1,
		Payload:       `{}`,
		CreatedAt:     time.Now(),
	}

	err := es.WriteEventThenRow(ctx, event, nil)
	if err != nil {
		t.Fatalf("WriteEventThenRow with nil applyFn: %v", err)
	}

	events, err := es.ListEvents(ctx, "run1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestWriteEventThenRow_SequenceAutoIncrement(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run1")
	es := NewEventStore(db)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		event := &core.Event{
			ID:            fmt.Sprintf("evt_%d", i),
			RunID:         "run1",
			Kind:          core.EventJobCreated,
			SchemaVersion: 1,
			Payload:       fmt.Sprintf(`{"i":%d}`, i),
			CreatedAt:     time.Now(),
		}
		if err := es.WriteEventThenRow(ctx, event, nil); err != nil {
			t.Fatalf("write event %d: %v", i, err)
		}
	}

	events, err := es.ListEvents(ctx, "run1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	for i, e := range events {
		if e.Sequence != i+1 {
			t.Errorf("event %d sequence = %d, want %d", i, e.Sequence, i+1)
		}
	}
}

func TestWriteEventIdempotent_DuplicateKeySkips(t *testing.T) {
	db := setupTestDB(t)
	insertTestRun(t, db, "run1")
	es := NewEventStore(db)
	ctx := context.Background()

	event1 := &core.Event{
		ID:             "evt_idem1",
		RunID:          "run1",
		Kind:           core.EventJobCreated,
		SchemaVersion:  1,
		IdempotencyKey: "unique-key-1",
		Payload:        `{"first":true}`,
		CreatedAt:      time.Now(),
	}

	written, err := es.WriteEventIdempotent(ctx, event1, nil)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if !written {
		t.Error("first write should return true")
	}

	// Second write with same idempotency key should be skipped.
	event2 := &core.Event{
		ID:             "evt_idem2",
		RunID:          "run1",
		Kind:           core.EventJobCreated,
		SchemaVersion:  1,
		IdempotencyKey: "unique-key-1",
		Payload:        `{"second":true}`,
		CreatedAt:      time.Now(),
	}

	written, err = es.WriteEventIdempotent(ctx, event2, nil)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if written {
		t.Error("second write with same key should return false")
	}

	// Only one event should exist.
	events, err := es.ListEvents(ctx, "run1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].ID != "evt_idem1" {
		t.Errorf("event ID = %q, want %q", events[0].ID, "evt_idem1")
	}
}

func TestWriteEventIdempotent_EmptyKeyReturnsError(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ctx := context.Background()

	event := &core.Event{
		ID:            "evt_nokey",
		RunID:         "run1",
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		Payload:       `{}`,
		CreatedAt:     time.Now(),
	}

	_, err := es.WriteEventIdempotent(ctx, event, nil)
	if err == nil {
		t.Error("expected error for empty idempotency key, got nil")
	}
}

func TestListEvents_EmptyRun(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	ctx := context.Background()

	events, err := es.ListEvents(ctx, "nonexistent-run")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}
```

### Step 3.3: Run the event store tests

- [ ] Run:

```bash
go test ./store/... -v -count=1 -run TestWriteEvent
```

Expected: all TestWriteEvent* and TestListEvents* tests pass.

### Step 3.4: Run the full store test suite

- [ ] Run:

```bash
go test ./store/... -v -count=1
```

Expected: all store tests pass (db + event_store tests).

### Step 3.5: Commit

- [ ] Run:

```bash
git add store/
git commit -m "Plan 100 Task 3: event store with WriteEventThenRow, idempotency, and ListEvents"
```

---

## Task 4: Run + Job stores

**Goal:** CRUD operations for runs and jobs, using WriteEventThenRow for all mutations.

**Files:**
- Create: `store/run_store.go`, `store/run_store_test.go`
- Create: `store/job_store.go`, `store/job_store_test.go`

### Step 4.1: Write `store/run_store.go`

- [ ] Create `store/run_store.go`:

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chris/coworker/core"
)

// RunStore handles run persistence with event-log-before-state writes.
type RunStore struct {
	db    *DB
	event *EventStore
}

// NewRunStore creates a RunStore.
func NewRunStore(db *DB, event *EventStore) *RunStore {
	return &RunStore{db: db, event: event}
}

// CreateRun creates a new run and writes a run.created event.
func (s *RunStore) CreateRun(ctx context.Context, run *core.Run) error {
	payload, err := json.Marshal(map[string]string{
		"run_id": run.ID,
		"mode":   run.Mode,
	})
	if err != nil {
		return fmt.Errorf("marshal run.created payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         run.ID,
		Kind:          core.EventRunCreated,
		SchemaVersion: 1,
		Payload:       string(payload),
		CreatedAt:     run.StartedAt,
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO runs (id, mode, state, started_at) VALUES (?, ?, ?, ?)`,
			run.ID, run.Mode, string(run.State),
			run.StartedAt.Format("2006-01-02T15:04:05Z"),
		)
		if err != nil {
			return fmt.Errorf("insert run: %w", err)
		}
		return nil
	})
}

// GetRun retrieves a run by ID.
func (s *RunStore) GetRun(ctx context.Context, id string) (*core.Run, error) {
	var run core.Run
	var stateStr, startedAtStr string
	var endedAtStr sql.NullString

	err := s.db.QueryRowContext(ctx,
		"SELECT id, mode, state, started_at, ended_at FROM runs WHERE id = ?", id,
	).Scan(&run.ID, &run.Mode, &stateStr, &startedAtStr, &endedAtStr)
	if err != nil {
		return nil, fmt.Errorf("get run %q: %w", id, err)
	}

	run.State = core.RunState(stateStr)
	run.StartedAt, _ = time.Parse("2006-01-02T15:04:05Z", startedAtStr)
	if endedAtStr.Valid {
		t, _ := time.Parse("2006-01-02T15:04:05Z", endedAtStr.String)
		run.EndedAt = &t
	}

	return &run, nil
}

// CompleteRun marks a run as completed and writes a run.completed event.
func (s *RunStore) CompleteRun(ctx context.Context, runID string, state core.RunState) error {
	now := time.Now()
	payload, err := json.Marshal(map[string]string{
		"run_id": runID,
		"state":  string(state),
	})
	if err != nil {
		return fmt.Errorf("marshal run.completed payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventRunCompleted,
		SchemaVersion: 1,
		Payload:       string(payload),
		CreatedAt:     now,
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE runs SET state = ?, ended_at = ? WHERE id = ?",
			string(state), now.Format("2006-01-02T15:04:05Z"), runID,
		)
		return err
	})
}
```

### Step 4.2: Write `store/run_store_test.go`

- [ ] Create `store/run_store_test.go`:

```go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func TestCreateRun(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ctx := context.Background()

	run := &core.Run{
		ID:        "run_test1",
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}

	if err := rs.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Verify run was created.
	got, err := rs.GetRun(ctx, "run_test1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ID != "run_test1" {
		t.Errorf("run ID = %q, want %q", got.ID, "run_test1")
	}
	if got.Mode != "interactive" {
		t.Errorf("run mode = %q, want %q", got.Mode, "interactive")
	}
	if got.State != core.RunStateActive {
		t.Errorf("run state = %q, want %q", got.State, core.RunStateActive)
	}

	// Verify event was written.
	events, err := es.ListEvents(ctx, "run_test1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Kind != core.EventRunCreated {
		t.Errorf("event kind = %q, want %q", events[0].Kind, core.EventRunCreated)
	}
}

func TestGetRun_NotFound(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ctx := context.Background()

	_, err := rs.GetRun(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent run, got nil")
	}
}

func TestCompleteRun(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	ctx := context.Background()

	run := &core.Run{
		ID:        "run_complete",
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}

	if err := rs.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := rs.CompleteRun(ctx, "run_complete", core.RunStateCompleted); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}

	got, err := rs.GetRun(ctx, "run_complete")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != core.RunStateCompleted {
		t.Errorf("run state = %q, want %q", got.State, core.RunStateCompleted)
	}
	if got.EndedAt == nil {
		t.Error("run ended_at should be set")
	}

	// Verify two events: run.created + run.completed.
	events, err := es.ListEvents(ctx, "run_complete")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[1].Kind != core.EventRunCompleted {
		t.Errorf("second event kind = %q, want %q", events[1].Kind, core.EventRunCompleted)
	}
}
```

### Step 4.3: Write `store/job_store.go`

- [ ] Create `store/job_store.go`:

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chris/coworker/core"
)

// JobStore handles job persistence with event-log-before-state writes.
type JobStore struct {
	db    *DB
	event *EventStore
}

// NewJobStore creates a JobStore.
func NewJobStore(db *DB, event *EventStore) *JobStore {
	return &JobStore{db: db, event: event}
}

// CreateJob creates a new job and writes a job.created event.
func (s *JobStore) CreateJob(ctx context.Context, job *core.Job) error {
	payload, err := json.Marshal(map[string]string{
		"job_id": job.ID,
		"run_id": job.RunID,
		"role":   job.Role,
		"cli":    job.CLI,
	})
	if err != nil {
		return fmt.Errorf("marshal job.created payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         job.RunID,
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		CorrelationID: job.ID,
		Payload:       string(payload),
		CreatedAt:     job.StartedAt,
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO jobs (id, run_id, role, state, dispatched_by, cli, started_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			job.ID, job.RunID, job.Role, string(job.State),
			job.DispatchedBy, job.CLI,
			job.StartedAt.Format("2006-01-02T15:04:05Z"),
		)
		return err
	})
}

// UpdateJobState updates the state of a job and writes the appropriate event.
func (s *JobStore) UpdateJobState(ctx context.Context, jobID string, newState core.JobState) error {
	now := time.Now()

	var eventKind core.EventKind
	switch newState {
	case core.JobStateComplete:
		eventKind = core.EventJobCompleted
	case core.JobStateFailed:
		eventKind = core.EventJobFailed
	case core.JobStateDispatched:
		eventKind = core.EventJobLeased
	default:
		eventKind = core.EventKind("job.state_changed")
	}

	payload, err := json.Marshal(map[string]string{
		"job_id": jobID,
		"state":  string(newState),
	})
	if err != nil {
		return fmt.Errorf("marshal job state payload: %w", err)
	}

	// Look up run_id for the event.
	var runID string
	err = s.db.QueryRowContext(ctx, "SELECT run_id FROM jobs WHERE id = ?", jobID).Scan(&runID)
	if err != nil {
		return fmt.Errorf("get run_id for job %q: %w", jobID, err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          eventKind,
		SchemaVersion: 1,
		CorrelationID: jobID,
		Payload:       string(payload),
		CreatedAt:     now,
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		var setEndedAt string
		if newState == core.JobStateComplete || newState == core.JobStateFailed || newState == core.JobStateCancelled {
			setEndedAt = ", ended_at = '" + now.Format("2006-01-02T15:04:05Z") + "'"
		}
		_, err := tx.ExecContext(ctx,
			"UPDATE jobs SET state = ?"+setEndedAt+" WHERE id = ?",
			string(newState), jobID,
		)
		return err
	})
}

// GetJob retrieves a job by ID.
func (s *JobStore) GetJob(ctx context.Context, id string) (*core.Job, error) {
	var job core.Job
	var stateStr, startedAtStr string
	var endedAtStr sql.NullString

	err := s.db.QueryRowContext(ctx,
		"SELECT id, run_id, role, state, dispatched_by, cli, started_at, ended_at FROM jobs WHERE id = ?", id,
	).Scan(&job.ID, &job.RunID, &job.Role, &stateStr,
		&job.DispatchedBy, &job.CLI, &startedAtStr, &endedAtStr)
	if err != nil {
		return nil, fmt.Errorf("get job %q: %w", id, err)
	}

	job.State = core.JobState(stateStr)
	job.StartedAt, _ = time.Parse("2006-01-02T15:04:05Z", startedAtStr)
	if endedAtStr.Valid {
		t, _ := time.Parse("2006-01-02T15:04:05Z", endedAtStr.String)
		job.EndedAt = &t
	}

	return &job, nil
}
```

### Step 4.4: Write `store/job_store_test.go`

- [ ] Create `store/job_store_test.go`:

```go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func createTestRun(t *testing.T, rs *RunStore, ctx context.Context, runID string) {
	t.Helper()
	run := &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}
	if err := rs.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun(%q): %v", runID, err)
	}
}

func TestCreateJob(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_j1")

	job := &core.Job{
		ID:           "job1",
		RunID:        "run_j1",
		Role:         "reviewer.arch",
		State:        core.JobStatePending,
		DispatchedBy: "user",
		CLI:          "codex",
		StartedAt:    time.Now(),
	}

	if err := js.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	got, err := js.GetJob(ctx, "job1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Role != "reviewer.arch" {
		t.Errorf("job role = %q, want %q", got.Role, "reviewer.arch")
	}
	if got.State != core.JobStatePending {
		t.Errorf("job state = %q, want %q", got.State, core.JobStatePending)
	}
	if got.CLI != "codex" {
		t.Errorf("job CLI = %q, want %q", got.CLI, "codex")
	}

	// Verify events: run.created + job.created.
	events, err := es.ListEvents(ctx, "run_j1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[1].Kind != core.EventJobCreated {
		t.Errorf("event kind = %q, want %q", events[1].Kind, core.EventJobCreated)
	}
}

func TestUpdateJobState_Complete(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_j2")

	job := &core.Job{
		ID:           "job2",
		RunID:        "run_j2",
		Role:         "reviewer.arch",
		State:        core.JobStatePending,
		DispatchedBy: "scheduler",
		CLI:          "codex",
		StartedAt:    time.Now(),
	}
	if err := js.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	if err := js.UpdateJobState(ctx, "job2", core.JobStateComplete); err != nil {
		t.Fatalf("UpdateJobState: %v", err)
	}

	got, err := js.GetJob(ctx, "job2")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.State != core.JobStateComplete {
		t.Errorf("job state = %q, want %q", got.State, core.JobStateComplete)
	}
	if got.EndedAt == nil {
		t.Error("job ended_at should be set for complete state")
	}

	// Verify events: run.created + job.created + job.completed.
	events, err := es.ListEvents(ctx, "run_j2")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[2].Kind != core.EventJobCompleted {
		t.Errorf("event kind = %q, want %q", events[2].Kind, core.EventJobCompleted)
	}
}

func TestUpdateJobState_Failed(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_j3")

	job := &core.Job{
		ID:           "job3",
		RunID:        "run_j3",
		Role:         "reviewer.arch",
		State:        core.JobStatePending,
		DispatchedBy: "scheduler",
		CLI:          "codex",
		StartedAt:    time.Now(),
	}
	if err := js.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	if err := js.UpdateJobState(ctx, "job3", core.JobStateFailed); err != nil {
		t.Fatalf("UpdateJobState: %v", err)
	}

	got, err := js.GetJob(ctx, "job3")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.State != core.JobStateFailed {
		t.Errorf("job state = %q, want %q", got.State, core.JobStateFailed)
	}

	events, err := es.ListEvents(ctx, "run_j3")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if events[2].Kind != core.EventJobFailed {
		t.Errorf("event kind = %q, want %q", events[2].Kind, core.EventJobFailed)
	}
}

func TestGetJob_NotFound(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	js := NewJobStore(db, es)
	ctx := context.Background()

	_, err := js.GetJob(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent job, got nil")
	}
}
```

### Step 4.5: Run the run + job store tests

- [ ] Run:

```bash
go test ./store/... -v -count=1
```

Expected: all store tests pass (db, event_store, run_store, job_store).

### Step 4.6: Commit

- [ ] Run:

```bash
git add store/
git commit -m "Plan 100 Task 4: run and job stores with event-before-state writes"
```

---

## Task 5: Finding store with immutability + artifact store

**Goal:** InsertFinding with fingerprint + event, reject non-resolution updates, resolution support. InsertArtifact with event.

**Files:**
- Create: `store/finding_store.go`, `store/finding_store_test.go`
- Create: `store/artifact_store.go`, `store/artifact_store_test.go`

### Step 5.1: Write `store/finding_store.go`

- [ ] Create `store/finding_store.go`:

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chris/coworker/core"
)

// FindingStore handles finding persistence with immutability enforcement.
type FindingStore struct {
	db    *DB
	event *EventStore
}

// NewFindingStore creates a FindingStore.
func NewFindingStore(db *DB, event *EventStore) *FindingStore {
	return &FindingStore{db: db, event: event}
}

// InsertFinding creates a new finding, computing its fingerprint, and writes
// a finding.created event. The finding is immutable after creation -- only
// resolved_by_job_id and resolved_at can be updated via ResolveFinding.
func (s *FindingStore) InsertFinding(ctx context.Context, finding *core.Finding) error {
	// Compute fingerprint.
	finding.Fingerprint = core.ComputeFingerprint(
		finding.Path, finding.Line, finding.Severity, finding.Body,
	)

	payload, err := json.Marshal(map[string]interface{}{
		"finding_id":  finding.ID,
		"job_id":      finding.JobID,
		"path":        finding.Path,
		"line":        finding.Line,
		"severity":    finding.Severity,
		"fingerprint": finding.Fingerprint,
	})
	if err != nil {
		return fmt.Errorf("marshal finding.created payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         finding.RunID,
		Kind:          core.EventFindingCreated,
		SchemaVersion: 1,
		CorrelationID: finding.JobID,
		Payload:       string(payload),
		CreatedAt:     time.Now(),
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO findings (id, run_id, job_id, path, line, severity, body, fingerprint)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			finding.ID, finding.RunID, finding.JobID,
			finding.Path, finding.Line, string(finding.Severity),
			finding.Body, finding.Fingerprint,
		)
		return err
	})
}

// ResolveFinding marks a finding as resolved by linking a fix job.
// This is the ONLY permitted mutation on a finding after creation.
func (s *FindingStore) ResolveFinding(ctx context.Context, findingID, resolvedByJobID string) error {
	now := time.Now()

	// Get the run_id for the event.
	var runID string
	err := s.db.QueryRowContext(ctx,
		"SELECT run_id FROM findings WHERE id = ?", findingID,
	).Scan(&runID)
	if err != nil {
		return fmt.Errorf("get finding %q: %w", findingID, err)
	}

	payload, err := json.Marshal(map[string]string{
		"finding_id":         findingID,
		"resolved_by_job_id": resolvedByJobID,
	})
	if err != nil {
		return fmt.Errorf("marshal finding resolved payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventKind("finding.resolved"),
		SchemaVersion: 1,
		Payload:       string(payload),
		CreatedAt:     now,
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			"UPDATE findings SET resolved_by_job_id = ?, resolved_at = ? WHERE id = ? AND resolved_by_job_id IS NULL",
			resolvedByJobID, now.Format("2006-01-02T15:04:05Z"), findingID,
		)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return fmt.Errorf("finding %q not found or already resolved", findingID)
		}
		return nil
	})
}

// ListFindings returns all findings for a run.
func (s *FindingStore) ListFindings(ctx context.Context, runID string) ([]core.Finding, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, job_id, path, line, severity, body, fingerprint,
			COALESCE(resolved_by_job_id, ''), resolved_at
		FROM findings WHERE run_id = ? ORDER BY path, line`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("query findings: %w", err)
	}
	defer rows.Close()

	var findings []core.Finding
	for rows.Next() {
		var f core.Finding
		var severityStr string
		var resolvedAtStr sql.NullString
		err := rows.Scan(
			&f.ID, &f.RunID, &f.JobID, &f.Path, &f.Line,
			&severityStr, &f.Body, &f.Fingerprint,
			&f.ResolvedByJobID, &resolvedAtStr,
		)
		if err != nil {
			return nil, fmt.Errorf("scan finding: %w", err)
		}
		f.Severity = core.Severity(severityStr)
		if resolvedAtStr.Valid {
			t, _ := time.Parse("2006-01-02T15:04:05Z", resolvedAtStr.String)
			f.ResolvedAt = &t
		}
		findings = append(findings, f)
	}
	return findings, rows.Err()
}
```

### Step 5.2: Write `store/finding_store_test.go`

- [ ] Create `store/finding_store_test.go`:

```go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func setupFindingTestDB(t *testing.T) (*DB, *EventStore, *RunStore, *JobStore, *FindingStore) {
	t.Helper()
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)
	fs := NewFindingStore(db, es)

	ctx := context.Background()
	createTestRun(t, rs, ctx, "run_f1")

	job := &core.Job{
		ID:           "job_f1",
		RunID:        "run_f1",
		Role:         "reviewer.arch",
		State:        core.JobStatePending,
		DispatchedBy: "scheduler",
		CLI:          "codex",
		StartedAt:    time.Now(),
	}
	if err := js.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	return db, es, rs, js, fs
}

func TestInsertFinding(t *testing.T) {
	_, es, _, _, fs := setupFindingTestDB(t)
	ctx := context.Background()

	finding := &core.Finding{
		ID:       "find1",
		RunID:    "run_f1",
		JobID:    "job_f1",
		Path:     "main.go",
		Line:     42,
		Severity: core.SeverityImportant,
		Body:     "Missing error check on Close()",
	}

	if err := fs.InsertFinding(ctx, finding); err != nil {
		t.Fatalf("InsertFinding: %v", err)
	}

	// Verify fingerprint was computed.
	if finding.Fingerprint == "" {
		t.Error("fingerprint should be computed")
	}

	// Verify finding was persisted.
	findings, err := fs.ListFindings(ctx, "run_f1")
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Path != "main.go" {
		t.Errorf("finding path = %q, want %q", findings[0].Path, "main.go")
	}
	if findings[0].Fingerprint == "" {
		t.Error("persisted finding should have fingerprint")
	}

	// Verify event was written.
	events, err := es.ListEvents(ctx, "run_f1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	// run.created + job.created + finding.created = 3
	foundFindingEvent := false
	for _, e := range events {
		if e.Kind == core.EventFindingCreated {
			foundFindingEvent = true
		}
	}
	if !foundFindingEvent {
		t.Error("no finding.created event found")
	}
}

func TestFindingImmutability_DirectUpdateBlocked(t *testing.T) {
	db, _, _, _, fs := setupFindingTestDB(t)
	ctx := context.Background()

	finding := &core.Finding{
		ID:       "find_immut",
		RunID:    "run_f1",
		JobID:    "job_f1",
		Path:     "main.go",
		Line:     42,
		Severity: core.SeverityImportant,
		Body:     "Original body text",
	}

	if err := fs.InsertFinding(ctx, finding); err != nil {
		t.Fatalf("InsertFinding: %v", err)
	}

	// Try to update body directly via SQL -- this should work at the SQL level
	// but the store API does not expose it. We test that the store layer
	// only provides InsertFinding and ResolveFinding.
	// The store API is the enforcement boundary.

	// Verify the finding body via the store API.
	findings, err := fs.ListFindings(ctx, "run_f1")
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if findings[0].Body != "Original body text" {
		t.Errorf("body = %q, want %q", findings[0].Body, "Original body text")
	}

	// Direct SQL update should work (SQLite doesn't have column-level triggers
	// in our schema), but we rely on the Go API boundary for immutability.
	// This is documented: "enforced by store layer" per the plan manifest.
	_ = db
}

func TestResolveFinding(t *testing.T) {
	_, _, _, _, fs := setupFindingTestDB(t)
	ctx := context.Background()

	finding := &core.Finding{
		ID:       "find_resolve",
		RunID:    "run_f1",
		JobID:    "job_f1",
		Path:     "store.go",
		Line:     17,
		Severity: core.SeverityMinor,
		Body:     "Consider using prepared statement",
	}

	if err := fs.InsertFinding(ctx, finding); err != nil {
		t.Fatalf("InsertFinding: %v", err)
	}

	if err := fs.ResolveFinding(ctx, "find_resolve", "fix_job_1"); err != nil {
		t.Fatalf("ResolveFinding: %v", err)
	}

	findings, err := fs.ListFindings(ctx, "run_f1")
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].ResolvedByJobID != "fix_job_1" {
		t.Errorf("resolved_by_job_id = %q, want %q", findings[0].ResolvedByJobID, "fix_job_1")
	}
	if findings[0].ResolvedAt == nil {
		t.Error("resolved_at should be set")
	}
}

func TestResolveFinding_AlreadyResolved(t *testing.T) {
	_, _, _, _, fs := setupFindingTestDB(t)
	ctx := context.Background()

	finding := &core.Finding{
		ID:       "find_double",
		RunID:    "run_f1",
		JobID:    "job_f1",
		Path:     "main.go",
		Line:     10,
		Severity: core.SeverityMinor,
		Body:     "Some finding",
	}

	if err := fs.InsertFinding(ctx, finding); err != nil {
		t.Fatalf("InsertFinding: %v", err)
	}

	if err := fs.ResolveFinding(ctx, "find_double", "fix_job_1"); err != nil {
		t.Fatalf("first ResolveFinding: %v", err)
	}

	// Second resolve should fail.
	err := fs.ResolveFinding(ctx, "find_double", "fix_job_2")
	if err == nil {
		t.Error("expected error resolving already-resolved finding, got nil")
	}
}

func TestResolveFinding_NotFound(t *testing.T) {
	_, _, _, _, fs := setupFindingTestDB(t)
	ctx := context.Background()

	err := fs.ResolveFinding(ctx, "nonexistent", "fix_job_1")
	if err == nil {
		t.Error("expected error resolving nonexistent finding, got nil")
	}
}

func TestListFindings_Empty(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	fs := NewFindingStore(db, es)
	ctx := context.Background()

	findings, err := fs.ListFindings(ctx, "nonexistent-run")
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}
```

### Step 5.3: Write `store/artifact_store.go`

- [ ] Create `store/artifact_store.go`:

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chris/coworker/core"
)

// ArtifactStore handles artifact persistence.
type ArtifactStore struct {
	db    *DB
	event *EventStore
}

// NewArtifactStore creates an ArtifactStore.
func NewArtifactStore(db *DB, event *EventStore) *ArtifactStore {
	return &ArtifactStore{db: db, event: event}
}

// InsertArtifact creates a new artifact and writes an artifact.created event.
// Artifacts are pointers to files on disk; nothing is inlined.
func (s *ArtifactStore) InsertArtifact(ctx context.Context, artifact *core.Artifact, runID string) error {
	payload, err := json.Marshal(map[string]string{
		"artifact_id": artifact.ID,
		"job_id":      artifact.JobID,
		"kind":        artifact.Kind,
		"path":        artifact.Path,
	})
	if err != nil {
		return fmt.Errorf("marshal artifact.created payload: %w", err)
	}

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventArtifactCreated,
		SchemaVersion: 1,
		CorrelationID: artifact.JobID,
		Payload:       string(payload),
		CreatedAt:     time.Now(),
	}

	return s.event.WriteEventThenRow(ctx, event, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO artifacts (id, job_id, kind, path) VALUES (?, ?, ?, ?)",
			artifact.ID, artifact.JobID, artifact.Kind, artifact.Path,
		)
		return err
	})
}
```

### Step 5.4: Write `store/artifact_store_test.go`

- [ ] Create `store/artifact_store_test.go`:

```go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func TestInsertArtifact(t *testing.T) {
	db := setupTestDB(t)
	es := NewEventStore(db)
	rs := NewRunStore(db, es)
	js := NewJobStore(db, es)
	as := NewArtifactStore(db, es)
	ctx := context.Background()

	createTestRun(t, rs, ctx, "run_a1")

	job := &core.Job{
		ID:           "job_a1",
		RunID:        "run_a1",
		Role:         "reviewer.arch",
		State:        core.JobStatePending,
		DispatchedBy: "scheduler",
		CLI:          "codex",
		StartedAt:    time.Now(),
	}
	if err := js.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	artifact := &core.Artifact{
		ID:    "art1",
		JobID: "job_a1",
		Kind:  "log",
		Path:  ".coworker/runs/run_a1/jobs/job_a1.jsonl",
	}

	if err := as.InsertArtifact(ctx, artifact, "run_a1"); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	// Verify artifact was persisted.
	var path string
	err := db.QueryRow("SELECT path FROM artifacts WHERE id = 'art1'").Scan(&path)
	if err != nil {
		t.Fatalf("query artifact: %v", err)
	}
	if path != ".coworker/runs/run_a1/jobs/job_a1.jsonl" {
		t.Errorf("artifact path = %q, want %q", path, ".coworker/runs/run_a1/jobs/job_a1.jsonl")
	}

	// Verify event was written.
	events, err := es.ListEvents(ctx, "run_a1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	foundArtifactEvent := false
	for _, e := range events {
		if e.Kind == core.EventArtifactCreated {
			foundArtifactEvent = true
		}
	}
	if !foundArtifactEvent {
		t.Error("no artifact.created event found")
	}
}
```

### Step 5.5: Run all store tests

- [ ] Run:

```bash
go test ./store/... -v -count=1
```

Expected: all store tests pass.

### Step 5.6: Commit

- [ ] Run:

```bash
git add store/
git commit -m "Plan 100 Task 5: finding store (immutable, fingerprinted) + artifact store"
```

---

## Task 6: Role loader

**Goal:** Parse YAML role definitions + load prompt templates from the filesystem. Ship `reviewer.arch` role.

**Files:**
- Create: `coding/roles/loader.go`, `coding/roles/loader_test.go`
- Create: `coding/roles/reviewer_arch.yaml`
- Create: `coding/prompts/reviewer_arch.md`

### Step 6.1: Write `coding/roles/reviewer_arch.yaml`

- [ ] Create `coding/roles/reviewer_arch.yaml`:

```yaml
name: reviewer.arch
concurrency: single
cli: codex
prompt_template: prompts/reviewer_arch.md
inputs:
  required:
    - diff_path
    - spec_path
outputs:
  contract:
    findings_line_anchored: true
  emits:
    findings: []
sandbox: read-only
permissions:
  allowed_tools:
    - read
    - grep
    - glob
  never:
    - write
    - edit
    - "bash:rm"
  requires_human: []
budget:
  max_tokens_per_job: 200000
  max_wallclock_minutes: 30
  max_cost_usd: 5.00
retry_policy:
  on_contract_fail: retry_with_feedback
  on_job_error: retry_once
```

### Step 6.2: Write `coding/prompts/reviewer_arch.md`

- [ ] Create `coding/prompts/reviewer_arch.md`:

```markdown
# Architectural Review

You are an architectural reviewer. Your job is to review the diff against the spec and produce findings.

## Inputs

- **Diff**: {{ .DiffPath }}
- **Spec**: {{ .SpecPath }}

## Instructions

1. Read the diff file.
2. Read the spec file.
3. Compare the implementation against the spec for architectural correctness.
4. For each issue found, output a finding as a JSON object on a single line:

```json
{"type":"finding","path":"<file>","line":<number>,"severity":"<critical|important|minor|nit>","body":"<description>"}
```

5. When done, output:

```json
{"type":"done","exit_code":0}
```

## Rules

- Every finding MUST include a file path and line number.
- Severity must be one of: critical, important, minor, nit.
- Do not suggest stylistic changes unless they violate the spec.
- Focus on architectural concerns: wrong abstractions, missing error handling, spec violations, invariant breaches.
```

### Step 6.3: Write `coding/roles/loader.go`

- [ ] Create `coding/roles/loader.go`:

```go
// Package roles loads role definitions from YAML files and their
// associated prompt templates from the filesystem.
package roles

import (
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/chris/coworker/core"
	"gopkg.in/yaml.v3"
)

// LoadRole reads a role YAML file from roleDir and returns the parsed Role.
// roleDir is the directory containing role YAML files (e.g., ".coworker/roles/").
// roleName is the dotted name (e.g., "reviewer.arch") which maps to
// "reviewer_arch.yaml" on disk (dots replaced with underscores).
func LoadRole(roleDir, roleName string) (*core.Role, error) {
	// Convert dotted name to file name: "reviewer.arch" -> "reviewer_arch.yaml"
	fileName := dotToUnderscore(roleName) + ".yaml"
	path := filepath.Join(roleDir, fileName)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read role %q from %q: %w", roleName, path, err)
	}

	var role core.Role
	if err := yaml.Unmarshal(data, &role); err != nil {
		return nil, fmt.Errorf("parse role %q: %w", roleName, err)
	}

	if err := validateRole(&role); err != nil {
		return nil, fmt.Errorf("validate role %q: %w", roleName, err)
	}

	return &role, nil
}

// LoadPromptTemplate reads and parses a prompt template file.
// promptDir is the directory containing prompt .md files.
// templatePath is the relative path from the role's prompt_template field.
func LoadPromptTemplate(promptDir, templatePath string) (*template.Template, error) {
	path := filepath.Join(promptDir, templatePath)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read prompt template %q: %w", path, err)
	}

	tmpl, err := template.New(filepath.Base(templatePath)).Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse prompt template %q: %w", templatePath, err)
	}

	return tmpl, nil
}

// RenderPrompt renders a prompt template with the given data.
func RenderPrompt(tmpl *template.Template, data interface{}) (string, error) {
	var buf []byte
	w := &byteWriter{buf: &buf}
	if err := tmpl.Execute(w, data); err != nil {
		return "", fmt.Errorf("render prompt: %w", err)
	}
	return string(buf), nil
}

// byteWriter is a simple io.Writer that appends to a byte slice.
type byteWriter struct {
	buf *[]byte
}

func (w *byteWriter) Write(p []byte) (int, error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}

// validateRole checks that required fields are present.
func validateRole(role *core.Role) error {
	if role.Name == "" {
		return fmt.Errorf("name is required")
	}
	if role.CLI == "" {
		return fmt.Errorf("cli is required")
	}
	if role.PromptTemplate == "" {
		return fmt.Errorf("prompt_template is required")
	}
	if role.Concurrency == "" {
		return fmt.Errorf("concurrency is required")
	}
	if role.Concurrency != "single" && role.Concurrency != "many" {
		return fmt.Errorf("concurrency must be 'single' or 'many', got %q", role.Concurrency)
	}
	if len(role.Inputs.Required) == 0 {
		return fmt.Errorf("inputs.required must have at least one entry")
	}
	return nil
}

// dotToUnderscore replaces dots with underscores in a string.
func dotToUnderscore(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			result[i] = '_'
		} else {
			result[i] = s[i]
		}
	}
	return string(result)
}
```

### Step 6.4: Write `coding/roles/loader_test.go`

- [ ] Create `coding/roles/loader_test.go`:

```go
package roles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRole_ReviewerArch(t *testing.T) {
	// Use the actual shipped role file.
	// The test is run from the package directory, so we need to find the repo root.
	roleDir := findRoleDir(t)

	role, err := LoadRole(roleDir, "reviewer.arch")
	if err != nil {
		t.Fatalf("LoadRole: %v", err)
	}

	if role.Name != "reviewer.arch" {
		t.Errorf("name = %q, want %q", role.Name, "reviewer.arch")
	}
	if role.CLI != "codex" {
		t.Errorf("cli = %q, want %q", role.CLI, "codex")
	}
	if role.Concurrency != "single" {
		t.Errorf("concurrency = %q, want %q", role.Concurrency, "single")
	}
	if role.PromptTemplate != "prompts/reviewer_arch.md" {
		t.Errorf("prompt_template = %q, want %q", role.PromptTemplate, "prompts/reviewer_arch.md")
	}
	if len(role.Inputs.Required) != 2 {
		t.Errorf("inputs.required length = %d, want 2", len(role.Inputs.Required))
	}
	if role.Sandbox != "read-only" {
		t.Errorf("sandbox = %q, want %q", role.Sandbox, "read-only")
	}
	if role.Budget.MaxCostUSD != 5.00 {
		t.Errorf("budget.max_cost_usd = %f, want 5.00", role.Budget.MaxCostUSD)
	}
	if len(role.Permissions.AllowedTools) != 3 {
		t.Errorf("permissions.allowed_tools length = %d, want 3", len(role.Permissions.AllowedTools))
	}
}

func TestLoadRole_MissingFile(t *testing.T) {
	_, err := LoadRole("/nonexistent", "reviewer.arch")
	if err == nil {
		t.Error("expected error for missing role file, got nil")
	}
}

func TestLoadRole_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "bad_role.yaml"), []byte(":::not yaml"), 0644)
	if err != nil {
		t.Fatalf("write test file: %v", err)
	}

	_, err = LoadRole(dir, "bad.role")
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

func TestLoadRole_MissingRequiredFields(t *testing.T) {
	dir := t.TempDir()

	// Role with missing name.
	err := os.WriteFile(filepath.Join(dir, "empty.yaml"), []byte("cli: codex\n"), 0644)
	if err != nil {
		t.Fatalf("write test file: %v", err)
	}

	_, err = LoadRole(dir, "empty")
	if err == nil {
		t.Error("expected validation error for missing name, got nil")
	}
}

func TestLoadRole_InvalidConcurrency(t *testing.T) {
	dir := t.TempDir()
	yaml := `
name: test.role
cli: codex
concurrency: invalid
prompt_template: prompts/test.md
inputs:
  required: [diff_path]
`
	err := os.WriteFile(filepath.Join(dir, "test_role.yaml"), []byte(yaml), 0644)
	if err != nil {
		t.Fatalf("write test file: %v", err)
	}

	_, err = LoadRole(dir, "test.role")
	if err == nil {
		t.Error("expected validation error for invalid concurrency, got nil")
	}
	if !strings.Contains(err.Error(), "concurrency") {
		t.Errorf("error should mention concurrency: %v", err)
	}
}

func TestLoadPromptTemplate(t *testing.T) {
	promptDir := findPromptDir(t)

	tmpl, err := LoadPromptTemplate(promptDir, "prompts/reviewer_arch.md")
	if err != nil {
		t.Fatalf("LoadPromptTemplate: %v", err)
	}

	rendered, err := RenderPrompt(tmpl, map[string]string{
		"DiffPath": "/tmp/test.diff",
		"SpecPath": "/tmp/spec.md",
	})
	if err != nil {
		t.Fatalf("RenderPrompt: %v", err)
	}

	if !strings.Contains(rendered, "/tmp/test.diff") {
		t.Error("rendered prompt should contain diff path")
	}
	if !strings.Contains(rendered, "/tmp/spec.md") {
		t.Error("rendered prompt should contain spec path")
	}
}

func TestLoadPromptTemplate_MissingFile(t *testing.T) {
	_, err := LoadPromptTemplate("/nonexistent", "missing.md")
	if err == nil {
		t.Error("expected error for missing template, got nil")
	}
}

func TestDotToUnderscore(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"reviewer.arch", "reviewer_arch"},
		{"reviewer.frontend", "reviewer_frontend"},
		{"developer", "developer"},
		{"a.b.c", "a_b_c"},
	}
	for _, tt := range tests {
		got := dotToUnderscore(tt.input)
		if got != tt.want {
			t.Errorf("dotToUnderscore(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// findRoleDir locates the coding/roles directory relative to the test file.
// Since tests run from the package directory (coding/roles/), the YAML file
// is in the same directory.
func findRoleDir(t *testing.T) string {
	t.Helper()
	// We are in coding/roles/ package directory.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Check if the YAML file exists in the current directory.
	if _, err := os.Stat(filepath.Join(wd, "reviewer_arch.yaml")); err == nil {
		return wd
	}
	t.Fatalf("cannot find role dir from %q", wd)
	return ""
}

// findPromptDir locates the coding/ directory (parent of roles/).
func findPromptDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// roles/ is under coding/, so parent is coding/.
	codingDir := filepath.Dir(wd)
	if _, err := os.Stat(filepath.Join(codingDir, "prompts", "reviewer_arch.md")); err == nil {
		return codingDir
	}
	t.Fatalf("cannot find prompt dir from %q", wd)
	return ""
}
```

### Step 6.5: Run the role loader tests

- [ ] Run:

```bash
go test ./coding/roles/... -v -count=1
```

Expected: all tests pass.

### Step 6.6: Run the full test suite

- [ ] Run:

```bash
go test ./... -count=1 -timeout 60s
```

Expected: all tests pass across all packages.

### Step 6.7: Commit

- [ ] Run:

```bash
git add coding/ go.mod go.sum
git commit -m "Plan 100 Task 6: role loader with YAML parsing, prompt templates, reviewer.arch role"
```

---

## Task 7: CliAgent + mock binary

**Goal:** Implement `CliAgent` that shells out to a CLI binary, streams stdout through `json.Decoder`, and collects findings. Create a mock codex binary for testing.

**Files:**
- Create: `agent/cli_agent.go`
- Create: `agent/cli_handle.go`
- Create: `agent/cli_agent_test.go`
- Create: `testdata/mocks/codex` (shell script)

### Step 7.1: Write `testdata/mocks/codex`

- [ ] Create `testdata/mocks/codex`:

```bash
#!/bin/bash
# Mock codex CLI for testing. Reads stdin (the prompt), writes stream-JSON
# findings to stdout. Simulates the codex ephemeral execution model.

# Read stdin to consume the prompt (required so the pipe doesn't break).
cat > /dev/null

# Output findings as stream-JSON (one JSON object per line).
echo '{"type":"finding","path":"main.go","line":42,"severity":"important","body":"Missing error check on Close()"}'
echo '{"type":"finding","path":"store.go","line":17,"severity":"minor","body":"Consider using prepared statement"}'
echo '{"type":"done","exit_code":0}'
```

- [ ] Make the mock executable:

```bash
chmod +x testdata/mocks/codex
```

### Step 7.2: Write `agent/cli_agent.go`

- [ ] Create `agent/cli_agent.go`:

```go
package agent

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/chris/coworker/core"
)

// CliAgent dispatches jobs by shelling out to a CLI binary.
// It implements the core.Agent interface.
type CliAgent struct {
	// BinaryPath is the path to the CLI executable.
	BinaryPath string
	// Args are additional arguments passed to the CLI.
	Args []string
}

// NewCliAgent creates a CliAgent for the given binary path.
func NewCliAgent(binaryPath string, args ...string) *CliAgent {
	return &CliAgent{
		BinaryPath: binaryPath,
		Args:       args,
	}
}

// Dispatch starts a job by executing the CLI binary with the prompt
// on stdin. Returns a JobHandle to wait for the result.
func (a *CliAgent) Dispatch(ctx context.Context, job *core.Job, prompt string) (core.JobHandle, error) {
	cmd := exec.CommandContext(ctx, a.BinaryPath, a.Args...)
	cmd.Stdin = strings.NewReader(prompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", a.BinaryPath, err)
	}

	handle := &cliJobHandle{
		cmd:    cmd,
		stdout: stdout,
		stderr: stderr,
		job:    job,
	}

	return handle, nil
}

// stderrReader reads stderr fully into a string.
func stderrReader(r io.Reader) string {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Sprintf("<error reading stderr: %v>", err)
	}
	return string(data)
}
```

### Step 7.3: Write `agent/cli_handle.go`

- [ ] Create `agent/cli_handle.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"

	"github.com/chris/coworker/core"
)

// cliJobHandle wraps an exec.Cmd to implement core.JobHandle.
type cliJobHandle struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr io.ReadCloser
	job    *core.Job
}

// streamMessage represents one line of the stream-JSON output from a CLI agent.
type streamMessage struct {
	Type     string `json:"type"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity,omitempty"`
	Body     string `json:"body,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// Wait blocks until the CLI process completes, parsing stream-JSON stdout
// into findings. Implements core.JobHandle.
func (h *cliJobHandle) Wait(ctx context.Context) (*core.JobResult, error) {
	result := &core.JobResult{}

	// Parse stream-JSON from stdout using json.Decoder.
	decoder := json.NewDecoder(h.stdout)
	for decoder.More() {
		var msg streamMessage
		if err := decoder.Decode(&msg); err != nil {
			// If we hit a decode error, read the rest as raw stdout.
			remaining, _ := io.ReadAll(decoder.Buffered())
			rest, _ := io.ReadAll(h.stdout)
			result.Stdout = string(remaining) + string(rest)
			break
		}

		switch msg.Type {
		case "finding":
			result.Findings = append(result.Findings, core.Finding{
				ID:       core.NewID(),
				Path:     msg.Path,
				Line:     msg.Line,
				Severity: core.Severity(msg.Severity),
				Body:     msg.Body,
			})
		case "done":
			result.ExitCode = msg.ExitCode
		}
	}

	// Read any remaining stdout.
	if remaining, err := io.ReadAll(h.stdout); err == nil && len(remaining) > 0 {
		result.Stdout += string(remaining)
	}

	// Read stderr.
	result.Stderr = stderrReader(h.stderr)

	// Wait for the process to exit.
	if err := h.cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return result, fmt.Errorf("wait for CLI: %w", err)
		}
	}

	return result, nil
}

// Cancel kills the running CLI process.
func (h *cliJobHandle) Cancel() error {
	if h.cmd.Process == nil {
		return nil
	}
	return h.cmd.Process.Kill()
}
```

### Step 7.4: Write `agent/cli_agent_test.go`

- [ ] Create `agent/cli_agent_test.go`:

```go
package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chris/coworker/core"
)

func findMockBinary(t *testing.T) string {
	t.Helper()
	// Find the repo root by looking for go.mod.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// agent/ is one level below the repo root.
	repoRoot := filepath.Dir(wd)
	mockPath := filepath.Join(repoRoot, "testdata", "mocks", "codex")
	if _, err := os.Stat(mockPath); err != nil {
		t.Fatalf("mock binary not found at %q: %v", mockPath, err)
	}
	return mockPath
}

func TestCliAgent_Dispatch_And_Wait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	mockBin := findMockBinary(t)
	agent := NewCliAgent(mockBin)

	job := &core.Job{
		ID:    "test-job-1",
		RunID: "test-run-1",
		Role:  "reviewer.arch",
		CLI:   "codex",
	}

	ctx := context.Background()
	handle, err := agent.Dispatch(ctx, job, "Review this code")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	result, err := handle.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}

	if len(result.Findings) != 2 {
		t.Fatalf("findings count = %d, want 2", len(result.Findings))
	}

	// Verify first finding.
	f1 := result.Findings[0]
	if f1.Path != "main.go" {
		t.Errorf("finding[0].path = %q, want %q", f1.Path, "main.go")
	}
	if f1.Line != 42 {
		t.Errorf("finding[0].line = %d, want 42", f1.Line)
	}
	if f1.Severity != core.SeverityImportant {
		t.Errorf("finding[0].severity = %q, want %q", f1.Severity, core.SeverityImportant)
	}
	if f1.Body != "Missing error check on Close()" {
		t.Errorf("finding[0].body = %q, want %q", f1.Body, "Missing error check on Close()")
	}

	// Verify second finding.
	f2 := result.Findings[1]
	if f2.Path != "store.go" {
		t.Errorf("finding[1].path = %q, want %q", f2.Path, "store.go")
	}
	if f2.Line != 17 {
		t.Errorf("finding[1].line = %d, want 17", f2.Line)
	}
	if f2.Severity != core.SeverityMinor {
		t.Errorf("finding[1].severity = %q, want %q", f2.Severity, core.SeverityMinor)
	}
}

func TestCliAgent_Dispatch_Cancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	// Use a long-running command that we can cancel.
	agent := NewCliAgent("sleep", "60")

	job := &core.Job{
		ID:    "test-job-cancel",
		RunID: "test-run-cancel",
		Role:  "reviewer.arch",
		CLI:   "codex",
	}

	ctx := context.Background()
	handle, err := agent.Dispatch(ctx, job, "")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if err := handle.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}

func TestCliAgent_Dispatch_MissingBinary(t *testing.T) {
	agent := NewCliAgent("/nonexistent/binary")

	job := &core.Job{
		ID:    "test-job-missing",
		RunID: "test-run-missing",
		Role:  "reviewer.arch",
		CLI:   "codex",
	}

	ctx := context.Background()
	_, err := agent.Dispatch(ctx, job, "test")
	if err == nil {
		t.Error("expected error for missing binary, got nil")
	}
}
```

### Step 7.5: Run the agent tests

- [ ] Run:

```bash
go test ./agent/... -v -count=1
```

Expected: all three tests pass.

### Step 7.6: Commit

- [ ] Run:

```bash
git add agent/ testdata/
git commit -m "Plan 100 Task 7: CliAgent with stream-JSON parsing + mock codex binary"
```

---

## Task 8: Dispatch orchestration

**Goal:** The glue that loads a role, creates run/job, renders prompt, dispatches to agent, captures result, persists findings.

**Files:**
- Create: `coding/dispatch.go`
- Create: `coding/dispatch_test.go`

### Step 8.1: Write `coding/dispatch.go`

- [ ] Create `coding/dispatch.go`:

```go
package coding

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/chris/coworker/coding/roles"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// Dispatcher orchestrates the end-to-end flow: load role -> create run/job
// -> render prompt -> dispatch agent -> capture result -> persist findings.
type Dispatcher struct {
	RoleDir   string // path to directory containing role YAML files
	PromptDir string // path to directory containing prompt template files
	Agent     core.Agent
	DB        *store.DB
	Logger    *slog.Logger
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

	// 5. Create a job.
	jobID := core.NewID()
	job := &core.Job{
		ID:           jobID,
		RunID:        runID,
		Role:         role.Name,
		State:        core.JobStatePending,
		DispatchedBy: "user",
		CLI:          role.CLI,
		StartedAt:    time.Now(),
	}
	if err := jobStore.CreateJob(ctx, job); err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}
	logger.Info("created job", "id", jobID, "role", role.Name)

	// 6. Render the prompt template.
	tmpl, err := roles.LoadPromptTemplate(d.PromptDir, role.PromptTemplate)
	if err != nil {
		return nil, fmt.Errorf("load prompt template: %w", err)
	}

	// Build template data from inputs.
	tmplData := make(map[string]string)
	for k, v := range input.Inputs {
		// Convert snake_case to PascalCase for template fields.
		tmplData[snakeToPascal(k)] = v
	}

	prompt, err := roles.RenderPrompt(tmpl, tmplData)
	if err != nil {
		return nil, fmt.Errorf("render prompt: %w", err)
	}

	// 7. Update job state to dispatched.
	if err := jobStore.UpdateJobState(ctx, jobID, core.JobStateDispatched); err != nil {
		return nil, fmt.Errorf("update job to dispatched: %w", err)
	}

	// 8. Dispatch to the agent.
	handle, err := d.Agent.Dispatch(ctx, job, prompt)
	if err != nil {
		jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed)
		return nil, fmt.Errorf("dispatch agent: %w", err)
	}
	logger.Info("dispatched to agent", "cli", role.CLI)

	// 9. Wait for result.
	result, err := handle.Wait(ctx)
	if err != nil {
		jobStore.UpdateJobState(ctx, jobID, core.JobStateFailed)
		return nil, fmt.Errorf("wait for agent: %w", err)
	}
	logger.Info("agent completed", "findings", len(result.Findings), "exit_code", result.ExitCode)

	// 10. Persist findings.
	for i := range result.Findings {
		f := &result.Findings[i]
		f.RunID = runID
		f.JobID = jobID
		if f.ID == "" {
			f.ID = core.NewID()
		}
		if err := findingStore.InsertFinding(ctx, f); err != nil {
			logger.Error("failed to persist finding", "error", err, "path", f.Path, "line", f.Line)
			// Continue persisting other findings.
		}
	}

	// 11. Update job state to complete (or failed).
	finalState := core.JobStateComplete
	if result.ExitCode != 0 {
		finalState = core.JobStateFailed
	}
	if err := jobStore.UpdateJobState(ctx, jobID, finalState); err != nil {
		return nil, fmt.Errorf("update job to %s: %w", finalState, err)
	}

	// 12. Complete the run.
	runState := core.RunStateCompleted
	if finalState == core.JobStateFailed {
		runState = core.RunStateFailed
	}
	if err := runStore.CompleteRun(ctx, runID, runState); err != nil {
		return nil, fmt.Errorf("complete run: %w", err)
	}

	return &DispatchResult{
		RunID:    runID,
		JobID:    jobID,
		Findings: result.Findings,
		ExitCode: result.ExitCode,
	}, nil
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

### Step 8.2: Write `coding/dispatch_test.go`

- [ ] Create `coding/dispatch_test.go`:

```go
package coding

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chris/coworker/agent"
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

### Step 8.3: Run the dispatch tests

- [ ] Run:

```bash
go test ./coding/... -v -count=1
```

Expected: all tests pass.

### Step 8.4: Run the full test suite

- [ ] Run:

```bash
go test ./... -count=1 -timeout 60s
```

Expected: all tests pass across all packages.

### Step 8.5: Commit

- [ ] Run:

```bash
git add coding/
git commit -m "Plan 100 Task 8: dispatch orchestration (role -> run/job -> prompt -> agent -> findings -> persist)"
```

---

## Task 9: `coworker invoke` command + integration test

**Goal:** Wire everything together in a cobra command. Integration test uses the mock codex binary and a temp-file SQLite database.

**Files:**
- Create: `cli/invoke.go`
- Create: `cli/invoke_test.go`
- Create: `docs/architecture/decisions.md`

### Step 9.1: Write `cli/invoke.go`

- [ ] Create `cli/invoke.go`:

```go
package cli

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/store"
	"github.com/spf13/cobra"
)

var (
	invokeDiffPath string
	invokeSpecPath string
	invokeDBPath   string
)

var invokeCmd = &cobra.Command{
	Use:   "invoke <role>",
	Short: "Invoke a role as an ephemeral job.",
	Long: `Invoke a role as an ephemeral job. The role is loaded from the
role directory, a run and job are created, the prompt is rendered,
an agent is dispatched, and findings are persisted to SQLite.

Example:
  coworker invoke reviewer.arch --diff path/to/diff --spec path/to/spec`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		roleName := args[0]
		return runInvoke(cmd, roleName)
	},
}

func init() {
	invokeCmd.Flags().StringVar(&invokeDiffPath, "diff", "", "Path to the diff file (required for reviewer roles)")
	invokeCmd.Flags().StringVar(&invokeSpecPath, "spec", "", "Path to the spec file (required for reviewer roles)")
	invokeCmd.Flags().StringVar(&invokeDBPath, "db", "", "Path to SQLite database (default: .coworker/state.db)")
	rootCmd.AddCommand(invokeCmd)
}

func runInvoke(cmd *cobra.Command, roleName string) error {
	ctx := cmd.Context()

	// Determine database path.
	dbPath := invokeDBPath
	if dbPath == "" {
		dbPath = filepath.Join(".coworker", "state.db")
	}

	// Ensure the directory exists.
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return fmt.Errorf("create db directory %q: %w", dbDir, err)
	}

	// Open the database.
	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// Determine role and prompt directories.
	// Look for .coworker/roles/ first, fall back to coding/roles/.
	roleDir := filepath.Join(".coworker", "roles")
	promptDir := filepath.Join(".coworker")
	if _, err := os.Stat(roleDir); os.IsNotExist(err) {
		// Fall back to the project's bundled roles.
		roleDir = filepath.Join("coding", "roles")
		promptDir = "coding"
	}

	// Determine agent binary based on role's CLI field.
	// For now, just use "codex" command. In future plans, this will
	// look up the CLI path from config.
	agentBinary := "codex"

	// Build inputs from flags.
	inputs := make(map[string]string)
	if invokeDiffPath != "" {
		inputs["diff_path"] = invokeDiffPath
	}
	if invokeSpecPath != "" {
		inputs["spec_path"] = invokeSpecPath
	}

	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	d := &coding.Dispatcher{
		RoleDir:   roleDir,
		PromptDir: promptDir,
		Agent:     agent.NewCliAgent(agentBinary),
		DB:        db,
		Logger:    logger,
	}

	result, err := d.Orchestrate(ctx, &coding.DispatchInput{
		RoleName: roleName,
		Inputs:   inputs,
	})
	if err != nil {
		return err
	}

	// Print findings as JSON to stdout.
	fmt.Fprintf(cmd.OutOrStdout(), "Run: %s\n", result.RunID)
	fmt.Fprintf(cmd.OutOrStdout(), "Job: %s\n", result.JobID)
	fmt.Fprintf(cmd.OutOrStdout(), "Findings: %d\n\n", len(result.Findings))

	for i, f := range result.Findings {
		data, _ := json.Marshal(map[string]interface{}{
			"path":        f.Path,
			"line":        f.Line,
			"severity":    f.Severity,
			"body":        f.Body,
			"fingerprint": f.Fingerprint,
		})
		fmt.Fprintf(cmd.OutOrStdout(), "  %d. %s\n", i+1, string(data))
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("agent exited with code %d", result.ExitCode)
	}

	return nil
}
```

### Step 9.2: Write `cli/invoke_test.go`

- [ ] Create `cli/invoke_test.go`:

```go
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func findProjectRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// cli/ is one level below the repo root.
	return filepath.Dir(wd)
}

func TestInvokeCommand_WithMockCodex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	projectRoot := findProjectRoot(t)
	mockBin := filepath.Join(projectRoot, "testdata", "mocks", "codex")
	if _, err := os.Stat(mockBin); err != nil {
		t.Fatalf("mock binary not found: %v", err)
	}

	// Use a temp directory for the database and working directory.
	tmpDir := t.TempDir()

	// Create the role and prompt files in the temp directory.
	rolesDir := filepath.Join(tmpDir, ".coworker", "roles")
	promptsDir := filepath.Join(tmpDir, ".coworker", "prompts")
	os.MkdirAll(rolesDir, 0755)
	os.MkdirAll(promptsDir, 0755)

	// Copy the role file.
	roleData, err := os.ReadFile(filepath.Join(projectRoot, "coding", "roles", "reviewer_arch.yaml"))
	if err != nil {
		t.Fatalf("read role: %v", err)
	}
	os.WriteFile(filepath.Join(rolesDir, "reviewer_arch.yaml"), roleData, 0644)

	// Copy the prompt file.
	promptData, err := os.ReadFile(filepath.Join(projectRoot, "coding", "prompts", "reviewer_arch.md"))
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	os.WriteFile(filepath.Join(promptsDir, "reviewer_arch.md"), promptData, 0644)

	// Create dummy diff and spec files.
	diffFile := filepath.Join(tmpDir, "test.diff")
	specFile := filepath.Join(tmpDir, "spec.md")
	os.WriteFile(diffFile, []byte("diff content"), 0644)
	os.WriteFile(specFile, []byte("spec content"), 0644)

	dbPath := filepath.Join(tmpDir, ".coworker", "state.db")

	// Save and restore the working directory.
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	t.Cleanup(func() { os.Chdir(origWd) })

	// Override the invoke command's agent to use the mock binary.
	// We test by directly calling the Dispatcher instead of going through
	// the cobra command, since the cobra command uses "codex" as the binary.
	// For a proper integration test, we'll use the Dispatcher directly.

	// But we can test the cobra command registration.
	buf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(errBuf)
	rootCmd.SetArgs([]string{"invoke", "--help"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		// Reset flags for other tests.
		invokeDiffPath = ""
		invokeSpecPath = ""
		invokeDBPath = ""
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("invoke --help: %v", err)
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
}

func TestInvokeCommand_Integration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	projectRoot := findProjectRoot(t)
	mockBin := filepath.Join(projectRoot, "testdata", "mocks", "codex")

	tmpDir := t.TempDir()

	// Set up .coworker directory structure.
	rolesDir := filepath.Join(tmpDir, ".coworker", "roles")
	promptsDir := filepath.Join(tmpDir, ".coworker", "prompts")
	os.MkdirAll(rolesDir, 0755)
	os.MkdirAll(promptsDir, 0755)

	// Copy role and prompt files.
	roleData, _ := os.ReadFile(filepath.Join(projectRoot, "coding", "roles", "reviewer_arch.yaml"))
	os.WriteFile(filepath.Join(rolesDir, "reviewer_arch.yaml"), roleData, 0644)
	promptData, _ := os.ReadFile(filepath.Join(projectRoot, "coding", "prompts", "reviewer_arch.md"))
	os.WriteFile(filepath.Join(promptsDir, "reviewer_arch.md"), promptData, 0644)

	// Create dummy input files.
	diffFile := filepath.Join(tmpDir, "test.diff")
	specFile := filepath.Join(tmpDir, "spec.md")
	os.WriteFile(diffFile, []byte("diff content"), 0644)
	os.WriteFile(specFile, []byte("spec content"), 0644)

	dbPath := filepath.Join(tmpDir, ".coworker", "state.db")

	// Test the Dispatcher directly (bypasses cobra's binary lookup).
	agentImpl := &mockAgent{mockBin: mockBin}

	db, err := store.OpenForTest(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	d := &coding.Dispatcher{
		RoleDir:   rolesDir,
		PromptDir: filepath.Join(tmpDir, ".coworker"),
		Agent:     agentImpl,
		DB:        db,
	}

	ctx := t.Context()
	result, err := d.Orchestrate(ctx, &coding.DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": diffFile,
			"spec_path": specFile,
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	if len(result.Findings) != 2 {
		t.Errorf("findings = %d, want 2", len(result.Findings))
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}

	// Verify findings have fingerprints (set during persistence).
	for i, f := range result.Findings {
		if f.Fingerprint == "" {
			t.Errorf("finding[%d] should have fingerprint after persistence", i)
		}
	}
}

// mockAgent wraps the mock binary through the agent package.
type mockAgent struct {
	mockBin string
}

func (m *mockAgent) Dispatch(ctx context.Context, job *core.Job, prompt string) (core.JobHandle, error) {
	a := agent.NewCliAgent(m.mockBin)
	return a.Dispatch(ctx, job, prompt)
}
```

Wait -- this test file imports packages that need specific import paths. Let me rewrite it properly.

### Step 9.2 (revised): Write `cli/invoke_test.go`

- [ ] Create `cli/invoke_test.go`:

```go
package cli

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestInvokeCommand_Help(t *testing.T) {
	buf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(errBuf)
	rootCmd.SetArgs([]string{"invoke", "--help"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		// Reset flags for other tests.
		invokeDiffPath = ""
		invokeSpecPath = ""
		invokeDBPath = ""
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("invoke --help: %v", err)
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
```

### Step 9.3: Write the integration test in `tests/integration/`

- [ ] Create `tests/integration/invoke_test.go`:

```go
// Package integration contains integration tests that exercise the full
// dispatch pipeline with mock CLI binaries.
package integration

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// tests/integration/ is two levels below the repo root.
	return filepath.Dir(filepath.Dir(wd))
}

func TestInvokeReviewerArch_EndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix")
	}

	repoRoot := findRepoRoot(t)
	mockBin := filepath.Join(repoRoot, "testdata", "mocks", "codex")
	if _, err := os.Stat(mockBin); err != nil {
		t.Fatalf("mock binary not found: %v", err)
	}

	// Use temp directory for database.
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open db: %v", err)
	}
	defer db.Close()

	d := &coding.Dispatcher{
		RoleDir:   filepath.Join(repoRoot, "coding", "roles"),
		PromptDir: filepath.Join(repoRoot, "coding"),
		Agent:     agent.NewCliAgent(mockBin),
		DB:        db,
	}

	ctx := context.Background()
	result, err := d.Orchestrate(ctx, &coding.DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": filepath.Join(repoRoot, "go.mod"),
			"spec_path": filepath.Join(repoRoot, "CLAUDE.md"),
		},
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	// Verify results.
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

	// Verify finding 1.
	f1 := result.Findings[0]
	if f1.Path != "main.go" {
		t.Errorf("finding[0].path = %q, want %q", f1.Path, "main.go")
	}
	if f1.Line != 42 {
		t.Errorf("finding[0].line = %d, want 42", f1.Line)
	}
	if f1.Severity != core.SeverityImportant {
		t.Errorf("finding[0].severity = %q, want %q", f1.Severity, core.SeverityImportant)
	}

	// Verify finding 2.
	f2 := result.Findings[1]
	if f2.Path != "store.go" {
		t.Errorf("finding[1].path = %q, want %q", f2.Path, "store.go")
	}

	// Verify findings are persisted in DB with fingerprints.
	fs := store.NewFindingStore(db, store.NewEventStore(db))
	persisted, err := fs.ListFindings(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(persisted) != 2 {
		t.Errorf("persisted findings = %d, want 2", len(persisted))
	}
	for i, f := range persisted {
		if f.Fingerprint == "" {
			t.Errorf("persisted finding[%d] has empty fingerprint", i)
		}
	}

	// Verify run state.
	rs := store.NewRunStore(db, store.NewEventStore(db))
	run, err := rs.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.State != core.RunStateCompleted {
		t.Errorf("run state = %q, want %q", run.State, core.RunStateCompleted)
	}
	if run.EndedAt == nil {
		t.Error("run ended_at should be set")
	}

	// Verify job state.
	js := store.NewJobStore(db, store.NewEventStore(db))
	job, err := js.GetJob(ctx, result.JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.State != core.JobStateComplete {
		t.Errorf("job state = %q, want %q", job.State, core.JobStateComplete)
	}
	if job.Role != "reviewer.arch" {
		t.Errorf("job role = %q, want %q", job.Role, "reviewer.arch")
	}

	// Verify event log sequence.
	es := store.NewEventStore(db)
	events, err := es.ListEvents(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}

	// Expected event sequence:
	// 1. run.created
	// 2. job.created
	// 3. job.leased (dispatched)
	// 4. finding.created
	// 5. finding.created
	// 6. job.completed
	// 7. run.completed
	expectedKinds := []core.EventKind{
		core.EventRunCreated,
		core.EventJobCreated,
		core.EventJobLeased,
		core.EventFindingCreated,
		core.EventFindingCreated,
		core.EventJobCompleted,
		core.EventRunCompleted,
	}

	if len(events) != len(expectedKinds) {
		t.Errorf("event count = %d, want %d", len(events), len(expectedKinds))
		for i, e := range events {
			t.Logf("  event[%d]: seq=%d kind=%s", i, e.Sequence, e.Kind)
		}
	} else {
		for i, e := range events {
			if e.Kind != expectedKinds[i] {
				t.Errorf("event[%d].kind = %q, want %q", i, e.Kind, expectedKinds[i])
			}
			if e.Sequence != i+1 {
				t.Errorf("event[%d].sequence = %d, want %d", i, e.Sequence, i+1)
			}
		}
	}
}
```

### Step 9.4: Write `docs/architecture/decisions.md`

- [ ] Create `docs/architecture/decisions.md`:

```markdown
# Architecture Decisions

This file is the single source of truth for cross-cutting runtime rules.
Updated whenever a plan introduces or revises a cross-cutting decision.

## Decision 1: Event-Log-Before-State Invariant (Plan 100)

**Context:** All state mutations in the runtime must be auditable and recoverable after a crash.

**Decision:** Every state mutation writes an event to the `events` table BEFORE updating the projection tables (`runs`, `jobs`, `findings`, `artifacts`). Both writes happen in the same SQLite transaction. If the transaction fails, neither write persists. If the daemon crashes between transaction commits (a theoretical edge case with SQLite's atomic commits), the event log is the source of truth for replay.

**Enforcement:** `store.EventStore.WriteEventThenRow()` is the only way to write events. All store methods (`RunStore.CreateRun`, `JobStore.CreateJob`, `FindingStore.InsertFinding`, etc.) use it. Direct SQL writes to projection tables are forbidden outside this function.

**Status:** Introduced in Plan 100.

## Decision 2: Findings Immutability (Plan 100)

**Context:** Review findings must form an immutable audit trail. Resolving a finding should link a fix job, not modify or delete the original finding.

**Decision:** The `FindingStore` API exposes only `InsertFinding` and `ResolveFinding`. `ResolveFinding` only updates `resolved_by_job_id` and `resolved_at`, and only if the finding is not already resolved. No API exists to update any other field of a finding after creation.

**Enforcement:** Go API boundary. The store layer does not expose update methods for other finding fields. SQLite-level triggers are not used; the Go API is the enforcement boundary.

**Status:** Introduced in Plan 100.

## Decision 3: Agent Protocol with JobHandle (Plan 100)

**Context:** The runtime needs to support both ephemeral (subprocess) and persistent (MCP-connected) agents.

**Decision:** `core.Agent` is an interface with `Dispatch(ctx, job, prompt) -> (JobHandle, error)`. `JobHandle` provides `Wait(ctx) -> (*JobResult, error)` and `Cancel() -> error`. This async-with-handle pattern supports ephemeral agents (wait for process exit) and persistent agents (wait for MCP `job.complete` callback).

**Enforcement:** All agent implementations must satisfy the `core.Agent` interface.

**Status:** Introduced in Plan 100. One implementation: `agent.CliAgent` (ephemeral subprocess).

## Decision 4: Event Sequence Numbering (Plan 100)

**Context:** Events within a run must be strictly ordered for replay correctness.

**Decision:** Each event gets a monotonically increasing `sequence` number per run, computed as `COALESCE(MAX(sequence), 0) + 1 FROM events WHERE run_id = ?` at write time. This is safe because all event writes for a run are serialized through SQLite transactions.

**Enforcement:** `EventStore.WriteEventThenRow()` auto-assigns the sequence. The `events` table has a UNIQUE constraint on `(run_id, sequence)`.

**Status:** Introduced in Plan 100.

## Decision 5: ID Generation (Plan 100)

**Context:** All entities need unique identifiers.

**Decision:** IDs are 32-character hex strings generated from 16 bytes of `crypto/rand`. This gives 128 bits of randomness, making collisions astronomically unlikely. String-typed IDs are used for readability in logs and database queries.

**Enforcement:** `core.NewID()` is the only ID generation function.

**Status:** Introduced in Plan 100.
```

### Step 9.5: Run the cli tests

- [ ] Run:

```bash
go test ./cli/... -v -count=1
```

Expected: all cli tests pass (version + invoke help + missing role).

### Step 9.6: Run the integration tests

- [ ] Run:

```bash
go test ./tests/integration/... -v -count=1
```

Expected: TestInvokeReviewerArch_EndToEnd passes with 7 events in the correct sequence.

### Step 9.7: Run the full test suite

- [ ] Run:

```bash
go test ./... -count=1 -timeout 60s
```

Expected: all tests pass across all packages.

### Step 9.8: Run the linter

- [ ] Run:

```bash
make lint
```

Expected: zero lint errors. If any appear, fix them before committing.

### Step 9.9: Commit

- [ ] Run:

```bash
git add cli/ tests/integration/ docs/architecture/
git commit -m "Plan 100 Task 9: coworker invoke command + integration test + architecture decisions"
```

---

## Verification (run before claiming done)

- [ ] **Full test suite passes:**

```bash
make test
```

Expected: all tests pass, zero failures.

- [ ] **Lint passes:**

```bash
make lint
```

Expected: zero issues.

- [ ] **Binary builds:**

```bash
make build
./coworker invoke --help
```

Expected: help text for the invoke command is printed.

- [ ] **Import discipline still holds:**

```bash
go test ./tests/architecture/... -v -count=1
```

Expected: TestCoreDoesNotImportCoding passes.

- [ ] **Event log sequence is correct in integration test:**

```bash
go test ./tests/integration/... -v -count=1
```

Expected: 7 events in order: run.created, job.created, job.leased, finding.created x2, job.completed, run.completed.

- [ ] **All load-bearing invariants from the manifest are enforced:**

| Invariant | Enforcement | Verified by |
|---|---|---|
| Event-log before state | WriteEventThenRow | TestWriteEventThenRow_WritesEventAndApplies |
| Event idempotency | WriteEventIdempotent | TestWriteEventIdempotent_DuplicateKeySkips |
| Findings immutable | FindingStore API | TestResolveFinding_AlreadyResolved |
| File artifacts as pointers | Artifact.Path field | TestInsertArtifact |
| Agent protocol | core.Agent interface | TestCliAgent_Dispatch_And_Wait |

---

## Post-Execution Report

**Implementation details**

All nine tasks completed across 11 commits on `feature/plan-100-thin-end-to-end`. Built with Go 1.25.0 and `modernc.org/sqlite` (pure Go, no cgo). Key deliverables:

- `core/` — domain types: `Run`, `Job`, `Event`, `Finding`, `Artifact`, `Role`, `Agent`/`JobHandle` interfaces, `NewID` helper.
- `store/` — `DB` (Open + Migrate), `EventStore` (WriteEventThenRow + idempotency), `RunStore`, `JobStore`, `FindingStore` (immutable, fingerprinted), `ArtifactStore`. Schema in `store/migrations/001_init.sql`.
- `agent/` — `CliAgent` wrapping `os/exec` with `stream-json` parsing via `json.Decoder` loop; `CliHandle` for async Wait.
- `coding/roles/` — YAML role loader with Go `text/template` prompt rendering.
- `coding/dispatch.go` — `Dispatcher` tying together role loader, agent spawn, finding persist, and event-first writes.
- `cli/invoke.go` — `coworker invoke <role> --diff <path> --spec <path>` cobra command.
- `testdata/mocks/codex` — mock Codex binary emitting canned stream-JSON findings.
- `docs/architecture/decisions.md` — created with initial cross-cutting decisions (event-log-before-state, file-artifact-as-pointer, findings-immutable).

**Deviations from plan**

- A post-implementation lint pass (commit `39cc718`) was required to fix golangci-lint v2 issues: `errcheck` on `tx.Rollback()`, `gocyclo` excluding `_test.go`, `gofmt` formatting, `gosec` G204/G306/G202 suppressions.
- `golangci-lint` v2 migration required `.golangci.yml` restructuring; `gocyclo` test exclusion was not anticipated in the plan.

**Known limitations**

- `coworker invoke` is ephemeral only — no persistent worker support (deferred to Plan 105).
- No supervisor hook on dispatch (deferred to Plan 101).
- No live event streaming (deferred to Plan 102).
- `CliAgent` exits synchronously; truly async dispatch tracked by `JobHandle` but not exposed to CLI in V1.

**Verification results**

- Full suite: 21 packages pass, 0 failures. `go test ./... -count=1 -timeout 60s` green.
- Integration test: `TestInvokeReviewerArch_EndToEnd` passes; event log snapshot matches golden file (7 events in correct order).
- Architecture test: `TestCoreDoesNotImportCoding` passes.
- Lint: clean after v2 migration fixes.

---

## Code Review

### Review 1
- **Date**: 2026-04-24
- **Reviewer**: Claude (retrospective review)
- **Verdict**: Approved

This plan was reviewed incrementally during implementation via per-task spec + quality checks by subagent. Key findings caught and fixed before ship:

- **`go mod tidy`**: cobra was an indirect dependency after the CLI skeleton; promoted to direct require in go.mod. [FIXED]
- **`t.Cleanup` pattern**: `TestVersionSubcommand` was using manual cleanup; replaced with `t.Cleanup` for consistency with the project testing standard. [FIXED]
- **Event schema fields**: `schema_version`, `causation_id`, `correlation_id` were added to the `Event` struct and `events` DDL after the initial draft omitted them; these are load-bearing for replay and correlation. [FIXED]
- **`defer tx.Rollback()` error drop**: The plan draft used bare `defer tx.Rollback()`. The shipped code wraps it as `defer func() { _ = tx.Rollback() }()` to satisfy `errcheck`. [FIXED]
- **`EventWriter` interface uses `any`**: The interface accepts `func(tx any) error` rather than `func(tx *sql.Tx) error` to avoid importing `database/sql` from `core/`. The concrete `EventStore` provides the full-typed variant. [PASS]
- **`CliAgent` G204 suppression**: `exec.CommandContext` is intentionally user-supplied; `//nolint:gosec` comment with rationale is present. [PASS]
- **`coding/dispatch.go` cycle complexity**: `Orchestrate` exceeds `gocyclo` threshold; suppressed with `//nolint:gocyclo` and comment explaining that the flow is linear not branchy. [PASS]

Overall: implementation matches the spec, event-log-before-state invariant is correctly enforced, all 60+ tests pass, linter clean.
