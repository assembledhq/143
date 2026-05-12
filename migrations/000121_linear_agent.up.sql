-- Linear agent (issue assignment / @-mention triggers a 143 session).
--
-- Three concerns share this migration because they're a single feature surface
-- and shipping them apart would mean a partially-functional intermediate state:
--   * linear_agent_sessions    — idempotency anchor + Linear↔143 session bridge
--   * linear_agent_activity_log — per-activity dedupe (one Linear write per
--                                 (agent_session, idem_key))
--   * linear_team_repo_mappings — `(team, project) → 143 repo` lookup so an
--                                 inbound assignment knows which repo to clone
--
-- All three are append-mostly low-write-volume tables. They are scoped per-org
-- and indexed for the access patterns the dispatcher and worker need:
--   * dispatcher: lookup by (org_id, linear_agent_session_id) to decide
--     "have we seen this AgentSession before?" → UNIQUE constraint is the
--     primary index.
--   * activity emit: lookup by (agent_session_row_id, idem_key) to dedupe
--     concurrent milestone fan-outs → UNIQUE constraint is the primary index.
--   * repo resolver: lookup by (org_id, linear_team_id) with optional project
--     scoping. The composite UNIQUE serves this.

SET LOCAL lock_timeout = '5s';

-- ---------------------------------------------------------------------------
-- linear_agent_sessions
-- ---------------------------------------------------------------------------
-- One row per Linear AgentSession we've observed. The linear_agent_session_id
-- column is the dedupe key Linear assigns; webhook re-deliveries collide on
-- the UNIQUE constraint and ON CONFLICT DO NOTHING preserves the original row
-- (and its session_id mapping) so the worker can recover the prior 143
-- session under retry.
--
-- session_id is nullable: the Linear AgentSessionEvent: created webhook
-- arrives before we've finished synchronously creating a 143 session. The
-- worker fills it in once the session exists. We never delete from this table
-- — a closed AgentSession lives on as the audit trail of "this issue was
-- handed to the agent".
CREATE TABLE linear_agent_sessions (
    id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                   uuid NOT NULL REFERENCES organizations(id),
    integration_id           uuid NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    linear_agent_session_id  text NOT NULL,
    linear_issue_id          text NOT NULL,
    linear_issue_identifier  text,
    linear_app_user_id       text,
    linear_creator_user_id   text,
    session_id               uuid REFERENCES sessions(id) ON DELETE SET NULL,
    state                    text NOT NULL DEFAULT 'pending',
    last_event_received_at   timestamptz,
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, linear_agent_session_id)
);

-- Validate the state vocabulary. Mirrors Linear's AgentSession state machine
-- (pending → working/inProgress → awaitingInput → complete | error). Kept in
-- lockstep with models.LinearAgentSessionState* in
-- internal/models/linear_agent_enums.go via TestLinearAgentSessionStateMigrationVocabularyMatchesGoEnum.
ALTER TABLE linear_agent_sessions
    ADD CONSTRAINT chk_linear_agent_sessions_state
        CHECK (state IN ('pending', 'in_progress', 'awaiting_input', 'complete', 'error'));

-- Operator-facing index: "show me all agent sessions for this 143 session".
-- Sparse on session_id (nullable), so a partial index is the cheapest shape.
CREATE INDEX idx_linear_agent_sessions_session
    ON linear_agent_sessions (session_id)
    WHERE session_id IS NOT NULL;

-- Sweeper / health-check index: "agent sessions whose worker job hasn't
-- completed and may need recovery". Bounded write rate, so a plain b-tree
-- on the timestamp pays its way.
CREATE INDEX idx_linear_agent_sessions_org_state_recent
    ON linear_agent_sessions (org_id, state, created_at DESC);

-- ---------------------------------------------------------------------------
-- linear_agent_activity_log
-- ---------------------------------------------------------------------------
-- One row per AgentActivity we've emitted to Linear. The (agent_session, key)
-- UNIQUE makes the writer idempotent: concurrent fan-outs for the same
-- milestone collide and the loser becomes a no-op via ON CONFLICT DO NOTHING.
-- linear_activity_id is captured post-write so retries that find the row
-- already present can short-circuit without re-asking Linear. activity_type
-- is denormalized (not normalized to a separate enum table) so an operator
-- can answer "what did we send" with a single SELECT.
CREATE TABLE linear_agent_activity_log (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                uuid NOT NULL REFERENCES organizations(id),
    agent_session_row_id  uuid NOT NULL REFERENCES linear_agent_sessions(id) ON DELETE CASCADE,
    idem_key              text NOT NULL,
    activity_type         text NOT NULL,
    linear_activity_id    text,
    created_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (agent_session_row_id, idem_key)
);

-- Operator surface: "show me everything we sent to Linear for org X today".
CREATE INDEX idx_linear_agent_activity_log_org_recent
    ON linear_agent_activity_log (org_id, created_at DESC);

-- activity_type vocabulary check. Linear's AgentActivity types are stable;
-- if Linear ships a new type before we adopt it, the CHECK rejects it loudly
-- rather than silently storing an unknown value the dispatcher can't replay.
ALTER TABLE linear_agent_activity_log
    ADD CONSTRAINT chk_linear_agent_activity_log_type
        CHECK (activity_type IN ('thought', 'action', 'elicitation', 'response', 'error'));

-- ---------------------------------------------------------------------------
-- linear_team_repo_mappings
-- ---------------------------------------------------------------------------
-- A per-org pairing of (Linear team, optional Linear project) → 143 repo.
-- The repo resolver consults this table when an AgentSession arrives so the
-- coding session knows which repo to clone. Project is optional; a NULL row
-- is the "team default" and matches issues that have no project. To keep the
-- UNIQUE constraint indexable, NULL is collapsed to '' via COALESCE in a
-- partial expression index.
--
-- priority resolves ambiguity if a future migration adds overlapping mappings
-- (e.g. wildcard project). Not used in v1; included so we don't have to
-- migrate the table later. Default 0 means "natural" priority; lower wins.
CREATE TABLE linear_team_repo_mappings (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             uuid NOT NULL REFERENCES organizations(id),
    linear_team_id     text NOT NULL,
    linear_project_id  text,
    repository_id      uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    default_branch     text,
    priority           integer NOT NULL DEFAULT 0,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

-- The COALESCE wrapper turns the (team, project) UNIQUE into a single index
-- spanning both the team-default row (project IS NULL) and per-project rows
-- without splitting into two partial indexes. PG can't UNIQUE on a NULL
-- column directly, so this is the standard idiom.
CREATE UNIQUE INDEX idx_linear_team_repo_mappings_unique
    ON linear_team_repo_mappings (org_id, linear_team_id, COALESCE(linear_project_id, ''));

-- Hot path for the resolver: "given a team, give me the per-project rows
-- and the team-default row in one shot". The compound index is selective
-- enough that the planner picks it for both lookups.
CREATE INDEX idx_linear_team_repo_mappings_org_team
    ON linear_team_repo_mappings (org_id, linear_team_id);
