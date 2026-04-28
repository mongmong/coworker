package store

import (
	"context"
	"database/sql"
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

// TestOpen_PerConnectionPragmas verifies that foreign_keys and
// busy_timeout are set on EVERY pooled connection, not just the first
// one used at Open. Plan 139 (Codex CRITICAL #5) — without DSN-encoded
// PRAGMAs, fresh pooled connections silently lost FK enforcement.
//
// We force the pool to acquire two distinct connections by holding the
// first conn open while we ask for a second.
func TestOpen_PerConnectionPragmas(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(tmp + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Allow up to 4 connections so we can get a fresh one.
	db.SetMaxOpenConns(4)

	ctx := context.Background()
	conn1, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn1: %v", err)
	}
	defer conn1.Close()

	conn2, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn2: %v", err)
	}
	defer conn2.Close()

	for i, conn := range []*sql.Conn{conn1, conn2} {
		var fk, busy int
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("conn%d foreign_keys: %v", i+1, err)
		}
		if fk != 1 {
			t.Errorf("conn%d foreign_keys = %d, want 1 (PRAGMA must apply per-connection)", i+1, fk)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatalf("conn%d busy_timeout: %v", i+1, err)
		}
		if busy != 5000 {
			t.Errorf("conn%d busy_timeout = %d, want 5000ms", i+1, busy)
		}
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
