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
