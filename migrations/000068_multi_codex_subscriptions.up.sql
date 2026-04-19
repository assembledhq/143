-- Allow multiple credentials per org+provider by adding a label column.
-- This enables round-robin across multiple Codex (ChatGPT) subscriptions.

ALTER TABLE org_credentials ADD COLUMN label text NOT NULL DEFAULT '';
ALTER TABLE org_credentials ADD COLUMN last_used_at timestamptz;
ALTER TABLE org_credentials ADD COLUMN created_by uuid REFERENCES users(id);

-- Replace the old unique constraint with one that includes label.
ALTER TABLE org_credentials DROP CONSTRAINT org_credentials_org_id_provider_key;
ALTER TABLE org_credentials ADD CONSTRAINT org_credentials_org_id_provider_label_key UNIQUE (org_id, provider, label);

-- Allow new statuses introduced by the device-code OAuth flow:
--   'pending_auth' — device code issued, waiting for user to enter it
--   'invalid'     — refresh token revoked; user must reconnect
ALTER TABLE org_credentials DROP CONSTRAINT chk_org_credentials_status;
ALTER TABLE org_credentials
    ADD CONSTRAINT chk_org_credentials_status CHECK (status IN (
        'active', 'disabled', 'pending_auth', 'invalid'
    )) NOT VALID;
ALTER TABLE org_credentials VALIDATE CONSTRAINT chk_org_credentials_status;

-- Index for round-robin selection: find the least-recently-used active credential.
CREATE INDEX idx_org_credentials_round_robin ON org_credentials (org_id, provider, status, last_used_at NULLS FIRST, created_at)
  WHERE status = 'active';
