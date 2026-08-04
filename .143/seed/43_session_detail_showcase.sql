-- A single rich session for reviewing the session-detail UI. Its three tabs
-- cover a completed implementation, a clean review, and a failed experiment,
-- while the parent session supplies issue provenance, result, diff, review,
-- question, validation, message, and log surfaces from one stable URL.

INSERT INTO sessions (
  id, org_id, repository_id, triggered_by_user_id, title, working_branch,
  target_branch, agent_type, status, autonomy_level, token_mode,
  sandbox_state, current_turn, last_activity_at, started_at, completed_at,
  created_at
)
VALUES (
  '00000000-0000-4000-a000-000000000308'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000100'::uuid,
  '00000000-0000-4000-a000-000000000003'::uuid,
  'Session detail UI showcase',
  'preview/session-detail-showcase',
  'main',
  'codex',
  'completed',
  'semi',
  'low',
  'snapshotted',
  3,
  now() - interval '18 minutes',
  now() - interval '52 minutes',
  now() - interval '18 minutes',
  now() - interval '52 minutes'
)
ON CONFLICT (id) DO UPDATE
SET title = EXCLUDED.title,
    working_branch = EXCLUDED.working_branch,
    target_branch = EXCLUDED.target_branch,
    agent_type = EXCLUDED.agent_type,
    status = EXCLUDED.status,
    sandbox_state = EXCLUDED.sandbox_state,
    current_turn = EXCLUDED.current_turn,
    last_activity_at = EXCLUDED.last_activity_at,
    started_at = EXCLUDED.started_at,
    completed_at = EXCLUDED.completed_at;

UPDATE sessions
SET origin = 'issue_trigger',
    interaction_mode = 'interactive',
    validation_policy = 'on_session_end',
    model_used = 'gpt-5.1-codex-max',
    result_summary = 'Condensed session metadata into a two-line hierarchy, added representative states, and kept secondary detail available on demand.',
    diff = $diff$diff --git a/frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx b/frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx
index 1234567..89abcde 100644
--- a/frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx
+++ b/frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx
@@ -940,8 +940,7 @@ function OverviewTab({ session }: { session: Session }) {
-  <div className="space-y-2">
-    <p>{originDisplay.detail}</p>
+  <div className="space-y-1.5">
+    <Badge variant="outline">Issue</Badge>
     <span>{repoBranchLabel}</span>
   </div>
 }
$diff$,
    diff_stats = '{"files_changed":2,"added":24,"removed":31}'::jsonb,
    diff_history = '[{"pass":1,"diff_stats":{"files_changed":2,"added":24,"removed":31},"summary":"Reduced session metadata to a compact two-tier hierarchy.","created_at":"2026-08-04T03:30:00Z"}]'::jsonb,
    diff_collected_at = now() - interval '20 minutes'
WHERE id = '00000000-0000-4000-a000-000000000308'::uuid
  AND org_id = '00000000-0000-4000-a000-000000000001'::uuid;

INSERT INTO session_issue_links (
  id, org_id, session_id, issue_id, role, position, added_by_user_id, created_at
)
VALUES (
  '00000000-0000-4000-a000-000000000638'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000308'::uuid,
  '00000000-0000-4000-a000-000000000600'::uuid,
  'primary',
  0,
  '00000000-0000-4000-a000-000000000003'::uuid,
  now() - interval '52 minutes'
)
ON CONFLICT (session_id, issue_id) DO UPDATE
SET role = EXCLUDED.role,
    position = EXCLUDED.position,
    added_by_user_id = EXCLUDED.added_by_user_id;

INSERT INTO session_turn_issue_snapshots (
  id, org_id, session_id, turn_number, linked_issues, created_at
)
VALUES (
  '00000000-0000-4000-a000-000000000645'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000308'::uuid,
  1,
  '[{"id":"00000000-0000-4000-a000-000000000600","role":"primary","title":"Dashboard filters drop archived sessions","source":"linear","severity":"high"}]'::jsonb,
  now() - interval '51 minutes'
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
    '00000000-0000-4000-a000-000000000708'::uuid,
    '00000000-0000-4000-a000-000000000308'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'codex',
    NULL,
    'Overview polish',
    'Refine the session Overview hierarchy and remove redundant provenance copy.',
    ARRAY['frontend/src/app/(dashboard)/sessions/[id]'],
    'completed',
    'seeded-thread-308-overview',
    3,
    now() - interval '18 minutes',
    'Reworked the Overview metadata into a quiet two-line hierarchy.',
    NULL,
    NULL,
    NULL,
    now() - interval '52 minutes',
    now() - interval '18 minutes',
    now() - interval '52 minutes',
    'seeded/snapshots/session-308/base',
    18.40,
    0,
    'seed'
  ),
  (
    '00000000-0000-4000-a000-000000000709'::uuid,
    '00000000-0000-4000-a000-000000000308'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'claude_code',
    NULL,
    'Copy review',
    'Review session-detail copy for redundancy, hierarchy, and actionability.',
    ARRAY['frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx'],
    'completed',
    'seeded-thread-308-review',
    1,
    now() - interval '22 minutes',
    'Review clean: source, repository, and timing remain clear without explanatory sentences.',
    NULL,
    NULL,
    NULL,
    now() - interval '38 minutes',
    now() - interval '22 minutes',
    now() - interval '38 minutes',
    'seeded/snapshots/session-308/review',
    7.60,
    0,
    'seed'
  ),
  (
    '00000000-0000-4000-a000-000000000710'::uuid,
    '00000000-0000-4000-a000-000000000308'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'codex',
    'gpt-5.1-codex-max',
    'Rejected experiment',
    'Try a dense property-grid treatment and record why it should not ship.',
    ARRAY['frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx'],
    'failed',
    'seeded-thread-308-experiment',
    1,
    now() - interval '27 minutes',
    NULL,
    NULL,
    'The property grid increased visual density and made the Overview harder to scan.',
    'design_validation',
    now() - interval '33 minutes',
    now() - interval '27 minutes',
    now() - interval '33 minutes',
    'seeded/snapshots/session-308/experiment',
    3.20,
    0,
    'seed'
  )
ON CONFLICT (id) DO UPDATE
SET agent_type = EXCLUDED.agent_type,
    model_override = EXCLUDED.model_override,
    label = EXCLUDED.label,
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

DELETE FROM session_thread_file_events
WHERE session_id = '00000000-0000-4000-a000-000000000308'::uuid;

INSERT INTO session_thread_file_events (
  org_id, session_id, thread_id, turn, path, event_type,
  before_hash, after_hash, observed_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000308'::uuid,
    '00000000-0000-4000-a000-000000000708'::uuid,
    2,
    'frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx',
    'modified',
    'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    'cccccccccccccccccccccccccccccccccccccccc',
    now() - interval '24 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000308'::uuid,
    '00000000-0000-4000-a000-000000000708'::uuid,
    3,
    'frontend/src/app/(dashboard)/sessions/[id]/page-overview.test.tsx',
    'modified',
    'dddddddddddddddddddddddddddddddddddddddd',
    'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
    now() - interval '20 minutes'
  );

INSERT INTO session_diff_snapshots (
  id, session_id, org_id, turn_number, sequence_number, source,
  base_commit_sha, head_commit_sha, working_branch, target_branch, diff,
  files_changed, lines_added, lines_removed, captured_at, workspace_dirty
)
VALUES (
  '00000000-0000-4000-a000-000000000722'::uuid,
  '00000000-0000-4000-a000-000000000308'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  3,
  1,
  'turn_complete',
  'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
  'cccccccccccccccccccccccccccccccccccccccc',
  'preview/session-detail-showcase',
  'main',
  'diff --git a/frontend/src/app/session-detail-content.tsx b/frontend/src/app/session-detail-content.tsx',
  2,
  24,
  31,
  now() - interval '20 minutes',
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
SET latest_diff_snapshot_id = '00000000-0000-4000-a000-000000000722'::uuid
WHERE id = '00000000-0000-4000-a000-000000000308'::uuid
  AND org_id = '00000000-0000-4000-a000-000000000001'::uuid;

INSERT INTO session_review_comments (
  id, session_id, org_id, user_id, file_path, line_number, diff_side,
  body, resolved, resolved_at, resolved_by_pass, pass_number,
  created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000732'::uuid,
  '00000000-0000-4000-a000-000000000308'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000002'::uuid,
  'frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx',
  948,
  'new',
  'Keep the full repository and branch available through the title while truncating the narrow summary.',
  true,
  now() - interval '19 minutes',
  1,
  1,
  now() - interval '23 minutes',
  now() - interval '19 minutes'
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
VALUES (
  '00000000-0000-4000-a000-000000000742'::uuid,
  '00000000-0000-4000-a000-000000000308'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  'Should origin remain visible after removing the explanatory sentence?',
  ARRAY['Keep a compact badge','Remove origin entirely'],
  'The source is useful for orientation, but it should not compete with status or repository context.',
  'implementation',
  'Keep a compact badge.',
  '00000000-0000-4000-a000-000000000003'::uuid,
  now() - interval '32 minutes',
  'answered',
  now() - interval '36 minutes'
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
VALUES (
  '00000000-0000-4000-a000-000000000752'::uuid,
  '00000000-0000-4000-a000-000000000308'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  'passed',
  'pass',
  'pass',
  'pass',
  'pass',
  'pass',
  '{"line_delta":-7,"covered_lines_delta":12}'::jsonb,
  'pass',
  '{"summary":"Session detail showcase passes layout, copy, and regression checks."}'::jsonb,
  now() - interval '21 minutes',
  now() - interval '19 minutes',
  now() - interval '21 minutes'
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

DELETE FROM session_messages
WHERE session_id = '00000000-0000-4000-a000-000000000308'::uuid;

INSERT INTO session_messages (
  session_id, org_id, user_id, turn_number, role, content, thread_id, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000308'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000003'::uuid,
    1,
    'user',
    'The Overview feels like a wall of metadata. Keep what helps me orient, and remove anything redundant.',
    '00000000-0000-4000-a000-000000000708'::uuid,
    now() - interval '52 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000308'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    NULL,
    2,
    'assistant',
    'I reduced the summary to status and ownership on the first line, then origin, repository, branch, and time on a compact context rail.',
    '00000000-0000-4000-a000-000000000708'::uuid,
    now() - interval '30 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000308'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000003'::uuid,
    1,
    'user',
    'Review the new hierarchy and flag any copy that merely repeats the source badge.',
    '00000000-0000-4000-a000-000000000709'::uuid,
    now() - interval '38 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000308'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    NULL,
    1,
    'assistant',
    'Review clean. The badge communicates provenance; the longer workflow sentence can be omitted.',
    '00000000-0000-4000-a000-000000000709'::uuid,
    now() - interval '22 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000308'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000003'::uuid,
    1,
    'user',
    'Try the dense property-grid version so we can compare it with the continuous canvas.',
    '00000000-0000-4000-a000-000000000710'::uuid,
    now() - interval '33 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000308'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    NULL,
    1,
    'assistant',
    'The experiment failed the design review because the grid added labels and density without improving decisions.',
    '00000000-0000-4000-a000-000000000710'::uuid,
    now() - interval '27 minutes'
  );

DELETE FROM session_logs
WHERE session_id = '00000000-0000-4000-a000-000000000308'::uuid;

INSERT INTO session_logs (
  session_id, org_id, timestamp, level, message, turn_number, thread_id
)
VALUES
  ('00000000-0000-4000-a000-000000000308'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '30 minutes', 'info', 'condensed session overview metadata', 2, '00000000-0000-4000-a000-000000000708'::uuid),
  ('00000000-0000-4000-a000-000000000308'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '22 minutes', 'info', 'copy review completed cleanly', 1, '00000000-0000-4000-a000-000000000709'::uuid),
  ('00000000-0000-4000-a000-000000000308'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '27 minutes', 'error', 'dense property-grid experiment rejected', 1, '00000000-0000-4000-a000-000000000710'::uuid);
