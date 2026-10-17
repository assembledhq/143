-- Validation takes SHARE UPDATE EXCLUSIVE rather than the ACCESS EXCLUSIVE lock
-- used to add the constraint in 315. Keeping it in a separate migration lets the
-- automations table keep serving reads and writes during its scan.
SET LOCAL lock_timeout = '30s';

ALTER TABLE automations
    VALIDATE CONSTRAINT chk_automations_fallback_models;
