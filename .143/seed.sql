-- Seed data for preview dogfooding.
-- Creates a default organization and four users so the preview is
-- immediately usable without requiring the registration flow, plus a
-- placeholder integration and a couple of repositories/projects so the
-- logged-in UI shows populated screens instead of empty states.
--
-- IMPORTANT: the seeded admin email + password below must match the
-- DEMO_EMAIL / DEMO_PASSWORD defaults in internal/config/config.go, since
-- the login-page banner renders those values and a reviewer copy-pastes
-- them into the sign-in form. If you change either side, regenerate the
-- bcrypt hash below (cost 10) and update the config defaults in lockstep.
--
-- Password for all preview users: "preview" (bcrypt hash below).
--
-- All rows use fixed UUIDs and conflict handlers so the seed is safely
-- re-runnable. Tables with secondary unique indexes (e.g.
-- repositories.idx_repositories_org_github) use the unqualified
-- ON CONFLICT DO NOTHING form so any unique violation — not just on id —
-- no-ops rather than aborting the transaction; identity rows use DO UPDATE
-- so old dogfood credentials converge to the current preview credentials.

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
VALUES
  (
    '00000000-0000-4000-a000-000000000002'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'preview-admin@143.dev',
    'Preview Admin',
    'admin',
    -- bcrypt hash of "preview" (cost 10)
    '$2y$10$MtyCwm3KVYgmLvAinVwMHO3c65omeHXqqyIqwlz9JXJ30.5V2fyAe',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000003'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'preview-member@143.dev',
    'Preview Member',
    'member',
    -- bcrypt hash of "preview" (cost 10)
    '$2y$10$MtyCwm3KVYgmLvAinVwMHO3c65omeHXqqyIqwlz9JXJ30.5V2fyAe',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000004'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'preview-builder@143.dev',
    'Preview Builder',
    'builder',
    -- bcrypt hash of "preview" (cost 10)
    '$2y$10$MtyCwm3KVYgmLvAinVwMHO3c65omeHXqqyIqwlz9JXJ30.5V2fyAe',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000005'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'preview-viewer@143.dev',
    'Preview Viewer',
    'viewer',
    -- bcrypt hash of "preview" (cost 10)
    '$2y$10$MtyCwm3KVYgmLvAinVwMHO3c65omeHXqqyIqwlz9JXJ30.5V2fyAe',
    now()
  )
ON CONFLICT (id) DO UPDATE
SET org_id = EXCLUDED.org_id,
    email = EXCLUDED.email,
    name = EXCLUDED.name,
    role = EXCLUDED.role,
    password_hash = EXCLUDED.password_hash;

INSERT INTO organization_memberships (user_id, org_id, role, created_at)
VALUES
  (
    '00000000-0000-4000-a000-000000000002'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'admin',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000003'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'member',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000004'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'builder',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000005'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'viewer',
    now()
  )
ON CONFLICT (user_id, org_id) DO UPDATE
SET role = EXCLUDED.role;

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

-- Representative issue backlog. Production is mostly Linear-backed issues with
-- priority and complexity sidecars, so the demo seed includes a small synthetic
-- spread across source/status/severity without copying any customer text.
INSERT INTO issues (
  id, org_id, external_id, source, source_integration_id, repository_id,
  title, description, raw_data, status, first_seen_at, last_seen_at,
  occurrence_count, affected_customer_count, severity, tags, fingerprint,
  project_id, created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000600'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'DEMO-LIN-101',
    'linear',
    '00000000-0000-4000-a000-000000000011'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    'Dashboard filters drop archived sessions',
    'Synthetic issue: archived sessions should stay visible when the archive filter is active.',
    '{"identifier":"DEMO-LIN-101","team":"Platform","labels":["frontend","sessions"]}'::jsonb,
    'open',
    now() - interval '9 days',
    now() - interval '20 minutes',
    18,
    4,
    'high',
    ARRAY['linear','sessions','frontend'],
    'demo:linear:dashboard-filters',
    '00000000-0000-4000-a000-000000000200'::uuid,
    now() - interval '9 days',
    now() - interval '20 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000601'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'DEMO-LIN-102',
    'linear',
    '00000000-0000-4000-a000-000000000011'::uuid,
    '00000000-0000-4000-a000-000000000101'::uuid,
    'Webhook replay cursor skips retried deliveries',
    'Synthetic issue: replay should preserve retry order when a delivery is reprocessed.',
    '{"identifier":"DEMO-LIN-102","team":"Integrations","labels":["webhooks","reliability"]}'::jsonb,
    'in_progress',
    now() - interval '5 days',
    now() - interval '1 hour',
    7,
    2,
    'medium',
    ARRAY['linear','webhooks','reliability'],
    'demo:linear:webhook-replay-cursor',
    '00000000-0000-4000-a000-000000000201'::uuid,
    now() - interval '5 days',
    now() - interval '1 hour'
  ),
  (
    '00000000-0000-4000-a000-000000000602'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'manual-preview-cold-start',
    'manual',
    NULL,
    '00000000-0000-4000-a000-000000000100'::uuid,
    'Preview cold starts exceed target on first request',
    'Synthetic issue: first preview request needs clearer warmup and timeout feedback.',
    '{"reported_by":"demo-operator","surface":"preview"}'::jsonb,
    'triaged',
    now() - interval '3 days',
    now() - interval '3 hours',
    4,
    0,
    'high',
    ARRAY['manual','preview','latency'],
    'demo:manual:preview-cold-start',
    NULL,
    now() - interval '3 days',
    now() - interval '3 hours'
  ),
  (
    '00000000-0000-4000-a000-000000000603'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'DEMO-LIN-103',
    'linear',
    '00000000-0000-4000-a000-000000000011'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    'PR readiness summary needs clearer failing check labels',
    'Synthetic issue: readiness cards should group failing checks by category.',
    '{"identifier":"DEMO-LIN-103","team":"Platform","labels":["pr-readiness","ux"]}'::jsonb,
    'fixed',
    now() - interval '14 days',
    now() - interval '1 day',
    3,
    1,
    'low',
    ARRAY['linear','pr-readiness','ux'],
    'demo:linear:readiness-labels',
    NULL,
    now() - interval '14 days',
    now() - interval '1 day'
  ),
  (
    '00000000-0000-4000-a000-000000000604'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'pm-agent-preview-copy',
    'pm_agent',
    NULL,
    '00000000-0000-4000-a000-000000000100'::uuid,
    'Consolidate preview failure copy across panels',
    'Synthetic PM proposal: make preview failure reasons consistent between list, detail, and PR surfaces.',
    '{"proposal_source":"seeded_pm_agent","confidence":"medium"}'::jsonb,
    'open',
    now() - interval '2 days',
    now() - interval '30 minutes',
    2,
    0,
    'medium',
    ARRAY['pm_agent','preview','copy'],
    'demo:pm-agent:preview-failure-copy',
    '00000000-0000-4000-a000-000000000200'::uuid,
    now() - interval '2 days',
    now() - interval '30 minutes'
  )
ON CONFLICT (id) DO UPDATE
SET org_id = EXCLUDED.org_id,
    external_id = EXCLUDED.external_id,
    source = EXCLUDED.source,
    source_integration_id = EXCLUDED.source_integration_id,
    repository_id = EXCLUDED.repository_id,
    title = EXCLUDED.title,
    description = EXCLUDED.description,
    raw_data = EXCLUDED.raw_data,
    status = EXCLUDED.status,
    first_seen_at = EXCLUDED.first_seen_at,
    last_seen_at = EXCLUDED.last_seen_at,
    occurrence_count = EXCLUDED.occurrence_count,
    affected_customer_count = EXCLUDED.affected_customer_count,
    severity = EXCLUDED.severity,
    tags = EXCLUDED.tags,
    fingerprint = EXCLUDED.fingerprint,
    project_id = EXCLUDED.project_id,
    created_at = EXCLUDED.created_at,
    updated_at = EXCLUDED.updated_at;

INSERT INTO priority_scores (
  id, issue_id, org_id, score, customer_impact_score, severity_score,
  recency_score, revenue_risk_score, direction_alignment, factors,
  eligible_for_agent, computed_at
)
VALUES
  ('00000000-0000-4000-a000-000000000610'::uuid, '00000000-0000-4000-a000-000000000600'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 88, 20, 30, 18, 5, 15, '{"signals":["recent","high_severity","linked_project"]}'::jsonb, true, now() - interval '20 minutes'),
  ('00000000-0000-4000-a000-000000000611'::uuid, '00000000-0000-4000-a000-000000000601'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 74, 12, 20, 17, 5, 20, '{"signals":["in_progress","reliability"]}'::jsonb, true, now() - interval '1 hour'),
  ('00000000-0000-4000-a000-000000000612'::uuid, '00000000-0000-4000-a000-000000000602'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 69, 0, 30, 19, 0, 20, '{"signals":["manual_triage","preview_surface"]}'::jsonb, true, now() - interval '3 hours'),
  ('00000000-0000-4000-a000-000000000613'::uuid, '00000000-0000-4000-a000-000000000603'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 35, 5, 10, 5, 0, 15, '{"signals":["fixed","low_severity"]}'::jsonb, false, now() - interval '1 day'),
  ('00000000-0000-4000-a000-000000000614'::uuid, '00000000-0000-4000-a000-000000000604'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 61, 0, 20, 16, 0, 25, '{"signals":["pm_proposal","demo_polish"]}'::jsonb, true, now() - interval '30 minutes')
ON CONFLICT (issue_id) DO UPDATE
SET score = EXCLUDED.score,
    customer_impact_score = EXCLUDED.customer_impact_score,
    severity_score = EXCLUDED.severity_score,
    recency_score = EXCLUDED.recency_score,
    revenue_risk_score = EXCLUDED.revenue_risk_score,
    direction_alignment = EXCLUDED.direction_alignment,
    factors = EXCLUDED.factors,
    eligible_for_agent = EXCLUDED.eligible_for_agent,
    computed_at = EXCLUDED.computed_at;

INSERT INTO complexity_estimates (
  id, issue_id, org_id, tier, label, confidence, issue_type, reasoning,
  estimated_files, estimated_tokens, model_used, computed_at, created_at
)
VALUES
  ('00000000-0000-4000-a000-000000000620'::uuid, '00000000-0000-4000-a000-000000000600'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 3, 'moderate', 0.82, 'frontend_state', 'Filter state and archived-session query behavior need coordinated changes.', ARRAY['frontend/src/app/sessions/page.tsx','internal/db/session_store.go'], 4200, 'seeded-estimator', now() - interval '20 minutes', now() - interval '20 minutes'),
  ('00000000-0000-4000-a000-000000000621'::uuid, '00000000-0000-4000-a000-000000000601'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 4, 'complex', 0.74, 'backend_reliability', 'Replay ordering touches ingestion idempotency and retry accounting.', ARRAY['internal/services/ingestion/service.go','internal/db/webhook_deliveries.go'], 6800, 'seeded-estimator', now() - interval '1 hour', now() - interval '1 hour'),
  ('00000000-0000-4000-a000-000000000622'::uuid, '00000000-0000-4000-a000-000000000602'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 3, 'moderate', 0.79, 'preview_runtime', 'Cold-start diagnostics need runtime timing and UI treatment.', ARRAY['internal/services/preview','frontend/src/components/preview'], 5400, 'seeded-estimator', now() - interval '3 hours', now() - interval '3 hours'),
  ('00000000-0000-4000-a000-000000000623'::uuid, '00000000-0000-4000-a000-000000000603'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 2, 'simple', 0.91, 'copy_and_grouping', 'Mostly presentation logic around readiness check labels.', ARRAY['frontend/src/components/pr-readiness'], 2600, 'seeded-estimator', now() - interval '1 day', now() - interval '1 day'),
  ('00000000-0000-4000-a000-000000000624'::uuid, '00000000-0000-4000-a000-000000000604'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 2, 'simple', 0.86, 'product_polish', 'Synthetic copy consolidation across existing preview surfaces.', ARRAY['frontend/src/components/preview'], 3100, 'seeded-estimator', now() - interval '30 minutes', now() - interval '30 minutes')
ON CONFLICT (issue_id) DO UPDATE
SET tier = EXCLUDED.tier,
    label = EXCLUDED.label,
    confidence = EXCLUDED.confidence,
    issue_type = EXCLUDED.issue_type,
    reasoning = EXCLUDED.reasoning,
    estimated_files = EXCLUDED.estimated_files,
    estimated_tokens = EXCLUDED.estimated_tokens,
    model_used = EXCLUDED.model_used,
    computed_at = EXCLUDED.computed_at;

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
--
-- Idempotency: rows with fixed PKs use ON CONFLICT (id) DO NOTHING. Rows in
-- tables where we don't own an id (session_messages, session_logs) have no
-- unique constraint on the seeded columns, so ON CONFLICT alone cannot
-- deduplicate them. To stay idempotent across repeated seed runs (e.g. a
-- sandbox restart against a persistent Postgres volume) we DELETE the seed
-- rows by their fixed session_ids before re-INSERTing. The session_ids
-- 00000000-0000-4000-a000-00000000030x are owned by this seed and cannot
-- collide with real sessions, which use gen_random_uuid().
-- =============================================================================

-- Five sessions spread across common production states: active PR, completed,
-- idle, awaiting human guidance, and failed. They are ordered by
-- last_activity_at DESC so the sessions list has a natural MRU shape.
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
  ),
  (
    '00000000-0000-4000-a000-000000000303'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    '00000000-0000-4000-a000-000000000003'::uuid,
    'Confirm dashboard archive behavior',
    'fix/archive-filter-state',
    'main',
    'codex',
    'needs_human_guidance',
    'semi',
    'low',
    'snapshotted',
    2,
    now() - interval '18 minutes',
    now() - interval '55 minutes',
    NULL,
    now() - interval '55 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000304'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000101'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    'Normalize webhook replay cursor',
    'fix/replay-cursor',
    'main',
    'codex',
    'failed',
    'semi',
    'low',
    'destroyed',
    2,
    now() - interval '6 hours',
    now() - interval '7 hours',
    now() - interval '6 hours',
    now() - interval '7 hours'
  )
ON CONFLICT (id) DO NOTHING;

UPDATE sessions
SET origin = CASE id
    WHEN '00000000-0000-4000-a000-000000000303'::uuid THEN 'issue_trigger'
    WHEN '00000000-0000-4000-a000-000000000304'::uuid THEN 'issue_trigger'
    ELSE origin
  END,
  interaction_mode = CASE id
    WHEN '00000000-0000-4000-a000-000000000303'::uuid THEN 'interactive'
    ELSE interaction_mode
  END,
  validation_policy = CASE id
    WHEN '00000000-0000-4000-a000-000000000304'::uuid THEN 'on_session_end'
    ELSE validation_policy
  END,
  failure_explanation = CASE id
    WHEN '00000000-0000-4000-a000-000000000304'::uuid THEN 'Synthetic failure: replay cursor test expected a stable retry order.'
    ELSE failure_explanation
  END,
  failure_category = CASE id
    WHEN '00000000-0000-4000-a000-000000000304'::uuid THEN 'regression_test'
    ELSE failure_category
  END,
  failure_next_steps = CASE id
    WHEN '00000000-0000-4000-a000-000000000304'::uuid THEN ARRAY['Inspect cursor ordering in the replay store', 'Add a regression test for retry ordering']
    ELSE failure_next_steps
  END,
  failure_retry_advised = CASE id
    WHEN '00000000-0000-4000-a000-000000000304'::uuid THEN true
    ELSE failure_retry_advised
  END
WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
  AND id IN (
    '00000000-0000-4000-a000-000000000303'::uuid,
    '00000000-0000-4000-a000-000000000304'::uuid
  );

INSERT INTO session_issue_links (
  id, org_id, session_id, issue_id, role, position, added_by_user_id, created_at
)
VALUES
  ('00000000-0000-4000-a000-000000000630'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000604'::uuid, 'primary', 0, '00000000-0000-4000-a000-000000000002'::uuid, now() - interval '35 minutes'),
  ('00000000-0000-4000-a000-000000000631'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000301'::uuid, '00000000-0000-4000-a000-000000000601'::uuid, 'primary', 0, '00000000-0000-4000-a000-000000000002'::uuid, now() - interval '2 hours'),
  ('00000000-0000-4000-a000-000000000632'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000302'::uuid, '00000000-0000-4000-a000-000000000602'::uuid, 'primary', 0, '00000000-0000-4000-a000-000000000002'::uuid, now() - interval '3 days'),
  ('00000000-0000-4000-a000-000000000633'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000303'::uuid, '00000000-0000-4000-a000-000000000600'::uuid, 'primary', 0, '00000000-0000-4000-a000-000000000003'::uuid, now() - interval '55 minutes'),
  ('00000000-0000-4000-a000-000000000634'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000304'::uuid, '00000000-0000-4000-a000-000000000601'::uuid, 'primary', 0, '00000000-0000-4000-a000-000000000002'::uuid, now() - interval '7 hours')
ON CONFLICT (session_id, issue_id) DO UPDATE
SET role = EXCLUDED.role,
    position = EXCLUDED.position,
    added_by_user_id = EXCLUDED.added_by_user_id;

INSERT INTO session_turn_issue_snapshots (
  id, org_id, session_id, turn_number, linked_issues, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000640'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000300'::uuid,
    1,
    '[{"id":"00000000-0000-4000-a000-000000000604","role":"primary","title":"Consolidate preview failure copy across panels","source":"pm_agent","severity":"medium"}]'::jsonb,
    now() - interval '34 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000641'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000301'::uuid,
    1,
    '[{"id":"00000000-0000-4000-a000-000000000601","role":"primary","title":"Webhook replay cursor skips retried deliveries","source":"linear","severity":"medium"}]'::jsonb,
    now() - interval '1 hour'
  ),
  (
    '00000000-0000-4000-a000-000000000642'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000303'::uuid,
    2,
    '[{"id":"00000000-0000-4000-a000-000000000600","role":"primary","title":"Dashboard filters drop archived sessions","source":"linear","severity":"high"}]'::jsonb,
    now() - interval '18 minutes'
  )
ON CONFLICT (session_id, turn_number) DO UPDATE
SET linked_issues = EXCLUDED.linked_issues,
    created_at = EXCLUDED.created_at;

INSERT INTO session_threads (
  id, session_id, org_id, agent_type, model_override, label, instructions,
  file_scope, status, agent_session_id, current_turn, last_activity_at,
  result_summary, diff, failure_explanation, failure_category, started_at,
  completed_at, created_at, base_snapshot_key, cost_cents, pending_message_count,
  created_by_source
)
VALUES
  (
    '00000000-0000-4000-a000-000000000700'::uuid,
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'codex',
    NULL,
    'Primary implementation',
    'Synthetic thread: implement preview teardown and update tests.',
    ARRAY['internal/services/preview','internal/services/github'],
    'completed',
    'seeded-thread-300',
    4,
    now() - interval '2 minutes',
    'Opened a synthetic PR preview teardown change with tests.',
    NULL,
    NULL,
    NULL,
    now() - interval '35 minutes',
    now() - interval '3 minutes',
    now() - interval '35 minutes',
    'seeded/snapshots/session-300/base',
    42.15,
    0,
    'seed'
  ),
  (
    '00000000-0000-4000-a000-000000000701'::uuid,
    '00000000-0000-4000-a000-000000000301'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'codex',
    NULL,
    'Webhook retry fix',
    'Synthetic thread: add retry coverage for webhook delivery handling.',
    ARRAY['internal/services/ingestion','internal/db/webhook_deliveries.go'],
    'completed',
    'seeded-thread-301',
    3,
    now() - interval '1 hour',
    'Added retry logic and tests for transient webhook failures.',
    NULL,
    NULL,
    NULL,
    now() - interval '2 hours',
    now() - interval '1 hour' - interval '5 minutes',
    now() - interval '2 hours',
    'seeded/snapshots/session-301/base',
    31.80,
    0,
    'seed'
  ),
  (
    '00000000-0000-4000-a000-000000000702'::uuid,
    '00000000-0000-4000-a000-000000000302'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'codex',
    NULL,
    'Cold-start diagnosis',
    'Synthetic thread: investigate cold-start timing before implementation.',
    ARRAY['internal/services/preview','deploy'],
    'idle',
    'seeded-thread-302',
    1,
    now() - interval '3 days',
    NULL,
    NULL,
    NULL,
    NULL,
    now() - interval '3 days' - interval '10 minutes',
    NULL,
    now() - interval '3 days' - interval '10 minutes',
    'seeded/snapshots/session-302/base',
    8.20,
    0,
    'seed'
  ),
  (
    '00000000-0000-4000-a000-000000000703'::uuid,
    '00000000-0000-4000-a000-000000000303'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'codex',
    NULL,
    'Archive filter guidance',
    'Synthetic thread: wait for product confirmation before changing default filter behavior.',
    ARRAY['frontend/src/app/sessions','internal/db/session_store.go'],
    'awaiting_input',
    'seeded-thread-303',
    2,
    now() - interval '18 minutes',
    NULL,
    NULL,
    NULL,
    NULL,
    now() - interval '55 minutes',
    NULL,
    now() - interval '55 minutes',
    'seeded/snapshots/session-303/base',
    16.40,
    1,
    'seed'
  ),
  (
    '00000000-0000-4000-a000-000000000704'::uuid,
    '00000000-0000-4000-a000-000000000304'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'codex',
    NULL,
    'Replay cursor repair',
    'Synthetic thread: normalize replay cursor ordering.',
    ARRAY['internal/services/ingestion','internal/db/webhook_deliveries.go'],
    'failed',
    'seeded-thread-304',
    2,
    now() - interval '6 hours',
    NULL,
    NULL,
    'Replay cursor test still observed unstable ordering.',
    'regression_test',
    now() - interval '7 hours',
    now() - interval '6 hours',
    now() - interval '7 hours',
    'seeded/snapshots/session-304/base',
    22.70,
    0,
    'seed'
  )
ON CONFLICT (id) DO UPDATE
SET label = EXCLUDED.label,
    instructions = EXCLUDED.instructions,
    file_scope = EXCLUDED.file_scope,
    status = EXCLUDED.status,
    current_turn = EXCLUDED.current_turn,
    last_activity_at = EXCLUDED.last_activity_at,
    result_summary = EXCLUDED.result_summary,
    failure_explanation = EXCLUDED.failure_explanation,
    failure_category = EXCLUDED.failure_category,
    started_at = EXCLUDED.started_at,
    completed_at = EXCLUDED.completed_at,
    base_snapshot_key = EXCLUDED.base_snapshot_key,
    cost_cents = EXCLUDED.cost_cents,
    pending_message_count = EXCLUDED.pending_message_count;

DELETE FROM session_thread_file_events WHERE session_id IN (
  '00000000-0000-4000-a000-000000000300'::uuid,
  '00000000-0000-4000-a000-000000000301'::uuid,
  '00000000-0000-4000-a000-000000000302'::uuid,
  '00000000-0000-4000-a000-000000000303'::uuid,
  '00000000-0000-4000-a000-000000000304'::uuid
);
INSERT INTO session_thread_file_events (
  org_id, session_id, thread_id, turn, path, event_type, before_hash, after_hash, observed_at
)
VALUES
  ('00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000700'::uuid, 1, 'internal/services/preview/recycler.go', 'modified', '1111111111111111111111111111111111111111', '2222222222222222222222222222222222222222', now() - interval '31 minutes'),
  ('00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000700'::uuid, 1, 'internal/services/preview/recycler_test.go', 'modified', '3333333333333333333333333333333333333333', '4444444444444444444444444444444444444444', now() - interval '29 minutes'),
  ('00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000301'::uuid, '00000000-0000-4000-a000-000000000701'::uuid, 1, 'internal/services/ingestion/service.go', 'modified', '5555555555555555555555555555555555555555', '6666666666666666666666666666666666666666', now() - interval '1 hour' - interval '20 minutes'),
  ('00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000303'::uuid, '00000000-0000-4000-a000-000000000703'::uuid, 2, 'frontend/src/app/sessions/page.tsx', 'modified', '7777777777777777777777777777777777777777', '8888888888888888888888888888888888888888', now() - interval '20 minutes'),
  ('00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000304'::uuid, '00000000-0000-4000-a000-000000000704'::uuid, 2, 'internal/db/webhook_deliveries.go', 'modified', '9999999999999999999999999999999999999999', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', now() - interval '6 hours');

INSERT INTO session_diff_snapshots (
  id, session_id, org_id, turn_number, sequence_number, source, base_commit_sha,
  head_commit_sha, working_branch, target_branch, diff, files_changed,
  lines_added, lines_removed, captured_at, workspace_dirty
)
VALUES
  (
    '00000000-0000-4000-a000-000000000720'::uuid,
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    1,
    1,
    'turn_complete',
    '1111111111111111111111111111111111111111',
    '2222222222222222222222222222222222222222',
    'feat/preview-teardown',
    'main',
    'diff --git a/internal/services/preview/recycler.go b/internal/services/preview/recycler.go',
    2,
    30,
    2,
    now() - interval '4 minutes',
    true
  ),
  (
    '00000000-0000-4000-a000-000000000721'::uuid,
    '00000000-0000-4000-a000-000000000301'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    1,
    1,
    'review',
    '3333333333333333333333333333333333333333',
    '4444444444444444444444444444444444444444',
    'fix/webhook-retry',
    'main',
    'diff --git a/internal/services/ingestion/service.go b/internal/services/ingestion/service.go',
    3,
    42,
    8,
    now() - interval '1 hour',
    false
  )
ON CONFLICT (id) DO UPDATE
SET turn_number = EXCLUDED.turn_number,
    sequence_number = EXCLUDED.sequence_number,
    source = EXCLUDED.source,
    head_commit_sha = EXCLUDED.head_commit_sha,
    diff = EXCLUDED.diff,
    files_changed = EXCLUDED.files_changed,
    lines_added = EXCLUDED.lines_added,
    lines_removed = EXCLUDED.lines_removed,
    captured_at = EXCLUDED.captured_at,
    workspace_dirty = EXCLUDED.workspace_dirty;

UPDATE sessions
SET latest_diff_snapshot_id = '00000000-0000-4000-a000-000000000720'::uuid
WHERE id = '00000000-0000-4000-a000-000000000300'::uuid
  AND org_id = '00000000-0000-4000-a000-000000000001'::uuid;

INSERT INTO session_review_comments (
  id, session_id, org_id, user_id, file_path, line_number, diff_side, body,
  resolved, resolved_at, resolved_by_pass, pass_number, created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000730'::uuid,
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000003'::uuid,
    'internal/services/preview/recycler.go',
    48,
    'new',
    'Synthetic review comment: keep the not-found path quiet when a PR has no preview.',
    true,
    now() - interval '8 minutes',
    2,
    1,
    now() - interval '18 minutes',
    now() - interval '8 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000731'::uuid,
    '00000000-0000-4000-a000-000000000303'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    'frontend/src/app/sessions/page.tsx',
    112,
    'new',
    'Synthetic review comment: confirm whether archived sessions should be included by default.',
    false,
    NULL,
    NULL,
    1,
    now() - interval '17 minutes',
    now() - interval '17 minutes'
  )
ON CONFLICT (id) DO UPDATE
SET body = EXCLUDED.body,
    resolved = EXCLUDED.resolved,
    resolved_at = EXCLUDED.resolved_at,
    resolved_by_pass = EXCLUDED.resolved_by_pass,
    updated_at = EXCLUDED.updated_at;

INSERT INTO session_questions (
  id, session_id, org_id, question_text, options, context, blocks_phase,
  answer_text, answered_by, answered_at, status, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000740'::uuid,
    '00000000-0000-4000-a000-000000000303'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'Should archived sessions appear by default when the archive filter is enabled?',
    ARRAY['Show archived only when explicitly selected','Include archived in all sessions'],
    'Synthetic product clarification for the archive filter behavior.',
    'implementation',
    NULL,
    NULL,
    NULL,
    'pending',
    now() - interval '18 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000741'::uuid,
    '00000000-0000-4000-a000-000000000301'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'Use exponential or fixed backoff for webhook replay?',
    ARRAY['Exponential backoff','Fixed backoff'],
    'Synthetic implementation decision recorded during the completed session.',
    'planning',
    'Use exponential backoff with a small cap.',
    '00000000-0000-4000-a000-000000000002'::uuid,
    now() - interval '1 hour' - interval '30 minutes',
    'answered',
    now() - interval '1 hour' - interval '45 minutes'
  )
ON CONFLICT (id) DO UPDATE
SET answer_text = EXCLUDED.answer_text,
    answered_by = EXCLUDED.answered_by,
    answered_at = EXCLUDED.answered_at,
    status = EXCLUDED.status;

INSERT INTO validations (
  id, session_id, org_id, status, direction_check, correctness_check,
  quality_check, security_scan, regression_test_check, coverage_delta,
  ci_check, details, started_at, completed_at, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000750'::uuid,
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'passed',
    'pass',
    'pass',
    'pass',
    'pass',
    'pass',
    '{"line_delta":28,"covered_lines_delta":24}'::jsonb,
    'pass',
    '{"summary":"Synthetic validation passed for preview teardown."}'::jsonb,
    now() - interval '5 minutes',
    now() - interval '4 minutes',
    now() - interval '5 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000751'::uuid,
    '00000000-0000-4000-a000-000000000304'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'failed',
    'pass',
    'fail',
    'pass',
    'pass',
    'fail',
    '{"line_delta":16,"covered_lines_delta":8}'::jsonb,
    'fail',
    '{"summary":"Synthetic validation failed because replay ordering still flaked."}'::jsonb,
    now() - interval '6 hours' - interval '5 minutes',
    now() - interval '6 hours',
    now() - interval '6 hours' - interval '5 minutes'
  )
ON CONFLICT (id) DO UPDATE
SET status = EXCLUDED.status,
    direction_check = EXCLUDED.direction_check,
    correctness_check = EXCLUDED.correctness_check,
    quality_check = EXCLUDED.quality_check,
    security_scan = EXCLUDED.security_scan,
    regression_test_check = EXCLUDED.regression_test_check,
    coverage_delta = EXCLUDED.coverage_delta,
    ci_check = EXCLUDED.ci_check,
    details = EXCLUDED.details,
    completed_at = EXCLUDED.completed_at;

-- Populate a small safe demo diff for the PR-created session so the Changes
-- view renders a real review surface in screenshot/demo mode.
UPDATE sessions
SET
  diff = $diff$diff --git a/internal/services/preview/recycler.go b/internal/services/preview/recycler.go
index 4b825dc..a15f4be 100644
--- a/internal/services/preview/recycler.go
+++ b/internal/services/preview/recycler.go
@@ -42,6 +42,17 @@ func (s *Service) HandlePullRequestClosed(ctx context.Context, event PullRequest
  if event.Repository == "" || event.Number == 0 {
    return nil
  }
+
+	preview, err := s.previewStore.GetByPullRequest(ctx, event.OrgID, event.Repository, event.Number)
+	if errors.Is(err, db.ErrNotFound) {
+		return nil
+	}
+	if err != nil {
+		return fmt.Errorf("lookup pr preview: %w", err)
+	}
+	if preview.Status == models.PreviewStatusReady {
+		return s.previewManager.StopPreview(ctx, event.OrgID, preview.ID)
+	}
  return nil
 }

diff --git a/internal/services/preview/recycler_test.go b/internal/services/preview/recycler_test.go
index 02f3a91..fb49d28 100644
--- a/internal/services/preview/recycler_test.go
+++ b/internal/services/preview/recycler_test.go
@@ -18,6 +18,24 @@ func TestHandlePullRequestClosed(t *testing.T) {
  t.Parallel()

  tests := []struct {
+		name          string
+		previewStatus models.PreviewStatus
+		expectStop    bool
+	}{
+		{name: "stops ready preview", previewStatus: models.PreviewStatusReady, expectStop: true},
+		{name: "ignores closed preview", previewStatus: models.PreviewStatusStopped, expectStop: false},
+	}
+
+	for _, tt := range tests {
+		t.Run(tt.name, func(t *testing.T) {
+			t.Parallel()
+			// preview manager expectations omitted for brevity
+		})
+	}
+
+	legacyCases := []struct {
    name string
  }{
$diff$,
  diff_stats = '{"files_changed":2,"added":30,"removed":2}'::jsonb,
  diff_history = '[{"pass":1,"diff_stats":{"files_changed":2,"added":30,"removed":2},"summary":"Stopped ready PR previews when the pull request closes.","created_at":"2026-05-26T20:00:00Z"}]'::jsonb,
  diff_collected_at = now() - interval '4 minutes'
WHERE id = '00000000-0000-4000-a000-000000000300'::uuid
  AND org_id = '00000000-0000-4000-a000-000000000001'::uuid;

-- A few chat messages per session so the detail pages render a conversation.
-- DELETE first to keep reseeds idempotent (see note at top of the illusory
-- section — session_messages has no unique constraint on the seeded cols).
DELETE FROM session_messages WHERE session_id IN (
  '00000000-0000-4000-a000-000000000300'::uuid,
  '00000000-0000-4000-a000-000000000301'::uuid,
  '00000000-0000-4000-a000-000000000302'::uuid,
  '00000000-0000-4000-a000-000000000303'::uuid,
  '00000000-0000-4000-a000-000000000304'::uuid
);
INSERT INTO session_messages (session_id, org_id, user_id, turn_number, role, content, thread_id, created_at)
VALUES
  (
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    1, 'user',
    'Please wire the preview recycler up to pull_request.closed so we stop paying for previews after a merge.',
    '00000000-0000-4000-a000-000000000700'::uuid,
    now() - interval '35 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    NULL,
    1, 'assistant',
    'Plan: inject preview manager into PRService, call StopPreview from the closed branch, mark pr_preview_state.status = ''closed''. Opened PR with a regression test.',
    '00000000-0000-4000-a000-000000000700'::uuid,
    now() - interval '34 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    2, 'user',
    'Looks good. Can you also make sure we do not blow up if the preview manager is not wired (self-hosted path)?',
    '00000000-0000-4000-a000-000000000700'::uuid,
    now() - interval '6 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000301'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    1, 'user',
    'Webhook deliveries are failing intermittently when GitHub returns a 502. Add a retry with backoff.',
    '00000000-0000-4000-a000-000000000701'::uuid,
    now() - interval '2 hours'
  ),
  (
    '00000000-0000-4000-a000-000000000301'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    NULL,
    1, 'assistant',
    'Added exponential backoff retry (3 attempts, 500ms/1s/2s) around the signature verification call. Tests cover 502, 503, and network timeouts.',
    '00000000-0000-4000-a000-000000000701'::uuid,
    now() - interval '1 hour' - interval '10 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000302'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    1, 'user',
    'Preview cold start is 90s+ on the dogfood env. Where is the time actually going?',
    '00000000-0000-4000-a000-000000000702'::uuid,
    now() - interval '3 days'
  ),
  (
    '00000000-0000-4000-a000-000000000303'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000003'::uuid,
    1, 'user',
    'Please keep archived sessions discoverable, but I am not sure whether they should show in the default list.',
    '00000000-0000-4000-a000-000000000703'::uuid,
    now() - interval '55 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000303'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    NULL,
    2, 'assistant',
    'I found two viable filter behaviors and paused for a product decision before changing the default.',
    '00000000-0000-4000-a000-000000000703'::uuid,
    now() - interval '18 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000304'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    1, 'user',
    'Normalize replay cursor ordering so retried webhook deliveries stay stable.',
    '00000000-0000-4000-a000-000000000704'::uuid,
    now() - interval '7 hours'
  ),
  (
    '00000000-0000-4000-a000-000000000304'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    NULL,
    2, 'assistant',
    'The synthetic replay-ordering test is still failing, so I stopped with a focused next-step summary.',
    '00000000-0000-4000-a000-000000000704'::uuid,
    now() - interval '6 hours'
  )
ON CONFLICT DO NOTHING;

-- A few log lines per session so the log stream UI has something to show.
-- DELETE first for the same idempotency reason as session_messages above.
DELETE FROM session_logs WHERE session_id IN (
  '00000000-0000-4000-a000-000000000300'::uuid,
  '00000000-0000-4000-a000-000000000301'::uuid,
  '00000000-0000-4000-a000-000000000302'::uuid,
  '00000000-0000-4000-a000-000000000303'::uuid,
  '00000000-0000-4000-a000-000000000304'::uuid
);
INSERT INTO session_logs (session_id, org_id, timestamp, level, message, turn_number, thread_id)
VALUES
  ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '34 minutes', 'info', 'sandbox provisioned', 1, '00000000-0000-4000-a000-000000000700'::uuid),
  ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '30 minutes', 'info', 'pushed branch feat/preview-teardown', 1, '00000000-0000-4000-a000-000000000700'::uuid),
  ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '28 minutes', 'info', 'opened pull request #42', 1, '00000000-0000-4000-a000-000000000700'::uuid),
  ('00000000-0000-4000-a000-000000000301'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '1 hour' - interval '5 minutes', 'info', 'session completed successfully', 1, '00000000-0000-4000-a000-000000000701'::uuid),
  ('00000000-0000-4000-a000-000000000303'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '18 minutes', 'question', 'waiting for product decision on archive filter behavior', 2, '00000000-0000-4000-a000-000000000703'::uuid),
  ('00000000-0000-4000-a000-000000000304'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '6 hours', 'error', 'synthetic replay-ordering regression remained failing', 2, '00000000-0000-4000-a000-000000000704'::uuid)
ON CONFLICT DO NOTHING;

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
SET current_status = EXCLUDED.current_status,
    latest_commit_sha = EXCLUDED.latest_commit_sha,
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
SET commit_sha = EXCLUDED.commit_sha,
    resolved_config_digest = EXCLUDED.resolved_config_digest,
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
    updated_at = EXCLUDED.updated_at;

-- A seeded "ready" preview instance pointing at session 300.
-- worker_node_id = 'seeded' keeps the real recycler from touching this row.
INSERT INTO preview_instances (
  id, session_id, org_id, user_id, profile_name, name, status, provider,
  worker_node_id, preview_handle, primary_service, port, preview_target_id,
  config_digest, base_commit_sha, current_phase, last_path, expires_at,
  ready_at, created_at, updated_at
)
VALUES
  (
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
    '00000000-0000-4000-a000-000000000431'::uuid,
    'seeded-config-digest-preview-teardown',
    '1111111111111111111111111111111111111111',
    'ready',
    '/sessions/00000000-0000-4000-a000-000000000300',
    now() + interval '24 hours',
    now() - interval '29 minutes',
    now() - interval '30 minutes',
    now() - interval '2 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000401'::uuid,
    '00000000-0000-4000-a000-000000000304'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    'manado',
    'Normalize webhook replay cursor',
    'failed',
    'seeded',
    'seeded',
    '',
    'frontend',
    3000,
    NULL,
    'seeded-config-digest-webhook-replay',
    '3333333333333333333333333333333333333333',
    'failed',
    '/',
    now() - interval '5 hours',
    NULL,
    now() - interval '7 hours',
    now() - interval '6 hours'
  )
ON CONFLICT (id) DO UPDATE
SET status = EXCLUDED.status,
    preview_target_id = EXCLUDED.preview_target_id,
    config_digest = EXCLUDED.config_digest,
    base_commit_sha = EXCLUDED.base_commit_sha,
    current_phase = EXCLUDED.current_phase,
    last_path = EXCLUDED.last_path,
    ready_at = EXCLUDED.ready_at,
    updated_at = EXCLUDED.updated_at;

INSERT INTO preview_services (
  id, preview_instance_id, service_name, role, status, port, error, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000410'::uuid,
    '00000000-0000-4000-a000-000000000400'::uuid,
    'frontend', 'primary', 'ready', 3000,
    '',
    now() - interval '30 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000411'::uuid,
    '00000000-0000-4000-a000-000000000400'::uuid,
    'server', 'support', 'ready', 8080,
    '',
    now() - interval '30 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000412'::uuid,
    '00000000-0000-4000-a000-000000000401'::uuid,
    'frontend', 'primary', 'failed', 3000,
    'Synthetic startup check failed before the app became reachable.',
    now() - interval '30 minutes'
  )
ON CONFLICT (id) DO UPDATE
SET status = EXCLUDED.status,
    error = EXCLUDED.error;

INSERT INTO preview_infrastructure (
  id, preview_instance_id, infra_name, template, status, error, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000420'::uuid,
    '00000000-0000-4000-a000-000000000400'::uuid,
    'db', 'postgres-17', 'healthy', '',
    now() - interval '30 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000421'::uuid,
    '00000000-0000-4000-a000-000000000401'::uuid,
    'db', 'postgres-17', 'healthy', '',
    now() - interval '7 hours'
  )
ON CONFLICT (id) DO UPDATE
SET status = EXCLUDED.status,
    error = EXCLUDED.error;

INSERT INTO preview_runtimes (
  id, org_id, preview_instance_id, runtime_epoch, worker_node_id, endpoint_url,
  preview_handle, primary_port, status, lease_expires_at, last_heartbeat_at,
  stopped_at, error, created_at, updated_at, unavailable_reason
)
VALUES (
  '00000000-0000-4000-a000-000000000433'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000401'::uuid,
  1,
  'seeded',
  '',
  '',
  3000,
  'failed',
  now() - interval '6 hours',
  now() - interval '6 hours',
  now() - interval '6 hours',
  'Synthetic runtime exited before health check passed.',
  now() - interval '7 hours',
  now() - interval '6 hours',
  ''
)
ON CONFLICT (preview_instance_id, runtime_epoch) DO UPDATE
SET status = EXCLUDED.status,
    stopped_at = EXCLUDED.stopped_at,
    error = EXCLUDED.error,
    updated_at = EXCLUDED.updated_at;

INSERT INTO preview_snapshots (
  id, preview_instance_id, trigger, url_path, blob_ref, viewport_width,
  viewport_height, console_errors, file_changes, created_at
)
VALUES (
  '00000000-0000-4000-a000-000000000434'::uuid,
  '00000000-0000-4000-a000-000000000400'::uuid,
  'baseline',
  '/',
  'seeded/previews/00000000-0000-4000-a000-000000000400/baseline.png',
  1440,
  900,
  '[]'::jsonb,
  '[{"path":"internal/services/preview/recycler.go","status":"modified"}]'::jsonb,
  now() - interval '28 minutes'
)
ON CONFLICT (id) DO UPDATE
SET blob_ref = EXCLUDED.blob_ref,
    console_errors = EXCLUDED.console_errors,
    file_changes = EXCLUDED.file_changes;

INSERT INTO preview_logs (
  id, preview_instance_id, org_id, level, step, message, metadata, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000435'::uuid,
    '00000000-0000-4000-a000-000000000400'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'info',
    'ready',
    'Seeded preview marked ready for demo browsing.',
    '{"service":"frontend","duration_ms":4200}'::jsonb,
    now() - interval '29 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000436'::uuid,
    '00000000-0000-4000-a000-000000000401'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'error',
    'health_check',
    'Synthetic preview failed before the app became reachable.',
    '{"service":"frontend","attempt":3}'::jsonb,
    now() - interval '6 hours'
  )
ON CONFLICT (id) DO UPDATE
SET level = EXCLUDED.level,
    step = EXCLUDED.step,
    message = EXCLUDED.message,
    metadata = EXCLUDED.metadata;

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
