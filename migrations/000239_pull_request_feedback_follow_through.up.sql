ALTER TABLE pull_requests
  ADD COLUMN feedback_monitoring text NOT NULL DEFAULT 'inherit',
  ADD COLUMN feedback_bot_epoch bigint NOT NULL DEFAULT 0,
  ADD COLUMN feedback_bot_cycles_in_epoch integer NOT NULL DEFAULT 0,
  ADD CONSTRAINT chk_pr_feedback_monitoring CHECK (feedback_monitoring IN ('inherit','enabled','disabled')),
  ADD CONSTRAINT chk_pr_feedback_bot_cycles CHECK (feedback_bot_cycles_in_epoch >= 0);

CREATE TABLE pull_request_feedback_batches (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES organizations(id),
  pull_request_id uuid NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
  session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  thread_id uuid REFERENCES session_threads(id) ON DELETE SET NULL,
  status text NOT NULL CHECK (status IN ('collecting','queued','running','pushing','responding','completed','needs_attention','cancelled')),
  source_kind text NOT NULL CHECK (source_kind IN ('human_or_mixed','bot_only')),
  bot_feedback_epoch bigint,
  expected_head_sha text NOT NULL,
  result_head_sha text,
  workspace_mode text CHECK (workspace_mode IS NULL OR workspace_mode IN ('snapshot_continuation','pr_head_reconstruction')),
  feedback_snapshot jsonb NOT NULL DEFAULT '[]',
  debounce_until timestamptz NOT NULL,
  max_collect_until timestamptz NOT NULL,
  attempt_count integer NOT NULL DEFAULT 0,
  result_summary text,
  error_code text,
  error_detail text,
  started_at timestamptz,
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_pr_feedback_one_active ON pull_request_feedback_batches (pull_request_id)
  WHERE status IN ('collecting','queued','running','pushing','responding');
CREATE INDEX idx_pr_feedback_batches_org_pr ON pull_request_feedback_batches (org_id,pull_request_id,created_at DESC);

CREATE TABLE pull_request_feedback_items (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES organizations(id),
  pull_request_id uuid NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
  batch_id uuid REFERENCES pull_request_feedback_batches(id) ON DELETE SET NULL,
  surface text NOT NULL CHECK (surface IN ('issue_comment','review_body','review_comment')),
  provider_object_id bigint NOT NULL,
  github_delivery_id text,
  github_review_id bigint,
  github_thread_root_comment_id bigint,
  in_reply_to_comment_id bigint,
  github_app_id bigint,
  github_app_slug text,
  author_login text NOT NULL,
  author_type text NOT NULL CHECK (author_type IN ('User','Bot','Mannequin','Organization','Unknown')),
  author_association text NOT NULL DEFAULT '',
  bot_eligibility_source text NOT NULL DEFAULT '' CHECK (bot_eligibility_source IN ('','private_repository_all','github_first_party','repository_installed_app','explicit_allowlist')),
  body text NOT NULL,
  body_hash text NOT NULL,
  processed_body_hash text,
  provider_finding_key text,
  finding_fingerprint text,
  automatic_attempt_count integer NOT NULL DEFAULT 0,
  path text,
  line integer,
  side text,
  diff_hunk text,
  comment_commit_sha text,
  observed_head_sha text NOT NULL DEFAULT '',
  intent text NOT NULL DEFAULT 'unknown' CHECK (intent IN ('unknown','change_request','question','mixed','acknowledgement','unsafe_or_unsupported')),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ignored','claimed','running','responded','needs_attention','cancelled')),
  ignore_reason text,
  github_response_comment_id bigint,
  response_body text,
  response_commit_sha text,
  provider_created_at timestamptz,
  provider_updated_at timestamptz,
  received_at timestamptz NOT NULL DEFAULT now(),
  processed_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (pull_request_id,surface,provider_object_id)
);
CREATE INDEX idx_pr_feedback_items_pending ON pull_request_feedback_items (org_id,pull_request_id,received_at) WHERE status='pending';
CREATE INDEX idx_pr_feedback_items_batch ON pull_request_feedback_items (org_id,batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX idx_pr_feedback_bot_fingerprint ON pull_request_feedback_items (org_id,pull_request_id,finding_fingerprint,observed_head_sha)
  WHERE author_type='Bot' AND finding_fingerprint IS NOT NULL;

ALTER TABLE review_comments ADD COLUMN source_feedback_item_id uuid
  REFERENCES pull_request_feedback_items(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX idx_review_comments_feedback_item ON review_comments (source_feedback_item_id) WHERE source_feedback_item_id IS NOT NULL;
