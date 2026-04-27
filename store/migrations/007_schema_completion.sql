-- Plan 119: schema completion to match V1 spec data model.
-- Adds plans, checkpoints, supervisor_events, cost_events tables and the
-- spec-required columns on runs and jobs. All additive; existing rows remain valid.

-- Plans table - DAG nodes per run.
CREATE TABLE IF NOT EXISTS plans (
    id        TEXT PRIMARY KEY,
    run_id    TEXT NOT NULL REFERENCES runs(id),
    number    INTEGER NOT NULL,
    title     TEXT NOT NULL,
    blocks_on TEXT NOT NULL DEFAULT '[]',
    branch    TEXT NOT NULL DEFAULT '',
    pr_url    TEXT NOT NULL DEFAULT '',
    state     TEXT NOT NULL DEFAULT 'pending'
);

CREATE INDEX IF NOT EXISTS idx_plans_run_id ON plans(run_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_plans_run_number
    ON plans(run_id, number);

-- Checkpoints table - durable record of checkpoint decisions.
-- Paired with attention items: an open checkpoint == open attention item.
-- plan_id may be NULL for run-level checkpoints.
CREATE TABLE IF NOT EXISTS checkpoints (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL REFERENCES runs(id),
    plan_id     TEXT REFERENCES plans(id),
    kind        TEXT NOT NULL,
    state       TEXT NOT NULL DEFAULT 'open',
    decision    TEXT NOT NULL DEFAULT '',
    decided_by  TEXT NOT NULL DEFAULT '',
    decided_at  TEXT,
    notes       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_checkpoints_run_id ON checkpoints(run_id);
CREATE INDEX IF NOT EXISTS idx_checkpoints_plan_id ON checkpoints(plan_id);

-- Supervisor events - projection of supervisor.verdict events.
CREATE TABLE IF NOT EXISTS supervisor_events (
    id         TEXT PRIMARY KEY,
    run_id     TEXT NOT NULL REFERENCES runs(id),
    job_id     TEXT NOT NULL REFERENCES jobs(id),
    kind       TEXT NOT NULL,
    verdict    TEXT NOT NULL,
    rule_id    TEXT NOT NULL DEFAULT '',
    message    TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_supervisor_events_run_id ON supervisor_events(run_id);
CREATE INDEX IF NOT EXISTS idx_supervisor_events_job_id ON supervisor_events(job_id);

-- Cost events - projection of cost.delta events.
CREATE TABLE IF NOT EXISTS cost_events (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL REFERENCES runs(id),
    job_id      TEXT NOT NULL REFERENCES jobs(id),
    provider    TEXT NOT NULL,
    model       TEXT NOT NULL,
    tokens_in   INTEGER NOT NULL DEFAULT 0,
    tokens_out  INTEGER NOT NULL DEFAULT 0,
    usd         REAL NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_cost_events_run_id ON cost_events(run_id);
CREATE INDEX IF NOT EXISTS idx_cost_events_job_id ON cost_events(job_id);

-- Runs: spec-required columns.
ALTER TABLE runs ADD COLUMN prd_path TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN spec_path TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN budget_usd REAL;

-- Jobs: spec-required columns.
ALTER TABLE jobs ADD COLUMN plan_id TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN phase_index INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0;
