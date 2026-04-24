ALTER TABLE sessions
    ADD COLUMN origin text NOT NULL DEFAULT 'issue_trigger',
    ADD COLUMN interaction_mode text NOT NULL DEFAULT 'single_run',
    ADD COLUMN validation_policy text NOT NULL DEFAULT 'on_turn_complete';

ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_origin
        CHECK (origin IN ('issue_trigger', 'manual', 'project', 'automation', 'revision')),
    ADD CONSTRAINT chk_sessions_interaction_mode
        CHECK (interaction_mode IN ('interactive', 'single_run')),
    ADD CONSTRAINT chk_sessions_validation_policy
        CHECK (validation_policy IN ('on_turn_complete', 'on_session_end', 'skip'));

UPDATE sessions
SET origin = CASE
        WHEN project_task_id IS NOT NULL THEN 'project'
        WHEN automation_run_id IS NOT NULL THEN 'automation'
        WHEN parent_session_id IS NOT NULL THEN 'revision'
        WHEN issue_id IS NOT NULL THEN 'issue_trigger'
        WHEN triggered_by_user_id IS NOT NULL THEN 'manual'
        ELSE 'issue_trigger'
    END,
    interaction_mode = CASE
        WHEN issue_id IS NOT NULL THEN 'single_run'
        WHEN triggered_by_user_id IS NOT NULL THEN 'interactive'
        ELSE 'single_run'
    END,
    validation_policy = CASE
        WHEN issue_id IS NOT NULL THEN 'on_turn_complete'
        WHEN triggered_by_user_id IS NOT NULL THEN 'on_session_end'
        ELSE 'on_turn_complete'
    END;

UPDATE sessions s
SET repository_id = i.repository_id
FROM issues i
WHERE s.repository_id IS NULL
  AND s.issue_id = i.id
  AND s.org_id = i.org_id
  AND i.repository_id IS NOT NULL;

CREATE TABLE session_issue_links (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           uuid NOT NULL REFERENCES organizations(id),
    session_id       uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    issue_id         uuid NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    role             text NOT NULL CHECK (role IN ('primary', 'related')),
    position         integer NOT NULL DEFAULT 0,
    added_by_user_id uuid REFERENCES users(id),
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, issue_id)
);

CREATE UNIQUE INDEX idx_session_issue_links_primary
    ON session_issue_links (session_id)
    WHERE role = 'primary';

CREATE INDEX idx_session_issue_links_org_session_position
    ON session_issue_links (org_id, session_id, position, created_at, issue_id);

CREATE INDEX idx_session_issue_links_org_issue
    ON session_issue_links (org_id, issue_id);

INSERT INTO session_issue_links (org_id, session_id, issue_id, role, position, added_by_user_id, created_at)
SELECT org_id, id, issue_id, 'primary', 0, triggered_by_user_id, created_at
FROM sessions
WHERE issue_id IS NOT NULL
ON CONFLICT (session_id, issue_id) DO NOTHING;

CREATE TABLE session_turn_issue_snapshots (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid NOT NULL REFERENCES organizations(id),
    session_id    uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_number   integer NOT NULL,
    linked_issues jsonb NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, turn_number)
);

CREATE INDEX idx_session_turn_issue_snapshots_org_session
    ON session_turn_issue_snapshots (org_id, session_id, turn_number DESC);
