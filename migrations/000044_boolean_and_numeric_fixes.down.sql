ALTER TABLE repositories
    ALTER COLUMN context_quality TYPE double precision;

ALTER TABLE complexity_estimates
    ALTER COLUMN confidence TYPE double precision;

ALTER TABLE priority_scores
    ALTER COLUMN score TYPE double precision,
    ALTER COLUMN customer_impact_score TYPE double precision,
    ALTER COLUMN severity_score TYPE double precision,
    ALTER COLUMN recency_score TYPE double precision,
    ALTER COLUMN revenue_risk_score TYPE double precision,
    ALTER COLUMN direction_alignment TYPE double precision;

ALTER TABLE sessions
    ALTER COLUMN failure_retry_advised DROP NOT NULL,
    ALTER COLUMN failure_retry_advised DROP DEFAULT;

ALTER TABLE review_comments
    ALTER COLUMN applied DROP NOT NULL,
    ALTER COLUMN applied DROP DEFAULT,
    ALTER COLUMN generalizable DROP NOT NULL,
    ALTER COLUMN generalizable DROP DEFAULT,
    ALTER COLUMN actionable DROP NOT NULL,
    ALTER COLUMN actionable DROP DEFAULT;
