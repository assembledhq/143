-- Validates the cost-admission CHECK constraints installed NOT VALID by 000275.
-- The scans run here under SHARE UPDATE EXCLUSIVE, which concurrent readers and
-- writers of preview_dependency_cache tolerate.
SET LOCAL lock_timeout = '5s';

ALTER TABLE preview_dependency_cache
    VALIDATE CONSTRAINT preview_dependency_cache_restore_attempt_count_nonnegative;
ALTER TABLE preview_dependency_cache
    VALIDATE CONSTRAINT preview_dependency_cache_restore_success_count_nonnegative;
ALTER TABLE preview_dependency_cache
    VALIDATE CONSTRAINT preview_dependency_cache_restore_total_duration_nonnegative;
ALTER TABLE preview_dependency_cache
    VALIDATE CONSTRAINT preview_dependency_cache_producer_duration_nonnegative;
ALTER TABLE preview_dependency_cache
    VALIDATE CONSTRAINT preview_dependency_cache_producer_benefit_count_nonnegative;
ALTER TABLE preview_dependency_cache
    VALIDATE CONSTRAINT preview_dependency_cache_producer_benefit_total_nonnegative;
ALTER TABLE preview_dependency_cache
    VALIDATE CONSTRAINT preview_dependency_cache_restore_success_lte_attempts;
