SET LOCAL lock_timeout = '5s';

ALTER TABLE session_pr_intents
    DROP CONSTRAINT IF EXISTS session_pr_intents_body_shape,
    DROP CONSTRAINT IF EXISTS session_pr_intents_title_shape,
    DROP COLUMN IF EXISTS body,
    DROP COLUMN IF EXISTS title;
