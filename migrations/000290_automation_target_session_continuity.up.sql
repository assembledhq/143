-- Automation target session continuity, Stage 1 schema (design doc 125).
--
-- Additive only. Old workers ignore every new column because
-- automations.session_continuity defaults to 'per_run'. The Stage 3 warm
-- sandbox columns (warm_sandbox_minutes, max_warm_targets, warm_hit,
-- warm_skipped_reason, session_sandbox_holders cleanup state, usage purpose)
-- are intentionally absent; they ship only if the Stage 3 gate passes.

ALTER TABLE automations
    ADD COLUMN session_continuity text NOT NULL DEFAULT 'per_run',
    ADD CONSTRAINT chk_automations_session_continuity
        CHECK (session_continuity IN ('per_run', 'per_target'));

-- Owner marker for automation-owned sessions. No FK: the generation row's
-- session_id is the authoritative link and an FK here would create a cycle
-- with automation_target_sessions.
ALTER TABLE sessions
    ADD COLUMN automation_owner_generation_id uuid;

CREATE INDEX idx_sessions_automation_owner
    ON sessions (org_id, automation_owner_generation_id)
    WHERE automation_owner_generation_id IS NOT NULL;

-- Identity, lifecycle, lock, and wake-outbox row for one
-- (automation, repository, pull request). Exists whether or not a session
-- has been created yet; active_generation = 0 means no session.
CREATE TABLE automation_targets (
    id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                      uuid NOT NULL REFERENCES organizations(id),
    automation_id               uuid NOT NULL REFERENCES automations(id),
    repository_id               uuid NOT NULL REFERENCES repositories(id),
    target_kind                 text NOT NULL,
    target_key                  text NOT NULL CHECK (length(target_key) BETWEEN 1 AND 64),
    active_generation           integer NOT NULL DEFAULT 0 CHECK (active_generation >= 0),
    lifecycle_state             text NOT NULL DEFAULT 'open',
    lifecycle_updated_at        timestamptz,
    observed_head_sha           text,
    observed_head_updated_at    timestamptz,
    head_epoch                  integer NOT NULL DEFAULT 0 CHECK (head_epoch >= 0),
    head_resolution_pending     boolean NOT NULL DEFAULT false,
    head_resolution_deadline_at timestamptz,
    wake_requested_at           timestamptz,
    created_at                  timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_automation_targets_target_kind
        CHECK (target_kind IN ('github_pull_request')),
    CONSTRAINT chk_automation_targets_lifecycle_state
        CHECK (lifecycle_state IN ('open', 'closed', 'merged')),
    UNIQUE (org_id, automation_id, repository_id, target_kind, target_key)
);

CREATE INDEX idx_automation_targets_wake
    ON automation_targets (org_id, wake_requested_at)
    WHERE wake_requested_at IS NOT NULL;

-- One row per generation of a target's session. At most one active
-- generation per target; retired rows stay for history.
CREATE TABLE automation_target_sessions (
    id                                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                            uuid NOT NULL REFERENCES organizations(id),
    target_id                         uuid NOT NULL REFERENCES automation_targets(id),
    generation                        integer NOT NULL CHECK (generation >= 1),
    session_id                        uuid NOT NULL REFERENCES sessions(id),
    status                            text NOT NULL DEFAULT 'active',
    retired_reason                    text,
    retired_at                        timestamptz,
    turn_count                        integer NOT NULL DEFAULT 0 CHECK (turn_count >= 0),
    ownership_release_pending         boolean NOT NULL DEFAULT false,
    last_attempted_head_sha           text,
    last_reviewed_head_sha            text,
    last_reviewed_epoch               integer NOT NULL DEFAULT 0 CHECK (last_reviewed_epoch >= 0),
    checkpoint_snapshot_key           text,
    checkpoint_head_sha               text,
    checkpoint_dependency_fingerprint text,
    checkpoint_review_complete        boolean,
    last_base_ref                     text,
    last_run_id                       uuid REFERENCES automation_runs(id),
    last_turn_at                      timestamptz,
    created_at                        timestamptz NOT NULL DEFAULT now(),
    updated_at                        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_automation_target_sessions_status
        CHECK (status IN ('active', 'retired')),
    CONSTRAINT chk_automation_target_sessions_retired_reason
        CHECK (retired_reason IN (
            'manual_reset', 'pr_closed', 'pr_merged', 'session_unavailable',
            'not_resumable', 'agent_config_changed', 'identity_changed',
            'base_retargeted', 'turn_limit', 'snapshot_too_large',
            'unsupported_workspace', 'awaiting_input', 'continuity_disabled')),
    CONSTRAINT chk_automation_target_sessions_retirement CHECK (
        (status = 'active'  AND retired_at IS NULL     AND retired_reason IS NULL) OR
        (status = 'retired' AND retired_at IS NOT NULL AND retired_reason IS NOT NULL)),
    UNIQUE (org_id, target_id, generation)
);

CREATE UNIQUE INDEX idx_automation_target_sessions_active
    ON automation_target_sessions (org_id, target_id)
    WHERE status = 'active';

CREATE INDEX idx_automation_target_sessions_session
    ON automation_target_sessions (org_id, session_id);

-- Run-local turn identity, dispatch state, attempt fencing, head authority,
-- and Stage 3 gate measurements. thread_id and job_id carry no FK because
-- thread rows cascade with sessions and job rows are pruned.
ALTER TABLE automation_runs
    ADD COLUMN target_id uuid REFERENCES automation_targets(id),
    ADD COLUMN target_generation integer,
    ADD COLUMN session_id uuid REFERENCES sessions(id),
    ADD COLUMN thread_id uuid,
    ADD COLUMN turn_number integer,
    ADD COLUMN github_action text,
    ADD COLUMN pull_request_updated_at timestamptz,
    ADD COLUMN head_epoch integer,
    ADD COLUMN head_resolution text,
    ADD COLUMN continuation_mode text,
    ADD COLUMN continuation_reason text,
    ADD COLUMN native_context boolean,
    ADD COLUMN previous_head_sha text,
    ADD COLUMN base_sha text,
    ADD COLUMN dispatch_state text,
    ADD COLUMN wait_reason text,
    ADD COLUMN wait_started_at timestamptz,
    ADD COLUMN execution_started_at timestamptz,
    ADD COLUMN job_id uuid,
    ADD COLUMN attempt integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    ADD COLUMN attempt_lock_token uuid,
    ADD COLUMN attempt_started_at timestamptz,
    ADD COLUMN superseded_by_run_id uuid REFERENCES automation_runs(id),
    ADD COLUMN outcome_reason text,
    ADD COLUMN head_lookup_degraded boolean NOT NULL DEFAULT false,
    ADD COLUMN worker_node_id text,
    ADD COLUMN restore_snapshot_bytes bigint,
    ADD COLUMN restore_duration_ms integer,
    ADD COLUMN turn_duration_ms integer,
    ADD CONSTRAINT chk_automation_runs_head_resolution
        CHECK (head_resolution IN ('authoritative', 'ambiguous', 'unresolved')),
    ADD CONSTRAINT chk_automation_runs_continuation_mode
        CHECK (continuation_mode IN ('fresh', 'continued', 'reconstructed')),
    ADD CONSTRAINT chk_automation_runs_continuation_reason
        CHECK (continuation_reason IN (
            'no_generation', 'kill_switch', 'session_unavailable', 'not_resumable',
            'agent_config_changed', 'identity_changed', 'base_retargeted', 'turn_limit',
            'snapshot_too_large', 'unsupported_workspace', 'awaiting_input',
            'snapshot_missing', 'restore_failed', 'sandbox_destroyed')),
    ADD CONSTRAINT chk_automation_runs_dispatch_state
        CHECK (dispatch_state IN ('waiting', 'executing', 'done')),
    ADD CONSTRAINT chk_automation_runs_wait_reason
        CHECK (wait_reason IN ('target_busy')),
    ADD CONSTRAINT chk_automation_runs_outcome_reason
        CHECK (outcome_reason IN (
            'turn_completed', 'head_lookup_degraded', 'agent_failed', 'cancelled', 'awaiting_input',
            'retries_exhausted', 'stale_head', 'duplicate_head', 'superseded', 'wait_timeout',
            'wait_overflow', 'pr_closed', 'repository_unavailable')),
    -- The reservation must install session, job, and start time in the
    -- same statement that flips dispatch_state to executing.
    ADD CONSTRAINT chk_automation_runs_executing CHECK (
        dispatch_state IS DISTINCT FROM 'executing'
        OR (session_id IS NOT NULL AND job_id IS NOT NULL AND execution_started_at IS NOT NULL));

-- At most one executing run per target across generations.
CREATE UNIQUE INDEX idx_automation_runs_one_executing_per_target
    ON automation_runs (org_id, target_id)
    WHERE dispatch_state = 'executing';

CREATE INDEX idx_automation_runs_session
    ON automation_runs (org_id, session_id, triggered_at DESC)
    WHERE session_id IS NOT NULL;

CREATE INDEX idx_automation_runs_waiting_target
    ON automation_runs (org_id, target_id, triggered_at)
    WHERE dispatch_state = 'waiting';

-- Result marker written by the orchestrator at every attempt end, in the
-- same transaction as the session status write, so a crash between the
-- turn ending and the completer running can be recovered from.
CREATE TABLE automation_run_results (
    run_id                 uuid PRIMARY KEY REFERENCES automation_runs(id),
    org_id                 uuid NOT NULL REFERENCES organizations(id),
    attempt                integer NOT NULL,
    attempt_lock_token     uuid NOT NULL,
    thread_id              uuid NOT NULL,
    turn_number            integer NOT NULL,
    outcome                text NOT NULL,
    review_complete        boolean NOT NULL DEFAULT false,
    checkpoint_key         text,
    checkpoint_published   boolean NOT NULL DEFAULT false,
    checkpoint_head_sha    text,
    native_context         boolean NOT NULL,
    dependency_fingerprint text,
    agent_session_id       text,
    recorded_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_automation_run_results_outcome
        CHECK (outcome IN ('turn_completed', 'agent_failed', 'cancelled', 'awaiting_input'))
);

-- Per-turn attribution for the user and assistant messages of a per-target
-- turn; usage rollups attribute token_usage by it.
ALTER TABLE session_messages
    ADD COLUMN automation_run_id uuid REFERENCES automation_runs(id);

CREATE INDEX idx_session_messages_automation_run
    ON session_messages (org_id, automation_run_id)
    WHERE automation_run_id IS NOT NULL;
