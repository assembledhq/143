DROP INDEX IF EXISTS idx_preview_api_tokens_created_by;
DROP INDEX IF EXISTS idx_preview_targets_created_by;

ALTER TABLE preview_api_tokens
    DROP CONSTRAINT preview_api_tokens_created_by_user_id_fkey,
    ADD CONSTRAINT preview_api_tokens_created_by_user_id_fkey
        FOREIGN KEY (created_by_user_id) REFERENCES users(id);
