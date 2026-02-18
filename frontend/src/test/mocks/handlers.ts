import { http, HttpResponse } from 'msw';
import type { Issue, AgentRun, Validation, PullRequest, ListResponse, SingleResponse } from '@/lib/types';

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
];
