-- All-status reverse evidence lookup; retained uncertainty survives lease/status
-- changes. Standalone concurrent build keeps old writers live during the scan.
CREATE INDEX CONCURRENTLY idx_recheck_consumer_session
    ON code_review_recheck_dispatches(org_id,session_id,assessment_id);
