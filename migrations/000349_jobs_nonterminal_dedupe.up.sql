-- Standalone concurrent build. Keep the old index during the compatible floor.
-- Build failure or an invalid leftover must stop migration, never be ignored.
CREATE UNIQUE INDEX CONCURRENTLY idx_jobs_nonterminal_dedupe
    ON jobs (queue, dedupe_key)
    WHERE dedupe_key IS NOT NULL
      AND status IN ('pending', 'running', 'awaiting_start_control');
