DROP TABLE IF EXISTS session_review_loop_passes;
DROP TABLE IF EXISTS session_review_loops;

ALTER TABLE automations
    DROP CONSTRAINT IF EXISTS chk_automations_pre_pr_review_loops;

ALTER TABLE automations
    DROP COLUMN IF EXISTS pre_pr_review_loops;
