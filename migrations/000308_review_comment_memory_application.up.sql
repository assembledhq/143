-- Durable progress for learned-memory application, distinct from revision feedback
-- application. Written atomically with the insert-only memory version.
ALTER TABLE review_comments ADD COLUMN memory_applied_at timestamptz;
