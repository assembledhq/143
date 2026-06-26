-- Remove older generated demo rows with the same natural preview identity before
-- inserting fixed IDs. These rows are seed-owned by repository/PR/branch and can
-- otherwise collide with the preview natural unique indexes on long-lived demo DBs.
DELETE FROM preview_links
WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
  AND id <> '00000000-0000-4000-a000-000000000432'::uuid
  AND link_type = 'pull_request'
  AND (
    (repository_id = '00000000-0000-4000-a000-000000000100'::uuid AND pr_number = 42)
    OR slug = 'assembledhq-143-42'
  );

DELETE FROM preview_targets
WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
  AND id <> '00000000-0000-4000-a000-000000000431'::uuid
  AND repository_id = '00000000-0000-4000-a000-000000000100'::uuid
  AND branch = 'feat/preview-teardown'
  AND commit_sha = '2222222222222222222222222222222222222222'
  AND preview_config_name = 'default';

DELETE FROM preview_groups
WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
  AND id <> '00000000-0000-4000-a000-000000000430'::uuid
  AND repository_id = '00000000-0000-4000-a000-000000000100'::uuid
  AND group_kind = 'pull_request'
  AND branch = 'feat/preview-teardown'
  AND preview_config_name = 'default'
  AND COALESCE(pull_request_number, 0) = 42
  AND source_type = 'pull_request'
  AND source_id = '42'
  AND pinned = false;

INSERT INTO preview_groups (
  id, org_id, repository_id, group_kind, branch, preview_config_name,
  pull_request_number, source_type, source_id, source_url, current_target_id,
  latest_commit_sha, current_status, pinned, created_by_user_id, created_at,
  last_activity_at
)
VALUES (
  '00000000-0000-4000-a000-000000000430'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000100'::uuid,
  'pull_request',
  'feat/preview-teardown',
  'default',
  42,
  'pull_request',
  '42',
  'https://github.com/assembledhq/143/pull/42',
  NULL,
  '2222222222222222222222222222222222222222',
  'ready',
  false,
  '00000000-0000-4000-a000-000000000002'::uuid,
  now() - interval '32 minutes',
  now() - interval '2 minutes'
)
ON CONFLICT (id) DO UPDATE
SET repository_id = EXCLUDED.repository_id,
    group_kind = EXCLUDED.group_kind,
    branch = EXCLUDED.branch,
    preview_config_name = EXCLUDED.preview_config_name,
    pull_request_number = EXCLUDED.pull_request_number,
    source_type = EXCLUDED.source_type,
    source_id = EXCLUDED.source_id,
    source_url = EXCLUDED.source_url,
    current_target_id = EXCLUDED.current_target_id,
    latest_commit_sha = EXCLUDED.latest_commit_sha,
    current_status = EXCLUDED.current_status,
    pinned = EXCLUDED.pinned,
    created_by_user_id = EXCLUDED.created_by_user_id,
    last_activity_at = EXCLUDED.last_activity_at;

INSERT INTO preview_targets (
  id, org_id, repository_id, branch, commit_sha, preview_config_name,
  resolved_config_digest, source_type, source_id, source_url,
  created_by_user_id, created_at, request_id, last_snapshot_key,
  preview_group_id
)
VALUES (
  '00000000-0000-4000-a000-000000000431'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000100'::uuid,
  'feat/preview-teardown',
  '2222222222222222222222222222222222222222',
  'default',
  'seeded-config-digest-preview-teardown',
  'pull_request',
  '42',
  'https://github.com/assembledhq/143/pull/42',
  '00000000-0000-4000-a000-000000000002'::uuid,
  now() - interval '32 minutes',
  'seeded-preview-target-42',
  'seeded/snapshots/preview-target-42/base',
  '00000000-0000-4000-a000-000000000430'::uuid
)
ON CONFLICT (id) DO UPDATE
SET repository_id = EXCLUDED.repository_id,
    branch = EXCLUDED.branch,
    commit_sha = EXCLUDED.commit_sha,
    preview_config_name = EXCLUDED.preview_config_name,
    resolved_config_digest = EXCLUDED.resolved_config_digest,
    source_type = EXCLUDED.source_type,
    source_id = EXCLUDED.source_id,
    source_url = EXCLUDED.source_url,
    created_by_user_id = EXCLUDED.created_by_user_id,
    request_id = EXCLUDED.request_id,
    last_snapshot_key = EXCLUDED.last_snapshot_key,
    preview_group_id = EXCLUDED.preview_group_id;

UPDATE preview_groups
SET current_target_id = '00000000-0000-4000-a000-000000000431'::uuid
WHERE id = '00000000-0000-4000-a000-000000000430'::uuid
  AND org_id = '00000000-0000-4000-a000-000000000001'::uuid;

INSERT INTO preview_links (
  id, org_id, preview_target_id, link_type, slug, repository_id, pr_number,
  created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000432'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000431'::uuid,
  'pull_request',
  'assembledhq-143-42',
  '00000000-0000-4000-a000-000000000100'::uuid,
  42,
  now() - interval '32 minutes',
  now() - interval '2 minutes'
)
ON CONFLICT (id) DO UPDATE
SET preview_target_id = EXCLUDED.preview_target_id,
    link_type = EXCLUDED.link_type,
    slug = EXCLUDED.slug,
    repository_id = EXCLUDED.repository_id,
    pr_number = EXCLUDED.pr_number,
    updated_at = EXCLUDED.updated_at;
