-- Revert: memories → review_patterns, drop new columns and constraints.

DROP INDEX IF EXISTS idx_memories_org_scope;
ALTER TABLE memories DROP CONSTRAINT IF EXISTS chk_memories_source;
ALTER TABLE memories DROP CONSTRAINT IF EXISTS chk_memories_scope;

ALTER TABLE memories DROP COLUMN IF EXISTS file_patterns;
ALTER TABLE memories DROP COLUMN IF EXISTS times_reinforced;
ALTER TABLE memories DROP COLUMN IF EXISTS last_used_at;
ALTER TABLE memories DROP COLUMN IF EXISTS source;
ALTER TABLE memories DROP COLUMN IF EXISTS scope;

ALTER INDEX idx_memories_org_repo_status RENAME TO idx_review_patterns_repo;
ALTER INDEX idx_memories_dedup RENAME TO idx_review_patterns_dedup;

ALTER TABLE memories RENAME TO review_patterns;
