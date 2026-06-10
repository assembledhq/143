DO $$
BEGIN
    IF to_regclass('public.eval_bootstrap_candidates') IS NOT NULL THEN
        DELETE FROM eval_bootstrap_candidates
        WHERE created_by_tool = 'legacy_candidates_backfill';
    END IF;
END $$;
