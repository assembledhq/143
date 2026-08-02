ALTER TABLE preview_dependency_cache
    ADD COLUMN restore_attempt_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN restore_success_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN restore_total_duration_ms bigint NOT NULL DEFAULT 0,
    ADD COLUMN producer_duration_ms bigint NOT NULL DEFAULT 0,
    ADD COLUMN producer_benefit_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN producer_benefit_total_ms bigint NOT NULL DEFAULT 0,
    ADD COLUMN last_restore_at timestamptz;

ALTER TABLE preview_dependency_cache
    ADD CONSTRAINT preview_dependency_cache_restore_attempt_count_nonnegative CHECK (restore_attempt_count >= 0),
    ADD CONSTRAINT preview_dependency_cache_restore_success_count_nonnegative CHECK (restore_success_count >= 0),
    ADD CONSTRAINT preview_dependency_cache_restore_total_duration_nonnegative CHECK (restore_total_duration_ms >= 0),
    ADD CONSTRAINT preview_dependency_cache_producer_duration_nonnegative CHECK (producer_duration_ms >= 0),
    ADD CONSTRAINT preview_dependency_cache_producer_benefit_count_nonnegative CHECK (producer_benefit_count >= 0),
    ADD CONSTRAINT preview_dependency_cache_producer_benefit_total_nonnegative CHECK (producer_benefit_total_ms >= 0),
    ADD CONSTRAINT preview_dependency_cache_restore_success_lte_attempts CHECK (restore_success_count <= restore_attempt_count);
