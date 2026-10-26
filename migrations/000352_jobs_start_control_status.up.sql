SET LOCAL lock_timeout = '5s';
-- Never permit a parked row before its broader global key is valid and both
-- new checks are validated. This installs representation, not a parking writer.
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_index
                   WHERE indexrelid='idx_jobs_nonterminal_dedupe'::regclass
                     AND indisvalid AND indisunique AND indisready)
       OR 2 <> (SELECT count(*) FROM pg_constraint
                WHERE conrelid='jobs'::regclass
                  AND conname IN ('chk_jobs_status','chk_jobs_parked_without_lease')
                  AND convalidated) THEN
        RAISE EXCEPTION 'lifecycle parked status prerequisites required' USING ERRCODE='55000';
    END IF;
END $$;
ALTER TABLE jobs DROP CONSTRAINT chk_jobs_status_legacy;
