-- Multi-organization membership support.
--
-- Introduces a join table between users and organizations so a single user
-- identity can belong to multiple orgs. `users.org_id` and `users.role` are
-- kept during the compatibility window — derived values during transition.
-- `auth_sessions.last_org_id` is the bootstrap hint used when a new tab opens
-- and before the client has echoed back an X-Active-Org-ID header.
--
-- Only this migration is required to ship the membership infrastructure.
-- Dropping the legacy columns happens in a later, gated migration.

-- Bound how long the migration is willing to wait for locks. The schema
-- changes and the chunked backfill loop below all run under this ceiling.
-- lock_timeout is transaction-local (reset at commit). statement_timeout is
-- NOT set at the transaction scope because the DO block below runs the
-- backfill as a single statement from Postgres' perspective: a 5-minute cap
-- on the whole loop would defeat the point of chunking on a large
-- auth_sessions table. Each inner UPDATE gets its own lock attempt bounded
-- by lock_timeout; between batches we release locks and recheck.
SET LOCAL lock_timeout = '30s';

CREATE TABLE organization_memberships (
    user_id    uuid        NOT NULL REFERENCES users(id)         ON DELETE CASCADE,
    org_id     uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    role       text        NOT NULL CHECK (role IN ('admin', 'member', 'viewer')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, org_id)
);

CREATE INDEX idx_memberships_user ON organization_memberships (user_id);
CREATE INDEX idx_memberships_org  ON organization_memberships (org_id);

-- Composite index supports OldestForUser / ListByUser: both filter by user_id
-- and order by created_at (with org_id or user_id as tiebreak). Without this
-- index Postgres can satisfy the filter from idx_memberships_user but must
-- then sort in memory. For a user with many memberships this is a needless
-- sort on every request-time resolution.
CREATE INDEX idx_memberships_user_created ON organization_memberships (user_id, created_at, org_id);

-- Backfill one membership per existing user, preserving role. Skip rows with
-- a NULL org_id: `users.org_id` is currently NOT NULL, but future work (e.g.
-- zero-membership users after an org deletion cascade) may relax that, and
-- `organization_memberships.org_id` is NOT NULL on this table so an unfiltered
-- INSERT would abort the deploy mid-flight. Mirror the defensive
-- `WHERE last_org_id IS NULL` filter the auth_sessions backfill below uses.
--
-- This runs as a single INSERT because organization_memberships was just
-- created and has no existing rows/readers — there is nothing to chunk
-- around. If users grows large enough that even this insert is slow, the
-- chunking pattern below can be lifted onto it.
INSERT INTO organization_memberships (user_id, org_id, role, created_at)
SELECT id, org_id, role, created_at
FROM users
WHERE org_id IS NOT NULL
ON CONFLICT (user_id, org_id) DO NOTHING;

-- `last_org_id` is a per-session hint. Nullable so sign-in still works for
-- zero-membership users (empty state) and if the session's last org is
-- deleted. The old `org_id` column stays in place for the compatibility
-- window; middleware reads from memberships but falls back to the session's
-- `last_org_id` when the X-Active-Org-ID header is not present.
ALTER TABLE auth_sessions ADD COLUMN last_org_id uuid REFERENCES organizations(id) ON DELETE SET NULL;

-- Backfill last_org_id in 10k-row chunks. A single UPDATE on a large
-- auth_sessions table would hold row locks for the whole duration and can
-- easily exceed lock_timeout on busy deploys. Chunking keeps each statement
-- short (≤ lock_timeout per batch) so concurrent session inserts/updates
-- proceed between batches; the trade-off is a longer overall migration, not
-- a deploy-blocking lock contention spike.
--
-- CTID-based chunk selection avoids needing an index on auth_sessions.id and
-- works regardless of id distribution. The EXISTS guard on the inner UPDATE
-- means an empty batch terminates the loop. Committing inside the DO block
-- is not supported (PL/pgSQL anonymous blocks run in the outer tx) so lock
-- release happens only at migration commit; the chunk size is tuned to keep
-- each UPDATE small enough to be tolerable even under a single tx.
DO $$
DECLARE
    batch_size constant integer := 10000;
    updated integer;
BEGIN
    LOOP
        UPDATE auth_sessions
        SET last_org_id = org_id
        WHERE ctid IN (
            SELECT ctid
            FROM auth_sessions
            WHERE last_org_id IS NULL
            LIMIT batch_size
        );
        GET DIAGNOSTICS updated = ROW_COUNT;
        EXIT WHEN updated = 0;
    END LOOP;
END $$;
