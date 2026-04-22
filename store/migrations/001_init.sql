-- Plan 100: initial schema for coworker runtime.
-- Tables: events, runs, jobs, findings, artifacts, schema_migrations.
-- The events table is the authoritative history; other tables are projections.

-- Schema migrations tracking.
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- SSE event log (authoritative history of a run).
CREATE TABLE IF NOT EXISTS events (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    kind TEXT NOT NULL,
    schema_version INTEGER NOT NULL DEFAULT 1,
    idempotency_key TEXT,
    causation_id TEXT,
    correlation_id TEXT,
    payload TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(run_id, sequence),
    UNIQUE(idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_events_run_id ON events(run_id);
CREATE INDEX IF NOT EXISTS idx_events_kind ON events(kind);

-- Top-level run.
CREATE TABLE IF NOT EXISTS runs (
    id TEXT PRIMARY KEY,
    mode TEXT NOT NULL DEFAULT 'interactive',
    state TEXT NOT NULL DEFAULT 'active',
    started_at TEXT NOT NULL,
    ended_at TEXT
);

-- Jobs = role invocations.
CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    role TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    dispatched_by TEXT NOT NULL DEFAULT 'scheduler',
    cli TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    ended_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_jobs_run_id ON jobs(run_id);
CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state);

-- Findings (immutable once written; only resolved_by_job_id and resolved_at can be updated).
CREATE TABLE IF NOT EXISTS findings (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    job_id TEXT NOT NULL REFERENCES jobs(id),
    path TEXT NOT NULL,
    line INTEGER NOT NULL,
    severity TEXT NOT NULL,
    body TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    resolved_by_job_id TEXT,
    resolved_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_findings_run_id ON findings(run_id);
CREATE INDEX IF NOT EXISTS idx_findings_fingerprint ON findings(fingerprint);

-- Artifacts (pointers to files on disk).
CREATE TABLE IF NOT EXISTS artifacts (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES jobs(id),
    kind TEXT NOT NULL,
    path TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_artifacts_job_id ON artifacts(job_id);
