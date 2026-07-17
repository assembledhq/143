CREATE TABLE session_publications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL,
    changeset_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    state text NOT NULL DEFAULT 'requested',
    source text NOT NULL DEFAULT 'backend',
    review_gate_state text NOT NULL DEFAULT 'not_required',
    job_queue text NOT NULL DEFAULT 'default',
    request_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    request_generation_at timestamptz NOT NULL DEFAULT now(),
    base_branch text NOT NULL,
    head_branch text NOT NULL,
    desired_head_sha text,
    published_head_sha text,
    github_pr_number integer,
    github_pr_url text,
    attempt_count integer NOT NULL DEFAULT 0,
    last_error_code text,
    last_error_message text,
    requested_at timestamptz NOT NULL DEFAULT now(),
    last_attempt_at timestamptz,
    branch_published_at timestamptz,
    pr_resolved_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT session_publications_session_scope_fkey
        FOREIGN KEY (session_id, org_id)
        REFERENCES sessions(id, org_id) ON DELETE CASCADE,
    CONSTRAINT session_publications_changeset_scope_fkey
        FOREIGN KEY (changeset_id, org_id, session_id)
        REFERENCES session_changesets(id, org_id, session_id) ON DELETE CASCADE,
    CONSTRAINT session_publications_repository_scope_fkey
        FOREIGN KEY (repository_id, org_id)
        REFERENCES repositories(id, org_id) ON DELETE RESTRICT,
    CONSTRAINT session_publications_state_check CHECK (state IN (
        'requested', 'review_pending', 'ready_to_publish', 'branch_published',
        'pr_resolved', 'recorded', 'completed', 'completed_noop',
        'retryable_failed', 'terminal_failed'
    )),
    CONSTRAINT session_publications_source_check CHECK (source IN (
        'user', 'automation', 'agent_tool', 'backend', 'webhook',
        'reconciler', 'backfill'
    )),
    CONSTRAINT session_publications_review_gate_state_check CHECK (review_gate_state IN (
        'not_required', 'pending', 'passed', 'needs_human', 'failed'
    )),
    CONSTRAINT session_publications_job_queue_check CHECK (job_queue IN ('default', 'agent')),
    CONSTRAINT session_publications_attempt_count_nonnegative CHECK (attempt_count >= 0),
    CONSTRAINT session_publications_pr_number_positive CHECK (
        github_pr_number IS NULL OR github_pr_number > 0
    ),
    UNIQUE (org_id, changeset_id)
);

CREATE INDEX idx_session_publications_session
    ON session_publications (org_id, session_id, created_at DESC);

CREATE INDEX idx_session_publications_reconcile
    ON session_publications (org_id, updated_at, id)
    WHERE state IN (
        'requested', 'review_pending', 'ready_to_publish', 'branch_published',
        'pr_resolved', 'recorded', 'retryable_failed'
    );

-- `GetByChangesetID` is a single-row contract. Historical data can contain
-- more than one PR for a session's primary changeset, so retain the newest
-- open (otherwise newest) association and keep older PRs session-linked as
-- legacy rows before enforcing the invariant for new webhook associations.
WITH ranked_pull_requests AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY org_id, changeset_id
               ORDER BY (status = 'open') DESC, created_at DESC, id DESC
           ) AS association_rank
    FROM pull_requests
    WHERE changeset_id IS NOT NULL
)
UPDATE pull_requests pr
SET changeset_id = NULL,
    updated_at = now()
FROM ranked_pull_requests ranked
WHERE pr.id = ranked.id
  AND ranked.association_rank > 1;

CREATE UNIQUE INDEX uq_pull_requests_changeset
    ON pull_requests (org_id, changeset_id)
    WHERE changeset_id IS NOT NULL;

-- Seed reconciliation candidates for the historical false-no-op signature:
-- a real persisted diff and an owned remote branch, but publication was
-- classified as "No changes" before GitHub PR discovery ran. The reconciler
-- validates the branch against GitHub before creating any local association.
INSERT INTO session_publications (
    org_id, session_id, changeset_id, repository_id, state, source,
    review_gate_state, job_queue, request_payload,
    base_branch, head_branch, desired_head_sha,
    published_head_sha, last_error_code, last_error_message
)
SELECT
    s.org_id,
    s.id,
    sc.id,
    s.repository_id,
    'retryable_failed',
    'backfill',
    CASE WHEN s.automation_run_id IS NULL THEN 'not_required' ELSE 'passed' END,
    'default',
    jsonb_build_object(
        'session_id', s.id::text,
        'changeset_id', sc.id::text,
        'org_id', s.org_id::text,
        'publication_source', 'backfill',
        'publication_queue', 'default'
    ),
    sc.base_branch,
    sc.working_branch,
    sc.head_sha,
    sc.expected_remote_head_sha,
    'legacy_false_no_changes',
    sps.pr_creation_error
FROM sessions s
JOIN session_changesets sc
  ON sc.org_id = s.org_id
 AND sc.session_id = s.id
 AND sc.is_primary
JOIN session_publish_state sps
  ON sps.org_id = s.org_id
 AND sps.session_id = s.id
WHERE s.repository_id IS NOT NULL
  AND sc.working_branch IS NOT NULL
  AND NULLIF(trim(s.diff), '') IS NOT NULL
  AND sps.pr_creation_state = 'failed'
  AND lower(COALESCE(sps.pr_creation_error, '')) LIKE '%no changes%'
ON CONFLICT (org_id, changeset_id) DO NOTHING;
