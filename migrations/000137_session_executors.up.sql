ALTER TABLE jobs
    ADD COLUMN owner_kind text NOT NULL DEFAULT 'worker';

ALTER TABLE jobs
    ADD CONSTRAINT chk_jobs_owner_kind CHECK (owner_kind IN ('worker', 'session_executor')) NOT VALID;
ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_owner_kind;

CREATE TABLE session_executors (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           uuid        NOT NULL REFERENCES organizations(id),
    session_id       uuid        NOT NULL REFERENCES sessions(id),
    thread_id        uuid,
    job_id           uuid        NOT NULL REFERENCES jobs(id),
    job_type         text        NOT NULL,
    host_node_id     text        NOT NULL REFERENCES nodes(id),
    owner_id         text        NOT NULL,
    lock_token       uuid        NOT NULL,
    status           text        NOT NULL DEFAULT 'starting',
    image            text        NOT NULL DEFAULT '',
    build_sha        text        NOT NULL DEFAULT '',
    heartbeat_at     timestamptz,
    lease_expires_at timestamptz,
    started_at       timestamptz NOT NULL DEFAULT now(),
    completed_at     timestamptz,
    exit_code        int,
    last_error       text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_session_executors_status CHECK (status IN (
        'starting', 'running', 'draining', 'requeued', 'completed', 'failed', 'lost'
    ))
);

CREATE UNIQUE INDEX idx_session_executors_one_active
    ON session_executors (org_id, session_id)
    WHERE status IN ('starting', 'running', 'draining');

CREATE INDEX idx_session_executors_stale
    ON session_executors (status, heartbeat_at, lease_expires_at)
    WHERE status IN ('starting', 'running', 'draining');

CREATE INDEX idx_session_executors_job
    ON session_executors (org_id, job_id);

CREATE INDEX idx_jobs_owner_kind_running
    ON jobs (owner_kind, status, lease_expires_at)
    WHERE status = 'running';
