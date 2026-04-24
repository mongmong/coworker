# Plan 103 — Freeform Workflow + Attention Queue Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans` to implement this plan task-by-task.

**Goal:** Enable interactive ad-hoc role dispatch via `coworker session` and `coworker invoke`, unify four kinds of human-input flow (permission, subprocess, question, checkpoint) into a single attention queue, implement policy configuration loading with source tracking, and record user hand-edits as synthetic jobs.

**Architecture:** New packages:
- `core/attention.go` — `AttentionItem` struct + `AttentionKind` enum
- `core/policy.go` — `Policy` struct + layered config merging
- `core/workflow.go` — `Workflow` interface + `FreeformWorkflow` implementation
- `store/attention_store.go` — CRUD operations on `attention` table
- `coding/policy.go` — policy loader with source tracking + validator
- New cobra commands: `cli/session.go`, `cli/advance.go`, `cli/rollback.go`, `cli/config_inspect.go`, `cli/record_human_edit.go`
- New SQLite migration: `store/migrations/002_attention.sql`

**Tech Stack:** Go 1.25+, `gopkg.in/yaml.v3`, `go-playground/validator`.

**Branch:** `feature/plan-103-freeform-attention` (already checked out).

**Manifest entry:** `docs/specs/001-plan-manifest.md` sections 103.1 through 103.7.

---

## Architecture Overview

Plan 103 layers four pieces on top of Plans 100–102:

1. **Policy Layering:** Loads built-in defaults → global config → repo config → repo policy → role YAML → CLI flags. `coworker config inspect` prints effective config with source annotations.

2. **Attention Queue:** Single table + store with 4 kinds (permission, subprocess, question, checkpoint). Each item has presented-on and answered-on channels (JSON lists of response sources).

3. **Workflow Protocol:** Minimal interface (`Workflow` with `Dispatch` method). `FreeformWorkflow` dispatches roles on demand with synthesized run/plan/phase context from user input.

4. **Session Envelope:** `coworker session` creates a run and holds a lock file at `.coworker/session.lock` with run ID + PID. Subsequent `coworker invoke` / `coworker advance` / `coworker rollback` commands read the lock and attach to the active session.

5. **Human-Edit Recording:** `coworker record-human-edit --commit <sha>` creates a synthetic `job.human-edit` event linking to the commit. Workspace dirty detection surfaces as `workspace.dirty` attention items.

6. **Session Commands:** `coworker session`, `coworker advance`, `coworker rollback`, `coworker config inspect`, `coworker record-human-edit`.

7. **Tests:** Session lifecycle, attention flows, policy loading, human-edit recording.

---

## Task 1: Attention Table + Schema Migration

**Files:**
- Create: `store/migrations/002_attention.sql`
- Create: `core/attention.go`
- Create: `store/attention_store.go`
- Create: `store/attention_store_test.go`

### Step 1.1: Add the attention table schema

- [ ] Create `store/migrations/002_attention.sql`:

```sql
-- Plan 103: Attention queue (unified human-input surface)
-- Four kinds: permission, subprocess, question, checkpoint
-- presented_on and answered_on are JSON arrays of response sources
-- answered_by is the source that provided the answer

CREATE TABLE IF NOT EXISTS attention (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    kind TEXT NOT NULL,
    source TEXT NOT NULL,
    job_id TEXT,
    question TEXT,
    options TEXT,
    presented_on TEXT,
    answered_on TEXT,
    answered_by TEXT,
    answer TEXT,
    created_at TEXT NOT NULL,
    resolved_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_attention_run_id ON attention(run_id);
CREATE INDEX IF NOT EXISTS idx_attention_kind ON attention(kind);
CREATE INDEX IF NOT EXISTS idx_attention_job_id ON attention(job_id);
CREATE INDEX IF NOT EXISTS idx_attention_answered_on ON attention(answered_on);
```

### Step 1.2: Define the AttentionItem struct

- [ ] Create `core/attention.go`:

```go
package core

import "time"

// AttentionKind identifies the type of attention item.
type AttentionKind string

const (
	AttentionPermission AttentionKind = "permission"
	AttentionSubprocess AttentionKind = "subprocess"
	AttentionQuestion   AttentionKind = "question"
	AttentionCheckpoint AttentionKind = "checkpoint"
)

// AttentionItem is a unified human-input request blocking a run or job.
type AttentionItem struct {
	ID          string            `json:"id"`
	RunID       string            `json:"run_id"`
	Kind        AttentionKind     `json:"kind"`
	Source      string            `json:"source"`
	JobID       string            `json:"job_id,omitempty"`
	Question    string            `json:"question,omitempty"`
	Options     []string          `json:"options,omitempty"`
	PresentedOn []string          `json:"presented_on,omitempty"` // e.g., ["tui", "cli_pane_1"]
	AnsweredOn  []string          `json:"answered_on,omitempty"`
	AnsweredBy  string            `json:"answered_by,omitempty"`
	Answer      string            `json:"answer,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	ResolvedAt  *time.Time        `json:"resolved_at,omitempty"`
}

// IsAnswered returns true if the item has been answered.
func (a *AttentionItem) IsAnswered() bool {
	return a.Answer != "" && a.AnsweredBy != ""
}
```

### Step 1.3: Write AttentionStore CRUD operations

- [ ] Create `store/attention_store.go`:

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

// AttentionStore provides access to the attention table.
type AttentionStore struct {
	db *DB
}

// NewAttentionStore creates a new AttentionStore.
func NewAttentionStore(db *DB) *AttentionStore {
	return &AttentionStore{db: db}
}

// InsertAttention creates a new attention item.
func (s *AttentionStore) InsertAttention(ctx context.Context, item *core.AttentionItem) error {
	if item.ID == "" {
		item.ID = core.NewID()
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}

	presentedOn, _ := json.Marshal(item.PresentedOn)
	answeredOn, _ := json.Marshal(item.AnsweredOn)
	options, _ := json.Marshal(item.Options)

	query := `
		INSERT INTO attention (
			id, run_id, kind, source, job_id, question, options,
			presented_on, answered_on, answered_by, answer, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		item.ID, item.RunID, item.Kind, item.Source, item.JobID,
		item.Question, string(options),
		string(presentedOn), string(answeredOn), item.AnsweredBy, item.Answer,
		item.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert attention: %w", err)
	}
	return nil
}

// GetAttentionByID retrieves an attention item by ID.
func (s *AttentionStore) GetAttentionByID(ctx context.Context, id string) (*core.AttentionItem, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, run_id, kind, source, job_id, question, options,
		presented_on, answered_on, answered_by, answer, created_at, resolved_at
		FROM attention WHERE id = ?`, id)

	item := &core.AttentionItem{}
	var options, presentedOn, answeredOn, resolvedAt sql.NullString

	err := row.Scan(
		&item.ID, &item.RunID, &item.Kind, &item.Source, &item.JobID,
		&item.Question, &options,
		&presentedOn, &answeredOn, &item.AnsweredBy, &item.Answer,
		&item.CreatedAt, &resolvedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return fmt.Errorf("get attention: %w", err)
	}

	_ = json.Unmarshal([]byte(options.String), &item.Options)
	_ = json.Unmarshal([]byte(presentedOn.String), &item.PresentedOn)
	_ = json.Unmarshal([]byte(answeredOn.String), &item.AnsweredOn)

	if resolvedAt.Valid {
		t, _ := time.Parse(time.RFC3339, resolvedAt.String)
		item.ResolvedAt = &t
	}

	return item, nil
}

// ListAttentionByRun retrieves all attention items for a run, optionally filtered by kind.
func (s *AttentionStore) ListAttentionByRun(ctx context.Context, runID string, kind *core.AttentionKind) ([]*core.AttentionItem, error) {
	query := `SELECT id, run_id, kind, source, job_id, question, options,
	presented_on, answered_on, answered_by, answer, created_at, resolved_at
	FROM attention WHERE run_id = ?`
	args := []interface{}{runID}

	if kind != nil {
		query += ` AND kind = ?`
		args = append(args, string(*kind))
	}

	query += ` ORDER BY created_at ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list attention: %w", err)
	}
	defer rows.Close()

	var items []*core.AttentionItem
	for rows.Next() {
		item := &core.AttentionItem{}
		var options, presentedOn, answeredOn, resolvedAt sql.NullString

		if err := rows.Scan(
			&item.ID, &item.RunID, &item.Kind, &item.Source, &item.JobID,
			&item.Question, &options,
			&presentedOn, &answeredOn, &item.AnsweredBy, &item.Answer,
			&item.CreatedAt, &resolvedAt,
		); err != nil {
			return nil, fmt.Errorf("scan attention: %w", err)
		}

		_ = json.Unmarshal([]byte(options.String), &item.Options)
		_ = json.Unmarshal([]byte(presentedOn.String), &item.PresentedOn)
		_ = json.Unmarshal([]byte(answeredOn.String), &item.AnsweredOn)

		if resolvedAt.Valid {
			t, _ := time.Parse(time.RFC3339, resolvedAt.String)
			item.ResolvedAt = &t
		}

		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return items, nil
}

// AnswerAttention marks an attention item as answered.
func (s *AttentionStore) AnswerAttention(ctx context.Context, id, answer, answeredBy string) error {
	query := `
		UPDATE attention
		SET answered_on = json_array(json_extract(answered_on, '$') || ?),
		    answered_by = ?, answer = ?
		WHERE id = ?
	`

	_, err := s.db.ExecContext(ctx, query, answeredBy, answeredBy, answer, id)
	if err != nil {
		return fmt.Errorf("answer attention: %w", err)
	}
	return nil
}

// ResolveAttention marks an attention item as resolved.
func (s *AttentionStore) ResolveAttention(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	query := `UPDATE attention SET resolved_at = ? WHERE id = ?`
	_, err := s.db.ExecContext(ctx, query, now, id)
	if err != nil {
		return fmt.Errorf("resolve attention: %w", err)
	}
	return nil
}

// ListUnansweredByRun returns all unanswered attention items for a run.
func (s *AttentionStore) ListUnansweredByRun(ctx context.Context, runID string) ([]*core.AttentionItem, error) {
	query := `SELECT id, run_id, kind, source, job_id, question, options,
	presented_on, answered_on, answered_by, answer, created_at, resolved_at
	FROM attention WHERE run_id = ? AND answer IS NULL
	ORDER BY created_at ASC`

	rows, err := s.db.QueryContext(ctx, query, runID)
	if err != nil {
		return nil, fmt.Errorf("list unanswered: %w", err)
	}
	defer rows.Close()

	var items []*core.AttentionItem
	for rows.Next() {
		item := &core.AttentionItem{}
		var options, presentedOn, answeredOn, resolvedAt sql.NullString

		if err := rows.Scan(
			&item.ID, &item.RunID, &item.Kind, &item.Source, &item.JobID,
			&item.Question, &options,
			&presentedOn, &answeredOn, &item.AnsweredBy, &item.Answer,
			&item.CreatedAt, &resolvedAt,
		); err != nil {
			return nil, fmt.Errorf("scan unanswered: %w", err)
		}

		_ = json.Unmarshal([]byte(options.String), &item.Options)
		_ = json.Unmarshal([]byte(presentedOn.String), &item.PresentedOn)
		_ = json.Unmarshal([]byte(answeredOn.String), &item.AnsweredOn)

		if resolvedAt.Valid {
			t, _ := time.Parse(time.RFC3339, resolvedAt.String)
			item.ResolvedAt = &t
		}

		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return items, nil
}
```

### Step 1.4: Write tests for AttentionStore

- [ ] Create `store/attention_store_test.go`:

```go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func TestAttentionStore_InsertGetByID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db, _ := Open(":memory:")
	defer db.Close()

	store := NewAttentionStore(db)

	item := &core.AttentionItem{
		RunID:    "run_att_test",
		Kind:     core.AttentionQuestion,
		Source:   "user",
		Question: "Proceed?",
		Options:  []string{"yes", "no"},
	}

	if err := store.InsertAttention(ctx, item); err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	retrieved, _ := store.GetAttentionByID(ctx, item.ID)
	if retrieved == nil {
		t.Fatal("retrieved nil")
	}
	if retrieved.Question != "Proceed?" {
		t.Errorf("got question %q, want %q", retrieved.Question, "Proceed?")
	}
}

func TestAttentionStore_AnswerAttention(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db, _ := Open(":memory:")
	defer db.Close()

	store := NewAttentionStore(db)

	item := &core.AttentionItem{
		RunID:  "run_answer_test",
		Kind:   core.AttentionQuestion,
		Source: "user",
		Question: "Proceed?",
	}

	if err := store.InsertAttention(ctx, item); err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	if err := store.AnswerAttention(ctx, item.ID, "yes", "tui"); err != nil {
		t.Fatalf("answer failed: %v", err)
	}

	retrieved, _ := store.GetAttentionByID(ctx, item.ID)
	if retrieved.Answer != "yes" {
		t.Errorf("got answer %q, want %q", retrieved.Answer, "yes")
	}
	if retrieved.AnsweredBy != "tui" {
		t.Errorf("got answered_by %q, want %q", retrieved.AnsweredBy, "tui")
	}
}

func TestAttentionStore_ListUnanswered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db, _ := Open(":memory:")
	defer db.Close()

	store := NewAttentionStore(db)
	runID := "run_unanswered_test"

	for i := 1; i <= 3; i++ {
		item := &core.AttentionItem{
			RunID:    runID,
			Kind:     core.AttentionQuestion,
			Source:   "user",
			Question: "Q" + string(rune('0'+i)),
		}
		if err := store.InsertAttention(ctx, item); err != nil {
			t.Fatalf("insert %d failed: %v", i, err)
		}

		if i == 2 {
			if err := store.AnswerAttention(ctx, item.ID, "yes", "tui"); err != nil {
				t.Fatalf("answer failed: %v", err)
			}
		}
	}

	unanswered, _ := store.ListUnansweredByRun(ctx, runID)
	if len(unanswered) != 2 {
		t.Errorf("got %d unanswered, want 2", len(unanswered))
	}
}
```

---

## Task 2: Policy Loader + Config Layering

**Files:**
- Create: `core/policy.go`
- Create: `coding/policy.go`
- Create: `coding/policy_test.go`
- Create: `.coworker/policy.yaml` (built-in default template)

### Step 2.1: Define the Policy struct in core

- [ ] Create `core/policy.go`:

```go
package core

// Policy represents the merged configuration for a coworker run.
// It is built by layering built-in defaults, global config, repo config,
// and policy YAML. Each field is annotated with its source.
type Policy struct {
	// Checkpoints maps checkpoint kind to enforcement level.
	// "block" | "on-failure" | "auto" | "never"
	Checkpoints map[string]string `json:"checkpoints"`

	// SupervisorLimits defines retry and cycle caps.
	SupervisorLimits SupervisorLimitConfig `json:"supervisor_limits"`

	// Concurrency controls parallelism bounds.
	ConcurrencyConfig ConcurrencyConfig `json:"concurrency"`

	// Permissions defines the permission policy.
	PermissionConfig PermissionConfig `json:"permissions"`

	// WorkflowOverrides allows per-repo customization of workflow stages.
	WorkflowOverrides map[string]map[string][]string `json:"workflow_overrides"`

	// Source tracks where each field came from for `coworker config inspect`.
	// Key = "checkpoints.spec-approved", value = "repo policy.yaml line 5"
	Source map[string]string `json:"-"`
}

type SupervisorLimitConfig struct {
	MaxRetriesPerJob      int
	MaxFixCyclesPerPhase  int
}

type ConcurrencyConfig struct {
	MaxParallelPlans     int
	MaxParallelReviewers int
}

type PermissionConfig struct {
	OnUndeclared string // "block" | "warn" | "auto"
}

// NewDefaultPolicy returns the built-in default policy.
func NewDefaultPolicy() *Policy {
	return &Policy{
		Checkpoints: map[string]string{
			"spec-approved":     "block",
			"plan-approved":     "block",
			"phase-clean":       "on-failure",
			"ready-to-ship":     "block",
			"compliance-breach": "block",
			"quality-gate":      "block",
		},
		SupervisorLimits: SupervisorLimitConfig{
			MaxRetriesPerJob:     3,
			MaxFixCyclesPerPhase: 5,
		},
		ConcurrencyConfig: ConcurrencyConfig{
			MaxParallelPlans:     2,
			MaxParallelReviewers: 3,
		},
		PermissionConfig: PermissionConfig{
			OnUndeclared: "block",
		},
		WorkflowOverrides: make(map[string]map[string][]string),
		Source:            make(map[string]string),
	}
}
```

### Step 2.2: Implement the policy loader in coding

- [ ] Create `coding/policy.go`:

```go
package coding

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chris/coworker/core"
	"gopkg.in/yaml.v3"
)

// PolicyLoader loads and merges policy from multiple sources.
type PolicyLoader struct {
	GlobalConfigPath string // e.g., ~/.config/coworker/policy.yaml
	RepoConfigPath   string // e.g., .coworker/policy.yaml
}

// Load merges policies in order: built-in defaults -> global -> repo.
func (l *PolicyLoader) Load() (*core.Policy, error) {
	policy := core.NewDefaultPolicy()
	for k := range policy.Checkpoints {
		policy.Source["checkpoints."+k] = "built-in default"
	}

	// Load global config if it exists.
	if l.GlobalConfigPath != "" {
		if data, err := os.ReadFile(l.GlobalConfigPath); err == nil {
			rawPolicy := make(map[string]interface{})
			if err := yaml.Unmarshal(data, &rawPolicy); err != nil {
				return nil, fmt.Errorf("parse global policy: %w", err)
			}
			l.mergePolicy(policy, rawPolicy, "global "+l.GlobalConfigPath)
		}
	}

	// Load repo policy if it exists.
	if l.RepoConfigPath != "" {
		if data, err := os.ReadFile(l.RepoConfigPath); err == nil {
			rawPolicy := make(map[string]interface{})
			if err := yaml.Unmarshal(data, &rawPolicy); err != nil {
				return nil, fmt.Errorf("parse repo policy: %w", err)
			}
			l.mergePolicy(policy, rawPolicy, "repo "+l.RepoConfigPath)
		}
	}

	return policy, nil
}

// mergePolicy overlays YAML fields into the policy struct.
func (l *PolicyLoader) mergePolicy(p *core.Policy, raw map[string]interface{}, source string) {
	if checkpoints, ok := raw["checkpoints"].(map[string]interface{}); ok {
		if p.Checkpoints == nil {
			p.Checkpoints = make(map[string]string)
		}
		for k, v := range checkpoints {
			if s, ok := v.(string); ok {
				p.Checkpoints[k] = s
				p.Source["checkpoints."+k] = source
			}
		}
	}

	if limits, ok := raw["supervisor_limits"].(map[string]interface{}); ok {
		if v, ok := limits["max_retries_per_job"].(int); ok {
			p.SupervisorLimits.MaxRetriesPerJob = v
			p.Source["supervisor_limits.max_retries_per_job"] = source
		}
		if v, ok := limits["max_fix_cycles_per_phase"].(int); ok {
			p.SupervisorLimits.MaxFixCyclesPerPhase = v
			p.Source["supervisor_limits.max_fix_cycles_per_phase"] = source
		}
	}

	if conc, ok := raw["concurrency"].(map[string]interface{}); ok {
		if v, ok := conc["max_parallel_plans"].(int); ok {
			p.ConcurrencyConfig.MaxParallelPlans = v
			p.Source["concurrency.max_parallel_plans"] = source
		}
		if v, ok := conc["max_parallel_reviewers"].(int); ok {
			p.ConcurrencyConfig.MaxParallelReviewers = v
			p.Source["concurrency.max_parallel_reviewers"] = source
		}
	}

	if perms, ok := raw["permissions"].(map[string]interface{}); ok {
		if v, ok := perms["on_undeclared"].(string); ok {
			p.PermissionConfig.OnUndeclared = v
			p.Source["permissions.on_undeclared"] = source
		}
	}

	if overrides, ok := raw["workflow_overrides"].(map[string]interface{}); ok {
		if p.WorkflowOverrides == nil {
			p.WorkflowOverrides = make(map[string]map[string][]string)
		}
		for workflowName, stages := range overrides {
			if stagesMap, ok := stages.(map[string]interface{}); ok {
				if _, ok := p.WorkflowOverrides[workflowName]; !ok {
					p.WorkflowOverrides[workflowName] = make(map[string][]string)
				}
				for stageName, roleList := range stagesMap {
					if roles, ok := roleList.([]interface{}); ok {
						var roleStrs []string
						for _, r := range roles {
							if s, ok := r.(string); ok {
								roleStrs = append(roleStrs, s)
							}
						}
						p.WorkflowOverrides[workflowName][stageName] = roleStrs
						p.Source[fmt.Sprintf("workflow_overrides.%s.%s", workflowName, stageName)] = source
					}
				}
			}
		}
	}
}

// InspectString returns a human-readable policy dump with source annotations.
func InspectString(p *core.Policy) string {
	var sb strings.Builder
	sb.WriteString("# Effective Policy\n\n")

	sb.WriteString("## Checkpoints\n")
	for k, v := range p.Checkpoints {
		src := p.Source["checkpoints."+k]
		if src == "" {
			src = "unknown"
		}
		sb.WriteString(fmt.Sprintf("  %s: %s (from %s)\n", k, v, src))
	}

	sb.WriteString("\n## Supervisor Limits\n")
	src := p.Source["supervisor_limits.max_retries_per_job"]
	if src == "" {
		src = "built-in default"
	}
	sb.WriteString(fmt.Sprintf("  max_retries_per_job: %d (from %s)\n",
		p.SupervisorLimits.MaxRetriesPerJob, src))
	src = p.Source["supervisor_limits.max_fix_cycles_per_phase"]
	if src == "" {
		src = "built-in default"
	}
	sb.WriteString(fmt.Sprintf("  max_fix_cycles_per_phase: %d (from %s)\n",
		p.SupervisorLimits.MaxFixCyclesPerPhase, src))

	sb.WriteString("\n## Concurrency\n")
	src = p.Source["concurrency.max_parallel_plans"]
	if src == "" {
		src = "built-in default"
	}
	sb.WriteString(fmt.Sprintf("  max_parallel_plans: %d (from %s)\n",
		p.ConcurrencyConfig.MaxParallelPlans, src))
	src = p.Source["concurrency.max_parallel_reviewers"]
	if src == "" {
		src = "built-in default"
	}
	sb.WriteString(fmt.Sprintf("  max_parallel_reviewers: %d (from %s)\n",
		p.ConcurrencyConfig.MaxParallelReviewers, src))

	sb.WriteString("\n## Permissions\n")
	src = p.Source["permissions.on_undeclared"]
	if src == "" {
		src = "built-in default"
	}
	sb.WriteString(fmt.Sprintf("  on_undeclared: %s (from %s)\n",
		p.PermissionConfig.OnUndeclared, src))

	return sb.String()
}
```

### Step 2.3: Write policy loader tests

- [ ] Create `coding/policy_test.go`:

```go
package coding

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chris/coworker/core"
)

func TestPolicyLoader_DefaultPolicy(t *testing.T) {
	t.Parallel()

	loader := &PolicyLoader{}
	policy, err := loader.Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if policy.Checkpoints["spec-approved"] != "block" {
		t.Errorf("got spec-approved=%q, want block", policy.Checkpoints["spec-approved"])
	}
	if policy.SupervisorLimits.MaxRetriesPerJob != 3 {
		t.Errorf("got max retries=%d, want 3", policy.SupervisorLimits.MaxRetriesPerJob)
	}
}

func TestPolicyLoader_RepoOverride(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.yaml")

	// Write a simple override.
	content := `
checkpoints:
  phase-clean: block
supervisor_limits:
  max_retries_per_job: 5
`
	if err := os.WriteFile(policyPath, []byte(content), 0644); err != nil {
		t.Fatalf("write policy: %v", err)
	}

	loader := &PolicyLoader{RepoConfigPath: policyPath}
	policy, err := loader.Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if policy.Checkpoints["phase-clean"] != "block" {
		t.Errorf("got phase-clean=%q, want block", policy.Checkpoints["phase-clean"])
	}
	if policy.SupervisorLimits.MaxRetriesPerJob != 5 {
		t.Errorf("got max retries=%d, want 5", policy.SupervisorLimits.MaxRetriesPerJob)
	}
	if src, ok := policy.Source["supervisor_limits.max_retries_per_job"]; !ok || !contains(src, "policy.yaml") {
		t.Errorf("expected source to mention policy.yaml, got %q", src)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && len(substr) > 0 && s[len(s)-len(substr):] == substr
}
```

---

## Task 3: Workflow Interface + FreeformWorkflow

**Files:**
- Create: `core/workflow.go`
- Create: `coding/freeform_workflow.go`
- Create: `coding/freeform_workflow_test.go`

### Step 3.1: Define the Workflow interface

- [ ] Create `core/workflow.go`:

```go
package core

import "context"

// WorkflowKind identifies the workflow type.
type WorkflowKind string

const (
	WorkflowBuildFromPRD WorkflowKind = "build-from-prd"
	WorkflowFreeform     WorkflowKind = "freeform"
)

// DispatchInput is the input to a workflow's Dispatch method.
type DispatchInput struct {
	Role     string            // Role name to dispatch
	Inputs   map[string]string // Role inputs (e.g., diff_path, spec_path)
	PlanID   int               // Optional plan ID for context
	PhaseIdx int               // Optional phase index for context
}

// Workflow defines the orchestration contract for a run.
type Workflow interface {
	// Dispatch initiates a job for the given role with context.
	// Returns the job ID or an error.
	Dispatch(ctx context.Context, input *DispatchInput) (string, error)
}
```

### Step 3.2: Implement FreeformWorkflow

- [ ] Create `coding/freeform_workflow.go`:

```go
package coding

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// FreeformWorkflow is the minimal workflow for ad-hoc interactive dispatch.
// It creates jobs on-demand without enforcing phase structure or sequencing.
type FreeformWorkflow struct {
	RunID       string
	Dispatcher  *Dispatcher
	JobStore    *store.JobStore
	Logger      *slog.Logger
}

// Dispatch creates a job for the given role and returns the job ID.
func (fw *FreeformWorkflow) Dispatch(ctx context.Context, input *core.DispatchInput) (string, error) {
	logger := fw.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Create the job.
	jobID := core.NewID()
	job := &core.Job{
		ID:           jobID,
		RunID:        fw.RunID,
		Role:         input.Role,
		State:        core.JobStatePending,
		DispatchedBy: "user",
		CLI:          "", // Will be set by dispatcher
		StartedAt:    time.Now(),
	}

	if err := fw.JobStore.CreateJob(ctx, job); err != nil {
		return "", fmt.Errorf("create job: %w", err)
	}
	logger.Info("created job in freeform workflow", "id", jobID, "role", input.Role)

	return jobID, nil
}

// NewFreeformWorkflow creates a new FreeformWorkflow.
func NewFreeformWorkflow(
	runID string,
	dispatcher *Dispatcher,
	jobStore *store.JobStore,
	logger *slog.Logger,
) *FreeformWorkflow {
	return &FreeformWorkflow{
		RunID:      runID,
		Dispatcher: dispatcher,
		JobStore:   jobStore,
		Logger:     logger,
	}
}
```

### Step 3.3: Write workflow tests

- [ ] Create `coding/freeform_workflow_test.go`:

```go
package coding

import (
	"context"
	"testing"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

func TestFreeformWorkflow_Dispatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db, _ := store.Open(":memory:")
	defer db.Close()

	runStore := store.NewRunStore(db, store.NewEventStore(db))
	jobStore := store.NewJobStore(db, store.NewEventStore(db))

	runID := core.NewID()
	run := &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: core.Now(),
	}
	if err := runStore.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	fw := NewFreeformWorkflow(runID, nil, jobStore, nil)

	input := &core.DispatchInput{
		Role:   "developer",
		Inputs: map[string]string{},
	}

	jobID, err := fw.Dispatch(ctx, input)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if jobID == "" {
		t.Fatal("got empty job ID")
	}

	retrieved, _ := jobStore.GetJobByID(ctx, jobID)
	if retrieved == nil {
		t.Fatal("job not found")
	}
	if retrieved.Role != "developer" {
		t.Errorf("got role %q, want developer", retrieved.Role)
	}
	if retrieved.RunID != runID {
		t.Errorf("got runID %q, want %q", retrieved.RunID, runID)
	}
}
```

---

## Task 4: Session Envelope + Lock File Management

**Files:**
- Create: `core/session.go`
- Create: `coding/session.go`
- Create: `coding/session_test.go`

### Step 4.1: Define session types

- [ ] Create `core/session.go`:

```go
package core

import "time"

// Session represents an active user work session.
type Session struct {
	RunID     string
	CreatedAt time.Time
	PID       int
}
```

### Step 4.2: Implement session manager

- [ ] Create `coding/session.go`:

```go
package coding

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chris/coworker/core"
)

// SessionManager handles session lock files at .coworker/session.lock.
type SessionManager struct {
	LockPath string // e.g., .coworker/session.lock
}

// NewSessionManager creates a new manager at the given lock path.
func NewSessionManager(lockPath string) *SessionManager {
	return &SessionManager{LockPath: lockPath}
}

// CreateSession creates a new session and writes the lock file.
func (sm *SessionManager) CreateSession(runID string, pid int) error {
	session := &core.Session{
		RunID:     runID,
		PID:       pid,
		CreatedAt: core.Now(),
	}

	data, _ := json.Marshal(session)

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(sm.LockPath), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	// Write atomically: write to temp file, then rename.
	tmpPath := sm.LockPath + ".tmp"
	if err := ioutil.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp lock: %w", err)
	}

	if err := os.Rename(tmpPath, sm.LockPath); err != nil {
		return fmt.Errorf("rename lock: %w", err)
	}

	return nil
}

// GetActiveSession reads the lock file if it exists.
func (sm *SessionManager) GetActiveSession() (*core.Session, error) {
	data, err := ioutil.ReadFile(sm.LockPath)
	if os.IsNotExist(err) {
		return nil, nil // No active session.
	}
	if err != nil {
		return nil, fmt.Errorf("read lock: %w", err)
	}

	var session core.Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("parse lock: %w", err)
	}

	return &session, nil
}

// ClearSession removes the lock file.
func (sm *SessionManager) ClearSession() error {
	if err := os.Remove(sm.LockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove lock: %w", err)
	}
	return nil
}

// ValidateSessionPID checks if the process ID in the lock file is still alive.
// Returns (is_valid, error).
func (sm *SessionManager) ValidateSessionPID(pid int) (bool, error) {
	// Try to send signal 0 (no-op) to check if process is alive.
	p, err := os.FindProcess(pid)
	if err != nil {
		return false, nil // Process doesn't exist.
	}
	if p == nil {
		return false, nil
	}

	// On Unix, Signal(0) doesn't actually send a signal but checks permission.
	// If no error, the process is alive.
	if err := p.Signal(os.Signal(nil)); err != nil && !strings.Contains(err.Error(), "operation not permitted") {
		return false, nil
	}

	return true, nil
}
```

### Step 4.3: Write session tests

- [ ] Create `coding/session_test.go`:

```go
package coding

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chris/coworker/core"
)

func TestSessionManager_CreateAndGet(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lockPath := filepath.Join(dir, "session.lock")

	sm := NewSessionManager(lockPath)
	runID := "run_session_test"

	if err := sm.CreateSession(runID, os.Getpid()); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	session, err := sm.GetActiveSession()
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if session == nil {
		t.Fatal("got nil session")
	}
	if session.RunID != runID {
		t.Errorf("got runID %q, want %q", session.RunID, runID)
	}
}

func TestSessionManager_ClearSession(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lockPath := filepath.Join(dir, "session.lock")

	sm := NewSessionManager(lockPath)
	sm.CreateSession("run_clear_test", os.Getpid())

	if err := sm.ClearSession(); err != nil {
		t.Fatalf("clear failed: %v", err)
	}

	session, _ := sm.GetActiveSession()
	if session != nil {
		t.Fatal("session still exists after clear")
	}
}
```

---

## Task 5: Human-Edit Recording + Synthetic Jobs

**Files:**
- Create: `coding/human_edit.go`
- Create: `coding/human_edit_test.go`

### Step 5.1: Implement human-edit recording

- [ ] Create `coding/human_edit.go`:

```go
package coding

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// HumanEditRecorder creates synthetic human-edit jobs.
type HumanEditRecorder struct {
	RunID    string
	JobStore *store.JobStore
	Logger   *slog.Logger
}

// RecordCommit creates a synthetic job.human-edit event for a commit.
func (her *HumanEditRecorder) RecordCommit(ctx context.Context, commitSHA string) (string, error) {
	logger := her.Logger
	if logger == nil {
		logger = slog.Default()
	}

	jobID := core.NewID()
	job := &core.Job{
		ID:           jobID,
		RunID:        her.RunID,
		Role:         "human-edit",
		State:        core.JobStateComplete,
		DispatchedBy: "user",
		CLI:          "user-commit",
		StartedAt:    time.Now(),
	}

	now := time.Now()
	job.EndedAt = &now

	if err := her.JobStore.CreateJob(ctx, job); err != nil {
		return "", fmt.Errorf("create synthetic job: %w", err)
	}

	logger.Info("recorded human edit", "job_id", jobID, "commit", commitSHA)
	return jobID, nil
}

// NewHumanEditRecorder creates a new recorder.
func NewHumanEditRecorder(runID string, jobStore *store.JobStore, logger *slog.Logger) *HumanEditRecorder {
	return &HumanEditRecorder{
		RunID:    runID,
		JobStore: jobStore,
		Logger:   logger,
	}
}
```

### Step 5.2: Write human-edit tests

- [ ] Create `coding/human_edit_test.go`:

```go
package coding

import (
	"context"
	"testing"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

func TestHumanEditRecorder_RecordCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db, _ := store.Open(":memory:")
	defer db.Close()

	runStore := store.NewRunStore(db, store.NewEventStore(db))
	jobStore := store.NewJobStore(db, store.NewEventStore(db))

	runID := core.NewID()
	run := &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: core.Now(),
	}
	runStore.CreateRun(ctx, run)

	recorder := NewHumanEditRecorder(runID, jobStore, nil)
	jobID, err := recorder.RecordCommit(ctx, "abc123def456")
	if err != nil {
		t.Fatalf("record failed: %v", err)
	}
	if jobID == "" {
		t.Fatal("got empty job ID")
	}

	retrieved, _ := jobStore.GetJobByID(ctx, jobID)
	if retrieved == nil {
		t.Fatal("job not found")
	}
	if retrieved.Role != "human-edit" {
		t.Errorf("got role %q, want human-edit", retrieved.Role)
	}
	if retrieved.State != core.JobStateComplete {
		t.Errorf("got state %q, want complete", retrieved.State)
	}
}
```

---

## Task 6: CLI Commands (session, advance, rollback, config, record-human-edit)

**Files:**
- Create: `cli/session.go`
- Create: `cli/advance.go`
- Create: `cli/rollback.go`
- Create: `cli/config_inspect.go`
- Create: `cli/record_human_edit.go`

### Step 6.1: Implement `coworker session`

- [ ] Create `cli/session.go`:

```go
package cli

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
	"github.com/spf13/cobra"
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Start a new interactive session.",
	Long: `Start a new interactive session. Creates a run and lock file at
.coworker/session.lock. Subsequent coworker commands in the same
directory will attach to this session.

Example:
  coworker session`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSession(cmd)
	},
}

func init() {
	rootCmd.AddCommand(sessionCmd)
}

func runSession(cmd *cobra.Command) error {
	ctx := cmd.Context()

	// Open or create the database.
	dbPath := filepath.Join(".coworker", "state.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// Create a new run.
	eventStore := store.NewEventStore(db)
	runStore := store.NewRunStore(db, eventStore)

	runID := core.NewID()
	run := &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: core.Now(),
	}
	if err := runStore.CreateRun(ctx, run); err != nil {
		return fmt.Errorf("create run: %w", err)
	}

	// Create session lock file.
	lockPath := filepath.Join(".coworker", "session.lock")
	sm := coding.NewSessionManager(lockPath)
	if err := sm.CreateSession(runID, os.Getpid()); err != nil {
		return fmt.Errorf("create session lock: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	logger.Info("session started", "run_id", runID, "pid", os.Getpid())

	fmt.Fprintf(cmd.OutOrStdout(), "Session started: %s\n", runID)
	fmt.Fprintf(cmd.OutOrStdout(), "Lock file: %s\n", lockPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Use 'coworker invoke <role>' to dispatch work.\n")

	return nil
}
```

### Step 6.2: Implement `coworker advance`

- [ ] Create `cli/advance.go`:

```go
package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/store"
	"github.com/spf13/cobra"
)

var advanceCmd = &cobra.Command{
	Use:   "advance [checkpoint-id]",
	Short: "Skip or approve a checkpoint in the active session.",
	Long: `Advance past a checkpoint. If checkpoint-id is provided, resolves
that specific checkpoint. Otherwise, resolves the oldest unresolved
checkpoint in the active session's run.

Example:
  coworker advance
  coworker advance chk_abc123`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAdvance(cmd, args)
	},
}

func init() {
	rootCmd.AddCommand(advanceCmd)
}

func runAdvance(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Get active session.
	sm := coding.NewSessionManager(filepath.Join(".coworker", "session.lock"))
	session, err := sm.GetActiveSession()
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("no active session; run 'coworker session' first")
	}

	// Open database.
	db, err := store.Open(filepath.Join(".coworker", "state.db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// TODO: implement checkpoint resolution logic.
	// For now, just acknowledge the command.

	fmt.Fprintf(cmd.OutOrStdout(), "Advanced run %s\n", session.RunID)
	return nil
}
```

### Step 6.3: Implement `coworker rollback`

- [ ] Create `cli/rollback.go`:

```go
package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/store"
	"github.com/spf13/cobra"
)

var rollbackCmd = &cobra.Command{
	Use:   "rollback <checkpoint-id>",
	Short: "Rollback a prior decision in the active session.",
	Long: `Rollback past a checkpoint by ID, restoring the run to a prior state
for re-dispatch. Only checkpoints with reversible decisions are supported.

Example:
  coworker rollback chk_abc123`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRollback(cmd, args)
	},
}

func init() {
	rootCmd.AddCommand(rollbackCmd)
}

func runRollback(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	checkpointID := args[0]

	// Get active session.
	sm := coding.NewSessionManager(filepath.Join(".coworker", "session.lock"))
	session, err := sm.GetActiveSession()
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("no active session")
	}

	// Open database.
	db, err := store.Open(filepath.Join(".coworker", "state.db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// TODO: implement rollback logic.

	fmt.Fprintf(cmd.OutOrStdout(), "Rolled back checkpoint %s in run %s\n", checkpointID, session.RunID)
	return nil
}
```

### Step 6.4: Implement `coworker config inspect`

- [ ] Create `cli/config_inspect.go`:

```go
package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chris/coworker/coding"
	"github.com/spf13/cobra"
)

var configInspectCmd = &cobra.Command{
	Use:   "config inspect",
	Short: "Show the effective configuration with source annotations.",
	Long: `Print the effective configuration, showing where each setting
came from (built-in default, global, repo, or CLI).

Example:
  coworker config inspect`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigInspect(cmd)
	},
}

func init() {
	rootCmd.AddCommand(configInspectCmd)
}

func runConfigInspect(cmd *cobra.Command) error {
	// Load policy from built-in + global + repo.
	globalConfigPath := filepath.Join(os.ExpandEnv("$HOME"), ".config", "coworker", "policy.yaml")
	repoConfigPath := filepath.Join(".coworker", "policy.yaml")

	loader := &coding.PolicyLoader{
		GlobalConfigPath: globalConfigPath,
		RepoConfigPath:   repoConfigPath,
	}
	policy, err := loader.Load()
	if err != nil {
		return fmt.Errorf("load policy: %w", err)
	}

	output := coding.InspectString(policy)
	fmt.Fprint(cmd.OutOrStdout(), output)
	return nil
}
```

### Step 6.5: Implement `coworker record-human-edit`

- [ ] Create `cli/record_human_edit.go`:

```go
package cli

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/store"
	"github.com/spf13/cobra"
)

var recordHumanEditCommit string

var recordHumanEditCmd = &cobra.Command{
	Use:   "record-human-edit",
	Short: "Record a manual git commit as a synthetic human-edit job.",
	Long: `Create a synthetic human-edit job linked to a specific commit
SHA. This marks manual work in the event log for auditing.

Example:
  coworker record-human-edit --commit abc123def456`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRecordHumanEdit(cmd)
	},
}

func init() {
	recordHumanEditCmd.Flags().StringVar(&recordHumanEditCommit, "commit", "", "Git commit SHA to record (required)")
	recordHumanEditCmd.MarkFlagRequired("commit")
	rootCmd.AddCommand(recordHumanEditCmd)
}

func runRecordHumanEdit(cmd *cobra.Command) error {
	ctx := cmd.Context()

	// Get active session.
	sm := coding.NewSessionManager(filepath.Join(".coworker", "session.lock"))
	session, err := sm.GetActiveSession()
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("no active session")
	}

	// Open database.
	db, err := store.Open(filepath.Join(".coworker", "state.db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	jobStore := store.NewJobStore(db, store.NewEventStore(db))
	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	recorder := coding.NewHumanEditRecorder(session.RunID, jobStore, logger)
	jobID, err := recorder.RecordCommit(ctx, recordHumanEditCommit)
	if err != nil {
		return fmt.Errorf("record commit: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Recorded human edit: %s\n", jobID)
	return nil
}
```

---

## Task 7: Tests (Session Lifecycle, Attention Flows, Policy Loading, Human-Edit Recording)

**Files:**
- Create: `tests/integration/session_lifecycle_test.go`
- Create: `tests/integration/attention_flow_test.go`

### Step 7.1: Session lifecycle integration test

- [ ] Create `tests/integration/session_lifecycle_test.go`:

```go
package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

func TestSessionLifecycle(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lockPath := filepath.Join(dir, "session.lock")
	dbPath := filepath.Join(dir, "state.db")

	// Step 1: Create a session.
	sm := coding.NewSessionManager(lockPath)
	runID := core.NewID()
	if err := sm.CreateSession(runID, os.Getpid()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Step 2: Create database and run.
	ctx := context.Background()
	db, _ := store.Open(dbPath)
	defer db.Close()

	eventStore := store.NewEventStore(db)
	runStore := store.NewRunStore(db, eventStore)

	run := &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: core.Now(),
	}
	if err := runStore.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Step 3: Retrieve session from lock file.
	session, _ := sm.GetActiveSession()
	if session == nil {
		t.Fatal("session not found after creation")
	}
	if session.RunID != runID {
		t.Errorf("got runID %q, want %q", session.RunID, runID)
	}

	// Step 4: Clear session.
	if err := sm.ClearSession(); err != nil {
		t.Fatalf("clear session: %v", err)
	}

	session, _ = sm.GetActiveSession()
	if session != nil {
		t.Fatal("session still exists after clear")
	}
}
```

### Step 7.2: Attention flow integration test

- [ ] Create `tests/integration/attention_flow_test.go`:

```go
package integration

import (
	"context"
	"testing"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

func TestAttentionFlow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db, _ := store.Open(":memory:")
	defer db.Close()

	// Create a run.
	eventStore := store.NewEventStore(db)
	runStore := store.NewRunStore(db, eventStore)

	runID := core.NewID()
	run := &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: core.Now(),
	}
	if err := runStore.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Create an attention item.
	attentionStore := store.NewAttentionStore(db)

	item := &core.AttentionItem{
		RunID:    runID,
		Kind:     core.AttentionCheckpoint,
		Source:   "scheduler",
		Question: "Ready to ship?",
		Options:  []string{"yes", "no"},
	}

	if err := attentionStore.InsertAttention(ctx, item); err != nil {
		t.Fatalf("insert attention: %v", err)
	}

	// Query unanswered items.
	unanswered, _ := attentionStore.ListUnansweredByRun(ctx, runID)
	if len(unanswered) != 1 {
		t.Errorf("got %d unanswered, want 1", len(unanswered))
	}

	// Answer the question.
	if err := attentionStore.AnswerAttention(ctx, item.ID, "yes", "user-cli"); err != nil {
		t.Fatalf("answer: %v", err)
	}

	// Verify it's no longer unanswered.
	unanswered, _ = attentionStore.ListUnansweredByRun(ctx, runID)
	if len(unanswered) != 0 {
		t.Errorf("got %d unanswered after answer, want 0", len(unanswered))
	}

	// Retrieve and verify the answer.
	retrieved, _ := attentionStore.GetAttentionByID(ctx, item.ID)
	if retrieved.Answer != "yes" {
		t.Errorf("got answer %q, want yes", retrieved.Answer)
	}
	if retrieved.AnsweredBy != "user-cli" {
		t.Errorf("got answered_by %q, want user-cli", retrieved.AnsweredBy)
	}
}
```

---

## Post-Implementation Checklist

- [ ] Run `go test ./... -count=1 -timeout 60s` and verify all tests pass
- [ ] Run `golangci-lint run ./...` and fix any linter warnings
- [ ] Verify that all seven CLI commands are registered in root.go and callable
- [ ] Write a short README in docs/ describing the new session + attention queue features
- [ ] Update CLAUDE.md if any new conventions are needed
- [ ] Commit all code with message: "Plan 103 Phase [N]: [task description]"

---

## Code Review

### Review 1

- **Date**: 2026-04-23
- **Reviewer**: Codex (GPT-5.5)
- **Verdict**: Approved with required fixes

**Important**

1. `[FIXED]` **`AnswerAttention` stores `answered_on` as plain timestamp, not JSON array.**
   → Response: Now stores as `json.Marshal([]string{answeredBy})`. Test added to verify round-trip.

2. `[FIXED]` **No exclusive write guard on session lock file.**
   → Response: `writeLock` now uses `O_WRONLY|O_CREATE|O_EXCL` for atomic exclusive creation. Returns `ErrLockExists` if lock already exists.

3. `[FIXED]` **`coding/session` imports `*store.RunStore` by concrete type.**
   → Response: Extracted `RunRepository` interface in `coding/session/repository.go`. Manager now accepts the interface; `*store.RunStore` satisfies it.

**Suggestions**

4. `[WONTFIX]` `idx_attention_answered_on` index is of no practical use — accepted for now, can drop later.
5. `[WONTFIX]` PID in lock file stored but never checked for liveness — `CurrentSession` validates via DB state instead.
6. `[WONTFIX]` Event-before-row invariant inverted in `RecordCommit` — low-severity for synthetic human-edit jobs.
7. `[WONTFIX]` `core.Workflow` interface too minimal (only `Name()`) — extend when second workflow (build-from-prd, Plan 106) exists.
8. `[WONTFIX]` `config inspect` Cobra hierarchy — cosmetic, fix when `config` gets siblings.
9. `[FIXED]` Lock path hardcoded independently in 4 CLI commands — extracted `sessionLockPath()` helper in `cli/helpers.go`.
10. `[WONTFIX]` Integration tests don't cover `ResolveAttention` or concurrent session — add when those paths become critical.

---

## Post-Execution Report

*(To be filled after implementation is complete and verified)*
