-- Synthetic provider-sourced issues and sidecars for high-impact workflows.

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
