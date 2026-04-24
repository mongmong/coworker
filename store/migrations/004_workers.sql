-- Plan 105: persistent worker registry.
-- Persistent CLI workers register here on boot and heartbeat every 15 s.
-- State: live -> stale -> evicted (watchdog transitions; see WorkerStore).

CREATE TABLE IF NOT EXISTS workers (
    handle            TEXT PRIMARY KEY,
    role              TEXT NOT NULL,
    pid               INTEGER NOT NULL DEFAULT 0,
    session_id        TEXT NOT NULL DEFAULT '',
    cli               TEXT NOT NULL,
    registered_at     TEXT NOT NULL,
    last_heartbeat_at TEXT NOT NULL,
    state             TEXT NOT NULL DEFAULT 'live'
);

CREATE INDEX IF NOT EXISTS idx_workers_role_state
    ON workers (role, state);
