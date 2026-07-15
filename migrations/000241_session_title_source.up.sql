ALTER TABLE sessions
    ADD COLUMN title_source text NOT NULL DEFAULT 'legacy',
    ADD COLUMN title_intent text,
    ADD COLUMN title_pivoted_at_turn integer,
    ADD COLUMN title_generated_at timestamptz;

ALTER TABLE sessions
    ALTER COLUMN title_source SET DEFAULT 'generated';

ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_title_source CHECK (
        title_source IN ('legacy', 'generated', 'issue', 'manual')
    ) NOT VALID;

ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_title_source;
