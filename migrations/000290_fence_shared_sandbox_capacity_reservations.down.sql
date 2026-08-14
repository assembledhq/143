DROP TRIGGER IF EXISTS trg_sandbox_capacity_require_attempt_token
    ON sandbox_capacity_reservations;
DROP FUNCTION IF EXISTS reject_unfenced_sandbox_capacity_reservation();

ALTER TABLE sandbox_capacity_reservations
    DROP CONSTRAINT IF EXISTS chk_sandbox_capacity_reservations_job_attempt,
    DROP COLUMN IF EXISTS job_lock_token;
