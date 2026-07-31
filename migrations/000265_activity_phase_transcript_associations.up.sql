-- lint:allow-hot-table-no-fk reason="partitioned transcript hot tables validate phase ownership in every write path"
-- The marker above is the greppable audit trail for this reviewed exception;
-- cmd/lint-schema only scans CREATE TABLE statements, so it does not enforce
-- anything in an ALTER-only migration. session_logs and session_messages cannot
-- support a foreign key to session_activity_phases without including their
-- partition keys in the referenced identity. Every write path validates that
-- the phase belongs to the same org, session, thread, and turn instead — see
-- SessionLogStore.Create and SessionMessageStore.Create.
--
-- Deploy note: CREATE INDEX CONCURRENTLY is not supported on a partitioned
-- parent, so the two parent indexes below build under a lock that blocks
-- writes on every existing partition of these high-write tables. Apply during
-- a low-traffic window, or split into `CREATE INDEX ... ON ONLY <parent>`,
-- per-partition `CREATE INDEX CONCURRENTLY`, then `ALTER INDEX ... ATTACH
-- PARTITION`.
--
-- Orphan note: only session_human_input_requests carries a real FK
-- (ON DELETE SET NULL). Deleting a phase leaves stale activity_phase_id values
-- on session_logs/session_messages, so read paths must resolve phase -> entries
-- rather than joining entries -> phase and assuming referential integrity.
ALTER TABLE session_logs
    ADD COLUMN activity_phase_id uuid;

ALTER TABLE session_messages
    ADD COLUMN activity_phase_id uuid;

ALTER TABLE session_human_input_requests
    ADD COLUMN activity_phase_id uuid
    REFERENCES session_activity_phases(id) ON DELETE SET NULL;

CREATE INDEX idx_session_logs_activity_phase
    ON session_logs (org_id, activity_phase_id, timestamp)
    WHERE activity_phase_id IS NOT NULL;

CREATE INDEX idx_session_messages_activity_phase
    ON session_messages (org_id, activity_phase_id, created_at)
    WHERE activity_phase_id IS NOT NULL;

CREATE INDEX idx_session_human_input_requests_activity_phase
    ON session_human_input_requests (org_id, activity_phase_id, created_at)
    WHERE activity_phase_id IS NOT NULL;
