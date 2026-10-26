-- Index the current eligible tenant/job subset, including parked consumers.
-- Insert-only terminal rows never enter this index; dead eligible versions from
-- status/lease churn still need vacuum-lag and maintenance measurements.
-- PostgreSQL 17's planner does not use partial-index expression statistics.
-- Optimizer hooks, extended statistics and future versions need revalidation.
CREATE INDEX CONCURRENTLY idx_jobs_lifecycle_nonterminal_input_consumers
    ON jobs((CASE WHEN job_type IN ('run_agent','continue_session')
      AND status IN ('pending', 'running', 'awaiting_start_control') THEN org_id END),id)
    WHERE (CASE WHEN job_type IN ('run_agent','continue_session')
      AND status IN ('pending', 'running', 'awaiting_start_control') THEN org_id END) IS NOT NULL;
