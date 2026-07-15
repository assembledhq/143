-- The backfill is intentionally not deleted on rollback: unified identity
-- rows may have been claimed or updated after migration and are user data.
DROP TABLE IF EXISTS external_user_observations;
DELETE FROM session_attributions WHERE source = 'linear';
ALTER TABLE session_attributions
    DROP CONSTRAINT chk_session_attributions_source;
ALTER TABLE session_attributions
    ADD CONSTRAINT chk_session_attributions_source CHECK (source IN ('slack', 'external_api'));
