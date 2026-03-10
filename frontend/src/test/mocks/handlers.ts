import { http, HttpResponse } from 'msw';
import type { Issue, AgentRun, AgentRunLog, AgentSession, Validation, PullRequest, ListResponse, SingleResponse, PMStatus, PMDecisionsResponse, ProjectDetail } from '@/lib/types';

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

export const mockRuns: AgentRun[] = [
  {
    id: 'run-abcdef12-3456-7890',
    issue_id: 'issue-1',
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
    created_at: '2026-02-17T07:00:00Z',
  },
  {
    id: 'run-98765432-abcd-ef01',
    issue_id: 'issue-2',
    org_id: 'org-1',
    agent_type: 'codex',
    status: 'failed',
    autonomy_level: 'supervised',
    token_mode: 'standard',
    failure_explanation: 'Could not reproduce the error in test environment',
    started_at: '2026-02-17T06:00:00Z',
    completed_at: '2026-02-17T06:03:00Z',
    created_at: '2026-02-17T06:00:00Z',
  },
];

export const mockValidation: Validation = {
  id: 'val-1',
  agent_run_id: 'run-abcdef12-3456-7890',
  org_id: 'org-1',
  status: 'passed',
  direction_check: 'pass',
  direction_check_details: 'Changes align with issue description',
  correctness_check: 'pass',
  correctness_check_details: 'Logic is correct',
  quality_check: 'pass',
  quality_check_details: null,
  security_scan: 'pass',
  security_scan_details: null,
  regression_test_check: 'fail',
  regression_test_check_details: 'One test regressed',
  ci_check: null,
  ci_check_details: null,
  created_at: '2026-02-17T07:06:00Z',
  updated_at: '2026-02-17T07:06:00Z',
};

export const mockPR: PullRequest = {
  id: 'pr-1',
  agent_run_id: 'run-abcdef12-3456-7890',
  org_id: 'org-1',
  github_pr_number: 42,
  github_pr_url: 'https://github.com/example/repo/pull/42',
  github_repo: 'example/repo',
  title: 'Fix TypeError by adding null check',
  body: 'Adds a null check before accessing properties.',
  status: 'open',
  branch_name: 'fix/type-error-null-check',
  review_status: 'pending',
  merged_at: null,
  closed_at: null,
  created_at: '2026-02-17T07:06:00Z',
  updated_at: '2026-02-17T07:06:00Z',
};

export const mockSessions: AgentSession[] = [
  {
    id: 'session-plan-1',
    type: 'plan',
    status: 'completed',
    triggered_by: 'scheduled',
    title: 'Analyzed 5 open issues and delegated 2 tasks.',
    analysis: 'Found critical auth timeout and payment bug requiring immediate attention.',
    tasks: [
      {
        rank: 1,
        title: 'Fix auth timeout',
        issue_ids: ['issue-1'],
        complexity: 'moderate',
        confidence: 'high',
        reasoning: 'Critical user-facing issue',
        approach: 'Check session handler timeout config',
        risk: 'Low - isolated change',
        status: 'delegated',
        agent_run_id: 'run-abcdef12-3456-7890',
        run_status: 'completed',
        run_result_summary: 'Fixed TypeError by adding null check',
        run_confidence_score: 0.92,
        run_started_at: '2026-02-17T07:00:00Z',
        run_completed_at: '2026-02-17T07:05:30Z',
      },
    ],
    clusters: [],
    skipped_issues: [],
    issues_reviewed: 5,
    task_count: 1,
    active_run_count: 0,
    completed_run_count: 1,
    failed_run_count: 0,
    created_at: '2026-02-17T08:00:00Z',
    completed_at: '2026-02-17T08:10:00Z',
  },
  {
    id: 'session-manual-1',
    type: 'manual',
    status: 'failed',
    triggered_by: 'fix_this',
    title: 'Run run-9876',
    tasks: [
      {
        rank: 1,
        title: 'Fix issue',
        issue_ids: ['issue-2'],
        status: 'delegated',
        agent_run_id: 'run-98765432-abcd-ef01',
        run_status: 'failed',
        run_started_at: '2026-02-17T06:00:00Z',
        run_completed_at: '2026-02-17T06:03:00Z',
      },
    ],
    task_count: 1,
    active_run_count: 0,
    completed_run_count: 0,
    failed_run_count: 1,
    created_at: '2026-02-17T06:00:00Z',
    completed_at: '2026-02-17T06:03:00Z',
  },
];

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

export const mockPMStatus: PMStatus = {
  is_running: false,
  issues_reviewed: 0,
  success_rate: 0,
  success_count: 0,
  total_delegated: 0,
};

export const handlers = [
  http.get('/api/v1/issues', () => {
    return HttpResponse.json({
      data: mockIssues,
      meta: {},
    } satisfies ListResponse<Issue>);
  }),

  http.get('/api/v1/runs', () => {
    return HttpResponse.json({
      data: mockRuns,
      meta: {},
    } satisfies ListResponse<AgentRun>);
  }),

  http.get('/api/v1/runs/:id', ({ params }) => {
    const run = mockRuns.find((r) => r.id === params.id);
    if (!run) {
      return HttpResponse.json(
        { error: { code: 'NOT_FOUND', message: 'Run not found' } },
        { status: 404 },
      );
    }
    return HttpResponse.json({ data: run } satisfies SingleResponse<AgentRun>);
  }),

  http.get('/api/v1/runs/:id/logs', () => {
    return HttpResponse.json({
      data: [] as AgentRunLog[],
      meta: {},
    } satisfies ListResponse<AgentRunLog>);
  }),

  http.get('/api/v1/runs/:id/validation', () => {
    return HttpResponse.json({ data: mockValidation } satisfies SingleResponse<Validation>);
  }),

  http.get('/api/v1/runs/:id/pr', () => {
    return HttpResponse.json({ data: mockPR } satisfies SingleResponse<PullRequest>);
  }),

  http.get('/api/v1/runs/:id/questions', () => {
    return HttpResponse.json({ data: [], meta: {} });
  }),

  http.post('/api/v1/issues/:id/fix', () => {
    return HttpResponse.json({
      data: mockRuns[0],
    } satisfies SingleResponse<AgentRun>);
  }),

  http.get('/api/v1/sessions', () => {
    return HttpResponse.json({
      data: mockSessions,
      meta: {},
    } satisfies ListResponse<AgentSession>);
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

  http.get('/api/v1/pm/decisions', () => {
    return HttpResponse.json({
      data: [],
      summary: { total_delegated: 0, succeeded: 0, failed: 0, still_open: 0 },
      meta: {},
    } satisfies PMDecisionsResponse);
  }),

  http.get('/api/v1/sessions/:id', ({ params }) => {
    const session = mockSessions.find((s) => s.id === params.id);
    if (!session) {
      return HttpResponse.json(
        { error: { code: 'NOT_FOUND', message: 'Session not found' } },
        { status: 404 },
      );
    }
    return HttpResponse.json({ data: session } satisfies SingleResponse<AgentSession>);
  }),
];
