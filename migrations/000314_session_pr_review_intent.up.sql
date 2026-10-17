-- Staging is metadata only. Freezing an empty row fences a late agent tool
-- call before the completion coordinator creates durable publication work.
ALTER TABLE automation_runs
    ADD CONSTRAINT automation_runs_id_org_id_key UNIQUE (id, org_id);
ALTER TABLE session_automation_links
    ADD CONSTRAINT session_automation_links_run_scope_key
    UNIQUE (session_id, org_id, automation_run_id);
ALTER TABLE session_publications
    ADD CONSTRAINT session_publications_id_org_id_key UNIQUE (id, org_id);

CREATE TABLE session_pr_intents (
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL,
    automation_run_id uuid NOT NULL,
    changeset_id uuid NOT NULL,
    team_reviewers text[] NOT NULL DEFAULT '{}',
    frozen_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, session_id, automation_run_id, changeset_id),
    FOREIGN KEY (session_id, org_id, automation_run_id)
        REFERENCES session_automation_links(session_id, org_id, automation_run_id) ON DELETE CASCADE,
    FOREIGN KEY (automation_run_id, org_id)
        REFERENCES automation_runs(id, org_id) ON DELETE CASCADE,
    FOREIGN KEY (changeset_id, org_id, session_id)
        REFERENCES session_changesets(id, org_id, session_id) ON DELETE CASCADE,
    CONSTRAINT session_pr_intents_team_count CHECK (cardinality(team_reviewers) <= 10)
);

-- A reservation is committed before the external request. An uncertain send
-- remains sending; it must never be retried merely because a team no longer
-- appears in GitHub's list of pending review requests.
CREATE TABLE publication_team_review_requests (
    org_id uuid NOT NULL REFERENCES organizations(id),
    publication_id uuid NOT NULL,
    team text NOT NULL,
    state text NOT NULL DEFAULT 'sending',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, publication_id, team),
    FOREIGN KEY (publication_id, org_id)
        REFERENCES session_publications(id, org_id) ON DELETE CASCADE,
    CONSTRAINT publication_team_review_requests_team CHECK (
        team ~ '^[a-z0-9]([a-z0-9-]{0,37}[a-z0-9])?/[a-z0-9]([a-z0-9-]{0,98}[a-z0-9])?$'
    ),
    CONSTRAINT publication_team_review_requests_state CHECK (
        state IN ('sending', 'requested', 'retryable')
    )
);
