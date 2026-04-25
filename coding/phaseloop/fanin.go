// Package phaseloop implements the per-phase execution loop for the
// build-from-prd workflow: developer → fan-out reviewers/tester → dedupe →
// fix-loop.
package phaseloop

import (
	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/core"
)

// AggregatedResults holds the merged output of a parallel fan-out step.
type AggregatedResults struct {
	// Findings is the raw (pre-dedupe) concatenation of all findings from
	// each dispatched job. Call DedupeFindings to collapse duplicates.
	Findings []core.Finding

	// Artifacts is the concatenation of artifacts from all dispatched jobs.
	// The phase executor logs a warning when two jobs produce the same path,
	// but does not treat it as a hard error at this layer.
	Artifacts []core.Artifact

	// TestsPassed is true iff every dispatched job exited with code 0.
	// A single non-zero exit code flips this to false.
	TestsPassed bool

	// TotalCost is the sum of costs reported by each job. Placeholder for
	// Plan 113+ cost tracking; always 0.0 in this plan.
	TotalCost float64
}

// AggregateResults merges the outputs from multiple parallel dispatch results
// into a single AggregatedResults. An empty results slice returns a zero-value
// AggregatedResults with TestsPassed == true (vacuous pass).
func AggregateResults(results []*coding.DispatchResult) *AggregatedResults {
	out := &AggregatedResults{TestsPassed: true}
	for _, r := range results {
		out.Findings = append(out.Findings, r.Findings...)
		out.Artifacts = append(out.Artifacts, r.Artifacts...)
		if r.ExitCode != 0 {
			out.TestsPassed = false
		}
	}
	return out
}

// DedupeFindings collapses findings with identical fingerprints into a single
// canonical finding. The canonical finding is the first occurrence encountered
// in the input slice. All source job IDs (including the canonical finding's own
// JobID) are recorded in Finding.SourceJobIDs.
//
// Findings without a fingerprint are computed on the fly via
// core.ComputeFingerprint before grouping.
//
// The order of the returned slice follows the first-occurrence order of each
// distinct fingerprint in the input.
func DedupeFindings(findings []core.Finding) []core.Finding {
	if len(findings) == 0 {
		return nil
	}

	type group struct {
		idx        int // position in out slice
		sourceJobs []string
	}

	out := make([]core.Finding, 0, len(findings))
	seen := make(map[string]*group, len(findings))

	for i := range findings {
		f := findings[i]

		// Compute fingerprint if missing.
		if f.Fingerprint == "" {
			f.Fingerprint = core.ComputeFingerprint(f.Path, f.Line, f.Severity, f.Body)
		}

		if g, exists := seen[f.Fingerprint]; exists {
			// Duplicate — accumulate source job ID.
			if f.JobID != "" {
				g.sourceJobs = append(g.sourceJobs, f.JobID)
				// Copy to avoid sharing the backing array with g.sourceJobs.
				jobsCopy := make([]string, len(g.sourceJobs))
				copy(jobsCopy, g.sourceJobs)
				out[g.idx].SourceJobIDs = jobsCopy
			}
			continue
		}

		// First occurrence — record canonical finding.
		idx := len(out)
		sourceJobs := []string{}
		if f.JobID != "" {
			sourceJobs = append(sourceJobs, f.JobID)
		}
		// Copy before storing so the out slice element and the group's
		// sourceJobs do not share the same backing array.
		jobsCopy := make([]string, len(sourceJobs))
		copy(jobsCopy, sourceJobs)
		f.SourceJobIDs = jobsCopy
		out = append(out, f)
		seen[f.Fingerprint] = &group{idx: idx, sourceJobs: sourceJobs}
	}

	return out
}
