package core

// Artifact is a pointer to a file produced by a job.
// File artifacts are references by path; nothing durable is inlined into SQLite.
type Artifact struct {
	ID    string
	JobID string
	Kind  string // "diff", "spec", "plan", "report", "log", etc.
	Path  string // filesystem path, repo-relative
}
