-- Linear session linking (design doc 62).
--
-- session_issue_links remains provider-agnostic; all Linear-specific persisted
-- state (attachment IDs, rolling comment IDs, prior-state captures, last-known
-- state cache) lives in this side table keyed by link_id. Future trackers grow
-- their own rows in the same table without polluting the join.

-- The two ALTER TABLE statements below take AccessExclusiveLock on `sessions`
-- (a hot table). Each ADD COLUMN is metadata-only on PG ≥11 because the
-- DEFAULTs are constants, but if a long-running transaction is holding any
-- lock on `sessions`, this migration will queue behind it and stall every new
-- query until it acquires. Fail fast instead — operators can replay the
-- migration during a quieter window. lock_timeout is transaction-local and
-- resets at commit.
SET LOCAL lock_timeout = '5s';

CREATE TABLE session_issue_link_provider_state (
    link_id    uuid PRIMARY KEY REFERENCES session_issue_links(id) ON DELETE CASCADE,
    org_id     uuid NOT NULL REFERENCES organizations(id),
    provider   text NOT NULL,
    state      jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_session_issue_link_provider_state_org_provider
    ON session_issue_link_provider_state (org_id, provider);

-- Append-only audit log of state-sync decisions. UNIQUE constraint enforces
-- fire-once: replays of the same (session, issue, event_kind) become no-ops.
-- skipped_reason captures why a transition was *not* taken (debounced, user
-- recently edited, coexistence with Linear's GitHub integration, etc.).
CREATE TABLE session_issue_link_state_events (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid NOT NULL REFERENCES organizations(id),
    session_id      uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    issue_id        uuid NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    event_kind      text NOT NULL,
    transition_from text,
    transition_to   text,
    skipped_reason  text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, issue_id, event_kind)
);

CREATE INDEX idx_session_issue_link_state_events_org_session
    ON session_issue_link_state_events (org_id, session_id, created_at DESC);

-- Cache of Linear team keys per integration. Detection only treats a bare
-- identifier (e.g. "ACS-1234") as a Linear ref when the prefix matches a team
-- key in this cache; without it, JIRA keys, AWS resource IDs, and internal
-- codes would all be false positives. Refreshed on OAuth install and every 24h.
CREATE TABLE linear_team_keys (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         uuid NOT NULL REFERENCES organizations(id),
    integration_id uuid NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    workspace_id   text NOT NULL,
    team_id        text NOT NULL,
    team_key       text NOT NULL,
    team_name      text NOT NULL,
    refreshed_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (integration_id, team_key)
);

CREATE INDEX idx_linear_team_keys_org_workspace
    ON linear_team_keys (org_id, workspace_id);

-- Per-session Linear policy + prepare-state columns. Combined into a single
-- ALTER so the catalog update is one statement against the hot sessions
-- table. Flags are frozen at session create — see design 62 §"Composer
-- controls must express distinct semantics" for why these are not editable
-- later (avoids confusing "post the missed events now" backfills).
-- linear_prepare_state is the idempotent dedupe surface for prepare-and-link
-- work; the job worker uses (session_id, source_inputs_hash) as the key so
-- re-detections collapse cleanly.
ALTER TABLE sessions
    ADD COLUMN linear_private boolean NOT NULL DEFAULT false,
    ADD COLUMN linear_state_sync_disabled boolean NOT NULL DEFAULT false,
    ADD COLUMN linear_identifier_hint text,
    ADD COLUMN linear_prepare_state text NOT NULL DEFAULT 'none',
    ADD CONSTRAINT chk_sessions_linear_prepare_state
        CHECK (linear_prepare_state IN ('none', 'pending', 'ready', 'failed'));

-- Org/team Linear automation defaults. Stored as JSON in org_settings to avoid
-- a fresh per-team table for v1 — see linear_settings.go for the parser.
-- (No schema change required here; documented for future readers.)
