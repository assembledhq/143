ALTER TABLE sessions
    DROP COLUMN IF EXISTS confidence_score,
    DROP COLUMN IF EXISTS confidence_reasoning,
    DROP COLUMN IF EXISTS risk_factors;

ALTER TABLE session_threads
    DROP COLUMN IF EXISTS confidence_score;
