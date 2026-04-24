-- Plan 105: allow worker events with no run_id.
-- SQLite cannot drop a NOT NULL constraint; we recreate the table.
-- This migration is a no-op if run_id is already nullable.
PRAGMA foreign_keys=OFF;

CREATE TABLE IF NOT EXISTS events_new (
    id              TEXT PRIMARY KEY,
    run_id          TEXT,  -- nullable for worker-scope events
    sequence        INTEGER NOT NULL DEFAULT 0,
    kind            TEXT NOT NULL,
    schema_version  INTEGER NOT NULL DEFAULT 1,
    idempotency_key TEXT,
    causation_id    TEXT,
    correlation_id  TEXT,
    payload         TEXT NOT NULL DEFAULT '{}',
    created_at      TEXT NOT NULL
);

INSERT INTO events_new SELECT * FROM events;
DROP TABLE events;
ALTER TABLE events_new RENAME TO events;

CREATE UNIQUE INDEX IF NOT EXISTS idx_events_run_id_sequence ON events(run_id, sequence);
CREATE UNIQUE INDEX IF NOT EXISTS idx_events_idempotency_key ON events(idempotency_key);
CREATE INDEX IF NOT EXISTS idx_events_run_id ON events(run_id);
CREATE INDEX IF NOT EXISTS idx_events_kind   ON events(kind);

PRAGMA foreign_keys=ON;
