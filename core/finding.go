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

	// SourceJobIDs is populated in-memory during fan-in deduplication to record
	// all job IDs that produced the same fingerprint. It is NOT persisted to
	// SQLite — the individual per-job findings already carry their own JobID.
	SourceJobIDs []string `json:"-" yaml:"-"`
}

// ComputeFingerprint generates a stable fingerprint for deduplication.
// The fingerprint is based on path + line + severity + body, so that
// identical findings from different reviewers can be deduplicated.
func ComputeFingerprint(path string, line int, severity Severity, body string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s:%d:%s:%s", path, line, severity, body)
	return fmt.Sprintf("%x", h.Sum(nil))[:32]
}
