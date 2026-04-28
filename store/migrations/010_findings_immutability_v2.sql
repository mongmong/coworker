-- Plan 137: extend the findings immutability trigger to cover the new
-- columns added in Plan 125 (008_findings_dispatches_drift.sql):
-- plan_id, phase_index, reviewer_handle.
--
-- Migration 006 used a closed list of "must remain unchanged" columns.
-- When Plan 125 added new columns, the trigger silently ignored them,
-- leaving plan_id/phase_index/reviewer_handle mutable in violation of
-- Decision 2 (findings are immutable). The 2026-04-27 re-audit caught
-- this — see docs/reviews/2026-04-27-comprehensive-audit.md follow-ups.
--
-- Fix: drop the v1 trigger and recreate it with all 10 immutable columns.
-- The new trigger uses the same allowlist semantics: an UPDATE is allowed
-- iff every immutable column is unchanged. Only resolved_by_job_id and
-- resolved_at may change (used by ResolveFinding).

DROP TRIGGER IF EXISTS findings_immutable_before_update;

CREATE TRIGGER findings_immutable_before_update
BEFORE UPDATE ON findings
FOR EACH ROW
WHEN NOT (
    NEW.run_id          = OLD.run_id          AND
    NEW.job_id          = OLD.job_id          AND
    NEW.path            = OLD.path            AND
    NEW.line            = OLD.line            AND
    NEW.severity        = OLD.severity        AND
    NEW.body            = OLD.body            AND
    NEW.fingerprint     = OLD.fingerprint     AND
    NEW.plan_id         = OLD.plan_id         AND
    NEW.phase_index     = OLD.phase_index     AND
    NEW.reviewer_handle = OLD.reviewer_handle
)
BEGIN
    SELECT RAISE(ABORT, 'findings: only resolved_by_job_id and resolved_at may be updated after insertion');
END;
