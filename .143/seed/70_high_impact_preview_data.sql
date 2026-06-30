-- High-impact product surface data for the dogfood preview. Everything here
-- is synthetic and safe to publish, but the shapes mirror real records closely
-- enough for project, automation, autopilot, code review, usage, and provider
-- integration pages to render non-empty workflows.

UPDATE organizations
SET settings = COALESCE(settings, '{}'::jsonb) || '{
  "default_work_repository_id": "00000000-0000-4000-a000-000000000100",
  "product_context": {
    "product": "143",
    "audience": "engineering teams delegating code work to agents",
    "principles": [
      "show agent work with evidence",
      "keep tenant data isolated",
      "make preview and PR readiness obvious"
    ],
    "current_focus": [
      "preview reliability",
      "provider-triggered automations",
      "review quality gates"
    ]
  }
}'::jsonb,
    updated_at = now()
WHERE id = '00000000-0000-4000-a000-000000000001'::uuid;

UPDATE repositories
SET settings = COALESCE(settings, '{}'::jsonb) || '{
  "preview_config": {
    "detected": true,
    "entrypoint": "make preview",
    "health_path": "/healthz",
    "ports": [3000, 8080],
    "last_detected_at": "seeded"
  },
  "branching": {
    "protected_branches": ["main"],
    "agent_branch_prefix": "codex/"
  }
}'::jsonb,
    updated_at = now()
WHERE id = '00000000-0000-4000-a000-000000000100'::uuid;

UPDATE repositories
SET settings = COALESCE(settings, '{}'::jsonb) || '{
  "preview_config": {
    "detected": true,
    "entrypoint": "npm run dev",
    "health_path": "/ready",
    "ports": [5173],
    "last_detected_at": "seeded"
  },
  "branching": {
    "protected_branches": ["main"],
    "agent_branch_prefix": "codex/"
  }
}'::jsonb,
    updated_at = now()
WHERE id = '00000000-0000-4000-a000-000000000101'::uuid;

INSERT INTO integrations (id, org_id, provider, config, status, last_synced_at, created_at)
VALUES
  (
    '00000000-0000-4000-a000-000000000012'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'sentry',
    '{"organization_slug":"preview-demo","projects":["143-api","example-service"],"sync":"seeded"}'::jsonb,
    'active',
    now() - interval '8 minutes',
    now() - interval '20 days'
  ),
  (
    '00000000-0000-4000-a000-000000000013'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'slack',
    '{"workspace":"143 Preview","team_id":"T143DEMO","sync":"seeded"}'::jsonb,
    'active',
    now() - interval '12 minutes',
    now() - interval '18 days'
  ),
  (
    '00000000-0000-4000-a000-000000000014'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'pagerduty',
    '{"account_subdomain":"preview-demo","region":"us","sync":"seeded"}'::jsonb,
    'active',
    now() - interval '6 minutes',
    now() - interval '17 days'
  )
ON CONFLICT (id) DO UPDATE
SET config = EXCLUDED.config,
    status = EXCLUDED.status,
    last_synced_at = EXCLUDED.last_synced_at;

INSERT INTO slack_installations (
  id, org_id, integration_id, team_id, team_name, api_app_id, bot_user_id,
  bot_id, scope, status, installed_by_user_id, installed_at, last_event_at,
  created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000800'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000013'::uuid,
  'T143DEMO',
  '143 Preview',
  'A143DEMO',
  'U143BOT',
  'B143BOT',
  ARRAY['app_mentions:read','channels:history','chat:write','commands','users:read.email'],
  'active',
  '00000000-0000-4000-a000-000000000002'::uuid,
  now() - interval '18 days',
  now() - interval '3 minutes',
  now() - interval '18 days',
  now() - interval '3 minutes'
)
ON CONFLICT (id) DO UPDATE
SET team_name = EXCLUDED.team_name,
    scope = EXCLUDED.scope,
    status = EXCLUDED.status,
    last_event_at = EXCLUDED.last_event_at,
    updated_at = EXCLUDED.updated_at;

INSERT INTO slack_bot_settings (
  id, org_id, slack_installation_id, default_repository_id, default_branch,
  routing_mode, response_visibility, allowed_actions, notification_preset,
  notification_subscriptions, active, created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000801'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000800'::uuid,
  '00000000-0000-4000-a000-000000000100'::uuid,
  'main',
  'start_work',
  'thread',
  ARRAY['session','preview','pr_request','human_input'],
  'balanced',
  '{"session.completed":true,"session.failed":true,"preview.ready":true,"human_input.requested":true}'::jsonb,
  true,
  now() - interval '18 days',
  now() - interval '1 day'
)
ON CONFLICT (org_id, slack_installation_id) DO UPDATE
SET default_repository_id = EXCLUDED.default_repository_id,
    routing_mode = EXCLUDED.routing_mode,
    response_visibility = EXCLUDED.response_visibility,
    allowed_actions = EXCLUDED.allowed_actions,
    notification_preset = EXCLUDED.notification_preset,
    notification_subscriptions = EXCLUDED.notification_subscriptions,
    active = EXCLUDED.active,
    updated_at = EXCLUDED.updated_at;

INSERT INTO slack_channel_settings (
  id, org_id, slack_installation_id, slack_team_id, slack_channel_id,
  slack_channel_name, channel_type, default_repository_id, default_branch,
  response_visibility, allowed_actions, notification_subscriptions, active,
  routing_mode, notification_preset, created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000802'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000800'::uuid,
    'T143DEMO',
    'C143ENG',
    'eng-agent-queue',
    'channel',
    '00000000-0000-4000-a000-000000000100'::uuid,
    'main',
    'thread',
    ARRAY['session','preview','pr_request','human_input'],
    '{"session.started":true,"session.completed":true,"pr.ready":true}'::jsonb,
    true,
    'start_work',
    'balanced',
    now() - interval '14 days',
    now() - interval '1 hour'
  ),
  (
    '00000000-0000-4000-a000-000000000803'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000800'::uuid,
    'T143DEMO',
    'C143INC',
    'incidents',
    'channel',
    '00000000-0000-4000-a000-000000000100'::uuid,
    'main',
    'thread',
    ARRAY['session','preview','human_input'],
    '{"pagerduty.incident":true,"session.failed":true}'::jsonb,
    true,
    'auto',
    'verbose',
    now() - interval '14 days',
    now() - interval '20 minutes'
  )
ON CONFLICT (id) DO UPDATE
SET slack_channel_name = EXCLUDED.slack_channel_name,
    default_repository_id = EXCLUDED.default_repository_id,
    response_visibility = EXCLUDED.response_visibility,
    allowed_actions = EXCLUDED.allowed_actions,
    notification_subscriptions = EXCLUDED.notification_subscriptions,
    active = EXCLUDED.active,
    routing_mode = EXCLUDED.routing_mode,
    notification_preset = EXCLUDED.notification_preset,
    updated_at = EXCLUDED.updated_at;

INSERT INTO slack_user_links (
  id, org_id, slack_installation_id, user_id, slack_team_id, slack_user_id,
  slack_email, slack_display_name, source, linked_at, created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000804'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000800'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    'T143DEMO',
    'U143ADMIN',
    'preview-admin@143.dev',
    'Preview Admin',
    'email_match',
    now() - interval '13 days',
    now() - interval '13 days',
    now() - interval '13 days'
  ),
  (
    '00000000-0000-4000-a000-000000000805'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000800'::uuid,
    '00000000-0000-4000-a000-000000000003'::uuid,
    'T143DEMO',
    'U143MEMBER',
    'preview-member@143.dev',
    'Preview Member',
    'admin_linked',
    now() - interval '12 days',
    now() - interval '12 days',
    now() - interval '12 days'
  )
ON CONFLICT (id) DO UPDATE
SET user_id = EXCLUDED.user_id,
    slack_email = EXCLUDED.slack_email,
    slack_display_name = EXCLUDED.slack_display_name,
    source = EXCLUDED.source,
    linked_at = EXCLUDED.linked_at,
    updated_at = EXCLUDED.updated_at;

INSERT INTO external_user_links (
  id, org_id, provider, provider_workspace_id, provider_user_id, user_id,
  source, status, confidence, external_email, external_handle,
  external_display_name, linked_by_user_id, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000806'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'slack',
    'T143DEMO',
    'U143ADMIN',
    '00000000-0000-4000-a000-000000000002'::uuid,
    'email_match',
    'active',
    99,
    'preview-admin@143.dev',
    'preview-admin',
    'Preview Admin',
    '00000000-0000-4000-a000-000000000002'::uuid,
    now() - interval '13 days'
  ),
  (
    '00000000-0000-4000-a000-000000000807'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'linear',
    'LIN-WS-DEMO',
    'LIN-USER-BUILDER',
    '00000000-0000-4000-a000-000000000004'::uuid,
    'directory',
    'active',
    94,
    'preview-builder@143.dev',
    'preview-builder',
    'Preview Builder',
    '00000000-0000-4000-a000-000000000002'::uuid,
    now() - interval '11 days'
  )
ON CONFLICT (id) DO UPDATE
SET user_id = EXCLUDED.user_id,
    source = EXCLUDED.source,
    status = EXCLUDED.status,
    confidence = EXCLUDED.confidence,
    external_email = EXCLUDED.external_email,
    external_handle = EXCLUDED.external_handle,
    external_display_name = EXCLUDED.external_display_name;

INSERT INTO slack_org_selections (
  id, org_id, slack_installation_id, slack_team_id, api_app_id,
  slack_user_id, selected_at, created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000808'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000800'::uuid,
  'T143DEMO',
  'A143DEMO',
  'U143ADMIN',
  now() - interval '13 days',
  now() - interval '13 days',
  now() - interval '13 days'
)
ON CONFLICT (id) DO UPDATE
SET selected_at = EXCLUDED.selected_at,
    updated_at = EXCLUDED.updated_at;

INSERT INTO pagerduty_integrations (
  id, org_id, integration_id, account_subdomain, service_region,
  oauth_mode, credential_ref, webhook_secret_ref, status, scopes,
  last_synced_at, last_health_check_at, default_repository_id,
  writeback_enabled, auto_create_webhook, created_by, created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000810'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000014'::uuid,
  'preview-demo',
  'us',
  'scoped',
  'seeded/pagerduty/oauth',
  'seeded/pagerduty/webhook',
  'active',
  ARRAY['incidents.read','incidents.write','services.read','webhooks.write'],
  now() - interval '6 minutes',
  now() - interval '6 minutes',
  '00000000-0000-4000-a000-000000000100'::uuid,
  true,
  false,
  '00000000-0000-4000-a000-000000000002'::uuid,
  now() - interval '17 days',
  now() - interval '6 minutes'
)
ON CONFLICT (id) DO UPDATE
SET status = EXCLUDED.status,
    scopes = EXCLUDED.scopes,
    last_synced_at = EXCLUDED.last_synced_at,
    last_health_check_at = EXCLUDED.last_health_check_at,
    default_repository_id = EXCLUDED.default_repository_id,
    updated_at = EXCLUDED.updated_at;

INSERT INTO pagerduty_service_repo_mappings (
  id, org_id, pagerduty_integration_id, pagerduty_service_id,
  pagerduty_service_name, pagerduty_team_id, repository_id, base_branch,
  enabled, created_by, created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000811'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000810'::uuid,
  'PD-SVC-PREVIEW',
  'Preview Gateway',
  'PD-TEAM-PLATFORM',
  '00000000-0000-4000-a000-000000000100'::uuid,
  'main',
  true,
  '00000000-0000-4000-a000-000000000002'::uuid,
  now() - interval '17 days',
  now() - interval '1 day'
)
ON CONFLICT (id) DO UPDATE
SET pagerduty_service_name = EXCLUDED.pagerduty_service_name,
    repository_id = EXCLUDED.repository_id,
    enabled = EXCLUDED.enabled,
    updated_at = EXCLUDED.updated_at;

INSERT INTO linear_team_repo_mappings (
  id, org_id, linear_team_id, linear_project_id, repository_id,
  default_branch, priority, created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000820'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'LIN-TEAM-PLATFORM',
    NULL,
    '00000000-0000-4000-a000-000000000100'::uuid,
    'main',
    0,
    now() - interval '16 days',
    now() - interval '2 days'
  ),
  (
    '00000000-0000-4000-a000-000000000821'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'LIN-TEAM-INTEGRATIONS',
    'LIN-PROJ-WEBHOOKS',
    '00000000-0000-4000-a000-000000000101'::uuid,
    'main',
    5,
    now() - interval '16 days',
    now() - interval '2 days'
  )
ON CONFLICT (id) DO UPDATE
SET repository_id = EXCLUDED.repository_id,
    default_branch = EXCLUDED.default_branch,
    priority = EXCLUDED.priority,
    updated_at = EXCLUDED.updated_at;

INSERT INTO linear_user_links (
  id, org_id, integration_id, user_id, linear_workspace_id, linear_user_id,
  linear_email, linear_display_name, source, linked_at, created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000822'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000011'::uuid,
    '00000000-0000-4000-a000-000000000003'::uuid,
    'LIN-WS-DEMO',
    'LIN-USER-MEMBER',
    'preview-member@143.dev',
    'Preview Member',
    'email_match',
    now() - interval '10 days',
    now() - interval '10 days',
    now() - interval '10 days'
  ),
  (
    '00000000-0000-4000-a000-000000000823'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000011'::uuid,
    '00000000-0000-4000-a000-000000000004'::uuid,
    'LIN-WS-DEMO',
    'LIN-USER-BUILDER',
    'preview-builder@143.dev',
    'Preview Builder',
    'admin_linked',
    now() - interval '11 days',
    now() - interval '11 days',
    now() - interval '11 days'
  )
ON CONFLICT (id) DO UPDATE
SET user_id = EXCLUDED.user_id,
    linear_email = EXCLUDED.linear_email,
    linear_display_name = EXCLUDED.linear_display_name,
    source = EXCLUDED.source,
    linked_at = EXCLUDED.linked_at,
    updated_at = EXCLUDED.updated_at;

INSERT INTO issues (
  id, org_id, external_id, source, source_integration_id, repository_id,
  title, description, raw_data, status, first_seen_at, last_seen_at,
  occurrence_count, affected_customer_count, severity, tags, fingerprint,
  project_id, created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000605'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'DEMO-SEN-201',
    'sentry',
    '00000000-0000-4000-a000-000000000012'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    'Auth callback panics when session cookie expires',
    'Synthetic Sentry issue: expired login callbacks should return a recoverable auth state instead of a panic.',
    '{"issue_id":"DEMO-SEN-201","project":"143-api","environment":"production","event_count":42,"culprit":"internal/api/handlers/auth.go"}'::jsonb,
    'open',
    now() - interval '2 days',
    now() - interval '8 minutes',
    42,
    9,
    'critical',
    ARRAY['sentry','auth','panic'],
    'demo:sentry:auth-callback-expired-cookie',
    '00000000-0000-4000-a000-000000000200'::uuid,
    now() - interval '2 days',
    now() - interval '8 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000606'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'DEMO-SEN-202',
    'sentry',
    '00000000-0000-4000-a000-000000000012'::uuid,
    '00000000-0000-4000-a000-000000000101'::uuid,
    'Webhook payload parser drops nested retry metadata',
    'Synthetic Sentry issue: retry metadata should survive nested provider payload parsing.',
    '{"issue_id":"DEMO-SEN-202","project":"example-service","environment":"staging","event_count":17,"culprit":"src/routes/webhooks.ts"}'::jsonb,
    'triaged',
    now() - interval '4 days',
    now() - interval '45 minutes',
    17,
    3,
    'medium',
    ARRAY['sentry','webhooks','parser'],
    'demo:sentry:webhook-retry-metadata',
    '00000000-0000-4000-a000-000000000201'::uuid,
    now() - interval '4 days',
    now() - interval '45 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000607'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'PD-DEMO-901',
    'pagerduty',
    '00000000-0000-4000-a000-000000000014'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    'Preview gateway p95 latency above paging threshold',
    'Synthetic PagerDuty incident: preview gateway latency breached the alert threshold during a deploy window.',
    '{"incident_id":"PD-DEMO-901","incident_number":901,"service":"Preview Gateway","urgency":"high","status":"acknowledged"}'::jsonb,
    'in_progress',
    now() - interval '6 hours',
    now() - interval '6 minutes',
    31,
    5,
    'high',
    ARRAY['pagerduty','preview','latency'],
    'demo:pagerduty:preview-gateway-latency',
    '00000000-0000-4000-a000-000000000200'::uuid,
    now() - interval '6 hours',
    now() - interval '6 minutes'
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
    updated_at = EXCLUDED.updated_at;

INSERT INTO priority_scores (
  id, issue_id, org_id, score, customer_impact_score, severity_score,
  recency_score, revenue_risk_score, direction_alignment, factors,
  eligible_for_agent, computed_at
)
VALUES
  ('00000000-0000-4000-a000-000000000615'::uuid, '00000000-0000-4000-a000-000000000605'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 96, 25, 35, 20, 6, 10, '{"signals":["critical_sentry","active_customers","recent_regression"]}'::jsonb, true, now() - interval '8 minutes'),
  ('00000000-0000-4000-a000-000000000616'::uuid, '00000000-0000-4000-a000-000000000606'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 64, 10, 20, 15, 4, 15, '{"signals":["triaged","provider_payload","integration_reliability"]}'::jsonb, true, now() - interval '45 minutes'),
  ('00000000-0000-4000-a000-000000000617'::uuid, '00000000-0000-4000-a000-000000000607'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 91, 18, 30, 20, 8, 15, '{"signals":["pagerduty","high_urgency","linked_service"]}'::jsonb, true, now() - interval '6 minutes')
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
  ('00000000-0000-4000-a000-000000000625'::uuid, '00000000-0000-4000-a000-000000000605'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 3, 'moderate', 0.84, 'backend_auth', 'Auth callback handling spans middleware, session lookup, and redirect state.', ARRAY['internal/api/handlers/auth.go','internal/api/middleware/auth.go','internal/db/auth_session_store.go'], 5600, 'seeded-estimator', now() - interval '8 minutes', now() - interval '8 minutes'),
  ('00000000-0000-4000-a000-000000000626'::uuid, '00000000-0000-4000-a000-000000000606'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 2, 'simple', 0.88, 'provider_payload', 'Parser fix is localized but needs regression coverage for nested retry metadata.', ARRAY['src/routes/webhooks.ts','src/lib/provider-payloads.ts'], 3200, 'seeded-estimator', now() - interval '45 minutes', now() - interval '45 minutes'),
  ('00000000-0000-4000-a000-000000000627'::uuid, '00000000-0000-4000-a000-000000000607'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 4, 'complex', 0.76, 'preview_runtime', 'Latency investigation spans gateway timeouts, preview warmup, and status reporting.', ARRAY['internal/services/preview','internal/services/gateway','deploy'], 8200, 'seeded-estimator', now() - interval '6 minutes', now() - interval '6 minutes')
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

INSERT INTO pagerduty_incidents (
  id, org_id, pagerduty_integration_id, issue_id, incident_id,
  incident_number, html_url, title, status, urgency, priority_id,
  priority_name, service_id, service_name, escalation_policy_id,
  escalation_policy_name, incident_type, assigned_user_ids, team_ids,
  latest_note, raw_data, triggered_at, acknowledged_at, resolved_at,
  last_event_at, created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000812'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000810'::uuid,
  '00000000-0000-4000-a000-000000000607'::uuid,
  'PD-DEMO-901',
  901,
  NULL,
  'Preview gateway p95 latency above paging threshold',
  'acknowledged',
  'high',
  'P2',
  'High',
  'PD-SVC-PREVIEW',
  'Preview Gateway',
  'PD-ESC-PLATFORM',
  'Platform On-call',
  'incident',
  ARRAY['PD-USER-ONCALL'],
  ARRAY['PD-TEAM-PLATFORM'],
  'Synthetic incident: agent is checking gateway timeout and warmup paths.',
  '{"service":{"id":"PD-SVC-PREVIEW","summary":"Preview Gateway"},"incident_key":"seeded-preview-latency"}'::jsonb,
  now() - interval '6 hours',
  now() - interval '5 hours' - interval '40 minutes',
  NULL,
  now() - interval '6 minutes',
  now() - interval '6 hours',
  now() - interval '6 minutes'
)
ON CONFLICT (id) DO UPDATE
SET issue_id = EXCLUDED.issue_id,
    status = EXCLUDED.status,
    urgency = EXCLUDED.urgency,
    latest_note = EXCLUDED.latest_note,
    raw_data = EXCLUDED.raw_data,
    last_event_at = EXCLUDED.last_event_at,
    updated_at = EXCLUDED.updated_at;

INSERT INTO pagerduty_inbound_events (
  id, org_id, pagerduty_integration_id, provider_event_id, event_type,
  resource_type, incident_id, occurred_at, payload, headers, status,
  created_at, processed_at
)
VALUES (
  '00000000-0000-4000-a000-000000000813'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000810'::uuid,
  'pd-demo-event-901',
  'incident.triggered',
  'incident',
  'PD-DEMO-901',
  now() - interval '6 hours',
  '{"incident_id":"PD-DEMO-901","service_id":"PD-SVC-PREVIEW","urgency":"high"}'::jsonb,
  '{"x-seeded":"true"}'::jsonb,
  'processed',
  now() - interval '6 hours',
  now() - interval '6 hours' + interval '10 seconds'
)
ON CONFLICT (id) DO UPDATE
SET payload = EXCLUDED.payload,
    status = EXCLUDED.status,
    processed_at = EXCLUDED.processed_at;

INSERT INTO automations (
  id, org_id, repository_id, name, goal, scope, icon_type, icon_value,
  agent_type, model_override, reasoning_effort, execution_mode,
  max_concurrent, base_branch, identity_scope, pre_pr_review_loops,
  schedule_type, interval_value, interval_unit, interval_run_at,
  cron_expression, timezone, github_event_triggers, github_event_filters,
  next_run_at, last_run_at, enabled, created_by, paused_by, paused_at,
  priority, external_metadata, created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000830'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    'PagerDuty incident triage',
    'When a production incident maps to a repository, inspect the linked service, propose a small fix, and keep the incident thread updated with evidence.',
    'Preview gateway, auth callbacks, and provider webhooks only.',
    'emoji',
    'P',
    'codex',
    NULL,
    'high',
    'sequential',
    1,
    'main',
    'org',
    1,
    'none',
    NULL,
    NULL,
    NULL,
    NULL,
    'UTC',
    ARRAY[]::text[],
    '{}'::jsonb,
    NULL,
    now() - interval '6 minutes',
    true,
    '00000000-0000-4000-a000-000000000002'::uuid,
    NULL,
    NULL,
    90,
    '{"provider":"pagerduty","seeded":true}'::jsonb,
    now() - interval '12 days',
    now() - interval '6 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000831'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000101'::uuid,
    'Linear backlog groomer',
    'Review eligible Linear and Sentry issues, cluster related provider signals, and delegate the safest high-impact items to agents.',
    'Example service integration backlog and webhook reliability work.',
    'emoji',
    'L',
    'pm_agent',
    NULL,
    'medium',
    'dependency_graph',
    2,
    'main',
    'org',
    0,
    'interval',
    1,
    'days',
    '09:00',
    NULL,
    'America/Los_Angeles',
    ARRAY[]::text[],
    '{}'::jsonb,
    date_trunc('day', now()) + interval '1 day' + interval '9 hours',
    now() - interval '18 hours',
    true,
    '00000000-0000-4000-a000-000000000002'::uuid,
    NULL,
    NULL,
    70,
    '{"provider":"linear","seeded":true}'::jsonb,
    now() - interval '10 days',
    now() - interval '18 hours'
  ),
  (
    '00000000-0000-4000-a000-000000000832'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    'Code review comment responder',
    'Watch review-comment events, reproduce the reported concern, and update the PR with a focused fix or a clear explanation.',
    'Small review comments on assembledhq/143 pull requests.',
    'emoji',
    'R',
    'codex',
    NULL,
    'medium',
    'sequential',
    1,
    'main',
    'personal',
    2,
    'none',
    NULL,
    NULL,
    NULL,
    NULL,
    'UTC',
    ARRAY['github.pull_request.opened','github.pull_request_review_comment.created'],
    '{"repositories":["assembledhq/143"],"labels":["needs-agent"]}'::jsonb,
    NULL,
    now() - interval '2 days',
    false,
    '00000000-0000-4000-a000-000000000002'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    now() - interval '2 hours',
    60,
    '{"provider":"github","seeded":true}'::jsonb,
    now() - interval '9 days',
    now() - interval '2 hours'
  )
ON CONFLICT (id) DO UPDATE
SET name = EXCLUDED.name,
    goal = EXCLUDED.goal,
    scope = EXCLUDED.scope,
    icon_type = EXCLUDED.icon_type,
    icon_value = EXCLUDED.icon_value,
    agent_type = EXCLUDED.agent_type,
    reasoning_effort = EXCLUDED.reasoning_effort,
    execution_mode = EXCLUDED.execution_mode,
    max_concurrent = EXCLUDED.max_concurrent,
    base_branch = EXCLUDED.base_branch,
    identity_scope = EXCLUDED.identity_scope,
    pre_pr_review_loops = EXCLUDED.pre_pr_review_loops,
    schedule_type = EXCLUDED.schedule_type,
    interval_value = EXCLUDED.interval_value,
    interval_unit = EXCLUDED.interval_unit,
    interval_run_at = EXCLUDED.interval_run_at,
    timezone = EXCLUDED.timezone,
    github_event_triggers = EXCLUDED.github_event_triggers,
    github_event_filters = EXCLUDED.github_event_filters,
    next_run_at = EXCLUDED.next_run_at,
    last_run_at = EXCLUDED.last_run_at,
    enabled = EXCLUDED.enabled,
    paused_by = EXCLUDED.paused_by,
    paused_at = EXCLUDED.paused_at,
    priority = EXCLUDED.priority,
    external_metadata = EXCLUDED.external_metadata,
    updated_at = EXCLUDED.updated_at;

INSERT INTO automation_event_triggers (
  id, org_id, automation_id, provider, event_types, filter,
  repository_id, enabled, created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000833'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000830'::uuid,
    'pagerduty',
    ARRAY['incident.triggered','incident.reopened'],
    '{"service_ids":["PD-SVC-PREVIEW"],"urgency":["high"]}'::jsonb,
    '00000000-0000-4000-a000-000000000100'::uuid,
    true,
    now() - interval '12 days',
    now() - interval '6 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000834'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000831'::uuid,
    'linear',
    ARRAY['issue.created','issue.updated'],
    '{"team_ids":["LIN-TEAM-INTEGRATIONS"],"labels":["ready-for-agent","bug"]}'::jsonb,
    '00000000-0000-4000-a000-000000000101'::uuid,
    true,
    now() - interval '10 days',
    now() - interval '18 hours'
  ),
  (
    '00000000-0000-4000-a000-000000000835'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000832'::uuid,
    'github',
    ARRAY['github.pull_request.opened','github.pull_request_review_comment.created'],
    '{"branches":["main"],"labels":["needs-agent"]}'::jsonb,
    '00000000-0000-4000-a000-000000000100'::uuid,
    false,
    now() - interval '9 days',
    now() - interval '2 hours'
  )
ON CONFLICT (id) DO UPDATE
SET event_types = EXCLUDED.event_types,
    filter = EXCLUDED.filter,
    repository_id = EXCLUDED.repository_id,
    enabled = EXCLUDED.enabled,
    updated_at = EXCLUDED.updated_at;

INSERT INTO automation_runs (
  id, automation_id, org_id, triggered_at, triggered_by,
  triggered_by_user_id, scheduled_time, trigger_id, provider,
  provider_event_id, trigger_context, goal_snapshot, config_snapshot,
  status, capability_snapshot, completed_at, result_summary,
  created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000836'::uuid,
    '00000000-0000-4000-a000-000000000830'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    now() - interval '5 hours',
    'provider_event',
    NULL,
    NULL,
    '00000000-0000-4000-a000-000000000833'::uuid,
    'pagerduty',
    'pd-demo-event-900',
    '{"incident_id":"PD-DEMO-900","service_id":"PD-SVC-PREVIEW"}'::jsonb,
    'When a production incident maps to a repository, inspect the linked service, propose a small fix, and keep the incident thread updated with evidence.',
    '{"repository":"assembledhq/143","base_branch":"main","trigger":"pagerduty"}'::jsonb,
    'completed',
    '[{"capability_id":"pagerduty","access_level":"read"},{"capability_id":"github","access_level":"write"}]'::jsonb,
    now() - interval '4 hours' - interval '40 minutes',
    'Confirmed a synthetic timeout regression and opened a follow-up issue for gateway warmup telemetry.',
    now() - interval '5 hours',
    now() - interval '4 hours' - interval '40 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000837'::uuid,
    '00000000-0000-4000-a000-000000000830'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    now() - interval '6 minutes',
    'provider_event',
    NULL,
    NULL,
    '00000000-0000-4000-a000-000000000833'::uuid,
    'pagerduty',
    'pd-demo-event-901',
    '{"incident_id":"PD-DEMO-901","service_id":"PD-SVC-PREVIEW","urgency":"high"}'::jsonb,
    'When a production incident maps to a repository, inspect the linked service, propose a small fix, and keep the incident thread updated with evidence.',
    '{"repository":"assembledhq/143","base_branch":"main","trigger":"pagerduty"}'::jsonb,
    'running',
    '[{"capability_id":"pagerduty","access_level":"read"},{"capability_id":"github","access_level":"write"}]'::jsonb,
    NULL,
    NULL,
    now() - interval '6 minutes',
    now() - interval '6 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000838'::uuid,
    '00000000-0000-4000-a000-000000000831'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    now() - interval '18 hours',
    'schedule',
    NULL,
    date_trunc('day', now()) - interval '15 hours',
    NULL,
    NULL,
    NULL,
    '{"schedule":"daily","window":"business_hours"}'::jsonb,
    'Review eligible Linear and Sentry issues, cluster related provider signals, and delegate the safest high-impact items to agents.',
    '{"repository":"assembledhq/example-service","base_branch":"main","trigger":"schedule"}'::jsonb,
    'completed',
    '[{"capability_id":"linear","access_level":"read"},{"capability_id":"github","access_level":"write"}]'::jsonb,
    now() - interval '17 hours' - interval '42 minutes',
    'Clustered two webhook issues and delegated one parser fix to the queue.',
    now() - interval '18 hours',
    now() - interval '17 hours' - interval '42 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000839'::uuid,
    '00000000-0000-4000-a000-000000000831'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    now() - interval '2 days',
    'schedule',
    NULL,
    date_trunc('day', now()) - interval '2 days' + interval '9 hours',
    NULL,
    NULL,
    NULL,
    '{"schedule":"daily","window":"business_hours"}'::jsonb,
    'Review eligible Linear and Sentry issues, cluster related provider signals, and delegate the safest high-impact items to agents.',
    '{"repository":"assembledhq/example-service","base_branch":"main","trigger":"schedule"}'::jsonb,
    'failed',
    '[{"capability_id":"linear","access_level":"read"},{"capability_id":"github","access_level":"write"}]'::jsonb,
    now() - interval '2 days' + interval '16 minutes',
    'Synthetic failure: Linear webhook payload was missing a team mapping before the seed added one.',
    now() - interval '2 days',
    now() - interval '2 days' + interval '16 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000840'::uuid,
    '00000000-0000-4000-a000-000000000832'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    now() - interval '2 days',
    'github',
    NULL,
    NULL,
    '00000000-0000-4000-a000-000000000835'::uuid,
    'github',
    'gh-demo-review-comment-42',
    '{"repository":"assembledhq/143","pull_request":42,"comment_path":"frontend/src/components/preview/PreviewStatus.tsx"}'::jsonb,
    'Watch review-comment events, reproduce the reported concern, and update the PR with a focused fix or a clear explanation.',
    '{"repository":"assembledhq/143","base_branch":"main","trigger":"github"}'::jsonb,
    'completed_noop',
    '[{"capability_id":"github","access_level":"read"}]'::jsonb,
    now() - interval '2 days' + interval '8 minutes',
    'Automation was paused after deduping the review comment against an active code-review session.',
    now() - interval '2 days',
    now() - interval '2 days' + interval '8 minutes'
  )
ON CONFLICT (id) DO UPDATE
SET triggered_at = EXCLUDED.triggered_at,
    triggered_by = EXCLUDED.triggered_by,
    triggered_by_user_id = EXCLUDED.triggered_by_user_id,
    scheduled_time = EXCLUDED.scheduled_time,
    trigger_id = EXCLUDED.trigger_id,
    provider = EXCLUDED.provider,
    provider_event_id = EXCLUDED.provider_event_id,
    trigger_context = EXCLUDED.trigger_context,
    goal_snapshot = EXCLUDED.goal_snapshot,
    config_snapshot = EXCLUDED.config_snapshot,
    status = EXCLUDED.status,
    capability_snapshot = EXCLUDED.capability_snapshot,
    completed_at = EXCLUDED.completed_at,
    result_summary = EXCLUDED.result_summary,
    updated_at = EXCLUDED.updated_at;

INSERT INTO agent_capability_policies (
  id, org_id, policy_type, automation_id, name, active, created_by, created_at
)
VALUES
  ('00000000-0000-4000-a000-000000000841'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 'automation', '00000000-0000-4000-a000-000000000830'::uuid, 'PagerDuty triage capabilities', true, '00000000-0000-4000-a000-000000000002'::uuid, now() - interval '12 days'),
  ('00000000-0000-4000-a000-000000000842'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 'automation', '00000000-0000-4000-a000-000000000831'::uuid, 'Backlog grooming capabilities', true, '00000000-0000-4000-a000-000000000002'::uuid, now() - interval '10 days'),
  ('00000000-0000-4000-a000-000000000843'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, 'automation', '00000000-0000-4000-a000-000000000832'::uuid, 'Review response capabilities', true, '00000000-0000-4000-a000-000000000002'::uuid, now() - interval '9 days')
ON CONFLICT (id) DO UPDATE
SET name = EXCLUDED.name,
    active = EXCLUDED.active;

INSERT INTO agent_capability_policy_grants (
  id, org_id, policy_id, capability_id, access_level, enabled,
  config, created_by, created_at
)
VALUES
  ('00000000-0000-4000-a000-000000000844'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000841'::uuid, 'pagerduty', 'read', true, '{"services":["PD-SVC-PREVIEW"]}'::jsonb, '00000000-0000-4000-a000-000000000002'::uuid, now() - interval '12 days'),
  ('00000000-0000-4000-a000-000000000845'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000841'::uuid, 'github', 'write', true, '{"repositories":["assembledhq/143"]}'::jsonb, '00000000-0000-4000-a000-000000000002'::uuid, now() - interval '12 days'),
  ('00000000-0000-4000-a000-000000000846'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000842'::uuid, 'linear', 'read', true, '{"teams":["LIN-TEAM-INTEGRATIONS"]}'::jsonb, '00000000-0000-4000-a000-000000000002'::uuid, now() - interval '10 days'),
  ('00000000-0000-4000-a000-000000000847'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000842'::uuid, 'github', 'write', true, '{"repositories":["assembledhq/example-service"]}'::jsonb, '00000000-0000-4000-a000-000000000002'::uuid, now() - interval '10 days'),
  ('00000000-0000-4000-a000-000000000848'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000843'::uuid, 'github', 'read', true, '{"repositories":["assembledhq/143"]}'::jsonb, '00000000-0000-4000-a000-000000000002'::uuid, now() - interval '9 days')
ON CONFLICT (id) DO UPDATE
SET access_level = EXCLUDED.access_level,
    enabled = EXCLUDED.enabled,
    config = EXCLUDED.config;

INSERT INTO sessions (
  id, org_id, repository_id, triggered_by_user_id, title, working_branch,
  target_branch, agent_type, status, autonomy_level, token_mode,
  sandbox_state, current_turn, last_activity_at, started_at, completed_at,
  created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000305'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    NULL,
    'Triage Preview Gateway latency incident',
    'auto/pd-preview-gateway-latency',
    'main',
    'codex',
    'running',
    'semi',
    'high',
    'running',
    2,
    now() - interval '1 minute',
    now() - interval '6 minutes',
    NULL,
    now() - interval '6 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000306'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    NULL,
    'Review PR 42 preview teardown',
    NULL,
    'main',
    'codex',
    'completed',
    'semi',
    'low',
    'destroyed',
    1,
    now() - interval '26 minutes',
    now() - interval '44 minutes',
    now() - interval '26 minutes',
    now() - interval '44 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000307'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    NULL,
    'Run second-pass code review for PR 42',
    NULL,
    'main',
    'codex',
    'running',
    'semi',
    'low',
    'running',
    1,
    now() - interval '4 minutes',
    now() - interval '12 minutes',
    NULL,
    now() - interval '12 minutes'
  )
ON CONFLICT (id) DO UPDATE
SET title = EXCLUDED.title,
    working_branch = EXCLUDED.working_branch,
    target_branch = EXCLUDED.target_branch,
    status = EXCLUDED.status,
    sandbox_state = EXCLUDED.sandbox_state,
    current_turn = EXCLUDED.current_turn,
    last_activity_at = EXCLUDED.last_activity_at,
    started_at = EXCLUDED.started_at,
    completed_at = EXCLUDED.completed_at;

UPDATE sessions
SET origin = CASE id
    WHEN '00000000-0000-4000-a000-000000000305'::uuid THEN 'automation'
    WHEN '00000000-0000-4000-a000-000000000306'::uuid THEN 'code_review'
    WHEN '00000000-0000-4000-a000-000000000307'::uuid THEN 'code_review'
    ELSE origin
  END,
  interaction_mode = CASE id
    WHEN '00000000-0000-4000-a000-000000000305'::uuid THEN 'single_run'
    ELSE 'single_run'
  END,
  validation_policy = CASE id
    WHEN '00000000-0000-4000-a000-000000000305'::uuid THEN 'on_turn_complete'
    ELSE 'skip'
  END,
  model_used = CASE id
    WHEN '00000000-0000-4000-a000-000000000305'::uuid THEN 'gpt-5.1-codex-max'
    ELSE 'gpt-5.1-codex-max'
  END,
  revision_context = CASE id
    WHEN '00000000-0000-4000-a000-000000000306'::uuid THEN '{"pull_request_author":"preview-builder","pull_request_title":"Ship PR preview auto-teardown"}'::jsonb
    WHEN '00000000-0000-4000-a000-000000000307'::uuid THEN '{"pull_request_author":"preview-builder","pull_request_title":"Ship PR preview auto-teardown","review_pass":"second"}'::jsonb
    ELSE COALESCE(revision_context, '{}'::jsonb)
  END,
  input_manifest = CASE id
    WHEN '00000000-0000-4000-a000-000000000305'::uuid THEN '{"source":"automation_run","automation_run_id":"00000000-0000-4000-a000-000000000837","issue_ids":["00000000-0000-4000-a000-000000000607","00000000-0000-4000-a000-000000000605"]}'::jsonb
    ELSE COALESCE(input_manifest, '{}'::jsonb)
  END
WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
  AND id IN (
    '00000000-0000-4000-a000-000000000305'::uuid,
    '00000000-0000-4000-a000-000000000306'::uuid,
    '00000000-0000-4000-a000-000000000307'::uuid
  );

INSERT INTO session_execution_metadata (
  session_id, org_id, capability_snapshot, git_identity_source,
  git_identity_user_id, created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000305'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '[{"capability_id":"pagerduty","access_level":"read"},{"capability_id":"github","access_level":"write"}]'::jsonb,
    'org',
    NULL,
    now() - interval '6 minutes',
    now() - interval '6 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000306'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '[{"capability_id":"github","access_level":"read"}]'::jsonb,
    'org',
    NULL,
    now() - interval '44 minutes',
    now() - interval '26 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000307'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '[{"capability_id":"github","access_level":"read"}]'::jsonb,
    'org',
    NULL,
    now() - interval '12 minutes',
    now() - interval '4 minutes'
  )
ON CONFLICT (session_id) DO UPDATE
SET capability_snapshot = EXCLUDED.capability_snapshot,
    git_identity_source = EXCLUDED.git_identity_source,
    git_identity_user_id = EXCLUDED.git_identity_user_id,
    updated_at = EXCLUDED.updated_at;

INSERT INTO session_threads (
  id, session_id, org_id, agent_type, model_override, label, instructions,
  file_scope, status, agent_session_id, current_turn, last_activity_at,
  result_summary, diff, failure_explanation, failure_category, started_at,
  completed_at, created_at, base_snapshot_key, cost_cents, pending_message_count,
  created_by_source
)
VALUES
  (
    '00000000-0000-4000-a000-000000000705'::uuid,
    '00000000-0000-4000-a000-000000000305'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'codex',
    NULL,
    'Incident triage',
    'Synthetic thread: inspect preview gateway latency and identify the smallest safe fix.',
    ARRAY['internal/services/preview','internal/services/gateway','deploy'],
    'running',
    'seeded-thread-305',
    2,
    now() - interval '1 minute',
    NULL,
    NULL,
    NULL,
    NULL,
    now() - interval '6 minutes',
    NULL,
    now() - interval '6 minutes',
    'seeded/snapshots/session-305/base',
    9.40,
    0,
    'seed'
  ),
  (
    '00000000-0000-4000-a000-000000000706'::uuid,
    '00000000-0000-4000-a000-000000000306'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'codex',
    NULL,
    'Code review summary',
    'Synthetic thread: review PR 42 for risk, tests, and preview teardown behavior.',
    ARRAY['internal/services/preview','frontend/src/components/preview'],
    'completed',
    'seeded-thread-306',
    1,
    now() - interval '26 minutes',
    'Posted a comment-only review with one selected inline finding.',
    NULL,
    NULL,
    NULL,
    now() - interval '44 minutes',
    now() - interval '26 minutes',
    now() - interval '44 minutes',
    'seeded/snapshots/session-306/base',
    14.25,
    0,
    'seed'
  ),
  (
    '00000000-0000-4000-a000-000000000707'::uuid,
    '00000000-0000-4000-a000-000000000307'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'codex',
    NULL,
    'Second-pass code review',
    'Synthetic thread: compare reviewer findings and prepare orchestrator decision.',
    ARRAY['internal/services/github','internal/db/code_reviews.go'],
    'running',
    'seeded-thread-307',
    1,
    now() - interval '4 minutes',
    NULL,
    NULL,
    NULL,
    NULL,
    now() - interval '12 minutes',
    NULL,
    now() - interval '12 minutes',
    'seeded/snapshots/session-307/base',
    6.80,
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
    started_at = EXCLUDED.started_at,
    completed_at = EXCLUDED.completed_at,
    base_snapshot_key = EXCLUDED.base_snapshot_key,
    cost_cents = EXCLUDED.cost_cents,
    pending_message_count = EXCLUDED.pending_message_count;

INSERT INTO session_automation_links (
  session_id, org_id, automation_run_id, created_at
)
VALUES (
  '00000000-0000-4000-a000-000000000305'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000837'::uuid,
  now() - interval '6 minutes'
)
ON CONFLICT (session_id) DO UPDATE
SET automation_run_id = EXCLUDED.automation_run_id,
    created_at = EXCLUDED.created_at;

INSERT INTO session_issue_links (
  id, org_id, session_id, issue_id, role, position, added_by_user_id, created_at
)
VALUES
  ('00000000-0000-4000-a000-000000000635'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000305'::uuid, '00000000-0000-4000-a000-000000000607'::uuid, 'primary', 0, NULL, now() - interval '6 minutes'),
  ('00000000-0000-4000-a000-000000000636'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000305'::uuid, '00000000-0000-4000-a000-000000000605'::uuid, 'related', 1, NULL, now() - interval '6 minutes'),
  ('00000000-0000-4000-a000-000000000637'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000302'::uuid, '00000000-0000-4000-a000-000000000606'::uuid, 'related', 1, '00000000-0000-4000-a000-000000000002'::uuid, now() - interval '45 minutes')
ON CONFLICT (session_id, issue_id) DO UPDATE
SET role = EXCLUDED.role,
    position = EXCLUDED.position,
    added_by_user_id = EXCLUDED.added_by_user_id;

INSERT INTO session_turn_issue_snapshots (
  id, org_id, session_id, turn_number, linked_issues, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000643'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000305'::uuid,
    1,
    '[{"id":"00000000-0000-4000-a000-000000000607","role":"primary","title":"Preview gateway p95 latency above paging threshold","source":"pagerduty","severity":"high"},{"id":"00000000-0000-4000-a000-000000000605","role":"related","title":"Auth callback panics when session cookie expires","source":"sentry","severity":"critical"}]'::jsonb,
    now() - interval '5 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000644'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000302'::uuid,
    2,
    '[{"id":"00000000-0000-4000-a000-000000000602","role":"primary","title":"Preview cold starts exceed target on first request","source":"manual","severity":"high"},{"id":"00000000-0000-4000-a000-000000000606","role":"related","title":"Webhook payload parser drops nested retry metadata","source":"sentry","severity":"medium"}]'::jsonb,
    now() - interval '45 minutes'
  )
ON CONFLICT (session_id, turn_number) DO UPDATE
SET linked_issues = EXCLUDED.linked_issues,
    created_at = EXCLUDED.created_at;

INSERT INTO slack_session_links (
  id, org_id, session_id, slack_installation_id, slack_team_id,
  slack_channel_id, slack_thread_ts, slack_root_ts, slack_message_permalink,
  slack_user_id, mapped_user_id, team_session, latest_status_message_ts,
  final_message_ts, latest_progress_kind, created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000809'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000303'::uuid,
  '00000000-0000-4000-a000-000000000800'::uuid,
  'T143DEMO',
  'C143ENG',
  '1780000000.000100',
  '1780000000.000100',
  '',
  'U143MEMBER',
  '00000000-0000-4000-a000-000000000003'::uuid,
  true,
  '1780000018.000200',
  NULL,
  'awaiting_input',
  now() - interval '55 minutes',
  now() - interval '18 minutes'
)
ON CONFLICT (id) DO UPDATE
SET latest_status_message_ts = EXCLUDED.latest_status_message_ts,
    latest_progress_kind = EXCLUDED.latest_progress_kind,
    updated_at = EXCLUDED.updated_at;

INSERT INTO slack_inbound_events (
  id, org_id, slack_installation_id, slack_event_id, slack_team_id,
  event_type, channel_id, user_id, event_ts, payload, status,
  received_at, processed_at
)
VALUES (
  '00000000-0000-4000-a000-000000000814'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000800'::uuid,
  'Ev143SeededMention',
  'T143DEMO',
  'app_mention',
  'C143ENG',
  'U143MEMBER',
  '1780000000.000100',
  '{"text":"Can an agent confirm the archive filter behavior before we change it?","channel":"C143ENG","thread_ts":"1780000000.000100"}'::jsonb,
  'processed',
  now() - interval '55 minutes',
  now() - interval '55 minutes' + interval '8 seconds'
)
ON CONFLICT (id) DO UPDATE
SET payload = EXCLUDED.payload,
    status = EXCLUDED.status,
    processed_at = EXCLUDED.processed_at;

INSERT INTO slack_outbound_messages (
  id, org_id, slack_session_link_id, slack_team_id, slack_channel_id,
  slack_message_ts, message_kind, status, last_payload_hash,
  created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000815'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000809'::uuid,
  'T143DEMO',
  'C143ENG',
  '1780000018.000200',
  'status_update',
  'posted',
  'seeded-slack-status-303',
  now() - interval '18 minutes',
  now() - interval '18 minutes'
)
ON CONFLICT (id) DO UPDATE
SET message_kind = EXCLUDED.message_kind,
    status = EXCLUDED.status,
    last_payload_hash = EXCLUDED.last_payload_hash,
    updated_at = EXCLUDED.updated_at;

INSERT INTO linear_agent_sessions (
  id, org_id, integration_id, linear_agent_session_id, linear_issue_id,
  linear_issue_identifier, linear_app_user_id, linear_creator_user_id,
  session_id, state, last_event_received_at, created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000824'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000011'::uuid,
  'lin-agent-session-demo-101',
  'lin-issue-demo-101',
  'DEMO-LIN-101',
  'lin-app-user-143',
  'LIN-USER-MEMBER',
  '00000000-0000-4000-a000-000000000303'::uuid,
  'awaiting_input',
  now() - interval '18 minutes',
  now() - interval '55 minutes',
  now() - interval '18 minutes'
)
ON CONFLICT (id) DO UPDATE
SET session_id = EXCLUDED.session_id,
    state = EXCLUDED.state,
    last_event_received_at = EXCLUDED.last_event_received_at,
    updated_at = EXCLUDED.updated_at;

INSERT INTO linear_agent_activity_log (
  id, org_id, agent_session_row_id, idem_key, activity_type,
  linear_activity_id, created_at
)
VALUES
  ('00000000-0000-4000-a000-000000000825'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000824'::uuid, 'started', 'thought', 'lin-activity-demo-1', now() - interval '55 minutes'),
  ('00000000-0000-4000-a000-000000000826'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000824'::uuid, 'needs-product-confirmation', 'elicitation', 'lin-activity-demo-2', now() - interval '18 minutes')
ON CONFLICT (id) DO UPDATE
SET activity_type = EXCLUDED.activity_type,
    linear_activity_id = EXCLUDED.linear_activity_id;

INSERT INTO pm_documents (
  id, org_id, title, content, doc_type, sort_order, source_type,
  source_id, source_meta, last_synced_at, created_by, created_at,
  updated_at, active, logical_id, content_hash
)
VALUES
  (
    '00000000-0000-4000-a000-000000000860'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'Preview reliability context',
    $pm$# Preview reliability context

Focus on issues that affect a reviewer getting from session output to a live preview. Prioritize clear status, fast recovery, and trustworthy cleanup.

## Non-goals

- Do not add provider-specific secrets to preview data.
- Do not make broad runtime changes without a targeted regression test.
$pm$,
    'context',
    0,
    'manual',
    'seeded-preview-context',
    '{"owner":"platform","seeded":true}'::jsonb,
    now() - interval '3 days',
    '00000000-0000-4000-a000-000000000002'::uuid,
    now() - interval '3 days',
    now() - interval '3 days',
    true,
    '00000000-0000-4000-a000-000000000861'::uuid,
    'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
  ),
  (
    '00000000-0000-4000-a000-000000000862'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'Q3 agent surface roadmap',
    $pm$# Q3 agent surface roadmap

1. Make autopilot explain why an issue was selected.
2. Show automation runs with enough provider context to debug a trigger.
3. Make code-review findings actionable without hiding reviewer disagreement.
$pm$,
    'roadmap',
    1,
    'autogenerated',
    'seeded-q3-roadmap',
    '{"generator":"pm-agent","seeded":true}'::jsonb,
    now() - interval '2 days',
    '00000000-0000-4000-a000-000000000002'::uuid,
    now() - interval '2 days',
    now() - interval '2 days',
    true,
    '00000000-0000-4000-a000-000000000863'::uuid,
    'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
  )
ON CONFLICT (id) DO UPDATE
SET title = EXCLUDED.title,
    content = EXCLUDED.content,
    doc_type = EXCLUDED.doc_type,
    sort_order = EXCLUDED.sort_order,
    source_type = EXCLUDED.source_type,
    source_id = EXCLUDED.source_id,
    source_meta = EXCLUDED.source_meta,
    last_synced_at = EXCLUDED.last_synced_at,
    active = EXCLUDED.active,
    content_hash = EXCLUDED.content_hash,
    updated_at = EXCLUDED.updated_at;

INSERT INTO pm_document_set_pins (id, org_id, created_at)
VALUES (
  '00000000-0000-4000-a000-000000000864'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  now() - interval '2 days'
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO pm_document_set_pin_members (pin_id, document_id)
VALUES
  ('00000000-0000-4000-a000-000000000864'::uuid, '00000000-0000-4000-a000-000000000860'::uuid),
  ('00000000-0000-4000-a000-000000000864'::uuid, '00000000-0000-4000-a000-000000000862'::uuid)
ON CONFLICT DO NOTHING;

INSERT INTO pm_plans (
  id, org_id, status, analysis, tasks, clusters, skipped_issues,
  issues_reviewed, product_context_snapshot, token_usage,
  triggered_by, created_at, completed_at
)
VALUES (
  '00000000-0000-4000-a000-000000000870'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  'completed',
  'Synthetic PM plan: prioritize the live incident, batch related preview reliability work, and leave the lower-risk parser fix for the next automation window.',
  '[{"issue_id":"00000000-0000-4000-a000-000000000607","action":"delegate","reason":"High priority PagerDuty incident with repository mapping"},{"issue_id":"00000000-0000-4000-a000-000000000606","action":"queue","reason":"Simple parser fix with good testability"}]'::jsonb,
  '[{"name":"Preview reliability","issue_ids":["00000000-0000-4000-a000-000000000602","00000000-0000-4000-a000-000000000607"]},{"name":"Provider ingestion","issue_ids":["00000000-0000-4000-a000-000000000601","00000000-0000-4000-a000-000000000606"]}]'::jsonb,
  '[{"issue_id":"00000000-0000-4000-a000-000000000603","reason":"Already fixed"}]'::jsonb,
  8,
  '{"product":"143","current_focus":["preview reliability","provider-triggered automations","review quality gates"]}'::jsonb,
  '{"input_tokens":18200,"output_tokens":3600,"cost_usd":1.42}'::jsonb,
  'cron',
  now() - interval '18 hours',
  now() - interval '17 hours' - interval '52 minutes'
)
ON CONFLICT (id) DO UPDATE
SET status = EXCLUDED.status,
    analysis = EXCLUDED.analysis,
    tasks = EXCLUDED.tasks,
    clusters = EXCLUDED.clusters,
    skipped_issues = EXCLUDED.skipped_issues,
    issues_reviewed = EXCLUDED.issues_reviewed,
    product_context_snapshot = EXCLUDED.product_context_snapshot,
    token_usage = EXCLUDED.token_usage,
    completed_at = EXCLUDED.completed_at;

INSERT INTO pm_decision_log (
  id, org_id, plan_id, issue_id, decision, reasoning, outcome, created_at
)
VALUES
  ('00000000-0000-4000-a000-000000000871'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000870'::uuid, '00000000-0000-4000-a000-000000000607'::uuid, 'delegate', 'PagerDuty incident is high urgency, mapped to assembledhq/143, and has a bounded preview-gateway investigation path.', 'still_open', now() - interval '17 hours' - interval '50 minutes'),
  ('00000000-0000-4000-a000-000000000872'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000870'::uuid, '00000000-0000-4000-a000-000000000606'::uuid, 'delegate', 'Parser issue has clear reproduction data and low blast radius in example-service.', 'succeeded', now() - interval '17 hours' - interval '49 minutes'),
  ('00000000-0000-4000-a000-000000000873'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, '00000000-0000-4000-a000-000000000870'::uuid, '00000000-0000-4000-a000-000000000603'::uuid, 'skip', 'Issue is already fixed and should not consume an agent slot.', 'succeeded', now() - interval '17 hours' - interval '48 minutes')
ON CONFLICT (id) DO UPDATE
SET decision = EXCLUDED.decision,
    reasoning = EXCLUDED.reasoning,
    outcome = EXCLUDED.outcome;

DELETE FROM session_pm_context
WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
  AND project_task_id IN (
    '00000000-0000-4000-a000-000000000880'::uuid,
    '00000000-0000-4000-a000-000000000881'::uuid,
    '00000000-0000-4000-a000-000000000882'::uuid,
    '00000000-0000-4000-a000-000000000883'::uuid,
    '00000000-0000-4000-a000-000000000884'::uuid,
    '00000000-0000-4000-a000-000000000885'::uuid,
    '00000000-0000-4000-a000-000000000886'::uuid
  );

DELETE FROM project_task_dependencies
WHERE task_id IN (
    '00000000-0000-4000-a000-000000000880'::uuid,
    '00000000-0000-4000-a000-000000000881'::uuid,
    '00000000-0000-4000-a000-000000000882'::uuid,
    '00000000-0000-4000-a000-000000000883'::uuid,
    '00000000-0000-4000-a000-000000000884'::uuid,
    '00000000-0000-4000-a000-000000000885'::uuid,
    '00000000-0000-4000-a000-000000000886'::uuid
  )
   OR depends_on_id IN (
    '00000000-0000-4000-a000-000000000880'::uuid,
    '00000000-0000-4000-a000-000000000881'::uuid,
    '00000000-0000-4000-a000-000000000882'::uuid,
    '00000000-0000-4000-a000-000000000883'::uuid,
    '00000000-0000-4000-a000-000000000884'::uuid,
    '00000000-0000-4000-a000-000000000885'::uuid,
    '00000000-0000-4000-a000-000000000886'::uuid
  );

DELETE FROM project_tasks
WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
  AND id IN (
    '00000000-0000-4000-a000-000000000880'::uuid,
    '00000000-0000-4000-a000-000000000881'::uuid,
    '00000000-0000-4000-a000-000000000882'::uuid,
    '00000000-0000-4000-a000-000000000883'::uuid,
    '00000000-0000-4000-a000-000000000884'::uuid,
    '00000000-0000-4000-a000-000000000885'::uuid,
    '00000000-0000-4000-a000-000000000886'::uuid
  );

INSERT INTO project_tasks (
  id, project_id, org_id, title, description, approach, reasoning,
  sort_order, depends_on, batch_number, status, complexity, confidence,
  session_id, issue_id, branch_name, pr_url, outcome_notes,
  retry_count, max_retries, created_at, updated_at, completed_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000880'::uuid,
    '00000000-0000-4000-a000-000000000200'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'Write preview teardown spec',
    'Define lifecycle states and cleanup expectations for PR preview teardown.',
    'Capture behavior in a short technical spec and align with PR preview state.',
    'Spec first so implementation tasks share a stable contract.',
    10,
    NULL,
    1,
    'completed',
    'simple',
    'high',
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000604'::uuid,
    'feat/preview-teardown',
    'https://github.com/assembledhq/143/pull/42',
    'Spec captured cleanup states and owner handoff.',
    0,
    2,
    now() - interval '2 days',
    now() - interval '35 minutes',
    now() - interval '35 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000881'::uuid,
    '00000000-0000-4000-a000-000000000200'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'Implement PR preview auto-teardown',
    'Stop stale preview runtimes when a PR closes, merges, or loses its active target.',
    'Use existing preview state records and make teardown idempotent.',
    'Keeps demo and production workers from leaking preview runtimes.',
    20,
    ARRAY['00000000-0000-4000-a000-000000000880'::uuid],
    1,
    'completed',
    'moderate',
    'high',
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000604'::uuid,
    'feat/preview-teardown',
    'https://github.com/assembledhq/143/pull/42',
    'Synthetic PR opened with one failing frontend check.',
    0,
    2,
    now() - interval '2 days',
    now() - interval '3 minutes',
    now() - interval '3 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000882'::uuid,
    '00000000-0000-4000-a000-000000000200'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'Add branch preview policy states',
    'Expose stopped, expired, and pinned preview states in the project rollout plan.',
    'Extend status mapping before adding more preview records.',
    'Makes branch preview history understandable in the project timeline.',
    30,
    ARRAY['00000000-0000-4000-a000-000000000881'::uuid],
    2,
    'running',
    'moderate',
    'medium',
    '00000000-0000-4000-a000-000000000302'::uuid,
    '00000000-0000-4000-a000-000000000602'::uuid,
    'feat/branch-preview-states',
    NULL,
    NULL,
    0,
    2,
    now() - interval '1 day',
    now() - interval '45 minutes',
    NULL
  ),
  (
    '00000000-0000-4000-a000-000000000883'::uuid,
    '00000000-0000-4000-a000-000000000200'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'Backfill preview usage rollups',
    'Create representative usage rollups so the usage dashboard shows recent preview and agent activity.',
    'Seed aggregated data only; raw billing events stay empty in the preview.',
    'Rollups unblock the usage page without introducing fake raw events.',
    40,
    ARRAY['00000000-0000-4000-a000-000000000880'::uuid],
    2,
    'pending',
    'simple',
    'high',
    NULL,
    '00000000-0000-4000-a000-000000000606'::uuid,
    NULL,
    NULL,
    NULL,
    0,
    2,
    now() - interval '12 hours',
    now() - interval '12 hours',
    NULL
  ),
  (
    '00000000-0000-4000-a000-000000000884'::uuid,
    '00000000-0000-4000-a000-000000000201'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'Map webhook retries to Linear agent activity',
    'Record retry milestones back to the Linear agent session activity log.',
    'Reuse the idempotent activity log and avoid duplicate provider writes.',
    'Preserves operator context when webhooks are replayed.',
    10,
    NULL,
    1,
    'completed',
    'moderate',
    'high',
    '00000000-0000-4000-a000-000000000301'::uuid,
    '00000000-0000-4000-a000-000000000601'::uuid,
    'fix/webhook-retry',
    NULL,
    'Retry activity is represented in the seeded Linear activity log.',
    0,
    2,
    now() - interval '5 days',
    now() - interval '1 hour',
    now() - interval '1 hour'
  ),
  (
    '00000000-0000-4000-a000-000000000885'::uuid,
    '00000000-0000-4000-a000-000000000200'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'Harden PagerDuty incident writeback',
    'Keep incident notes in sync while an automation session investigates the mapped service.',
    'Use provider event idempotency and repository service mapping.',
    'Incident context should stay visible in automation run detail.',
    50,
    ARRAY['00000000-0000-4000-a000-000000000881'::uuid],
    3,
    'running',
    'complex',
    'medium',
    '00000000-0000-4000-a000-000000000305'::uuid,
    '00000000-0000-4000-a000-000000000607'::uuid,
    'auto/pd-preview-gateway-latency',
    NULL,
    NULL,
    0,
    2,
    now() - interval '6 hours',
    now() - interval '6 minutes',
    NULL
  ),
  (
    '00000000-0000-4000-a000-000000000886'::uuid,
    '00000000-0000-4000-a000-000000000200'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'Add auth callback regression coverage',
    'Cover expired cookie and missing return state around auth callback handling.',
    'Block until the incident triage confirms the failing path.',
    'Avoids shipping a fix that only covers the latency symptom.',
    60,
    ARRAY['00000000-0000-4000-a000-000000000885'::uuid],
    3,
    'blocked',
    'moderate',
    'medium',
    NULL,
    '00000000-0000-4000-a000-000000000605'::uuid,
    NULL,
    NULL,
    NULL,
    0,
    2,
    now() - interval '5 hours',
    now() - interval '5 hours',
    NULL
  );

INSERT INTO project_task_dependencies (task_id, depends_on_id)
VALUES
  ('00000000-0000-4000-a000-000000000881'::uuid, '00000000-0000-4000-a000-000000000880'::uuid),
  ('00000000-0000-4000-a000-000000000882'::uuid, '00000000-0000-4000-a000-000000000881'::uuid),
  ('00000000-0000-4000-a000-000000000883'::uuid, '00000000-0000-4000-a000-000000000880'::uuid),
  ('00000000-0000-4000-a000-000000000885'::uuid, '00000000-0000-4000-a000-000000000881'::uuid),
  ('00000000-0000-4000-a000-000000000886'::uuid, '00000000-0000-4000-a000-000000000885'::uuid)
ON CONFLICT DO NOTHING;

INSERT INTO project_specs (
  id, project_id, org_id, title, content, spec_type, sort_order,
  version, created_by, created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000890'::uuid,
    '00000000-0000-4000-a000-000000000200'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'Preview lifecycle spec',
    $spec$# Preview lifecycle spec

Preview targets move through ready, running, stopped, failed, and expired states. PR previews should recycle when the PR closes, while pinned branch previews can stay available for demos.
$spec$,
    'technical',
    0,
    2,
    '00000000-0000-4000-a000-000000000002'::uuid,
    now() - interval '2 days',
    now() - interval '35 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000891'::uuid,
    '00000000-0000-4000-a000-000000000201'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'Webhook replay acceptance criteria',
    $spec$# Webhook replay acceptance criteria

Replay preserves provider ordering, retries are idempotent, and Linear agent activity receives one milestone per logical event.
$spec$,
    'prd',
    0,
    1,
    '00000000-0000-4000-a000-000000000002'::uuid,
    now() - interval '5 days',
    now() - interval '1 hour'
  )
ON CONFLICT (id) DO UPDATE
SET title = EXCLUDED.title,
    content = EXCLUDED.content,
    spec_type = EXCLUDED.spec_type,
    sort_order = EXCLUDED.sort_order,
    version = EXCLUDED.version,
    updated_at = EXCLUDED.updated_at;

INSERT INTO project_attachments (
  id, project_id, org_id, file_name, file_url, file_type,
  thumbnail_url, file_size, category, caption, sort_order,
  uploaded_by, created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000892'::uuid,
    '00000000-0000-4000-a000-000000000200'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'preview-lifecycle-map.png',
    'seeded/projects/preview-lifecycle-map.png',
    'image',
    'seeded/projects/preview-lifecycle-map.thumb.png',
    184320,
    'wireframe',
    'Synthetic lifecycle map for PR and branch preview states.',
    0,
    '00000000-0000-4000-a000-000000000002'::uuid,
    now() - interval '2 days',
    now() - interval '2 days'
  ),
  (
    '00000000-0000-4000-a000-000000000893'::uuid,
    '00000000-0000-4000-a000-000000000201'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'webhook-replay-checklist.md',
    'seeded/projects/webhook-replay-checklist.md',
    'document',
    NULL,
    24576,
    'reference',
    'Synthetic checklist for replay ordering, dedupe, and writeback.',
    0,
    '00000000-0000-4000-a000-000000000002'::uuid,
    now() - interval '5 days',
    now() - interval '5 days'
  )
ON CONFLICT (id) DO UPDATE
SET file_name = EXCLUDED.file_name,
    file_url = EXCLUDED.file_url,
    file_type = EXCLUDED.file_type,
    thumbnail_url = EXCLUDED.thumbnail_url,
    file_size = EXCLUDED.file_size,
    category = EXCLUDED.category,
    caption = EXCLUDED.caption,
    sort_order = EXCLUDED.sort_order,
    updated_at = EXCLUDED.updated_at;

INSERT INTO project_cycles (
  id, project_id, org_id, pm_plan_id, cycle_number, analysis,
  decisions, progress_pct, tasks_completed_this_cycle,
  tasks_failed_this_cycle, tasks_created_this_cycle, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000894'::uuid,
    '00000000-0000-4000-a000-000000000200'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000870'::uuid,
    1,
    'Initial cycle established preview teardown contract and opened a PR-backed implementation.',
    '[{"decision":"complete_spec_first","reason":"teardown semantics needed agreement before code"},{"decision":"delegate_incident_triage","reason":"PagerDuty latency issue is now highest priority"}]'::jsonb,
    58,
    2,
    0,
    4,
    now() - interval '17 hours'
  ),
  (
    '00000000-0000-4000-a000-000000000895'::uuid,
    '00000000-0000-4000-a000-000000000201'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000870'::uuid,
    1,
    'Webhook ingestion cycle connected retry repair to Linear agent activity and parser follow-up work.',
    '[{"decision":"ship_retry_mapping","reason":"Existing session already verified retry state"},{"decision":"queue_parser_fix","reason":"Sentry signal is lower urgency but easy to test"}]'::jsonb,
    82,
    1,
    0,
    2,
    now() - interval '17 hours'
  )
ON CONFLICT (project_id, cycle_number) DO UPDATE
SET id = EXCLUDED.id,
    org_id = EXCLUDED.org_id,
    pm_plan_id = EXCLUDED.pm_plan_id,
    analysis = EXCLUDED.analysis,
    decisions = EXCLUDED.decisions,
    progress_pct = EXCLUDED.progress_pct,
    tasks_completed_this_cycle = EXCLUDED.tasks_completed_this_cycle,
    tasks_failed_this_cycle = EXCLUDED.tasks_failed_this_cycle,
    tasks_created_this_cycle = EXCLUDED.tasks_created_this_cycle,
    created_at = EXCLUDED.created_at;

INSERT INTO project_source_issues (project_id, issue_id)
VALUES
  ('00000000-0000-4000-a000-000000000200'::uuid, '00000000-0000-4000-a000-000000000602'::uuid),
  ('00000000-0000-4000-a000-000000000200'::uuid, '00000000-0000-4000-a000-000000000604'::uuid),
  ('00000000-0000-4000-a000-000000000200'::uuid, '00000000-0000-4000-a000-000000000605'::uuid),
  ('00000000-0000-4000-a000-000000000200'::uuid, '00000000-0000-4000-a000-000000000607'::uuid),
  ('00000000-0000-4000-a000-000000000201'::uuid, '00000000-0000-4000-a000-000000000601'::uuid),
  ('00000000-0000-4000-a000-000000000201'::uuid, '00000000-0000-4000-a000-000000000606'::uuid)
ON CONFLICT DO NOTHING;

INSERT INTO session_pm_context (
  session_id, org_id, pm_plan_id, pm_approach, pm_reasoning,
  project_task_id, created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000305'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000870'::uuid,
  'Start from provider incident evidence, inspect gateway timeout paths, then propose the narrowest remediation.',
  'The PagerDuty incident has the highest score and a repository/service mapping, so it is safer to delegate than leave in queue.',
  '00000000-0000-4000-a000-000000000885'::uuid,
  now() - interval '6 minutes',
  now() - interval '6 minutes'
)
ON CONFLICT (session_id) DO UPDATE
SET pm_plan_id = EXCLUDED.pm_plan_id,
    pm_approach = EXCLUDED.pm_approach,
    pm_reasoning = EXCLUDED.pm_reasoning,
    project_task_id = EXCLUDED.project_task_id,
    updated_at = EXCLUDED.updated_at;

INSERT INTO automation_goal_improvements (
  id, org_id, automation_id, repository_id, mode, status, input_name,
  input_goal, input_config, base_goal_hash, evidence_snapshot,
  proposed_goal, proposal, confidence, warnings, error_message,
  analysis_session_id, created_by, applied_by, applied_at,
  created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000849'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000831'::uuid,
  '00000000-0000-4000-a000-000000000101'::uuid,
  'fast',
  'completed',
  'Linear backlog groomer',
  'Review eligible Linear and Sentry issues, cluster related provider signals, and delegate the safest high-impact items to agents.',
  '{"schedule":"daily","repository":"assembledhq/example-service"}'::jsonb,
  'seeded-linear-groomer-v1',
  '{"recent_runs":[{"status":"completed"},{"status":"failed","reason":"missing mapping"}],"open_issues":3}'::jsonb,
  'Review eligible Linear and Sentry issues, verify team-to-repository mappings first, then delegate the safest high-impact items with explicit rollback notes.',
  '{"changes":["check mappings before delegation","include rollback notes"],"expected_impact":"fewer failed scheduled runs"}'::jsonb,
  'high',
  '[]'::jsonb,
  NULL,
  '00000000-0000-4000-a000-000000000305'::uuid,
  '00000000-0000-4000-a000-000000000002'::uuid,
  NULL,
  NULL,
  now() - interval '2 hours',
  now() - interval '2 hours'
)
ON CONFLICT (id) DO UPDATE
SET status = EXCLUDED.status,
    evidence_snapshot = EXCLUDED.evidence_snapshot,
    proposed_goal = EXCLUDED.proposed_goal,
    proposal = EXCLUDED.proposal,
    confidence = EXCLUDED.confidence,
    updated_at = EXCLUDED.updated_at;

INSERT INTO code_review_policies (
  id, org_id, repository_id, active, version, enabled, approval_mode,
  description_policy, risk_policy, agent_roster, inline_comment_limit,
  created_by_user_id, created_at
)
VALUES (
  '00000000-0000-4000-a000-000000000900'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000100'::uuid,
  true,
  1,
  true,
  'comment_only',
  '{"requirements":[{"key":"summary","title":"Clear summary","prompt":"Explain the change and user-visible behavior.","required":true}]}'::jsonb,
  '{"max_files_changed":12,"max_lines_changed":650,"require_passing_checks":true,"exclude_sensitive_paths":true,"sensitive_paths":["deploy/**","migrations/**",".env*"],"exclude_categories":["auth","billing","infra"],"require_up_to_date":false,"allow_forks":false,"allow_policy_changes":false,"low_risk_lane":{"enabled":true,"categories":["docs","copy"],"max_lines_changed":1000,"waive_reviewer_quorum":true}}'::jsonb,
  '{"reviewers":["codex","claude_code"],"orchestrator":"opencode","disagreement_blocks":true,"require_reviewer_quorum":2,"timeout_seconds":1800}'::jsonb,
  4,
  '00000000-0000-4000-a000-000000000002'::uuid,
  now() - interval '9 days'
)
ON CONFLICT (id) DO UPDATE
SET active = EXCLUDED.active,
    enabled = EXCLUDED.enabled,
    approval_mode = EXCLUDED.approval_mode,
    description_policy = EXCLUDED.description_policy,
    risk_policy = EXCLUDED.risk_policy,
    agent_roster = EXCLUDED.agent_roster,
    inline_comment_limit = EXCLUDED.inline_comment_limit;

INSERT INTO code_review_github_trigger_settings (
  id, org_id, repository_id, installation_id, active, version,
  team_slug, team_name, team_id, repo_permission, created_by_user_id,
  created_at
)
VALUES (
  '00000000-0000-4000-a000-000000000901'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000100'::uuid,
  99999,
  true,
  1,
  'agent-reviewers',
  'Agent Reviewers',
  143001,
  'pull',
  '00000000-0000-4000-a000-000000000002'::uuid,
  now() - interval '9 days'
)
ON CONFLICT (id) DO UPDATE
SET active = EXCLUDED.active,
    team_slug = EXCLUDED.team_slug,
    team_name = EXCLUDED.team_name,
    team_id = EXCLUDED.team_id;

INSERT INTO code_review_session_metadata (
  id, org_id, session_id, repository_id, pull_request_id, policy_id,
  base_sha, head_sha, from_fork, trigger_source, status, decision,
  acceptable, stale, superseded_by_session_id, review_output_key,
  prompt_artifact_key, github_review_id, github_review_url,
  final_review_body, failure_reason, completed_at, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000902'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000306'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    '00000000-0000-4000-a000-000000000501'::uuid,
    '00000000-0000-4000-a000-000000000900'::uuid,
    '1111111111111111111111111111111111111111',
    '2222222222222222222222222222222222222222',
    false,
    'auto_policy',
    'completed',
    'comment_only',
    false,
    false,
    NULL,
    'seeded/code-review/42/pass-1/output.json',
    'seeded/code-review/42/pass-1/prompt.json',
    42424201,
    'https://github.com/assembledhq/143/pull/42',
    'Synthetic review: one selected finding should be addressed before merge; checks are otherwise scoped correctly.',
    NULL,
    now() - interval '26 minutes',
    now() - interval '44 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000903'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000307'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    '00000000-0000-4000-a000-000000000501'::uuid,
    '00000000-0000-4000-a000-000000000900'::uuid,
    '1111111111111111111111111111111111111111',
    '3333333333333333333333333333333333333333',
    false,
    'slash_command',
    'running',
    NULL,
    NULL,
    false,
    NULL,
    'seeded/code-review/42/pass-2/output.json',
    'seeded/code-review/42/pass-2/prompt.json',
    NULL,
    NULL,
    NULL,
    NULL,
    NULL,
    now() - interval '12 minutes'
  )
ON CONFLICT (id) DO UPDATE
SET status = EXCLUDED.status,
    decision = EXCLUDED.decision,
    acceptable = EXCLUDED.acceptable,
    stale = EXCLUDED.stale,
    review_output_key = EXCLUDED.review_output_key,
    prompt_artifact_key = EXCLUDED.prompt_artifact_key,
    github_review_id = EXCLUDED.github_review_id,
    github_review_url = EXCLUDED.github_review_url,
    final_review_body = EXCLUDED.final_review_body,
    failure_reason = EXCLUDED.failure_reason,
    completed_at = EXCLUDED.completed_at;

INSERT INTO code_review_agent_results (
  id, org_id, session_id, agent_provider, agent_model, role, status,
  raw_output, structured_result, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000904'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000306'::uuid,
    'codex',
    'gpt-5.1-codex-max',
    'reviewer',
    'completed',
    'Synthetic reviewer output: preview teardown should log failed cleanup paths.',
    '{"decision":"comment_only","findings":["preview-cleanup-observability"],"confidence":"high"}'::jsonb,
    now() - interval '34 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000905'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000306'::uuid,
    'claude_code',
    'claude-opus-4-5',
    'reviewer',
    'completed',
    'Synthetic reviewer output: tests cover merged PRs but not closed unmerged PRs.',
    '{"decision":"comment_only","findings":["closed-pr-coverage"],"confidence":"medium"}'::jsonb,
    now() - interval '33 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000906'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000307'::uuid,
    'opencode',
    'gpt-5.5',
    'orchestrator',
    'running',
    NULL,
    '{"status":"comparing_reviewer_findings","reviewers_complete":1}'::jsonb,
    now() - interval '4 minutes'
  )
ON CONFLICT (id) DO UPDATE
SET agent_model = EXCLUDED.agent_model,
    role = EXCLUDED.role,
    status = EXCLUDED.status,
    raw_output = EXCLUDED.raw_output,
    structured_result = EXCLUDED.structured_result;

INSERT INTO code_review_findings (
  id, org_id, session_id, agent_result_id, dedupe_key, severity,
  confidence, path, start_line, end_line, summary, body,
  selected_for_inline, github_comment_id, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000907'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000306'::uuid,
    '00000000-0000-4000-a000-000000000904'::uuid,
    'preview-cleanup-observability',
    'medium',
    'high',
    'internal/services/preview/recycler.go',
    118,
    132,
    'Cleanup failures need structured status',
    'If teardown fails after a PR closes, the preview surface can look stale without a reason code. Record the failure category before returning.',
    true,
    42424211,
    now() - interval '32 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000908'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000306'::uuid,
    '00000000-0000-4000-a000-000000000905'::uuid,
    'closed-pr-coverage',
    'low',
    'medium',
    'internal/services/github/pr_handlers_test.go',
    210,
    236,
    'Closed unmerged PR path lacks coverage',
    'Merged PR teardown is covered, but the closed-without-merge case should get a regression test before enabling auto-teardown broadly.',
    false,
    NULL,
    now() - interval '31 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000909'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000306'::uuid,
    '00000000-0000-4000-a000-000000000904'::uuid,
    'preview-ui-label',
    'info',
    'high',
    'frontend/src/components/preview/PreviewStatus.tsx',
    48,
    54,
    'Status label can be clearer',
    'Consider using stopped instead of inactive for preview instances that were intentionally recycled.',
    false,
    NULL,
    now() - interval '30 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000910'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000307'::uuid,
    '00000000-0000-4000-a000-000000000906'::uuid,
    'orchestrator-disagreement',
    'medium',
    'medium',
    NULL,
    NULL,
    NULL,
    'Reviewer disagreement still being resolved',
    'One reviewer treats missing closed-PR coverage as required, while another classifies it as a follow-up. Orchestrator is still comparing policy.',
    false,
    NULL,
    now() - interval '4 minutes'
  )
ON CONFLICT (id) DO UPDATE
SET severity = EXCLUDED.severity,
    confidence = EXCLUDED.confidence,
    path = EXCLUDED.path,
    start_line = EXCLUDED.start_line,
    end_line = EXCLUDED.end_line,
    summary = EXCLUDED.summary,
    body = EXCLUDED.body,
    selected_for_inline = EXCLUDED.selected_for_inline,
    github_comment_id = EXCLUDED.github_comment_id;

INSERT INTO code_review_prompt_artifacts (
  id, org_id, session_id, artifact_key, role, agent_provider,
  content, metadata, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000911'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000306'::uuid,
    'seeded/code-review/42/pass-1/reviewer-codex.prompt',
    'reviewer',
    'codex',
    'Synthetic prompt artifact: review PR 42 for preview teardown risk, tests, and user-visible status changes.',
    '{"policy_id":"00000000-0000-4000-a000-000000000900","pass":1}'::jsonb,
    now() - interval '44 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000912'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000306'::uuid,
    'seeded/code-review/42/pass-1/reviewer-codex.output',
    'reviewer_output',
    'codex',
    'Synthetic output artifact: one medium finding selected for inline comment, two lower-severity observations retained as evidence.',
    '{"finding_count":3,"selected_inline":1}'::jsonb,
    now() - interval '32 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000913'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000307'::uuid,
    'seeded/code-review/42/pass-2/orchestrator.prompt',
    'orchestrator',
    'opencode',
    'Synthetic prompt artifact: compare reviewer disagreement and decide whether PR 42 should remain comment-only.',
    '{"policy_id":"00000000-0000-4000-a000-000000000900","pass":2}'::jsonb,
    now() - interval '12 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000914'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000307'::uuid,
    'seeded/code-review/42/pass-2/description-policy.prompt',
    'description_policy',
    'opencode',
    'Synthetic prompt artifact: verify PR description explains behavior and test plan.',
    '{"policy_id":"00000000-0000-4000-a000-000000000900","pass":2}'::jsonb,
    now() - interval '11 minutes'
  )
ON CONFLICT (org_id, artifact_key) DO UPDATE
SET role = EXCLUDED.role,
    agent_provider = EXCLUDED.agent_provider,
    content = EXCLUDED.content,
    metadata = EXCLUDED.metadata;

DELETE FROM usage_hourly
WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
  AND hour_utc >= date_trunc('day', now()) - interval '13 days'
  AND hour_utc < date_trunc('day', now()) + interval '1 day'
  AND (user_id IS NULL OR user_id IN (
    '00000000-0000-4000-a000-000000000002'::uuid,
    '00000000-0000-4000-a000-000000000003'::uuid,
    '00000000-0000-4000-a000-000000000004'::uuid
  ))
  AND (capacity_tier IS NULL OR capacity_tier IN (
    '2cpu_4096mb_10240diskmb',
    '4cpu_8192mb_20480diskmb'
  ));

WITH days AS (
  SELECT generate_series(0, 13) AS day_offset
),
buckets AS (
  SELECT
    date_trunc('day', now()) - make_interval(days => day_offset) + interval '10 hours' AS hour_utc,
    day_offset
  FROM days
),
rows AS (
  SELECT
    hour_utc,
    NULL::uuid AS user_id,
    NULL::text AS capacity_tier,
    130 + (day_offset * 4) AS container_minutes,
    8 + (day_offset % 3) AS sessions,
    10 + (day_offset % 4) AS starts,
    3 + (day_offset % 2) AS peak,
    980 + (day_offset * 12) AS avg_duration,
    1800 + (day_offset * 18) AS p95_duration,
    52000 + (day_offset * 900) AS input_tokens,
    11000 + (day_offset * 240) AS output_tokens,
    3.10 + (day_offset * 0.08) AS cost_usd
  FROM buckets
  UNION ALL
  SELECT
    hour_utc,
    '00000000-0000-4000-a000-000000000002'::uuid,
    '2cpu_4096mb_10240diskmb',
    46 + (day_offset * 2),
    3 + (day_offset % 2),
    4 + (day_offset % 2),
    2,
    840 + (day_offset * 8),
    1460 + (day_offset * 10),
    21000 + (day_offset * 420),
    4800 + (day_offset * 110),
    1.20 + (day_offset * 0.04)
  FROM buckets
  UNION ALL
  SELECT
    hour_utc,
    '00000000-0000-4000-a000-000000000004'::uuid,
    '4cpu_8192mb_20480diskmb',
    74 + (day_offset * 3),
    4 + (day_offset % 2),
    5 + (day_offset % 3),
    2 + (day_offset % 2),
    1120 + (day_offset * 9),
    2060 + (day_offset * 14),
    30000 + (day_offset * 520),
    6300 + (day_offset * 130),
    1.78 + (day_offset * 0.05)
  FROM buckets
)
INSERT INTO usage_hourly (
  id, org_id, hour_utc, user_id, capacity_tier,
  total_container_minutes, total_sessions, total_container_starts,
  peak_concurrent, avg_duration_sec, p95_duration_sec,
  total_input_tokens, total_output_tokens, total_llm_cost_usd,
  created_at, updated_at
)
SELECT
  gen_random_uuid(),
  '00000000-0000-4000-a000-000000000001'::uuid,
  hour_utc,
  user_id,
  capacity_tier,
  container_minutes,
  sessions,
  starts,
  peak,
  avg_duration,
  p95_duration,
  input_tokens,
  output_tokens,
  cost_usd,
  now(),
  now()
FROM rows;

DELETE FROM usage_hourly_execution
WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
  AND hour_utc >= date_trunc('day', now()) - interval '13 days'
  AND hour_utc < date_trunc('day', now()) + interval '1 day'
  AND agent_type IN ('codex','claude_code','opencode')
  AND capacity_key IN ('2cpu_4096mb_10240diskmb','4cpu_8192mb_20480diskmb');

WITH days AS (
  SELECT generate_series(0, 13) AS day_offset
),
buckets AS (
  SELECT
    date_trunc('day', now()) - make_interval(days => day_offset) + interval '10 hours' AS hour_utc,
    day_offset
  FROM days
),
rows AS (
  SELECT
    hour_utc,
    'codex'::text AS agent_type,
    'gpt-5.1-codex-max'::text AS model_used,
    'medium'::text AS reasoning_effort,
    '2cpu_4096mb_10240diskmb'::text AS capacity_key,
    54 + (day_offset * 2) AS container_minutes,
    3 + (day_offset % 2) AS sessions,
    4 + (day_offset % 2) AS starts,
    2 AS peak,
    23000 + (day_offset * 410) AS input_tokens,
    5200 + (day_offset * 115) AS output_tokens,
    1.36 + (day_offset * 0.03) AS cost_usd
  FROM buckets
  UNION ALL
  SELECT
    hour_utc,
    'claude_code',
    'claude-opus-4-5',
    'high',
    '4cpu_8192mb_20480diskmb',
    48 + (day_offset * 2),
    2 + (day_offset % 2),
    3 + (day_offset % 2),
    2,
    18000 + (day_offset * 360),
    3900 + (day_offset * 95),
    1.02 + (day_offset * 0.03)
  FROM buckets
  UNION ALL
  SELECT
    hour_utc,
    'opencode',
    'gpt-5.5',
    'medium',
    '2cpu_4096mb_10240diskmb',
    28 + day_offset,
    2,
    2,
    1,
    11000 + (day_offset * 220),
    1900 + (day_offset * 55),
    0.72 + (day_offset * 0.02)
  FROM buckets
)
INSERT INTO usage_hourly_execution (
  org_id, hour_utc, agent_type, model_used, reasoning_effort, capacity_key,
  total_container_minutes, total_sessions, total_container_starts,
  peak_concurrent, total_input_tokens, total_output_tokens, total_tokens,
  total_llm_cost_usd, created_at, updated_at
)
SELECT
  '00000000-0000-4000-a000-000000000001'::uuid,
  hour_utc,
  agent_type,
  model_used,
  reasoning_effort,
  capacity_key,
  container_minutes,
  sessions,
  starts,
  peak,
  input_tokens,
  output_tokens,
  input_tokens + output_tokens,
  cost_usd,
  now(),
  now()
FROM rows;
