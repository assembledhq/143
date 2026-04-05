ALTER TABLE sessions ADD COLUMN title TEXT;

-- Backfill: copy pm_approach into title for manually created sessions
-- (those without a PM plan), so existing sessions display correctly.
UPDATE sessions SET title = pm_approach
WHERE pm_plan_id IS NULL AND pm_approach IS NOT NULL;
