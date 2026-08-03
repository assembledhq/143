-- Expand the legacy output schema before switching runtime names. Existing
-- installations keep the old contracts synchronized for one rolling-deploy
-- window; fresh installations already have only the new schema from the
-- historical migrations.

-- Prompt records require two physical tables because INSERT ... ON CONFLICT
-- is not supported through a compatibility view. Keep them synchronized until
-- a later contract migration removes the legacy table.
DO $$
BEGIN
    IF to_regclass('code_review_prompt_artifacts') IS NOT NULL
       AND to_regclass('code_review_prompt_records') IS NULL THEN
        CREATE TABLE code_review_prompt_records (
            id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
            org_id uuid NOT NULL REFERENCES organizations(id),
            session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
            record_key text NOT NULL,
            role text NOT NULL CONSTRAINT chk_code_review_prompt_records_role
                CHECK (role IN ('reviewer', 'orchestrator', 'description_policy', 'reviewer_output', 'orchestrator_output')),
            agent_provider text NOT NULL DEFAULT '',
            content text NOT NULL,
            metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
            created_at timestamptz NOT NULL DEFAULT now()
        );

        CREATE UNIQUE INDEX idx_code_review_prompt_records_key
            ON code_review_prompt_records (org_id, record_key);
        CREATE INDEX idx_code_review_prompt_records_session
            ON code_review_prompt_records (org_id, session_id, created_at DESC);

        INSERT INTO code_review_prompt_records (
            id, org_id, session_id, record_key, role, agent_provider, content, metadata, created_at
        )
        SELECT id, org_id, session_id, artifact_key, role, agent_provider, content, metadata, created_at
        FROM code_review_prompt_artifacts
        ON CONFLICT (org_id, record_key) DO NOTHING;
    END IF;
END $$;

CREATE OR REPLACE FUNCTION sync_code_review_prompt_record_compatibility()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF pg_trigger_depth() > 1 THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;

    IF TG_OP = 'DELETE' THEN
        IF TG_TABLE_NAME = 'code_review_prompt_artifacts' THEN
            DELETE FROM code_review_prompt_records
            WHERE org_id = OLD.org_id AND record_key = OLD.artifact_key;
        ELSE
            DELETE FROM code_review_prompt_artifacts
            WHERE org_id = OLD.org_id AND artifact_key = OLD.record_key;
        END IF;
        RETURN OLD;
    END IF;

    -- A rekeyed row would not match the ON CONFLICT target below and would then
    -- collide with its own counterpart on the primary key, so retire the row
    -- filed under the previous key first.
    IF TG_OP = 'UPDATE' THEN
        IF TG_TABLE_NAME = 'code_review_prompt_artifacts' AND NEW.artifact_key IS DISTINCT FROM OLD.artifact_key THEN
            DELETE FROM code_review_prompt_records
            WHERE org_id = OLD.org_id AND record_key = OLD.artifact_key;
        ELSIF TG_TABLE_NAME = 'code_review_prompt_records' AND NEW.record_key IS DISTINCT FROM OLD.record_key THEN
            DELETE FROM code_review_prompt_artifacts
            WHERE org_id = OLD.org_id AND artifact_key = OLD.record_key;
        END IF;
    END IF;

    IF TG_TABLE_NAME = 'code_review_prompt_artifacts' THEN
        INSERT INTO code_review_prompt_records (
            id, org_id, session_id, record_key, role, agent_provider, content, metadata, created_at
        ) VALUES (
            NEW.id, NEW.org_id, NEW.session_id, NEW.artifact_key, NEW.role,
            NEW.agent_provider, NEW.content, NEW.metadata, NEW.created_at
        )
        ON CONFLICT (org_id, record_key) DO UPDATE SET
            session_id = EXCLUDED.session_id,
            role = EXCLUDED.role,
            agent_provider = EXCLUDED.agent_provider,
            content = EXCLUDED.content,
            metadata = EXCLUDED.metadata,
            created_at = EXCLUDED.created_at;
    ELSE
        INSERT INTO code_review_prompt_artifacts (
            id, org_id, session_id, artifact_key, role, agent_provider, content, metadata, created_at
        ) VALUES (
            NEW.id, NEW.org_id, NEW.session_id, NEW.record_key, NEW.role,
            NEW.agent_provider, NEW.content, NEW.metadata, NEW.created_at
        )
        ON CONFLICT (org_id, artifact_key) DO UPDATE SET
            session_id = EXCLUDED.session_id,
            role = EXCLUDED.role,
            agent_provider = EXCLUDED.agent_provider,
            content = EXCLUDED.content,
            metadata = EXCLUDED.metadata,
            created_at = EXCLUDED.created_at;
    END IF;
    RETURN NEW;
END;
$$;

DO $$
BEGIN
    IF to_regclass('code_review_prompt_artifacts') IS NOT NULL
       AND to_regclass('code_review_prompt_records') IS NOT NULL THEN
        DROP TRIGGER IF EXISTS trg_sync_code_review_prompt_artifacts ON code_review_prompt_artifacts;
        CREATE TRIGGER trg_sync_code_review_prompt_artifacts
            AFTER INSERT OR UPDATE OR DELETE ON code_review_prompt_artifacts
            FOR EACH ROW EXECUTE FUNCTION sync_code_review_prompt_record_compatibility();

        DROP TRIGGER IF EXISTS trg_sync_code_review_prompt_records ON code_review_prompt_records;
        CREATE TRIGGER trg_sync_code_review_prompt_records
            AFTER INSERT OR UPDATE OR DELETE ON code_review_prompt_records
            FOR EACH ROW EXECUTE FUNCTION sync_code_review_prompt_record_compatibility();
    END IF;
END $$;

-- Add and synchronize renamed columns without removing the names used by the
-- draining generation.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'code_review_session_metadata'
          AND column_name = 'prompt_artifact_key'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'code_review_session_metadata'
          AND column_name = 'prompt_record_key'
    ) THEN
        ALTER TABLE code_review_session_metadata ADD COLUMN prompt_record_key text;
        UPDATE code_review_session_metadata SET prompt_record_key = prompt_artifact_key;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'preview_verification_runs'
          AND column_name = 'artifacts'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'preview_verification_runs'
          AND column_name = 'captures'
    ) THEN
        ALTER TABLE preview_verification_runs
            ADD COLUMN captures jsonb NOT NULL DEFAULT '[]'::jsonb
            CONSTRAINT preview_verification_runs_captures_check
            CHECK (jsonb_typeof(captures) = 'array');
        UPDATE preview_verification_runs SET captures = artifacts;
    END IF;
END $$;

DO $$
DECLARE
    suffix text;
    data_type text;
    default_expr text;
BEGIN
    FOR suffix, data_type, default_expr IN
        SELECT * FROM (VALUES
            ('key', 'text', NULL),
            ('version', 'integer', NULL),
            ('compressed_bytes', 'bigint', '0'),
            ('uncompressed_bytes', 'bigint', '0'),
            ('file_count', 'integer', '0'),
            ('skipped_count', 'integer', '0'),
            ('truncated', 'boolean', 'false')
        ) AS definitions(suffix, data_type, default_expr)
    LOOP
        IF EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = current_schema() AND table_name = 'session_diff_snapshots'
              AND column_name = 'review_artifact_' || suffix
        ) AND NOT EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = current_schema() AND table_name = 'session_diff_snapshots'
              AND column_name = 'review_bundle_' || suffix
        ) THEN
            EXECUTE format(
                'ALTER TABLE session_diff_snapshots ADD COLUMN %I %s%s',
                'review_bundle_' || suffix,
                data_type,
                CASE WHEN default_expr IS NULL THEN '' ELSE ' NOT NULL DEFAULT ' || default_expr END
            );
            EXECUTE format(
                'UPDATE session_diff_snapshots SET %I = %I',
                'review_bundle_' || suffix,
                'review_artifact_' || suffix
            );
        END IF;
    END LOOP;
END $$;

CREATE INDEX IF NOT EXISTS idx_session_diff_snapshots_review_bundle_key
    ON session_diff_snapshots (org_id, session_id, review_bundle_key)
    WHERE review_bundle_key IS NOT NULL;

CREATE OR REPLACE FUNCTION sync_code_review_prompt_key_compatibility()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.prompt_record_key IS NULL THEN NEW.prompt_record_key := NEW.prompt_artifact_key; END IF;
        IF NEW.prompt_artifact_key IS NULL THEN NEW.prompt_artifact_key := NEW.prompt_record_key; END IF;
    ELSIF NEW.prompt_record_key IS DISTINCT FROM OLD.prompt_record_key THEN
        NEW.prompt_artifact_key := NEW.prompt_record_key;
    ELSIF NEW.prompt_artifact_key IS DISTINCT FROM OLD.prompt_artifact_key THEN
        NEW.prompt_record_key := NEW.prompt_artifact_key;
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION sync_preview_verification_capture_compatibility()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.captures = '[]'::jsonb AND NEW.artifacts <> '[]'::jsonb THEN NEW.captures := NEW.artifacts; END IF;
        IF NEW.artifacts = '[]'::jsonb AND NEW.captures <> '[]'::jsonb THEN NEW.artifacts := NEW.captures; END IF;
    ELSIF NEW.captures IS DISTINCT FROM OLD.captures THEN
        NEW.artifacts := NEW.captures;
    ELSIF NEW.artifacts IS DISTINCT FROM OLD.artifacts THEN
        NEW.captures := NEW.artifacts;
    END IF;
    NEW.steps := COALESCE((
        SELECT jsonb_agg(
            CASE
                WHEN step ? 'artifact' AND NOT step ? 'capture'
                    THEN step || jsonb_build_object('capture', step->'artifact')
                WHEN step ? 'capture' AND NOT step ? 'artifact'
                    THEN step || jsonb_build_object('artifact', step->'capture')
                ELSE step
            END
            ORDER BY ordinal
        )
        FROM jsonb_array_elements(NEW.steps) WITH ORDINALITY AS items(step, ordinal)
    ), '[]'::jsonb);
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION sync_session_review_bundle_compatibility()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.review_bundle_key IS NULL THEN NEW.review_bundle_key := NEW.review_artifact_key; END IF;
        IF NEW.review_artifact_key IS NULL THEN NEW.review_artifact_key := NEW.review_bundle_key; END IF;
        IF NEW.review_bundle_version IS NULL THEN NEW.review_bundle_version := NEW.review_artifact_version; END IF;
        IF NEW.review_artifact_version IS NULL THEN NEW.review_artifact_version := NEW.review_bundle_version; END IF;
        IF NEW.review_bundle_compressed_bytes = 0 THEN NEW.review_bundle_compressed_bytes := NEW.review_artifact_compressed_bytes; END IF;
        IF NEW.review_artifact_compressed_bytes = 0 THEN NEW.review_artifact_compressed_bytes := NEW.review_bundle_compressed_bytes; END IF;
        IF NEW.review_bundle_uncompressed_bytes = 0 THEN NEW.review_bundle_uncompressed_bytes := NEW.review_artifact_uncompressed_bytes; END IF;
        IF NEW.review_artifact_uncompressed_bytes = 0 THEN NEW.review_artifact_uncompressed_bytes := NEW.review_bundle_uncompressed_bytes; END IF;
        IF NEW.review_bundle_file_count = 0 THEN NEW.review_bundle_file_count := NEW.review_artifact_file_count; END IF;
        IF NEW.review_artifact_file_count = 0 THEN NEW.review_artifact_file_count := NEW.review_bundle_file_count; END IF;
        IF NEW.review_bundle_skipped_count = 0 THEN NEW.review_bundle_skipped_count := NEW.review_artifact_skipped_count; END IF;
        IF NEW.review_artifact_skipped_count = 0 THEN NEW.review_artifact_skipped_count := NEW.review_bundle_skipped_count; END IF;
        IF NOT NEW.review_bundle_truncated THEN NEW.review_bundle_truncated := NEW.review_artifact_truncated; END IF;
        IF NOT NEW.review_artifact_truncated THEN NEW.review_artifact_truncated := NEW.review_bundle_truncated; END IF;
    ELSE
        IF NEW.review_bundle_key IS DISTINCT FROM OLD.review_bundle_key THEN NEW.review_artifact_key := NEW.review_bundle_key;
        ELSIF NEW.review_artifact_key IS DISTINCT FROM OLD.review_artifact_key THEN NEW.review_bundle_key := NEW.review_artifact_key; END IF;
        IF NEW.review_bundle_version IS DISTINCT FROM OLD.review_bundle_version THEN NEW.review_artifact_version := NEW.review_bundle_version;
        ELSIF NEW.review_artifact_version IS DISTINCT FROM OLD.review_artifact_version THEN NEW.review_bundle_version := NEW.review_artifact_version; END IF;
        IF NEW.review_bundle_compressed_bytes IS DISTINCT FROM OLD.review_bundle_compressed_bytes THEN NEW.review_artifact_compressed_bytes := NEW.review_bundle_compressed_bytes;
        ELSIF NEW.review_artifact_compressed_bytes IS DISTINCT FROM OLD.review_artifact_compressed_bytes THEN NEW.review_bundle_compressed_bytes := NEW.review_artifact_compressed_bytes; END IF;
        IF NEW.review_bundle_uncompressed_bytes IS DISTINCT FROM OLD.review_bundle_uncompressed_bytes THEN NEW.review_artifact_uncompressed_bytes := NEW.review_bundle_uncompressed_bytes;
        ELSIF NEW.review_artifact_uncompressed_bytes IS DISTINCT FROM OLD.review_artifact_uncompressed_bytes THEN NEW.review_bundle_uncompressed_bytes := NEW.review_artifact_uncompressed_bytes; END IF;
        IF NEW.review_bundle_file_count IS DISTINCT FROM OLD.review_bundle_file_count THEN NEW.review_artifact_file_count := NEW.review_bundle_file_count;
        ELSIF NEW.review_artifact_file_count IS DISTINCT FROM OLD.review_artifact_file_count THEN NEW.review_bundle_file_count := NEW.review_artifact_file_count; END IF;
        IF NEW.review_bundle_skipped_count IS DISTINCT FROM OLD.review_bundle_skipped_count THEN NEW.review_artifact_skipped_count := NEW.review_bundle_skipped_count;
        ELSIF NEW.review_artifact_skipped_count IS DISTINCT FROM OLD.review_artifact_skipped_count THEN NEW.review_bundle_skipped_count := NEW.review_artifact_skipped_count; END IF;
        IF NEW.review_bundle_truncated IS DISTINCT FROM OLD.review_bundle_truncated THEN NEW.review_artifact_truncated := NEW.review_bundle_truncated;
        ELSIF NEW.review_artifact_truncated IS DISTINCT FROM OLD.review_artifact_truncated THEN NEW.review_bundle_truncated := NEW.review_artifact_truncated; END IF;
    END IF;
    RETURN NEW;
END;
$$;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'code_review_session_metadata' AND column_name = 'prompt_artifact_key') THEN
        DROP TRIGGER IF EXISTS trg_sync_code_review_prompt_key ON code_review_session_metadata;
        CREATE TRIGGER trg_sync_code_review_prompt_key
            BEFORE INSERT OR UPDATE ON code_review_session_metadata
            FOR EACH ROW EXECUTE FUNCTION sync_code_review_prompt_key_compatibility();
    END IF;

    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'preview_verification_runs' AND column_name = 'artifacts') THEN
        DROP TRIGGER IF EXISTS trg_sync_preview_verification_captures ON preview_verification_runs;
        CREATE TRIGGER trg_sync_preview_verification_captures
            BEFORE INSERT OR UPDATE ON preview_verification_runs
            FOR EACH ROW EXECUTE FUNCTION sync_preview_verification_capture_compatibility();
    END IF;

    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'session_diff_snapshots' AND column_name = 'review_artifact_key') THEN
        DROP TRIGGER IF EXISTS trg_sync_session_review_bundle ON session_diff_snapshots;
        CREATE TRIGGER trg_sync_session_review_bundle
            BEFORE INSERT OR UPDATE ON session_diff_snapshots
            FOR EACH ROW EXECUTE FUNCTION sync_session_review_bundle_compatibility();
    END IF;
END $$;

-- Preserve both names inside historical step objects during the compatibility
-- window. The application can then serve either client generation from raw JSON.
UPDATE preview_verification_runs
SET steps = COALESCE((
    SELECT jsonb_agg(
        CASE
            WHEN step ? 'artifact' AND NOT step ? 'capture'
                THEN step || jsonb_build_object('capture', step->'artifact')
            WHEN step ? 'capture' AND NOT step ? 'artifact'
                THEN step || jsonb_build_object('artifact', step->'capture')
            ELSE step
        END
        ORDER BY ordinal
    )
    FROM jsonb_array_elements(steps) WITH ORDINALITY AS items(step, ordinal)
), '[]'::jsonb)
WHERE EXISTS (
    SELECT 1 FROM jsonb_array_elements(steps) AS items(step)
    WHERE (step ? 'artifact') <> (step ? 'capture')
);

-- Normalize cache rows, schema defaults, embedded metadata, and late writes
-- from a draining worker generation.
ALTER TABLE preview_dependency_cache
    ALTER COLUMN cache_kind SET DEFAULT 'install_output';
ALTER TABLE preview_dependency_cache_locations
    ALTER COLUMN cache_kind SET DEFAULT 'install_output';

UPDATE preview_dependency_cache
SET cache_kind = CASE cache_kind
        WHEN 'install_artifact' THEN 'install_output'
        WHEN 'build_artifact' THEN 'build_output'
        ELSE cache_kind
    END,
    metadata = CASE
        WHEN metadata->>'kind' = 'install_artifact' THEN jsonb_set(metadata, '{kind}', '"install_output"'::jsonb)
        WHEN metadata->>'kind' = 'build_artifact' THEN jsonb_set(metadata, '{kind}', '"build_output"'::jsonb)
        ELSE metadata
    END
WHERE cache_kind IN ('install_artifact', 'build_artifact')
   OR metadata->>'kind' IN ('install_artifact', 'build_artifact');

UPDATE preview_dependency_cache_locations
SET cache_kind = CASE cache_kind
    WHEN 'install_artifact' THEN 'install_output'
    WHEN 'build_artifact' THEN 'build_output'
    ELSE cache_kind
END
WHERE cache_kind IN ('install_artifact', 'build_artifact');

CREATE OR REPLACE FUNCTION normalize_preview_cache_kind_compatibility()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.cache_kind := CASE NEW.cache_kind
        WHEN 'install_artifact' THEN 'install_output'
        WHEN 'build_artifact' THEN 'build_output'
        ELSE NEW.cache_kind
    END;
    IF TG_TABLE_NAME = 'preview_dependency_cache' THEN
        NEW.metadata := CASE
            WHEN NEW.metadata->>'kind' = 'install_artifact' THEN jsonb_set(NEW.metadata, '{kind}', '"install_output"'::jsonb)
            WHEN NEW.metadata->>'kind' = 'build_artifact' THEN jsonb_set(NEW.metadata, '{kind}', '"build_output"'::jsonb)
            ELSE NEW.metadata
        END;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_normalize_preview_dependency_cache_kind ON preview_dependency_cache;
CREATE TRIGGER trg_normalize_preview_dependency_cache_kind
    BEFORE INSERT OR UPDATE ON preview_dependency_cache
    FOR EACH ROW EXECUTE FUNCTION normalize_preview_cache_kind_compatibility();

DROP TRIGGER IF EXISTS trg_normalize_preview_dependency_cache_location_kind ON preview_dependency_cache_locations;
CREATE TRIGGER trg_normalize_preview_dependency_cache_location_kind
    BEFORE INSERT OR UPDATE ON preview_dependency_cache_locations
    FOR EACH ROW EXECUTE FUNCTION normalize_preview_cache_kind_compatibility();

UPDATE code_review_agent_results
SET structured_result = (structured_result - 'prompt_artifact_key' - 'raw_artifact_key')
    || CASE
        WHEN structured_result ? 'prompt_artifact_key'
        THEN jsonb_build_object('prompt_record_key', structured_result->'prompt_artifact_key')
        ELSE '{}'::jsonb
    END
    || CASE
        WHEN structured_result ? 'raw_artifact_key'
        THEN jsonb_build_object('raw_record_key', structured_result->'raw_artifact_key')
        ELSE '{}'::jsonb
    END
WHERE structured_result ? 'prompt_artifact_key'
   OR structured_result ? 'raw_artifact_key';

-- Fresh databases never had a legacy process generation, so do not leave
-- dormant compatibility functions or cache triggers behind there.
DO $$
BEGIN
    IF to_regclass('code_review_prompt_artifacts') IS NULL THEN
        DROP FUNCTION IF EXISTS sync_code_review_prompt_record_compatibility();
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'code_review_session_metadata'
          AND column_name = 'prompt_artifact_key'
    ) THEN
        DROP FUNCTION IF EXISTS sync_code_review_prompt_key_compatibility();
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'preview_verification_runs'
          AND column_name = 'artifacts'
    ) THEN
        DROP FUNCTION IF EXISTS sync_preview_verification_capture_compatibility();
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'session_diff_snapshots'
          AND column_name = 'review_artifact_key'
    ) THEN
        DROP FUNCTION IF EXISTS sync_session_review_bundle_compatibility();
    END IF;
    IF to_regclass('code_review_prompt_artifacts') IS NULL
       AND NOT EXISTS (
           SELECT 1 FROM information_schema.columns
           WHERE table_schema = current_schema() AND table_name = 'session_diff_snapshots'
             AND column_name = 'review_artifact_key'
       ) THEN
        DROP TRIGGER IF EXISTS trg_normalize_preview_dependency_cache_kind ON preview_dependency_cache;
        DROP TRIGGER IF EXISTS trg_normalize_preview_dependency_cache_location_kind ON preview_dependency_cache_locations;
        DROP FUNCTION IF EXISTS normalize_preview_cache_kind_compatibility();
    END IF;
END $$;
