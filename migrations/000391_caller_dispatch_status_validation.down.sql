-- PostgreSQL does not unvalidate constraints. The higher readiness down refuses
-- retained history before this boundary; the lower allowance down rechecks it.
SELECT 1;
