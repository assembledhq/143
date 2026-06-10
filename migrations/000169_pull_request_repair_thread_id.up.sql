ALTER TABLE pull_request_repair_runs
ADD COLUMN thread_id uuid NULL REFERENCES session_threads(id) ON DELETE SET NULL;

