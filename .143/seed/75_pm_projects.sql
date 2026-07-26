-- Reference context, human-authored project data, and automation improvements.

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
  ('00000000-0000-4000-a000-000000000864'::uuid, '00000000-0000-4000-a000-000000000860'::uuid)
ON CONFLICT DO NOTHING;

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
  NULL,
  'Start from provider incident evidence, inspect gateway timeout paths, then propose the narrowest remediation.',
  'The PagerDuty incident has a repository/service mapping and a bounded investigation path.',
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

-- Remove legacy PM/Autopilot-only demo artifacts on both fresh and upgraded
-- demo databases. The manual reference document and its immutable pin remain
-- because eval reproducibility still owns that data.
DELETE FROM project_cycles
WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
  AND id IN (
    '00000000-0000-4000-a000-000000000894'::uuid,
    '00000000-0000-4000-a000-000000000895'::uuid
  );

DELETE FROM pm_decision_log
WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
  AND id IN (
    '00000000-0000-4000-a000-000000000871'::uuid,
    '00000000-0000-4000-a000-000000000872'::uuid,
    '00000000-0000-4000-a000-000000000873'::uuid
  );

DELETE FROM pm_plans
WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
  AND id = '00000000-0000-4000-a000-000000000870'::uuid;

DELETE FROM pm_document_set_pin_members
WHERE pin_id = '00000000-0000-4000-a000-000000000864'::uuid
  AND document_id = '00000000-0000-4000-a000-000000000862'::uuid;

DELETE FROM pm_documents
WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
  AND id = '00000000-0000-4000-a000-000000000862'::uuid;

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
  'Linear backlog triage',
  'Review eligible Linear and Sentry issues, verify repository mappings, and summarize the safest high-impact follow-up work.',
  '{"schedule":"daily","repository":"assembledhq/example-service"}'::jsonb,
  'seeded-linear-groomer-v1',
  '{"recent_runs":[{"status":"completed"},{"status":"failed","reason":"missing mapping"}],"open_issues":3}'::jsonb,
  'Review eligible Linear and Sentry issues, verify team-to-repository mappings first, then summarize the safest high-impact follow-up work with explicit rollback notes.',
  '{"changes":["check mappings before recommendations","include rollback notes"],"expected_impact":"fewer failed scheduled runs"}'::jsonb,
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
    input_name = EXCLUDED.input_name,
    input_goal = EXCLUDED.input_goal,
    input_config = EXCLUDED.input_config,
    base_goal_hash = EXCLUDED.base_goal_hash,
    evidence_snapshot = EXCLUDED.evidence_snapshot,
    proposed_goal = EXCLUDED.proposed_goal,
    proposal = EXCLUDED.proposal,
    confidence = EXCLUDED.confidence,
    updated_at = EXCLUDED.updated_at;
