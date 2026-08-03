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
