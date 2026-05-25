ALTER TABLE session_threads
    ADD COLUMN IF NOT EXISTS confidence_score double precision;

ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS confidence_score float,
    ADD COLUMN IF NOT EXISTS confidence_reasoning text,
    ADD COLUMN IF NOT EXISTS risk_factors text[];
