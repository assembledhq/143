DROP TABLE IF EXISTS schema_compat_floors;

DROP INDEX IF EXISTS idx_jobs_pending_claim_channel;

ALTER TABLE nodes DROP COLUMN IF EXISTS channel;

ALTER TABLE jobs DROP COLUMN IF EXISTS channel;

ALTER TABLE organizations DROP COLUMN IF EXISTS release_channel;
