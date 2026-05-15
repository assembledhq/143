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
CREATE UNIQUE INDEX IF NOT EXISTS idx_integrations_linear_workspace
    ON integrations ((config->>'workspace_id'))
    WHERE provider = 'linear'
      AND status = 'active'
      AND config->>'workspace_id' IS NOT NULL;
