-- Plan 134 (N1): composite index for the ClaimNextDispatch hot path.
-- The query is `WHERE state = 'pending' AND role = ? ORDER BY created_at ASC LIMIT 1`,
-- run on every `orch_next_dispatch` call. The previous separate single-column
-- indexes (idx_dispatches_state, idx_dispatches_role) forced the planner to
-- pick one and filter the other; a composite (state, role) lets SQLite seek
-- directly to the relevant rows.

CREATE INDEX IF NOT EXISTS idx_dispatches_state_role
    ON dispatches(state, role);
