package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

type eventSnapshot struct {
	Sequence       int            `json:"sequence"`
	Kind           core.EventKind `json:"kind"`
	SchemaVersion  int            `json:"schema_version"`
	RunID          string         `json:"run_id,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	CausationID    string         `json:"causation_id,omitempty"`
	CorrelationID  string         `json:"correlation_id,omitempty"`
	Payload        any            `json:"payload,omitempty"`
}

type tokenMapper struct {
	prefix string
	next   int
	seen   map[string]string
}

func newTokenMapper(prefix string) *tokenMapper {
	return &tokenMapper{
		prefix: prefix,
		seen:   make(map[string]string),
	}
}

func (m *tokenMapper) Normalize(raw string) string {
	if raw == "" {
		return ""
	}
	if normalized, ok := m.seen[raw]; ok {
		return normalized
	}
	m.next++
	normalized := fmt.Sprintf("%s_%d", m.prefix, m.next)
	m.seen[raw] = normalized
	return normalized
}

type snapshotNormalizers struct {
	runs      *tokenMapper
	jobs      *tokenMapper
	findings  *tokenMapper
	artifacts *tokenMapper
	generic   *tokenMapper
}

func newSnapshotNormalizers() *snapshotNormalizers {
	return &snapshotNormalizers{
		runs:      newTokenMapper("run"),
		jobs:      newTokenMapper("job"),
		findings:  newTokenMapper("finding"),
		artifacts: newTokenMapper("artifact"),
		generic:   newTokenMapper("id"),
	}
}

func (n *snapshotNormalizers) lookup(raw string) (string, bool) {
	for _, mapper := range []*tokenMapper{n.runs, n.jobs, n.findings, n.artifacts, n.generic} {
		if normalized, ok := mapper.seen[raw]; ok {
			return normalized, true
		}
	}
	return "", false
}

func (n *snapshotNormalizers) normalizeID(field, raw string) string {
	if raw == "" {
		return ""
	}
	if normalized, ok := n.lookup(raw); ok {
		return normalized
	}

	switch field {
	case "run_id":
		return n.runs.Normalize(raw)
	case "job_id", "resolved_by_job_id":
		return n.jobs.Normalize(raw)
	case "finding_id":
		return n.findings.Normalize(raw)
	case "artifact_id":
		return n.artifacts.Normalize(raw)
	default:
		return n.generic.Normalize(raw)
	}
}

func (n *snapshotNormalizers) normalizeValue(field string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, child := range typed {
			normalized[key] = n.normalizeValue(key, child)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for i, child := range typed {
			normalized[i] = n.normalizeValue(field, child)
		}
		return normalized
	case string:
		if field == "id" || strings.HasSuffix(field, "_id") {
			return n.normalizeID(field, typed)
		}
		return typed
	default:
		return typed
	}
}

func loadEventSnapshot(ctx context.Context, eventStore *store.EventStore, runID string) ([]eventSnapshot, error) {
	events, err := eventStore.ListEvents(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("list events for snapshot: %w", err)
	}

	normalizers := newSnapshotNormalizers()
	snapshots := make([]eventSnapshot, 0, len(events))

	for _, event := range events {
		normalizedRunID := normalizers.normalizeID("run_id", event.RunID)

		var payloadValue any
		if event.Payload != "" {
			if err := json.Unmarshal([]byte(event.Payload), &payloadValue); err != nil {
				payloadValue = event.Payload
			}
			payloadValue = normalizers.normalizeValue("payload", payloadValue)
		}

		snapshots = append(snapshots, eventSnapshot{
			Sequence:       event.Sequence,
			Kind:           event.Kind,
			SchemaVersion:  event.SchemaVersion,
			RunID:          normalizedRunID,
			IdempotencyKey: event.IdempotencyKey,
			CausationID:    normalizers.normalizeID("causation_id", event.CausationID),
			CorrelationID:  normalizers.normalizeID("correlation_id", event.CorrelationID),
			Payload:        payloadValue,
		})
	}

	return snapshots, nil
}

// AssertGoldenEvents compares normalized events for one run against a golden file.
// Set GOLDEN_UPDATE=1 to rewrite the golden snapshot in place.
func AssertGoldenEvents(t *testing.T, eventStore *store.EventStore, runID string, goldenFile string) {
	t.Helper()

	snapshots, err := loadEventSnapshot(context.Background(), eventStore, runID)
	if err != nil {
		t.Fatalf("load event snapshot: %v", err)
	}

	got, err := json.MarshalIndent(snapshots, "", "  ")
	if err != nil {
		t.Fatalf("marshal event snapshot: %v", err)
	}
	got = append(got, '\n')

	if err := os.MkdirAll(filepath.Dir(goldenFile), 0o755); err != nil {
		t.Fatalf("create golden directory: %v", err)
	}

	if os.Getenv("GOLDEN_UPDATE") == "1" {
		if err := os.WriteFile(goldenFile, got, 0o600); err != nil {
			t.Fatalf("rewrite golden file: %v", err)
		}
		return
	}

	want, err := os.ReadFile(goldenFile)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.WriteFile(goldenFile, got, 0o600); err != nil {
				t.Fatalf("create golden file: %v", err)
			}
			t.Logf("created golden file: %s", goldenFile)
			return
		}
		t.Fatalf("read golden file %q: %v", goldenFile, err)
	}

	actualFile := goldenFile + ".actual"
	if bytes.Equal(want, got) {
		if err := os.Remove(actualFile); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove stale actual file %q: %v", actualFile, err)
		}
		return
	}

	if err := os.WriteFile(actualFile, got, 0o600); err != nil {
		t.Fatalf("write actual snapshot %q: %v", actualFile, err)
	}

	t.Fatalf("event snapshot mismatch for %s\nactual: %s\n%s", goldenFile, actualFile, diffLines(string(want), string(got)))
}

func diffLines(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	table := make([][]int, len(wantLines)+1)
	for i := range table {
		table[i] = make([]int, len(gotLines)+1)
	}

	for i := len(wantLines) - 1; i >= 0; i-- {
		for j := len(gotLines) - 1; j >= 0; j-- {
			if wantLines[i] == gotLines[j] {
				table[i][j] = table[i+1][j+1] + 1
				continue
			}
			if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}

	var out strings.Builder
	out.WriteString("--- want\n+++ got\n")

	i, j := 0, 0
	for i < len(wantLines) && j < len(gotLines) {
		if wantLines[i] == gotLines[j] {
			out.WriteString(" ")
			out.WriteString(wantLines[i])
			out.WriteByte('\n')
			i++
			j++
			continue
		}
		if table[i+1][j] >= table[i][j+1] {
			out.WriteString("-")
			out.WriteString(wantLines[i])
			out.WriteByte('\n')
			i++
			continue
		}
		out.WriteString("+")
		out.WriteString(gotLines[j])
		out.WriteByte('\n')
		j++
	}

	for ; i < len(wantLines); i++ {
		out.WriteString("-")
		out.WriteString(wantLines[i])
		out.WriteByte('\n')
	}
	for ; j < len(gotLines); j++ {
		out.WriteString("+")
		out.WriteString(gotLines[j])
		out.WriteByte('\n')
	}

	return out.String()
}
