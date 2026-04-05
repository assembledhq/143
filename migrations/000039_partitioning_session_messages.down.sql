-- Reverse session_messages partitioning.
CREATE TABLE session_messages_backup AS SELECT * FROM session_messages;

DROP TABLE session_messages CASCADE;

CREATE TABLE session_messages (
    id          bigserial   PRIMARY KEY,
    session_id  uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    org_id      uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id     uuid        REFERENCES users(id),
    turn_number int         NOT NULL,
    role        text        NOT NULL,
    content     text        NOT NULL,
    attachments text[],
    token_usage jsonb,
    thread_id   uuid        REFERENCES session_threads(id),
    created_at  timestamptz NOT NULL DEFAULT now()
);

INSERT INTO session_messages (id, session_id, org_id, user_id, turn_number, role, content, attachments, token_usage, thread_id, created_at)
SELECT id, session_id, org_id, user_id, turn_number, role, content, attachments, token_usage, thread_id, created_at
FROM session_messages_backup;

DO $$ BEGIN
    IF (SELECT count(*) FROM session_messages) != (SELECT count(*) FROM session_messages_backup) THEN
        RAISE EXCEPTION 'session_messages row count mismatch during partition rollback';
    END IF;
END $$;

SELECT setval('session_messages_id_seq', COALESCE((SELECT MAX(id) FROM session_messages), 1));

CREATE INDEX idx_session_messages_session ON session_messages (org_id, session_id, turn_number);
CREATE INDEX idx_session_messages_thread ON session_messages (thread_id) WHERE thread_id IS NOT NULL;

DROP TABLE session_messages_backup;
