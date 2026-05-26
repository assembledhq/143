-- Fix preview_api_tokens.created_by_user_id to cascade on user deletion,
-- matching the ON DELETE CASCADE already on preview_targets.created_by_user_id.
ALTER TABLE preview_api_tokens
    DROP CONSTRAINT preview_api_tokens_created_by_user_id_fkey,
    ADD CONSTRAINT preview_api_tokens_created_by_user_id_fkey
        FOREIGN KEY (created_by_user_id) REFERENCES users(id) ON DELETE CASCADE;

-- Add indexes to support cascade-delete performance and user-scoped queries.
CREATE INDEX IF NOT EXISTS idx_preview_targets_created_by
    ON preview_targets (created_by_user_id);

CREATE INDEX IF NOT EXISTS idx_preview_api_tokens_created_by
    ON preview_api_tokens (created_by_user_id);
