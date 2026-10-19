-- Staged pull request text is optional publication metadata, frozen with team
-- reviewers. NULL means "not staged": publication keeps generating the title
-- or body. Limits leave room for the server-owned footer and marker within
-- GitHub's description limit.
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_pr_intents
    ADD COLUMN title text,
    ADD COLUMN body text,
    ADD CONSTRAINT session_pr_intents_title_shape CHECK (
        title IS NULL OR (
            octet_length(title) BETWEEN 1 AND 120
            AND title = btrim(title)
            AND title !~ '[\r\n]'
        )
    ),
    ADD CONSTRAINT session_pr_intents_body_shape CHECK (
        body IS NULL OR (char_length(body) <= 60000 AND btrim(body) <> '')
    );
