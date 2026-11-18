-- Exact active repair-reservation discovery for ordinary admission. This index
-- supplies no repair scope, ownership or lifecycle authority.
-- Couple these expression keys to the production lookup: separate base-column
-- status/tenant qualifiers can reopen skew-sensitive custom-plan estimates.
CREATE INDEX CONCURRENTLY idx_admission_active_repair_lookup
    ON pull_request_repair_runs
    ((CASE WHEN active THEN org_id END),(CASE WHEN active THEN session_id END),id)
    WHERE (CASE WHEN active THEN org_id END) IS NOT NULL;
