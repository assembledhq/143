import { http, HttpResponse } from 'msw';
import type { Issue, Session, SessionDiff, SessionLog, SessionMessage, SessionReviewComment, SessionThread, SessionThreadFileEvent, SessionTimelineEntry, User, PullRequest, PullRequestHealthResponse, PullRequestRepairResponse, ListResponse, SingleResponse, PMStatus, PMDecisionsResponse, Project, ProjectDetail } from '@/lib/types';

export const mockIssues: Issue[] = [
  {
    id: 'issue-1',
    org_id: 'org-1',
    external_id: 'SENTRY-123',
    source: 'sentry',
    title: 'TypeError: Cannot read properties of undefined',
    description: 'Error in user dashboard',
    status: 'open',
    first_seen_at: '2026-02-10T10:00:00Z',
    last_seen_at: '2026-02-17T08:00:00Z',
    occurrence_count: 142,
    affected_customer_count: 23,
    severity: 'critical',
    tags: ['frontend', 'dashboard'],
    fingerprint: 'fp-abc123',
    created_at: '2026-02-10T10:00:00Z',
    updated_at: '2026-02-17T08:00:00Z',
  },
  {
    id: 'issue-2',
    org_id: 'org-1',
    external_id: 'LINEAR-456',
    source: 'linear',
    title: 'Null pointer exception in payment flow',
    description: 'Payment fails for certain card types',
    status: 'triaged',
    first_seen_at: '2026-02-12T14:00:00Z',
    last_seen_at: '2026-02-16T16:00:00Z',
    occurrence_count: 37,
    affected_customer_count: 5,
    severity: 'high',
    tags: ['backend', 'payments'],
    fingerprint: 'fp-def456',
    created_at: '2026-02-12T14:00:00Z',
    updated_at: '2026-02-16T16:00:00Z',
  },
];

export const mockSessions: Session[] = [
  {
    id: 'session-abcdef12-3456-7890',
    primary_issue_id: 'issue-1',
    org_id: 'org-1',
    agent_type: 'claude_code',
    status: 'completed',
    autonomy_level: 'full',
    token_mode: 'standard',
    confidence_score: 0.92,
    confidence_reasoning: 'High confidence fix',
    started_at: '2026-02-17T07:00:00Z',
    completed_at: '2026-02-17T07:05:30Z',
    result_summary: 'Fixed TypeError by adding null check',
    current_turn: 0,
    sandbox_state: 'none',
    pr_creation_state: 'idle',
    last_activity_at: '2026-02-17T07:05:30Z',
    created_at: '2026-02-17T07:00:00Z',
  },
  {
    id: 'session-98765432-abcd-ef01',
    primary_issue_id: 'issue-2',
    org_id: 'org-1',
    agent_type: 'codex',
    status: 'failed',
    autonomy_level: 'supervised',
    token_mode: 'standard',
    failure_explanation: 'Could not reproduce the error in test environment',
    started_at: '2026-02-17T06:00:00Z',
    completed_at: '2026-02-17T06:03:00Z',
    current_turn: 0,
    sandbox_state: 'none',
    pr_creation_state: 'idle',
    last_activity_at: '2026-02-17T06:03:00Z',
    created_at: '2026-02-17T06:00:00Z',
  },
];

export const mockPR: PullRequest = {
  id: 'pr-1',
  session_id: 'session-abcdef12-3456-7890',
  org_id: 'org-1',
  github_pr_number: 42,
  github_pr_url: 'https://github.com/example/repo/pull/42',
  github_repo: 'example/repo',
  title: 'Fix TypeError by adding null check',
  body: 'Adds a null check before accessing properties.',
  status: 'open',
  branch_name: 'fix/type-error-null-check',
  review_status: 'pending',
  ci_status: '',
  merged_at: null,
  closed_at: null,
  created_at: '2026-02-17T07:06:00Z',
  updated_at: '2026-02-17T07:06:00Z',
};

export const mockPRHealth: PullRequestHealthResponse = {
  pull_request_id: 'pr-1',
  pull_request_number: 42,
  repository: 'example/repo',
  url: 'https://github.com/example/repo/pull/42',
  status: 'open',
  head_sha: 'head-sha',
  base_sha: 'base-sha',
  health_version: 1,
  merge_state: 'clean',
  has_conflicts: false,
  failing_test_count: 0,
  needs_agent_action: false,
  github_state_synced_at: '2026-02-17T07:07:00Z',
  summary: 'PR #42 is mergeable and all required test checks are passing.',
  checks: [],
  checks_confirmed: false,
  can_resolve_conflicts: false,
  can_fix_tests: false,
  can_merge: false,
  active_repairs: [],
  enrichment_status: 'ready',
  enrichment_requested: false,
  enrichment_ready: true,
  conflict_detail_available: true,
  failing_test_detail_available: false,
};

export const mockProjectDetail: ProjectDetail = {
  project: {
    id: 'proj-1',
    org_id: 'org-1',
    repository_id: 'repo-1',
    title: 'Test Project',
    goal: 'Build something great',
    status: 'draft',
    priority: 50,
    execution_mode: 'sequential',
    max_concurrent: 1,
    auto_merge: false,
    base_branch: 'main',
    total_tasks: 3,
    completed_tasks: 1,
    failed_tasks: 0,
    proposed_by_pm: false,
    source_issue_ids: [],
    created_at: '2026-02-17T08:00:00Z',
    updated_at: '2026-02-17T08:00:00Z',
  },
  tasks: [],
  recent_cycles: [],
  attachments: [],
  specs: [],
};

export const mockProjects: Project[] = [
  {
    id: 'proj-1',
    org_id: 'org-1',
    repository_id: 'repo-1',
    title: 'Test Project',
    goal: 'Build something great',
    status: 'active',
    priority: 50,
    execution_mode: 'sequential',
    max_concurrent: 1,
    auto_merge: false,
    base_branch: 'main',
    total_tasks: 3,
    completed_tasks: 1,
    failed_tasks: 0,
    proposed_by_pm: false,
    source_issue_ids: [],
    created_at: '2026-02-17T08:00:00Z',
    updated_at: '2026-02-17T08:00:00Z',
  },
  {
    id: 'proj-2',
    org_id: 'org-1',
    repository_id: 'repo-1',
    title: 'Security Sweep',
    goal: 'Fix vulnerabilities',
    status: 'completed',
    priority: 25,
    execution_mode: 'parallel',
    max_concurrent: 2,
    auto_merge: false,
    base_branch: 'main',
    total_tasks: 5,
    completed_tasks: 5,
    failed_tasks: 0,
    proposed_by_pm: true,
    source_issue_ids: [],
    created_at: '2026-02-10T08:00:00Z',
    updated_at: '2026-02-15T08:00:00Z',
    completed_at: '2026-02-15T08:00:00Z',
  },
];

export const mockPMStatus: PMStatus = {
  is_running: false,
  issues_reviewed: 0,
  success_rate: 0,
  success_count: 0,
  total_delegated: 0,
};

export const mockMembers: User[] = [
  {
    id: 'user-1',
    org_id: 'org-1',
    email: 'alice@example.com',
    name: 'Alice Smith',
    role: 'admin',
    created_at: '2026-01-01T00:00:00Z',
  },
];

export const handlers = [
  http.get('/api/v1/auth/me', () => {
    return HttpResponse.json({
      data: mockMembers[0],
    } satisfies SingleResponse<User>);
  }),

  http.post('/api/v1/auth/active-org', () => {
    return new HttpResponse(null, { status: 204 });
  }),

  http.post('/api/v1/sessions/:id/view', () => {
    return new HttpResponse(null, { status: 204 });
  }),

  http.get('/api/v1/users/me/github-status', () => {
    return HttpResponse.json({
      data: { connected: true, username: 'alice' },
    });
  }),

  http.get('/api/v1/issues/:id', ({ params }) => {
    const issue = mockIssues.find((i) => i.id === params.id);
    if (!issue) {
      return HttpResponse.json(
        { error: { code: 'NOT_FOUND', message: 'Issue not found' } },
        { status: 404 },
      );
    }
    return HttpResponse.json({ data: issue } satisfies SingleResponse<Issue>);
  }),

  http.get('/api/v1/issues', () => {
    return HttpResponse.json({
      data: mockIssues,
      meta: {},
    } satisfies ListResponse<Issue>);
  }),

  http.get('/api/v1/sessions', () => {
    return HttpResponse.json({
      data: mockSessions,
      meta: {},
    } satisfies ListResponse<Session>);
  }),

  http.get('/api/v1/sessions/:id', ({ params }) => {
    const session = mockSessions.find((s) => s.id === params.id);
    if (!session) {
      return HttpResponse.json(
        { error: { code: 'NOT_FOUND', message: 'Session not found' } },
        { status: 404 },
      );
    }
    return HttpResponse.json({ data: session } satisfies SingleResponse<Session>);
  }),

  http.get('/api/v1/sessions/:id/diff', ({ params }) => {
    const session = mockSessions.find((s) => s.id === params.id);
    if (!session) {
      return HttpResponse.json(
        { error: { code: 'NOT_FOUND', message: 'Session diff not found' } },
        { status: 404 },
      );
    }
    return HttpResponse.json({
      data: {
        session_id: session.id,
        diff: session.diff,
        diff_stats: session.diff_stats,
        diff_history: session.diff_history ?? [],
        diff_truncated: false,
        diff_history_truncated: false,
      },
    } satisfies SingleResponse<SessionDiff>);
  }),

  http.patch('/api/v1/sessions/:id', async ({ request, params }) => {
    const body = await request.json() as { title: string };
    const session = mockSessions.find((s) => s.id === params.id);
    if (!session) {
      return HttpResponse.json(
        { error: { code: 'NOT_FOUND', message: 'Session not found' } },
        { status: 404 },
      );
    }
    return HttpResponse.json({
      data: {
        ...session,
        title: body.title,
      },
    } satisfies SingleResponse<Session>);
  }),

  http.get('/api/v1/sessions/:id/logs', () => {
    return HttpResponse.json({
      data: [] as SessionLog[],
      meta: {},
    } satisfies ListResponse<SessionLog>);
  }),

  http.get('/api/v1/sessions/:id/thread-file-events', () => {
    return HttpResponse.json({
      data: [],
      meta: {},
    });
  }),

  http.get('/api/v1/sessions/:id/timeline', () => {
    return HttpResponse.json({
      data: [] as SessionTimelineEntry[],
      meta: {},
    } satisfies ListResponse<SessionTimelineEntry>);
  }),

  http.get('/api/v1/sessions/:id/pr', ({ params }) => {
    const data = params.id === mockPR.session_id ? mockPR : null;
    return HttpResponse.json({ data } satisfies SingleResponse<PullRequest | null>);
  }),

  http.get('/api/v1/pull-requests/:id/health', () => {
    return HttpResponse.json({ data: mockPRHealth } satisfies SingleResponse<PullRequestHealthResponse>);
  }),

  http.post('/api/v1/pull-requests/:id/repair/fix-tests', () => {
    return HttpResponse.json({
      data: {
        session_id: mockSessions[0].id,
        mode: 'resumed',
        reused_in_flight: false,
        head_sha: 'head-sha',
        base_sha: 'base-sha',
        health_version: 1,
        repair_action_type: 'fix_tests',
      },
    } satisfies SingleResponse<PullRequestRepairResponse>);
  }),

  http.post('/api/v1/pull-requests/:id/repair/resolve-conflicts', () => {
    return HttpResponse.json({
      data: {
        session_id: mockSessions[0].id,
        mode: 'resumed',
        reused_in_flight: false,
        head_sha: 'head-sha',
        base_sha: 'base-sha',
        health_version: 1,
        repair_action_type: 'resolve_conflicts',
      },
    } satisfies SingleResponse<PullRequestRepairResponse>);
  }),

  http.get('/api/v1/sessions/:id/messages', () => {
    return HttpResponse.json({
      data: [] as SessionMessage[],
      meta: {},
    } satisfies ListResponse<SessionMessage>);
  }),

  http.get('/api/v1/sessions/:id/threads', () => {
    return HttpResponse.json({
      data: [] as SessionThread[],
      meta: {},
    } satisfies ListResponse<SessionThread>);
  }),

  http.post('/api/v1/sessions/:id/threads', async ({ request, params }) => {
    const body = await request.json() as { label?: string; agent_type?: string; model?: string };
    return HttpResponse.json({
      data: {
        id: 'thread-new',
        session_id: params.id as string,
        org_id: 'org-1',
        agent_type: body.agent_type || 'codex',
        model_override: body.model || undefined,
        label: body.label || 'Codex 2',
        status: 'idle',
        current_turn: 0,
        created_at: '2026-02-17T07:12:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
    } satisfies SingleResponse<SessionThread>, { status: 201 });
  }),

  http.patch('/api/v1/sessions/:id/threads/:threadId', async ({ request, params }) => {
    const body = await request.json() as { label?: string; agent_type?: string; model?: string };
    return HttpResponse.json({
      data: {
        id: params.threadId as string,
        session_id: params.id as string,
        org_id: 'org-1',
        agent_type: body.agent_type || 'codex',
        model_override: body.model || undefined,
        label: body.label || 'Codex 2',
        status: 'idle',
        current_turn: 0,
        created_at: '2026-02-17T07:12:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
    } satisfies SingleResponse<SessionThread>);
  }),

  http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
    return HttpResponse.json({
      data: [] as SessionMessage[],
      meta: {},
    } satisfies ListResponse<SessionMessage>);
  }),

  http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
    return HttpResponse.json({
      data: [] as SessionLog[],
      meta: {},
    } satisfies ListResponse<SessionLog>);
  }),

  http.get('/api/v1/sessions/:id/thread-file-events', () => {
    return HttpResponse.json({
      data: [] as SessionThreadFileEvent[],
      meta: {},
    } satisfies ListResponse<SessionThreadFileEvent>);
  }),

  http.post('/api/v1/sessions/:id/threads/:threadId/messages', async ({ request, params }) => {
    const body = await request.json() as { message: string; images?: string[] };
    return HttpResponse.json({
      data: {
        id: 2,
        session_id: params.id as string,
        org_id: 'org-1',
        thread_id: params.threadId as string,
        user_id: 'user-1',
        turn_number: 1,
        role: 'user' as const,
        content: body.message,
        attachments: body.images,
        created_at: '2026-02-17T07:12:00Z',
      },
    } satisfies SingleResponse<SessionMessage>, { status: 201 });
  }),

  http.post('/api/v1/sessions/:id/messages', () => {
    return HttpResponse.json({
      data: {
        id: 1,
        session_id: 'session-abcdef12-3456-7890',
        org_id: 'org-1',
        user_id: 'user-1',
        turn_number: 1,
        role: 'user' as const,
        content: 'test message',
        created_at: '2026-02-17T07:10:00Z',
      },
    } satisfies SingleResponse<SessionMessage>);
  }),

  http.post('/api/v1/sessions/:id/end', () => {
    return HttpResponse.json({
      data: { ...mockSessions[0], status: 'completed' },
    } satisfies SingleResponse<Session>);
  }),

  http.post('/api/v1/sessions/:id/pr', () => {
    return HttpResponse.json({ status: 'queued' }, { status: 202 });
  }),

  http.get('/api/v1/sessions/:id/review-comments', () => {
    return HttpResponse.json({
      data: [] as SessionReviewComment[],
      meta: {},
    } satisfies ListResponse<SessionReviewComment>);
  }),

  http.post('/api/v1/sessions/:id/review-comments', async ({ request, params }) => {
    const body = await request.json() as Record<string, unknown>;
    const comment: SessionReviewComment = {
      id: 'comment-new-1',
      session_id: params.id as string,
      org_id: 'org-1',
      user_id: 'user-1',
      file_path: body.file_path as string,
      line_number: body.line_number as number,
      diff_side: ((body.side as string) || 'new') as 'old' | 'new',
      body: body.body as string,
      resolved: false,
      pass_number: 1,
      created_at: '2026-02-17T07:10:00Z',
      updated_at: '2026-02-17T07:10:00Z',
    };
    return HttpResponse.json({ data: comment } satisfies SingleResponse<SessionReviewComment>, { status: 201 });
  }),

  http.patch('/api/v1/sessions/:id/review-comments/:commentId', async ({ request, params }) => {
    const body = await request.json() as Record<string, unknown>;
    const resolved = (body.resolved as boolean) ?? false;
    const comment: SessionReviewComment = {
      id: params.commentId as string,
      session_id: params.id as string,
      org_id: 'org-1',
      user_id: 'user-1',
      file_path: 'file.ts',
      line_number: 1,
      diff_side: 'new',
      body: (body.body as string) ?? 'original body',
      resolved,
      ...(resolved ? { resolved_at: '2026-02-17T07:11:00Z' } : {}),
      pass_number: 1,
      created_at: '2026-02-17T07:10:00Z',
      updated_at: '2026-02-17T07:11:00Z',
    };
    return HttpResponse.json({ data: comment } satisfies SingleResponse<SessionReviewComment>);
  }),

  http.delete('/api/v1/sessions/:id/review-comments/:commentId', () => {
    return new HttpResponse(null, { status: 204 });
  }),

  http.post('/api/v1/sessions/:id/review-comments/send', () => {
    return HttpResponse.json({
      data: { message: 'Please address the following code review comments:\n\n1. src/app.ts:2\n   "Add error handling here"' },
    } satisfies SingleResponse<{ message: string }>);
  }),

  http.get('/api/v1/sessions/:id/questions', () => {
    return HttpResponse.json({ data: [], meta: {} });
  }),

  http.post('/api/v1/issues/:id/fix', () => {
    return HttpResponse.json({
      data: mockSessions[0],
    } satisfies SingleResponse<Session>);
  }),

  http.get('/api/v1/projects', () => {
    return HttpResponse.json({
      data: mockProjects,
      meta: {},
    } satisfies ListResponse<Project>);
  }),

  http.get('/api/v1/projects/:id', () => {
    return HttpResponse.json({ data: mockProjectDetail } satisfies SingleResponse<ProjectDetail>);
  }),

  http.get('/api/v1/pm/status', () => {
    return HttpResponse.json({ data: mockPMStatus } satisfies SingleResponse<PMStatus>);
  }),

  http.post('/api/v1/pm/analyze', () => {
    return HttpResponse.json({ data: { job_id: 'job-1' } });
  }),

  http.get('/api/v1/team/members', () => {
    return HttpResponse.json({
      data: mockMembers,
      meta: {},
    } satisfies ListResponse<User>);
  }),

  http.get('/api/v1/repositories/summary', () => {
    return HttpResponse.json({
      data: [],
      meta: {},
    });
  }),

  http.get('/api/v1/repositories', () => {
    return HttpResponse.json({
      data: [],
      meta: {},
    });
  }),

  http.get('/api/v1/settings', () => {
    return HttpResponse.json({
      data: {
        id: 'org-1',
        name: 'Test Org',
        settings: {
          default_agent_type: 'claude_code',
          agent_config: {},
          autonomy_level: 'auto_simple',
          pm_schedule_hours: 24,
          pm_model: 'claude-sonnet-4-5-20250929',
        },
      },
    });
  }),

  http.get('/api/v1/auth/memberships', () => {
    return HttpResponse.json({
      data: {
        active_org_id: 'org-1',
        active_role: 'admin',
        memberships: [
          { org_id: 'org-1', org_name: 'Test Org', role: 'admin' },
        ],
      },
    });
  }),

  // Default to no pending invites — the org-switcher polls this on mount, so
  // every test rendering the switcher would otherwise hit an unhandled-request
  // warning. Tests that want to exercise the pending-invites surface override
  // this with server.use(...).
  http.get('/api/v1/invitations/pending', () => {
    return HttpResponse.json({ data: [], meta: {} });
  }),

  http.patch('/api/v1/settings', () => {
    return HttpResponse.json({
      data: {
        id: 'org-1',
        name: 'Test Org',
        settings: {},
      },
    });
  }),

  http.get('/api/v1/settings/codex-auth/status', () => {
    return HttpResponse.json({
      data: { status: 'none' },
    });
  }),

  http.get('/api/v1/settings/codex-auth/subscriptions', () => {
    return HttpResponse.json({ data: [] });
  }),

  http.get('/api/v1/settings/claude-code-auth/subscriptions', () => {
    return HttpResponse.json({ data: [] });
  }),

  http.get('/api/v1/integrations', () => {
    return HttpResponse.json({
      data: [],
      meta: {},
    });
  }),

  http.get('/api/v1/pm/documents', () => {
    return HttpResponse.json({
      data: [],
      meta: {},
    });
  }),

  http.get('/api/v1/pm/decisions', () => {
    return HttpResponse.json({
      data: [],
      summary: { total_delegated: 0, succeeded: 0, failed: 0, still_open: 0 },
      meta: {},
    } satisfies PMDecisionsResponse);
  }),

  http.get('/api/v1/issues/:id', ({ params }) => {
    const issue = mockIssues.find((i) => i.id === params.id);
    if (!issue) {
      return HttpResponse.json(
        { error: { code: 'NOT_FOUND', message: 'Issue not found' } },
        { status: 404 },
      );
    }
    // Return issue without description by default to avoid creating synthetic
    // timeline messages that break tests expecting empty timelines.
    return HttpResponse.json({ data: { ...issue, description: undefined } } satisfies SingleResponse<Issue>);
  }),

  http.post('/api/v1/sessions/:id/view', () => {
    return HttpResponse.json({ data: { status: 'ok' } });
  }),

  http.post('/api/v1/sessions/:id/cancel', () => {
    return HttpResponse.json({ data: { ...mockSessions[0], status: 'cancelled' } });
  }),

  http.post('/api/v1/sessions/:id/retry', () => {
    return HttpResponse.json({ data: { ...mockSessions[0], status: 'pending' } });
  }),

  http.get('/api/v1/users/me/github-status', () => {
    return HttpResponse.json({
      connected: false,
      has_repo_scope: false,
      pr_authorship_mode: 'bot',
      pr_draft_default: false,
    });
  }),

  http.get('/api/v1/sessions/:id/preview', () => {
    return HttpResponse.json({ data: null });
  }),

  http.get('/api/v1/sessions/:id/files', ({ request }) => {
    const url = new URL(request.url);
    const path = url.searchParams.get('path') || '';
    // Return mock directory listing
    const entries = path === '' ? [
      { path: 'src', type: 'dir' as const, size: 4096 },
      { path: 'internal', type: 'dir' as const, size: 4096 },
      { path: 'main.go', type: 'file' as const, size: 1234 },
      { path: 'README.md', type: 'file' as const, size: 567 },
    ] : [
      { path: `${path}/index.ts`, type: 'file' as const, size: 890 },
      { path: `${path}/utils.ts`, type: 'file' as const, size: 445 },
    ];
    return HttpResponse.json({ data: entries, meta: {} });
  }),

  http.get('/api/v1/sessions/:id/files/content', ({ request }) => {
    const url = new URL(request.url);
    const filePath = url.searchParams.get('path') || 'unknown';
    return HttpResponse.json({
      data: {
        path: filePath,
        content: '// Mock file content\nexport function hello() {\n  return "world";\n}\n',
        language: 'typescript',
      },
    });
  }),

  http.get('/api/v1/audit-logs', () => {
    return HttpResponse.json({
      data: [],
      meta: {},
    });
  }),

  http.get('/api/v1/settings/credentials/resolved', () => {
    return HttpResponse.json({
      data: [],
      meta: {},
    });
  }),

  http.get('/api/v1/settings/credentials/team', () => {
    return HttpResponse.json({
      data: [],
      meta: {},
    });
  }),

  http.get('/api/v1/settings/coding-auths', () => {
    return HttpResponse.json({
      data: [],
      meta: {},
    });
  }),

  http.get('/api/v1/coding-credentials', () => {
    return HttpResponse.json({
      data: [],
      meta: {},
    });
  }),

  http.get('/api/v1/sessions/:id/files/context', () => {
    return HttpResponse.json({
      data: {
        lines: [
          { number: 1, content: '// line 1' },
          { number: 2, content: '// line 2' },
          { number: 3, content: '// line 3' },
        ],
      },
    });
  }),
];
