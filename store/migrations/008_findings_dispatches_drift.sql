-- Plan 125: schema drift fixes (BLOCKERs B3 + B4 from 2026-04-27 audit).
-- Adds spec-required columns to findings and dispatches.

-- B3: findings — plan_id, phase_index, reviewer_handle.
ALTER TABLE findings ADD COLUMN plan_id TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN phase_index INTEGER NOT NULL DEFAULT 0;
ALTER TABLE findings ADD COLUMN reviewer_handle TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_findings_plan_id ON findings(plan_id);
CREATE INDEX IF NOT EXISTS idx_findings_run_plan ON findings(run_id, plan_id);

-- B4: dispatches — mode (persistent | ephemeral | in-process).
-- Pre-existing rows are all from the pull queue, so default to 'persistent'.
ALTER TABLE dispatches ADD COLUMN mode TEXT NOT NULL DEFAULT 'persistent';

CREATE INDEX IF NOT EXISTS idx_dispatches_mode ON dispatches(mode);
