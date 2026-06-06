ALTER TABLE session_threads
ADD COLUMN created_by_source text NOT NULL DEFAULT 'user',
ADD COLUMN created_by_thread_id uuid NULL REFERENCES session_threads(id) ON DELETE SET NULL;

ALTER TABLE session_messages
ADD COLUMN source text NOT NULL DEFAULT '';
