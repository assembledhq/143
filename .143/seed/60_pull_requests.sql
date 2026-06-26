-- Pull request row backing the pr_preview_state below. Any UI that joins
-- pr_preview_state to pull_requests (by org_id + github_repo + pr_number)
-- needs a pull_requests row to render a working link — without this, the
-- PR preview panel renders a broken "PR #42" link in the dogfood.
-- Note: github_pr_url points at a real PR on the public repo so the link
-- resolves; nothing in the dogfood actually calls the GitHub API.
INSERT INTO pull_requests (
  id, session_id, org_id, github_pr_number, github_pr_url, github_repo,
  title, body, status, review_status, authored_by, ci_status, head_sha,
  base_sha, merge_state, has_conflicts, failing_test_count,
  needs_agent_action, github_state_synced_at, health_version, head_ref,
  merge_when_ready_state, created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000501'::uuid,
  '00000000-0000-4000-a000-000000000300'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  42,
  'https://github.com/assembledhq/143/pull/42',
  'assembledhq/143',
  'Ship PR preview auto-teardown',
  'Wire preview teardown into pull_request.closed / merged.',
  'open',
  'pending',
  'app',
  'failure',
  '2222222222222222222222222222222222222222',
  '1111111111111111111111111111111111111111',
  'blocked',
  false,
  1,
  true,
  now() - interval '12 minutes',
  1,
  'feat/preview-teardown',
  'off',
  now() - interval '30 minutes',
  now() - interval '2 minutes'
)
ON CONFLICT (id) DO UPDATE
SET ci_status = EXCLUDED.ci_status,
    head_sha = EXCLUDED.head_sha,
    base_sha = EXCLUDED.base_sha,
    merge_state = EXCLUDED.merge_state,
    has_conflicts = EXCLUDED.has_conflicts,
    failing_test_count = EXCLUDED.failing_test_count,
    needs_agent_action = EXCLUDED.needs_agent_action,
    github_state_synced_at = EXCLUDED.github_state_synced_at,
    health_version = EXCLUDED.health_version,
    head_ref = EXCLUDED.head_ref,
    updated_at = EXCLUDED.updated_at;

INSERT INTO pull_request_health_snapshots (
  pull_request_id, org_id, version, head_sha, base_sha, summary_json,
  conflict_payload, failing_tests_payload, payload_size_bytes,
  enrichment_status, enriched_at, created_at
)
VALUES (
  '00000000-0000-4000-a000-000000000501'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  1,
  '2222222222222222222222222222222222222222',
  '1111111111111111111111111111111111111111',
  '{"merge_state":"blocked","has_conflicts":false,"failing_test_count":1,"needs_agent_action":true,"checks_confirmed":true,"checks":[{"name":"frontend typecheck","category":"test","status":"failed","provider":"seeded-ci","summary":"Synthetic check: one generated type narrowed too far."},{"name":"go test","category":"test","status":"passed","provider":"seeded-ci"},{"name":"gosec","category":"lint","status":"passed","provider":"seeded-ci"}]}'::jsonb,
  NULL,
  '{"checks":[{"name":"frontend typecheck","failure":"Synthetic TypeScript mismatch in preview teardown copy."}]}'::jsonb,
  128,
  'ready',
  now() - interval '11 minutes',
  now() - interval '12 minutes'
)
ON CONFLICT (pull_request_id, version) DO UPDATE
SET summary_json = EXCLUDED.summary_json,
    failing_tests_payload = EXCLUDED.failing_tests_payload,
    payload_size_bytes = EXCLUDED.payload_size_bytes,
    enrichment_status = EXCLUDED.enrichment_status,
    enriched_at = EXCLUDED.enriched_at;

INSERT INTO pull_request_health_current (
  pull_request_id, org_id, version, head_sha, base_sha, summary_json,
  summary_preview_json, enrichment_status, enriched_at, created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000501'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  1,
  '2222222222222222222222222222222222222222',
  '1111111111111111111111111111111111111111',
  '{"merge_state":"blocked","has_conflicts":false,"failing_test_count":1,"needs_agent_action":true,"checks_confirmed":true,"checks":[{"name":"frontend typecheck","category":"test","status":"failed","provider":"seeded-ci","summary":"Synthetic check: one generated type narrowed too far."},{"name":"go test","category":"test","status":"passed","provider":"seeded-ci"},{"name":"gosec","category":"lint","status":"passed","provider":"seeded-ci"}]}'::jsonb,
  '{"summary":"1 failing check needs agent action","checks":[{"name":"frontend typecheck","status":"failed"},{"name":"go test","status":"passed"},{"name":"gosec","status":"passed"}]}'::jsonb,
  'ready',
  now() - interval '11 minutes',
  now() - interval '12 minutes',
  now() - interval '2 minutes'
)
ON CONFLICT (pull_request_id) DO UPDATE
SET version = EXCLUDED.version,
    head_sha = EXCLUDED.head_sha,
    base_sha = EXCLUDED.base_sha,
    summary_json = EXCLUDED.summary_json,
    summary_preview_json = EXCLUDED.summary_preview_json,
    enrichment_status = EXCLUDED.enrichment_status,
    enriched_at = EXCLUDED.enriched_at,
    updated_at = EXCLUDED.updated_at;

-- PR-preview tracking for the "pr_created" session, backed by the seeded
-- pull_requests row above.
INSERT INTO pr_preview_state (
  id, org_id, repo_id, pr_number, github_comment_id, last_preview_instance_id,
  last_screenshot_blob_path, last_visual_diff_blob_path, base_snapshot_key,
  status, last_surface_sync_sha, last_surface_sync_at, created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000500'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000100'::uuid,
  42,
  424242,
  '00000000-0000-4000-a000-000000000400'::uuid,
  'seeded/pr-previews/42/screenshot.png',
  'seeded/pr-previews/42/visual-diff.json',
  'seeded/pr-previews/42/base',
  'running',
  '2222222222222222222222222222222222222222',
  now() - interval '10 minutes',
  now() - interval '30 minutes',
  now() - interval '2 minutes'
)
ON CONFLICT (id) DO UPDATE
SET github_comment_id = EXCLUDED.github_comment_id,
    last_preview_instance_id = EXCLUDED.last_preview_instance_id,
    last_screenshot_blob_path = EXCLUDED.last_screenshot_blob_path,
    last_visual_diff_blob_path = EXCLUDED.last_visual_diff_blob_path,
    base_snapshot_key = EXCLUDED.base_snapshot_key,
    status = EXCLUDED.status,
    last_surface_sync_sha = EXCLUDED.last_surface_sync_sha,
    last_surface_sync_at = EXCLUDED.last_surface_sync_at,
    updated_at = EXCLUDED.updated_at;
