-- Synthetic provider accounts, identity links, and repository mappings.

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
    'U143ADA',
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
    'U143GRACE',
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
    'U143ADA',
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
    'LIN-USER-TURING',
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
  'U143ADA',
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
    'LIN-USER-GRACE',
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
    'LIN-USER-TURING',
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
