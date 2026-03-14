-- Rename review_patterns → memories and add memory-system columns.

ALTER TABLE review_patterns RENAME TO memories;

-- Rename indexes.
ALTER INDEX idx_review_patterns_repo RENAME TO idx_memories_org_repo_status;
ALTER INDEX idx_review_patterns_dedup RENAME TO idx_memories_dedup;

-- New columns for strength-based memory scoring.
ALTER TABLE memories ADD COLUMN scope text NOT NULL DEFAULT 'repo';
ALTER TABLE memories ADD COLUMN source text NOT NULL DEFAULT 'review';
ALTER TABLE memories ADD COLUMN last_used_at timestamptz;
ALTER TABLE memories ADD COLUMN times_reinforced int NOT NULL DEFAULT 0;
ALTER TABLE memories ADD COLUMN file_patterns text[];

-- Backfill: existing patterns get their created_at as last_used_at.
UPDATE memories SET last_used_at = created_at WHERE last_used_at IS NULL;

-- Backfill: occurrence_count seeds times_reinforced.
UPDATE memories SET times_reinforced = occurrence_count;
