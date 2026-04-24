-- Plan 103: Attention queue (unified human-input surface)
-- Four kinds: permission, subprocess, question, checkpoint
-- presented_on and answered_on are JSON arrays of response sources
-- answered_by is the source that provided the answer

CREATE TABLE IF NOT EXISTS attention (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    kind TEXT NOT NULL,
    source TEXT NOT NULL,
    job_id TEXT,
    question TEXT,
    options TEXT,
    presented_on TEXT,
    answered_on TEXT,
    answered_by TEXT,
    answer TEXT,
    created_at TEXT NOT NULL,
    resolved_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_attention_run_id ON attention(run_id);
CREATE INDEX IF NOT EXISTS idx_attention_kind ON attention(kind);
CREATE INDEX IF NOT EXISTS idx_attention_job_id ON attention(job_id);
CREATE INDEX IF NOT EXISTS idx_attention_answered_on ON attention(answered_on);
