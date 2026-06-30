-- Code review policy, sessions, reviewer output, findings, and prompt artifacts.

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

