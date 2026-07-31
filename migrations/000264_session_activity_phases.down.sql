DROP TABLE IF EXISTS session_activity_phases;
DROP TABLE IF EXISTS thread_inbox_delivery_batches;
ALTER TABLE thread_inbox_entries DROP COLUMN IF EXISTS applied_at;
