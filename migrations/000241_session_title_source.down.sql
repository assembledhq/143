ALTER TABLE sessions DROP CONSTRAINT chk_sessions_title_source;
ALTER TABLE sessions
    DROP COLUMN title_generated_at,
    DROP COLUMN title_pivoted_at_turn,
    DROP COLUMN title_intent,
    DROP COLUMN title_source;
