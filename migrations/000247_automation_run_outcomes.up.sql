CREATE TABLE automation_run_outcomes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    automation_id uuid NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
    automation_run_id uuid NOT NULL REFERENCES automation_runs(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    repository text NOT NULL,
    pull_request_number integer NOT NULL CHECK (pull_request_number > 0),
    pull_request_url text NOT NULL,
    pull_request_title text,
    head_sha text,
    decision text NOT NULL CHECK (decision IN ('passed', 'changes_requested', 'advisory', 'not_applicable')),
    reason text NOT NULL CHECK (length(btrim(reason)) > 0),
    source text NOT NULL CHECK (source IN ('agent_reported', 'legacy_inferred')),
    reported_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, automation_run_id)
);

CREATE INDEX idx_automation_run_outcomes_decisions
    ON automation_run_outcomes (org_id, automation_id, repository, pull_request_number, reported_at DESC);

CREATE TABLE automation_run_external_actions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    outcome_id uuid NOT NULL REFERENCES automation_run_outcomes(id) ON DELETE CASCADE,
    provider text NOT NULL CHECK (provider IN ('github')),
    action_type text NOT NULL CHECK (action_type IN ('github_review_changes_requested', 'github_review_approved', 'github_comment')),
    external_id text,
    url text NOT NULL,
    verification_status text NOT NULL CHECK (verification_status IN ('reported', 'verified', 'unavailable')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, outcome_id)
);

CREATE INDEX idx_automation_run_external_actions_outcome
    ON automation_run_external_actions (org_id, outcome_id);

-- Conservatively backfill legacy Main-thread summaries. These are the exact
-- leading decision tokens emitted by the existing PR-evaluation automation.
-- Anything else remains unreported; REVIEW_CLEAN from the internal review
-- thread is intentionally not consulted.
WITH legacy AS (
    SELECT DISTINCT ON (ar.id)
        ar.org_id,
        ar.automation_id,
        ar.id AS automation_run_id,
        s.id AS session_id,
        ar.config_snapshot #>> '{github,repository}' AS repository,
        CASE
            WHEN ar.config_snapshot #>> '{github,pull_request_number}' ~ '^[1-9][0-9]*$'
            THEN (ar.config_snapshot #>> '{github,pull_request_number}')::integer
        END AS pull_request_number,
        COALESCE(
            NULLIF(ar.config_snapshot #>> '{github,pull_request_url}', ''),
            'https://github.com/' || (ar.config_snapshot #>> '{github,repository}') || '/pull/' || (ar.config_snapshot #>> '{github,pull_request_number}')
        ) AS pull_request_url,
        NULLIF(ar.config_snapshot #>> '{github,pull_request_title}', '') AS pull_request_title,
        NULLIF(ar.config_snapshot #>> '{github,head_sha}', '') AS head_sha,
        CASE lower((regexp_match(st.result_summary, '^#[0-9]+:[[:space:]]*([[:alpha:]_-]+)'))[1])
            WHEN 'pass' THEN 'passed'
            WHEN 'reject' THEN 'changes_requested'
            WHEN 'advise' THEN 'advisory'
            WHEN 'skipped' THEN 'not_applicable'
        END AS decision,
        st.result_summary AS reason,
        COALESCE(st.completed_at, s.completed_at, ar.completed_at, ar.updated_at) AS reported_at
    FROM automation_runs ar
    JOIN session_automation_links sal
      ON sal.org_id = ar.org_id
     AND sal.automation_run_id = ar.id
    JOIN sessions s
      ON s.org_id = sal.org_id
     AND s.id = sal.session_id
    JOIN session_threads st
      ON st.org_id = s.org_id
     AND st.session_id = s.id
     AND st.label = 'Main'
    WHERE ar.triggered_by = 'github'
      AND ar.config_snapshot #>> '{github,repository}' <> ''
      AND ar.config_snapshot #>> '{github,pull_request_number}' ~ '^[1-9][0-9]*$'
      AND st.result_summary ~* '^#[0-9]+:[[:space:]]*(pass|reject|advise|skipped)[[:space:]]+(—|-)'
    ORDER BY ar.id, st.completed_at DESC NULLS LAST, st.created_at DESC
)
INSERT INTO automation_run_outcomes (
    org_id, automation_id, automation_run_id, session_id,
    repository, pull_request_number, pull_request_url, pull_request_title, head_sha,
    decision, reason, source, reported_at
)
SELECT
    org_id, automation_id, automation_run_id, session_id,
    repository, pull_request_number, pull_request_url, pull_request_title, head_sha,
    decision, reason, 'legacy_inferred', reported_at
FROM legacy
WHERE decision IS NOT NULL
ON CONFLICT (org_id, automation_run_id) DO NOTHING;
