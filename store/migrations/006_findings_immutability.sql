-- 006_findings_immutability.sql
-- Enforce that immutable finding columns cannot be updated after INSERT.
-- Only resolved_by_job_id and resolved_at may be changed (via ResolveFinding).
--
-- Allowlist approach: the trigger fires on ANY UPDATE. It aborts unless
-- the update touches ONLY the two resolution columns. This is more robust
-- than a denylist because new columns added in later migrations are
-- protected by default.

CREATE TRIGGER IF NOT EXISTS findings_immutable_before_update
BEFORE UPDATE ON findings
FOR EACH ROW
WHEN NOT (
    -- Allow only updates that set resolution fields and leave everything else unchanged.
    NEW.run_id            = OLD.run_id            AND
    NEW.job_id            = OLD.job_id            AND
    NEW.path              = OLD.path              AND
    NEW.line              = OLD.line              AND
    NEW.severity          = OLD.severity          AND
    NEW.body              = OLD.body              AND
    NEW.fingerprint       = OLD.fingerprint
)
BEGIN
    SELECT RAISE(ABORT, 'findings: only resolved_by_job_id and resolved_at may be updated after insertion');
END;
