-- Add diff_stats and diff_history columns to sessions
ALTER TABLE sessions ADD COLUMN diff_stats jsonb;
ALTER TABLE sessions ADD COLUMN diff_history jsonb DEFAULT '[]';

-- Create session_review_comments table for inline code review comments
CREATE TABLE session_review_comments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    org_id UUID NOT NULL REFERENCES organizations(id),
    user_id UUID NOT NULL REFERENCES users(id),
    file_path TEXT NOT NULL,
    line_number INTEGER NOT NULL,
    diff_side TEXT NOT NULL DEFAULT 'new' CHECK (diff_side IN ('old', 'new')),
    body TEXT NOT NULL,
    resolved BOOLEAN NOT NULL DEFAULT false,
    resolved_at TIMESTAMPTZ,
    resolved_by_pass INTEGER,
    pass_number INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_session_review_comments_session ON session_review_comments(session_id);
CREATE INDEX idx_session_review_comments_session_file ON session_review_comments(session_id, file_path);
