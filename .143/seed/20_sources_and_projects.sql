-- Placeholder integrations so repositories and issue sources have valid FK
-- targets. The preview does not actually talk to providers, so configs are
-- inert and contain no credentials.
INSERT INTO integrations (id, org_id, provider, config, status, created_at)
VALUES
  (
    '00000000-0000-4000-a000-000000000010'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'github',
    '{}'::jsonb,
    'active',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000011'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'linear',
    '{"workspace":"demo","sync":"seeded"}'::jsonb,
    'active',
    now()
  )
ON CONFLICT (id) DO NOTHING;

-- The github_id, installation_id, and clone_url values are placeholders.
-- The dogfood preview has no GitHub App configured and will not actually
-- call the GitHub API, so any code path that tries to hit GitHub from
-- this seeded data will fail — that is expected. If you need real GitHub
-- integration in the preview, register a throwaway App and replace these.
INSERT INTO repositories (
  id, org_id, integration_id, github_id, full_name, default_branch,
  private, language, description, clone_url, installation_id, status,
  settings, created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000100'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000010'::uuid,
    1001,
    'assembledhq/143',
    'main',
    true,
    'Go',
    'The 143 agent platform itself (dogfood).',
    'https://github.com/assembledhq/143.git',
    99999,
    'active',
    '{}'::jsonb,
    now(),
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000101'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000010'::uuid,
    1002,
    'assembledhq/example-service',
    'main',
    true,
    'TypeScript',
    'Example service used for walkthroughs in the dogfood preview.',
    'https://github.com/assembledhq/example-service.git',
    99999,
    'active',
    '{}'::jsonb,
    now(),
    now()
  )
ON CONFLICT DO NOTHING;

INSERT INTO repository_pr_templates (
  id, repository_id, org_id, template_content, template_path, fetched_at,
  created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000120'::uuid,
  '00000000-0000-4000-a000-000000000100'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  $template$## Summary

- What changed?
- How was it verified?

## Demo Notes

- Keep screenshots and linked issues synthetic.
$template$,
  '.github/pull_request_template.md',
  now() - interval '7 days',
  now() - interval '7 days',
  now() - interval '7 days'
)
ON CONFLICT (repository_id) DO UPDATE
SET id = EXCLUDED.id,
    org_id = EXCLUDED.org_id,
    template_content = EXCLUDED.template_content,
    template_path = EXCLUDED.template_path,
    fetched_at = EXCLUDED.fetched_at,
    updated_at = EXCLUDED.updated_at;

INSERT INTO projects (
  id, org_id, repository_id, title, goal, scope, status, priority,
  execution_mode, max_concurrent, auto_merge, base_branch, created_by,
  created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000200'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    'Ship preview environments',
    'Add preview deploys so every session shows a live app.',
    'Preview provider, gateway routing, and UI integration.',
    'active',
    50,
    'sequential',
    2,
    false,
    'main',
    '00000000-0000-4000-a000-000000000002'::uuid,
    now(),
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000201'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000101'::uuid,
    'Webhook ingestion',
    'Wire example-service webhooks through the ingestion pipeline.',
    'Signature verification, idempotent delivery, replay.',
    'completed',
    50,
    'sequential',
    2,
    false,
    'main',
    '00000000-0000-4000-a000-000000000002'::uuid,
    now(),
    now()
  )
ON CONFLICT (id) DO NOTHING;
