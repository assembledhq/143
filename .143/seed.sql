-- Seed data for preview dogfooding.
-- Creates a default organization and admin user so the preview is
-- immediately usable without requiring the registration flow, plus a
-- placeholder integration and a couple of repositories/projects so the
-- logged-in UI shows populated screens instead of empty states.
--
-- Password: "preview-dogfood" (bcrypt hash below).
--
-- All rows use fixed UUIDs + ON CONFLICT DO NOTHING so the seed is
-- safely re-runnable. Tables with secondary unique indexes (e.g.
-- repositories.idx_repositories_org_github) use the unqualified
-- ON CONFLICT DO NOTHING form so any unique violation — not just on id —
-- no-ops rather than aborting the transaction.

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
ON CONFLICT DO NOTHING;

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

-- =============================================================================
-- Illusory session + preview rows.
--
-- The dogfood preview runs as OS processes inside a sandbox container and has
-- no access to a Docker socket, so it cannot actually spawn sessions or
-- previews. These rows exist so that the sessions list, session detail, and
-- preview panels render populated state for a reviewer clicking around.
--
-- preview_instances.worker_node_id is set to the sentinel 'seeded' so the
-- real RecycleWorker (which scans WHERE worker_node_id = <this worker>) never
-- touches these rows. expires_at is far in the future for the same reason.
-- =============================================================================

-- Three sessions: one "active PR", one "completed", one "idle" -- spread
-- across the two seeded repos and ordered by last_activity_at DESC so the
-- sessions list has a natural MRU shape.
INSERT INTO sessions (
  id, org_id, repository_id, triggered_by_user_id, title, working_branch,
  target_branch, agent_type, status, autonomy_level, token_mode,
  sandbox_state, current_turn, last_activity_at, started_at, completed_at,
  created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    'Ship PR preview auto-teardown',
    'feat/preview-teardown',
    'main',
    'codex',
    'pr_created',
    'semi',
    'low',
    'snapshotted',
    4,
    now() - interval '2 minutes',
    now() - interval '35 minutes',
    now() - interval '3 minutes',
    now() - interval '35 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000301'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000101'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    'Retry webhook signature on transient GitHub 5xx',
    'fix/webhook-retry',
    'main',
    'codex',
    'completed',
    'semi',
    'low',
    'snapshotted',
    3,
    now() - interval '1 hour',
    now() - interval '2 hours',
    now() - interval '1 hour' - interval '5 minutes',
    now() - interval '2 hours'
  ),
  (
    '00000000-0000-4000-a000-000000000302'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    'Investigate preview cold-start latency',
    NULL,
    'main',
    'codex',
    'idle',
    'semi',
    'low',
    'none',
    1,
    now() - interval '3 days',
    now() - interval '3 days' - interval '10 minutes',
    NULL,
    now() - interval '3 days' - interval '10 minutes'
  )
ON CONFLICT (id) DO NOTHING;

-- A few chat messages per session so the detail pages render a conversation.
INSERT INTO session_messages (session_id, org_id, user_id, turn_number, role, content, created_at)
VALUES
  (
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    1, 'user',
    'Please wire the preview recycler up to pull_request.closed so we stop paying for previews after a merge.',
    now() - interval '35 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    NULL,
    1, 'assistant',
    'Plan: inject preview manager into PRService, call StopPreview from the closed branch, mark pr_preview_state.status = ''closed''. Opened PR with a regression test.',
    now() - interval '34 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    2, 'user',
    'Looks good. Can you also make sure we do not blow up if the preview manager is not wired (self-hosted path)?',
    now() - interval '6 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000301'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    1, 'user',
    'Webhook deliveries are failing intermittently when GitHub returns a 502. Add a retry with backoff.',
    now() - interval '2 hours'
  ),
  (
    '00000000-0000-4000-a000-000000000301'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    NULL,
    1, 'assistant',
    'Added exponential backoff retry (3 attempts, 500ms/1s/2s) around the signature verification call. Tests cover 502, 503, and network timeouts.',
    now() - interval '1 hour' - interval '10 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000302'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    1, 'user',
    'Preview cold start is 90s+ on the dogfood env. Where is the time actually going?',
    now() - interval '3 days'
  )
ON CONFLICT DO NOTHING;

-- A few log lines per session so the log stream UI has something to show.
INSERT INTO session_logs (session_id, org_id, timestamp, level, message, turn_number)
VALUES
  ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '34 minutes', 'info', 'sandbox provisioned', 1),
  ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '30 minutes', 'info', 'pushed branch feat/preview-teardown', 1),
  ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '28 minutes', 'info', 'opened pull request #42', 1),
  ('00000000-0000-4000-a000-000000000301'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '1 hour' - interval '5 minutes', 'info', 'session completed successfully', 1)
ON CONFLICT DO NOTHING;

-- A seeded "ready" preview instance pointing at session 300.
-- worker_node_id = 'seeded' keeps the real recycler from touching this row.
INSERT INTO preview_instances (
  id, session_id, org_id, user_id, profile_name, name, status, provider,
  worker_node_id, preview_handle, primary_service, port, expires_at, created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000400'::uuid,
  '00000000-0000-4000-a000-000000000300'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000002'::uuid,
  'manado',
  'Ship PR preview auto-teardown',
  'ready',
  'seeded',
  'seeded',
  '',
  'frontend',
  3000,
  now() + interval '24 hours',
  now() - interval '30 minutes',
  now() - interval '2 minutes'
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO preview_services (
  id, preview_instance_id, service_name, role, status, port, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000410'::uuid,
    '00000000-0000-4000-a000-000000000400'::uuid,
    'frontend', 'primary', 'ready', 3000,
    now() - interval '30 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000411'::uuid,
    '00000000-0000-4000-a000-000000000400'::uuid,
    'server', 'support', 'ready', 8080,
    now() - interval '30 minutes'
  )
ON CONFLICT (id) DO NOTHING;

INSERT INTO preview_infrastructure (
  id, preview_instance_id, infra_name, template, status, created_at
)
VALUES (
  '00000000-0000-4000-a000-000000000420'::uuid,
  '00000000-0000-4000-a000-000000000400'::uuid,
  'db', 'postgres-17', 'healthy',
  now() - interval '30 minutes'
)
ON CONFLICT (id) DO NOTHING;

-- PR-preview tracking for the "pr_created" session. pr_number is placeholder;
-- no real PR exists on GitHub in the dogfood.
INSERT INTO pr_preview_state (
  id, org_id, repo_id, pr_number, last_preview_instance_id, status, created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000500'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000100'::uuid,
  42,
  '00000000-0000-4000-a000-000000000400'::uuid,
  'running',
  now() - interval '30 minutes',
  now() - interval '2 minutes'
)
ON CONFLICT DO NOTHING;
