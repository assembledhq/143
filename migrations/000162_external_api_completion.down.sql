ALTER TABLE automations
    DROP COLUMN IF EXISTS external_metadata;

ALTER TABLE session_attributions
    DROP CONSTRAINT chk_session_attributions_source;

DELETE FROM session_attributions WHERE source = 'external_api';

ALTER TABLE session_attributions
    ADD CONSTRAINT chk_session_attributions_source CHECK (source IN ('slack'));
