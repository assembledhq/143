-- Standalone for golang-migrate's non-transactional concurrent-index execution.
-- A leftover invalid/conflicting index must fail, never be silently reused.
CREATE UNIQUE INDEX CONCURRENTLY idx_session_input_dispatches_lane_claim
    ON session_input_dispatches (org_id, session_id, thread_id, id, admission_claim_token);
