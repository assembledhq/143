-- High-impact sessions and provider surfaces linked to those sessions.

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
    WHEN '00000000-0000-4000-a000-000000000306'::uuid THEN '{"pull_request_author":"alan-turing","pull_request_title":"Ship PR preview auto-teardown"}'::jsonb
    WHEN '00000000-0000-4000-a000-000000000307'::uuid THEN '{"pull_request_author":"alan-turing","pull_request_title":"Ship PR preview auto-teardown","review_pass":"second"}'::jsonb
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
  'U143GRACE',
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
  'U143GRACE',
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
  'LIN-USER-GRACE',
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
