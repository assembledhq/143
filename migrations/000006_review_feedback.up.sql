-- Phase 6: Review Feedback Loop
-- Two new tables: review_comments and review_patterns

CREATE TABLE review_comments (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    pull_request_id   uuid NOT NULL REFERENCES pull_requests(id),
    org_id            uuid NOT NULL REFERENCES organizations(id),
    github_comment_id bigint NOT NULL,
    reviewer          text NOT NULL,
    body              text NOT NULL,
    diff_path         text,
    diff_position     int,
    filter_status     text NOT NULL DEFAULT 'pending',       -- pending, filtered_structural,
                                                             -- filtered_not_actionable, accepted
    category          text,                  -- classified category (null until LLM pass)
    actionable        boolean DEFAULT true,
    generalizable     boolean DEFAULT false,
    generalized_rule  text,
    summary           text,
    applied           boolean DEFAULT false,  -- was this feedback applied via revision run?
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_review_comments_pr ON review_comments (pull_request_id);
CREATE INDEX idx_review_comments_org_category ON review_comments (org_id, category);
CREATE INDEX idx_review_comments_filter ON review_comments (org_id, filter_status);
CREATE UNIQUE INDEX idx_review_comments_github ON review_comments (pull_request_id, github_comment_id);

CREATE TABLE review_patterns (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             uuid NOT NULL REFERENCES organizations(id),
    repo               text NOT NULL,
    rule               text NOT NULL,
    category           text NOT NULL,
    source_comment_ids uuid[] NOT NULL DEFAULT '{}',
    occurrence_count   int NOT NULL DEFAULT 1,
    status             text NOT NULL DEFAULT 'candidate', -- candidate, active, dismissed
    manually_curated   boolean NOT NULL DEFAULT false,
    active             boolean NOT NULL DEFAULT true,      -- insert-only versioning flag
    created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_review_patterns_repo ON review_patterns (org_id, repo, status) WHERE active = true;
CREATE UNIQUE INDEX idx_review_patterns_dedup ON review_patterns (org_id, repo, rule) WHERE active = true;
