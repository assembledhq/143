-- Seed data for preview dogfooding.
-- Creates a default organization and admin user so the preview is
-- immediately usable without requiring the registration flow, plus a
-- placeholder integration and a couple of repositories/projects so the
-- logged-in UI shows populated screens instead of empty states.
--
-- Password: "preview-dogfood" (bcrypt hash below).
--
-- All rows use fixed UUIDs + ON CONFLICT (id) DO NOTHING so the seed is
-- safely re-runnable and does not conflict with user-created rows.

INSERT INTO organizations (id, name, settings, created_at, updated_at)
VALUES (
  '00000000-0000-4000-a000-000000000001'::uuid,
  '143 Dogfood',
  '{}'::jsonb,
  now(),
  now()
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO users (id, org_id, email, name, role, password_hash, created_at)
VALUES (
  '00000000-0000-4000-a000-000000000002'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  'dogfood@143.dev',
  'Preview Admin',
  'admin',
  -- bcrypt hash of "preview-dogfood" (cost 10)
  '$2a$10$yq0Z0nFAzgJa1IuC.zbMh.RmEdX2dAJk8XQwELbmOA1AztcbCUyVi',
  now()
)
ON CONFLICT (email) DO NOTHING;

-- Placeholder GitHub integration so repositories have a valid FK target.
-- The preview does not actually talk to GitHub, so the config is empty.
INSERT INTO integrations (id, org_id, provider, config, status, created_at)
VALUES (
  '00000000-0000-4000-a000-000000000010'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  'github',
  '{}'::jsonb,
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
ON CONFLICT (id) DO NOTHING;

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
