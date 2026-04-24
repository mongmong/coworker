-- Plan 104 Task 4: dispatch queue for the pull-model worker protocol.
-- Workers poll orch_next_dispatch to claim work; orch_job_complete marks completion.
-- State machine: pending -> leased -> completed | expired (-> pending on re-queue)

CREATE TABLE IF NOT EXISTS dispatches (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    role TEXT NOT NULL,
    job_id TEXT,
    prompt TEXT,
    inputs TEXT,  -- JSON
    state TEXT NOT NULL DEFAULT 'pending',  -- pending, leased, completed, expired
    worker_handle TEXT,
    leased_at TEXT,
    completed_at TEXT,
    created_at TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id)
);

CREATE INDEX IF NOT EXISTS idx_dispatches_state ON dispatches(state);
CREATE INDEX IF NOT EXISTS idx_dispatches_role ON dispatches(role);
