-- Keep request identity tenant/session scoped even for legacy threadless requests.
-- Canonical writers must separately verify exact nullable request-lane identity.
-- Standalone concurrent build; a leftover invalid index must fail visibly.
CREATE UNIQUE INDEX CONCURRENTLY idx_session_human_input_requests_org_session_id
    ON session_human_input_requests (org_id, session_id, id);
