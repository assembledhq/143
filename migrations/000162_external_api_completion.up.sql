ALTER TABLE session_attributions
    DROP CONSTRAINT chk_session_attributions_source;

ALTER TABLE session_attributions
    ADD CONSTRAINT chk_session_attributions_source CHECK (source IN ('slack', 'external_api'));

ALTER TABLE automations
    ADD COLUMN external_metadata jsonb NOT NULL DEFAULT '{}'::jsonb;
