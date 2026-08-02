-- Cost-aware admission telemetry for preview_dependency_cache.
--
-- This migration originally shipped as 000274 before that slot turned out to be
-- taken by 000274_code_review_pull_request_history_index on main. Some databases
-- (dev boxes, and session preview environments that tracked this branch) applied
-- the schema under the former version, so every operation here must tolerate it
-- already being present — see 000249_preview_resource_samples.up.sql, which was
-- renumbered for the same reason. Postgres has no ADD CONSTRAINT IF NOT EXISTS,
-- so the checks are dropped first and re-added, exactly as 000272 does.
--
-- Note on locking: the seven new columns are NOT NULL DEFAULT, which is
-- metadata-only on Postgres 11+ (no table rewrite). The CHECK constraints are
-- added NOT VALID so their validation scans do not run while the ACCESS
-- EXCLUSIVE catalog lock is held; migration 000276 validates them under a SHARE
-- UPDATE EXCLUSIVE lock that concurrent writers can tolerate.
--
-- Deploy check: preview_dependency_cache holds one row per (org, repo, kind,
-- cache key) — a narrow metadata table with no per-launch fan-out. Confirm its
-- row count before rolling this out.
SET LOCAL lock_timeout = '5s';

ALTER TABLE preview_dependency_cache
    ADD COLUMN IF NOT EXISTS restore_attempt_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS restore_success_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS restore_total_duration_ms bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS producer_duration_ms bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS producer_benefit_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS producer_benefit_total_ms bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_restore_at timestamptz;

ALTER TABLE preview_dependency_cache
    DROP CONSTRAINT IF EXISTS preview_dependency_cache_restore_attempt_count_nonnegative,
    DROP CONSTRAINT IF EXISTS preview_dependency_cache_restore_success_count_nonnegative,
    DROP CONSTRAINT IF EXISTS preview_dependency_cache_restore_total_duration_nonnegative,
    DROP CONSTRAINT IF EXISTS preview_dependency_cache_producer_duration_nonnegative,
    DROP CONSTRAINT IF EXISTS preview_dependency_cache_producer_benefit_count_nonnegative,
    DROP CONSTRAINT IF EXISTS preview_dependency_cache_producer_benefit_total_nonnegative,
    DROP CONSTRAINT IF EXISTS preview_dependency_cache_restore_success_lte_attempts;

ALTER TABLE preview_dependency_cache
    ADD CONSTRAINT preview_dependency_cache_restore_attempt_count_nonnegative
        CHECK (restore_attempt_count >= 0) NOT VALID,
    ADD CONSTRAINT preview_dependency_cache_restore_success_count_nonnegative
        CHECK (restore_success_count >= 0) NOT VALID,
    ADD CONSTRAINT preview_dependency_cache_restore_total_duration_nonnegative
        CHECK (restore_total_duration_ms >= 0) NOT VALID,
    ADD CONSTRAINT preview_dependency_cache_producer_duration_nonnegative
        CHECK (producer_duration_ms >= 0) NOT VALID,
    ADD CONSTRAINT preview_dependency_cache_producer_benefit_count_nonnegative
        CHECK (producer_benefit_count >= 0) NOT VALID,
    ADD CONSTRAINT preview_dependency_cache_producer_benefit_total_nonnegative
        CHECK (producer_benefit_total_ms >= 0) NOT VALID,
    ADD CONSTRAINT preview_dependency_cache_restore_success_lte_attempts
        CHECK (restore_success_count <= restore_attempt_count) NOT VALID;
