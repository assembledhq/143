-- The from_fork column was added to migration 000221 after that migration had
-- already been applied to existing databases, so golang-migrate never created
-- the column there. Add it idempotently: fresh databases that ran the edited
-- 000221 already have it, while databases that applied 000221 before the edit
-- are missing it (and crash the code review webhook handler without it).
ALTER TABLE code_review_session_metadata
    ADD COLUMN IF NOT EXISTS from_fork boolean NOT NULL DEFAULT false;
