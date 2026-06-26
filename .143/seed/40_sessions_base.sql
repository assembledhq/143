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
