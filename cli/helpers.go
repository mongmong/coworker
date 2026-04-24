package cli

import "path/filepath"

// sessionLockPath derives the session lock file path from the database path.
// By convention the lock lives alongside the database in the same directory.
func sessionLockPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "session.lock")
}
