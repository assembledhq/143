ALTER TABLE automations
    ADD COLUMN identity_scope TEXT NOT NULL DEFAULT 'org';

ALTER TABLE automations
    ADD CONSTRAINT chk_automations_identity_scope
    CHECK (identity_scope IN ('org', 'personal'));
