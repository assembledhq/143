-- Exact pending session-wide question lookup for non-answer admission.
-- Ordinary admission must not infer answer authority from aggregate status.
-- Match only these expression keys in the scoped EXISTS branch; base-column
-- tenant/status MCV estimates are not reliable evidence of bounded probe work.
CREATE INDEX CONCURRENTLY idx_admission_pending_question_lookup
    ON session_questions
    ((CASE WHEN status='pending' THEN org_id END),(CASE WHEN status='pending' THEN session_id END))
    WHERE (CASE WHEN status='pending' THEN org_id END) IS NOT NULL;
