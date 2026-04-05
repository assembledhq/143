-- Fix boolean columns that allow NULL when the intent is always true/false.
-- A NULL boolean silently bypasses WHERE col = true filters.

-- =============================================================================
-- Issue 6: NOT NULL on boolean columns
-- =============================================================================

-- review_comments: actionable, generalizable, applied
-- Backfill NULLs to their DEFAULT values first.
UPDATE review_comments SET actionable = true WHERE actionable IS NULL;
UPDATE review_comments SET generalizable = false WHERE generalizable IS NULL;
UPDATE review_comments SET applied = false WHERE applied IS NULL;

ALTER TABLE review_comments
    ALTER COLUMN actionable SET NOT NULL,
    ALTER COLUMN actionable SET DEFAULT true,
    ALTER COLUMN generalizable SET NOT NULL,
    ALTER COLUMN generalizable SET DEFAULT false,
    ALTER COLUMN applied SET NOT NULL,
    ALTER COLUMN applied SET DEFAULT false;

-- sessions: failure_retry_advised
UPDATE sessions SET failure_retry_advised = false WHERE failure_retry_advised IS NULL;

ALTER TABLE sessions
    ALTER COLUMN failure_retry_advised SET NOT NULL,
    ALTER COLUMN failure_retry_advised SET DEFAULT false;

-- =============================================================================
-- Issue 5: float → numeric for score columns
-- PostgreSQL float (double precision) has IEEE 754 precision issues.
-- numeric gives exact decimal arithmetic for threshold comparisons.
-- Note: pgx's Go float64 type handles numeric columns correctly.
-- =============================================================================

-- priority_scores: all score columns
ALTER TABLE priority_scores
    ALTER COLUMN score TYPE numeric(6,3),
    ALTER COLUMN customer_impact_score TYPE numeric(6,3),
    ALTER COLUMN severity_score TYPE numeric(6,3),
    ALTER COLUMN recency_score TYPE numeric(6,3),
    ALTER COLUMN revenue_risk_score TYPE numeric(6,3),
    ALTER COLUMN direction_alignment TYPE numeric(5,3);

-- complexity_estimates: confidence
ALTER TABLE complexity_estimates
    ALTER COLUMN confidence TYPE numeric(4,3);

-- repositories: context_quality
ALTER TABLE repositories
    ALTER COLUMN context_quality TYPE numeric(6,3);
