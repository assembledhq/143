-- Stop every compatibility trigger before reverting any data. The verification
-- and cache triggers normalize writes toward the new names, so a revert that
-- ran while they were installed would be silently undone by them.
DROP TRIGGER IF EXISTS trg_normalize_preview_dependency_cache_kind ON preview_dependency_cache;
DROP TRIGGER IF EXISTS trg_normalize_preview_dependency_cache_location_kind ON preview_dependency_cache_locations;
DROP FUNCTION IF EXISTS normalize_preview_cache_kind_compatibility();

DO $$
BEGIN
    IF to_regclass('code_review_prompt_artifacts') IS NOT NULL THEN
        DROP TRIGGER IF EXISTS trg_sync_code_review_prompt_artifacts ON code_review_prompt_artifacts;
    END IF;
    IF to_regclass('code_review_prompt_records') IS NOT NULL THEN
        DROP TRIGGER IF EXISTS trg_sync_code_review_prompt_records ON code_review_prompt_records;
    END IF;
    IF to_regclass('code_review_session_metadata') IS NOT NULL THEN
        DROP TRIGGER IF EXISTS trg_sync_code_review_prompt_key ON code_review_session_metadata;
    END IF;
    IF to_regclass('preview_verification_runs') IS NOT NULL THEN
        DROP TRIGGER IF EXISTS trg_sync_preview_verification_captures ON preview_verification_runs;
    END IF;
    IF to_regclass('session_diff_snapshots') IS NOT NULL THEN
        DROP TRIGGER IF EXISTS trg_sync_session_review_bundle ON session_diff_snapshots;
    END IF;
END $$;

DROP FUNCTION IF EXISTS sync_code_review_prompt_record_compatibility();
DROP FUNCTION IF EXISTS sync_code_review_prompt_key_compatibility();
DROP FUNCTION IF EXISTS sync_preview_verification_capture_compatibility();
DROP FUNCTION IF EXISTS sync_session_review_bundle_compatibility();

-- Revert only the data this migration's up actually rewrote. A fresh
-- installation was created with the current names by migrations 176/219/225/226
-- and the up made no data changes there, so its rollback must make none either.
-- That matters most for cache kinds: dependency cache lookups match cache_kind
-- exactly, with no read-side fallback, so renaming them on a fresh installation
-- would silently miss every lookup.
DO $$
DECLARE
    is_legacy_installation boolean;
BEGIN
    SELECT to_regclass('code_review_prompt_artifacts') IS NOT NULL
        OR EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = current_schema() AND table_name = 'session_diff_snapshots'
              AND column_name = 'review_artifact_key'
        )
    INTO is_legacy_installation;

    IF NOT is_legacy_installation THEN
        RETURN;
    END IF;

    UPDATE code_review_agent_results
    SET structured_result = (structured_result - 'prompt_record_key' - 'raw_record_key')
        || CASE
            WHEN structured_result ? 'prompt_record_key'
            THEN jsonb_build_object('prompt_artifact_key', structured_result->'prompt_record_key')
            ELSE '{}'::jsonb
        END
        || CASE
            WHEN structured_result ? 'raw_record_key'
            THEN jsonb_build_object('raw_artifact_key', structured_result->'raw_record_key')
            ELSE '{}'::jsonb
        END
    WHERE structured_result ? 'prompt_record_key'
       OR structured_result ? 'raw_record_key';

    ALTER TABLE preview_dependency_cache
        ALTER COLUMN cache_kind SET DEFAULT 'install_artifact';
    ALTER TABLE preview_dependency_cache_locations
        ALTER COLUMN cache_kind SET DEFAULT 'install_artifact';

    UPDATE preview_dependency_cache
    SET cache_kind = CASE cache_kind
            WHEN 'install_output' THEN 'install_artifact'
            WHEN 'build_output' THEN 'build_artifact'
            ELSE cache_kind
        END,
        metadata = CASE
            WHEN metadata->>'kind' = 'install_output' THEN jsonb_set(metadata, '{kind}', '"install_artifact"'::jsonb)
            WHEN metadata->>'kind' = 'build_output' THEN jsonb_set(metadata, '{kind}', '"build_artifact"'::jsonb)
            ELSE metadata
        END
    WHERE cache_kind IN ('install_output', 'build_output')
       OR metadata->>'kind' IN ('install_output', 'build_output');

    UPDATE preview_dependency_cache_locations
    SET cache_kind = CASE cache_kind
        WHEN 'install_output' THEN 'install_artifact'
        WHEN 'build_output' THEN 'build_artifact'
        ELSE cache_kind
    END
    WHERE cache_kind IN ('install_output', 'build_output');

    UPDATE preview_verification_runs
    SET steps = COALESCE((
        SELECT jsonb_agg(
            (CASE
                WHEN step ? 'capture' AND NOT step ? 'artifact'
                    THEN step || jsonb_build_object('artifact', step->'capture')
                ELSE step
            END) - 'capture'
            ORDER BY ordinal
        )
        FROM jsonb_array_elements(steps) WITH ORDINALITY AS items(step, ordinal)
    ), '[]'::jsonb)
    WHERE EXISTS (
        SELECT 1 FROM jsonb_array_elements(steps) AS items(step)
        WHERE step ? 'capture'
    );
END $$;

-- This rollback only undoes the expansion this migration performed. An existing
-- installation is contracted back to the legacy prompt table it arrived with. A
-- fresh installation already had only the new names at version 274 (migrations
-- 219/225/226 create them directly), so its schema is left untouched here and
-- inverted by those migrations' own rollbacks.
DO $$
BEGIN
    IF to_regclass('code_review_prompt_artifacts') IS NOT NULL
       AND to_regclass('code_review_prompt_records') IS NOT NULL THEN
        INSERT INTO code_review_prompt_artifacts (
            id, org_id, session_id, artifact_key, role, agent_provider, content, metadata, created_at
        )
        SELECT id, org_id, session_id, record_key, role, agent_provider, content, metadata, created_at
        FROM code_review_prompt_records
        ON CONFLICT (org_id, artifact_key) DO UPDATE SET
            session_id = EXCLUDED.session_id,
            role = EXCLUDED.role,
            agent_provider = EXCLUDED.agent_provider,
            content = EXCLUDED.content,
            metadata = EXCLUDED.metadata,
            created_at = EXCLUDED.created_at;
        DROP TABLE code_review_prompt_records;
    END IF;
END $$;

DO $$
DECLARE
    has_legacy boolean;
    has_current boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'code_review_session_metadata'
          AND column_name = 'prompt_artifact_key'
    ) INTO has_legacy;
    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'code_review_session_metadata'
          AND column_name = 'prompt_record_key'
    ) INTO has_current;

    IF has_legacy AND has_current THEN
        UPDATE code_review_session_metadata SET prompt_artifact_key = prompt_record_key;
        ALTER TABLE code_review_session_metadata DROP COLUMN prompt_record_key;
    END IF;
END $$;

DO $$
DECLARE
    has_legacy boolean;
    has_current boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'preview_verification_runs'
          AND column_name = 'artifacts'
    ) INTO has_legacy;
    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'preview_verification_runs'
          AND column_name = 'captures'
    ) INTO has_current;

    IF has_legacy AND has_current THEN
        UPDATE preview_verification_runs SET artifacts = captures;
        ALTER TABLE preview_verification_runs DROP COLUMN captures;
    END IF;
END $$;

DO $$
DECLARE
    suffix text;
    has_legacy boolean;
    has_current boolean;
BEGIN
    FOREACH suffix IN ARRAY ARRAY[
        'key', 'version', 'compressed_bytes', 'uncompressed_bytes',
        'file_count', 'skipped_count', 'truncated'
    ]
    LOOP
        SELECT EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = current_schema() AND table_name = 'session_diff_snapshots'
              AND column_name = 'review_artifact_' || suffix
        ) INTO has_legacy;
        SELECT EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = current_schema() AND table_name = 'session_diff_snapshots'
              AND column_name = 'review_bundle_' || suffix
        ) INTO has_current;

        IF has_legacy AND has_current THEN
            EXECUTE format(
                'UPDATE session_diff_snapshots SET %I = %I',
                'review_artifact_' || suffix,
                'review_bundle_' || suffix
            );
            EXECUTE format(
                'ALTER TABLE session_diff_snapshots DROP COLUMN %I',
                'review_bundle_' || suffix
            );
        END IF;
    END LOOP;
END $$;

-- The bundle-keyed index only exists on an installation this migration
-- expanded; a fresh installation keeps the one migration 219 created.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'session_diff_snapshots'
          AND column_name = 'review_artifact_key'
    ) THEN
        DROP INDEX IF EXISTS idx_session_diff_snapshots_review_bundle_key;
        CREATE INDEX IF NOT EXISTS idx_session_diff_snapshots_review_artifact_key
            ON session_diff_snapshots (org_id, session_id, review_artifact_key)
            WHERE review_artifact_key IS NOT NULL;
    END IF;
END $$;
