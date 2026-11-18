-- Remove dependent foreign keys before rolling back this prerequisite; no CASCADE.
DROP INDEX CONCURRENTLY idx_session_human_input_requests_org_session_id;
