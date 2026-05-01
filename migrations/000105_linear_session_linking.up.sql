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
--
-- workspace_id is informational only and not part of any unique constraint.
-- Linear treats workspace ids as effectively immutable, but if a row's
-- workspace_id ever drifts from what the integration is currently bound to
-- (e.g. mid-rollout reconnect), the row is still uniquely identified by
-- (integration_id, team_key), and ReplaceForIntegration upserts workspace_id
-- back to the live value. The detection path keys on team_key alone, so a
-- stale workspace_id can't redirect a write — but it can keep an old row
-- visible to ListByOrg until the next refresh.
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
    ADD COLUMN linear_prepare_state text NOT NULL DEFAULT 'none';

-- Validate the prepare-state vocabulary as a separate ADD CONSTRAINT NOT
-- VALID + VALIDATE pair. The split is mostly for readability — golang-migrate
-- wraps each migration file in a transaction by default, so the
-- AccessExclusiveLock taken by the ADD COLUMN above is held until COMMIT and
-- the VALIDATE inherits it (the looser SHARE UPDATE EXCLUSIVE lock VALIDATE
-- normally takes only kicks in when these statements run in their own
-- transactions). The split still has two real benefits: NOT VALID accepts
-- the constraint immediately so the failure mode for new bad rows is the
-- same as a normal CHECK, and if a future migration scales out this table
-- enough that the validation scan becomes painful, splitting VALIDATE into
-- its own non-transactional migration is a one-line change.
-- IMPORTANT: the value list below must stay in lockstep with
-- models.LinearPrepareState* in internal/models/session_enums.go. The
-- TestLinearPrepareStateMigrationVocabularyMatchesGoEnum test parses this
-- migration and fails if the two drift.
ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_linear_prepare_state
        CHECK (linear_prepare_state IN ('none', 'pending', 'ready', 'failed'))
        NOT VALID;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_linear_prepare_state;

-- Partial index on the Linear identifier hint. The column is sparsely
-- populated (only set on sessions that resolved a Linear primary), so a
-- partial index keeps the index small and avoids bloating sessions writes
-- for the common no-Linear case. Future queries that filter sessions by
-- Linear identifier (analytics, "all sessions for ACS-1234") use this.
CREATE INDEX idx_sessions_linear_identifier_hint
    ON sessions (org_id, linear_identifier_hint)
    WHERE linear_identifier_hint IS NOT NULL;

-- Enforce immutability of the per-session Linear policy flags. Both
-- linear_private and linear_state_sync_disabled are documented (and depended
-- on by the service layer) as "frozen at session create" — flipping them
-- mid-flight would produce confusing semantics like "post the missed events
-- now" backfills, and the worker's enqueue-time decisions are made under
-- the assumption that these don't change. A row-level BEFORE UPDATE trigger
-- is the cheapest enforcement: the column-list specifier (UPDATE OF …)
-- means the trigger is only consulted when the columns actually appear in
-- the SET clause, so the common case of an UPDATE that doesn't touch them
-- pays nothing. IS DISTINCT FROM correctly handles the NULL/non-NULL
-- transitions even though both columns are NOT NULL today (defensive
-- against a future migration relaxing that). Errors raise check_violation
-- so they show up alongside the prepare-state CHECK on the same surface.
CREATE OR REPLACE FUNCTION sessions_linear_flags_immutable()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.linear_private IS DISTINCT FROM NEW.linear_private THEN
        RAISE EXCEPTION 'sessions.linear_private is immutable after create (session_id=%)', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.linear_state_sync_disabled IS DISTINCT FROM NEW.linear_state_sync_disabled THEN
        RAISE EXCEPTION 'sessions.linear_state_sync_disabled is immutable after create (session_id=%)', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER sessions_linear_flags_immutable_trigger
    BEFORE UPDATE OF linear_private, linear_state_sync_disabled ON sessions
    FOR EACH ROW
    EXECUTE FUNCTION sessions_linear_flags_immutable();

-- Org/team Linear automation defaults. Stored as JSON in org_settings to avoid
-- a fresh per-team table for v1 — see linear_settings.go for the parser.
-- (No schema change required here; documented for future readers.)
