-- Migration 000280 installed a shared trigger on tables with different key
-- columns. PostgreSQL resolves record fields before evaluating an AND guard,
-- so each table's fields must be referenced in a separate procedural branch.
DO $migration$
BEGIN
    -- Fresh installations have only prompt_records and need no compatibility
    -- function. Repair only installations retaining both worker generations.
    IF to_regclass('code_review_prompt_artifacts') IS NULL
       OR to_regclass('code_review_prompt_records') IS NULL THEN
        RETURN;
    END IF;

    CREATE OR REPLACE FUNCTION sync_code_review_prompt_record_compatibility()
    RETURNS trigger
    LANGUAGE plpgsql
    AS $function$
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

        -- Retire a rekeyed row's previous counterpart before the insert below
        -- would collide with that counterpart's primary key.
        IF TG_OP = 'UPDATE' THEN
            IF TG_TABLE_NAME = 'code_review_prompt_artifacts' THEN
                IF NEW.artifact_key IS DISTINCT FROM OLD.artifact_key THEN
                    DELETE FROM code_review_prompt_records
                    WHERE org_id = OLD.org_id AND record_key = OLD.artifact_key;
                END IF;
            ELSE
                IF NEW.record_key IS DISTINCT FROM OLD.record_key THEN
                    DELETE FROM code_review_prompt_artifacts
                    WHERE org_id = OLD.org_id AND artifact_key = OLD.record_key;
                END IF;
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
    $function$;
END;
$migration$;
