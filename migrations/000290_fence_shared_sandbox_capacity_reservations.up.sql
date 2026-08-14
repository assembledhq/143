-- Fence job-backed final-admission leases to the queue claim attempt. Existing
-- live rows are backfilled when their job is still owned; unmatched ephemeral
-- rows remain valid until their short TTL expires, so the constraint is added
-- NOT VALID while still enforcing the invariant on new writes.
ALTER TABLE sandbox_capacity_reservations
    ADD COLUMN job_lock_token uuid;

UPDATE sandbox_capacity_reservations reservation
SET job_lock_token = job.lock_token
FROM jobs job
WHERE reservation.job_id = job.id
  AND reservation.job_lock_token IS NULL
  AND job.status = 'running'
  AND job.lock_token IS NOT NULL;

ALTER TABLE sandbox_capacity_reservations
    ADD CONSTRAINT chk_sandbox_capacity_reservations_job_attempt
    CHECK ((job_id IS NULL) = (job_lock_token IS NULL)) NOT VALID;

-- A pre-290 worker does not send the attempt token. A BEFORE INSERT trigger is
-- required in addition to the check constraint: its legacy ON CONFLICT update
-- could otherwise reuse a new worker's valid row without ever producing an
-- invalid final tuple. Fail those job-backed admissions closed during the
-- brief rolling overlap; preview reservations (job_id NULL) remain compatible.
CREATE FUNCTION reject_unfenced_sandbox_capacity_reservation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF (NEW.job_id IS NULL) <> (NEW.job_lock_token IS NULL) THEN
        RAISE EXCEPTION 'job-backed sandbox capacity reservation requires a matching job lock token'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_sandbox_capacity_require_attempt_token
    BEFORE INSERT OR UPDATE OF job_id, job_lock_token
    ON sandbox_capacity_reservations
    FOR EACH ROW
    EXECUTE FUNCTION reject_unfenced_sandbox_capacity_reservation();
