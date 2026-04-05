-- Partition session_messages by created_at.
-- IMPORTANT: Run during maintenance window. See 000037 for helper function definitions.

LOCK TABLE session_messages IN ACCESS EXCLUSIVE MODE;

ALTER TABLE session_messages RENAME TO session_messages_old;
ALTER SEQUENCE session_messages_id_seq RENAME TO session_messages_id_seq_old;
ALTER INDEX IF EXISTS idx_session_messages_session RENAME TO idx_session_messages_session_old;
ALTER INDEX IF EXISTS idx_session_messages_thread RENAME TO idx_session_messages_thread_old;

CREATE TABLE session_messages (
    id          bigserial   NOT NULL,
    session_id  uuid        NOT NULL,
    org_id      uuid        NOT NULL,
    user_id     uuid,
    turn_number int         NOT NULL,
    role        text        NOT NULL,
    content     text        NOT NULL,
    attachments text[],
    token_usage jsonb,
    thread_id   uuid,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

ALTER TABLE session_messages
    ADD CONSTRAINT fk_session_messages_session FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    ADD CONSTRAINT fk_session_messages_org FOREIGN KEY (org_id) REFERENCES organizations(id),
    ADD CONSTRAINT fk_session_messages_user FOREIGN KEY (user_id) REFERENCES users(id),
    ADD CONSTRAINT fk_session_messages_thread FOREIGN KEY (thread_id) REFERENCES session_threads(id) ON DELETE CASCADE;

CREATE INDEX idx_session_messages_session ON session_messages (org_id, session_id, turn_number);
CREATE INDEX idx_session_messages_thread ON session_messages (thread_id) WHERE thread_id IS NOT NULL;

SELECT create_monthly_partitions('session_messages', '2025-01-01'::date,
    (date_trunc('month', now()::date) + interval '3 months')::date);

CREATE TABLE session_messages_default PARTITION OF session_messages DEFAULT;

INSERT INTO session_messages (id, session_id, org_id, user_id, turn_number, role, content, attachments, token_usage, thread_id, created_at)
SELECT id, session_id, org_id, user_id, turn_number, role, content, attachments, token_usage, thread_id, created_at
FROM session_messages_old;

DO $$ BEGIN
    IF (SELECT count(*) FROM session_messages) != (SELECT count(*) FROM session_messages_old) THEN
        RAISE EXCEPTION 'session_messages row count mismatch after partition copy';
    END IF;
END $$;

SELECT setval('session_messages_id_seq', COALESCE((SELECT MAX(id) FROM session_messages), 1));

DROP TABLE session_messages_old;
