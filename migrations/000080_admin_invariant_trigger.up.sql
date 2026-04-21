-- DB-level "every organization must have at least one admin" invariant.
--
-- We already enforce this at the Go layer via OrganizationMembershipStore's
-- UpdateRoleGuarded / RemoveGuarded, which open a transaction and take a
-- FOR UPDATE lock on the admin rows before checking the count. That handles
-- the normal concurrent-demotion race correctly.
--
-- This trigger is the belt-and-suspenders layer: any code path that reaches
-- organization_memberships directly (backfills, ad-hoc SQL, a future handler
-- that forgets to call the Guarded variants, replication tooling, etc.) is
-- still prevented from orphaning an org. The application-level guard stays
-- the ergonomic check and returns a useful error; the trigger is the
-- catch-all that makes the invariant a schema-level property.
--
-- DEFERRABLE INITIALLY DEFERRED so multi-statement transactions that legit
-- reshuffle admins (e.g. promote-then-demote in one tx) see the final state
-- at commit time rather than tripping mid-flight. Apps that need the check
-- to fire earlier in a tx can `SET CONSTRAINTS enforce_last_admin IMMEDIATE`.
--
-- The DEFERRED semantics are LOAD-BEARING, not a performance tweak. Do not
-- flip this to IMMEDIATE without auditing every mutating admin path:
--   * UpdateRoleGuarded's in-tx demote check reads prevRole, takes the
--     lock, then runs the UPDATE. An IMMEDIATE trigger would fire after
--     the UPDATE and before the Go layer had a chance to check the count,
--     so a future path that inserts a replacement admin and demotes the
--     old one in one tx would spuriously fail on the intermediate state.
--   * Any future migration that backfills role changes across existing
--     memberships needs a single commit boundary to see "net roles", not
--     each row in isolation.
-- The Go-layer *Guarded methods and the commit-time mapLastAdminViolation
-- helper assume the check fires at COMMIT only; see
-- internal/db/organization_memberships.go.

CREATE OR REPLACE FUNCTION enforce_last_admin_invariant()
RETURNS TRIGGER AS $$
DECLARE
    target_org_id uuid;
    admin_count integer;
BEGIN
    -- org_id is part of the primary key so it cannot change via UPDATE;
    -- OLD.org_id is the right key for both DELETE and UPDATE.
    target_org_id := OLD.org_id;

    -- If the org was cascade-deleted, the invariant does not apply: there is
    -- no org left to protect. Checking existence avoids false positives when
    -- DROP ORG cascades through memberships.
    IF NOT EXISTS (SELECT 1 FROM organizations WHERE id = target_org_id) THEN
        RETURN NULL;
    END IF;

    SELECT COUNT(*) INTO admin_count
    FROM organization_memberships
    WHERE org_id = target_org_id AND role = 'admin';

    IF admin_count = 0 THEN
        -- CONSTRAINT tag lets the Go layer identify trigger-raised violations
        -- precisely (see mapLastAdminViolation) rather than matching every
        -- 23514 SQLSTATE as last-admin, which could catch unrelated check
        -- constraints added later.
        RAISE EXCEPTION 'organization % would be left with no admins', target_org_id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'enforce_last_admin';
    END IF;

    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER enforce_last_admin
AFTER DELETE OR UPDATE ON organization_memberships
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION enforce_last_admin_invariant();
