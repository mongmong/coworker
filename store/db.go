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
//
// Plan 139 (Codex CRITICAL #5): the DSN encodes PRAGMAs so they apply
// to every connection in the pool, not just the first one. Without
// this, fresh pooled connections silently lost foreign_keys
// enforcement under concurrent load. busy_timeout retries lock
// contention rather than failing immediately.
func Open(path string) (*DB, error) {
	dsn := buildSQLiteDSN(path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}

	// In-memory SQLite databases exist only on a single connection.
	// Limit the pool to one connection so all queries share the same DB
	// instance; otherwise concurrent or goroutine-based callers may
	// receive a fresh empty connection from the pool.
	if path == ":memory:" {
		sqlDB.SetMaxOpenConns(1)
	}

	// Probe the connection so PRAGMA failures (e.g., a corrupt file)
	// surface here instead of at the first real query.
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite %q: %w", path, err)
	}

	db := &DB{DB: sqlDB}
	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

// buildSQLiteDSN attaches per-connection PRAGMAs to the DSN. modernc.org/sqlite
// supports `_pragma=<name>(<value>)` query parameters that the driver applies
// every time it opens a fresh connection — avoiding the "first connection only"
// trap with sqlDB.Exec("PRAGMA ...").
//
// PRAGMAs we set:
//   - foreign_keys(1)   — enforce FK constraints. SQLite default is OFF; per-conn.
//   - busy_timeout(5000) — wait up to 5s for locks instead of failing immediately.
//   - journal_mode(WAL)  — concurrent readers + one writer. (WAL persists in
//     the file header so technically only the first conn needs to set it,
//     but encoding it here makes intent obvious and is harmless on later opens.)
//
// Plan 139 (Codex CRITICAL #5).
func buildSQLiteDSN(path string) string {
	pragmas := "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	if path == ":memory:" {
		// In-memory: pool is capped at 1 connection (above), so the
		// single connection gets the pragmas. Do NOT use cache=shared
		// here — that would make every Open(":memory:") call share the
		// same DB, breaking test isolation.
		return "file::memory:?" + pragmas
	}
	// Non-memory: file: URI form so query params parse correctly.
	return "file:" + path + "?" + pragmas
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
			_ = tx.Rollback()
			return fmt.Errorf("exec migration %d: %w", version, err)
		}

		if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}

	return nil
}
