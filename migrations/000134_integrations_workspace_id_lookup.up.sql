-- Multi-tenant Linear webhook routing
--
-- A single Linear OAuth app has one webhook URL across every workspace it is
-- installed in. Linear includes `organizationId` (the Linear workspace id) in
-- every webhook payload; we resolve the owning 143 org by matching that
-- against integrations.config->>'workspace_id', which the Linear OAuth
-- callback now persists alongside the existing credential write.
--
-- A partial UNIQUE index does the lookup in O(log n) at SaaS scale and also
-- enforces a one-workspace-per-app invariant: a Linear workspace cannot be
-- bound to two 143 orgs simultaneously. If the same workspace is connected
-- twice (e.g. a customer migrates from one 143 org to another), the older
-- integration must be disconnected first — this surfaces the conflict at
-- write time instead of at webhook time, where it would be undebuggable.
--
-- Note on locking: this CREATE INDEX takes an ACCESS EXCLUSIVE lock on
-- `integrations` for the duration of the build. We accept the brief lock
-- (rather than CREATE INDEX CONCURRENTLY) because our migrator wraps each
-- file in a single transaction, and CONCURRENTLY cannot run inside one.
-- The `integrations` table is small (one row per (org, provider) install,
-- at most a few hundred rows at SaaS scale) so the build completes in
-- milliseconds. The 5s lock_timeout below is the safety bound — if the
-- table is somehow much larger or some other session is holding a
-- conflicting lock, the migration fails fast instead of blocking writes.
SET LOCAL lock_timeout = '5s';
CREATE UNIQUE INDEX IF NOT EXISTS idx_integrations_linear_workspace
    ON integrations ((config->>'workspace_id'))
    WHERE provider = 'linear'
      AND status = 'active'
      AND config->>'workspace_id' IS NOT NULL;
