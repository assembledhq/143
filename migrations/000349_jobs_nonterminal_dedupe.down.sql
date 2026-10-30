-- Ordered rollback first restores the six-status constraint in migration 352.
DROP INDEX CONCURRENTLY idx_jobs_nonterminal_dedupe;
