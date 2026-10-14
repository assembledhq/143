-- Controller polls must not scan every failed continuation in the tenant.
-- Keep the default-off probe and committed intent recovery bounded by the
-- session and metadata identities, including after activation is disabled.
-- A failed concurrent build requires inspecting and dropping its invalid
-- index before repairing the migration marker under operational authorization.
CREATE INDEX CONCURRENTLY idx_jobs_review_preparation_intents
    ON jobs (org_id, (payload->>'session_id'),
             (payload->'terminal_preparation_intent'->>'metadata_id'),
             updated_at DESC, id)
    WHERE job_type = 'continue_session'
      AND status = 'dead_letter'
      AND payload ? 'terminal_preparation_intent'
      AND NOT (payload ? 'terminal_preparation_intent_consumed');
