package agent

import (
	"io"
	"os"
	"path/filepath"
)

// OpenJobLog creates the .coworker/runs/<runID>/jobs/<jobID>.jsonl file
// (creating parent directories as needed) and returns a WriteCloser.
// If coworkerDir is empty, returns a no-op WriteCloser backed by io.Discard
// so the caller's tee is a no-op.
func OpenJobLog(coworkerDir, runID, jobID string) (io.WriteCloser, error) {
	if coworkerDir == "" {
		return nopCloser{io.Discard}, nil
	}
	dir := filepath.Join(coworkerDir, "runs", runID, "jobs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, jobID+".jsonl")
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
}

// nopCloser wraps an io.Writer and provides a no-op Close method.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
