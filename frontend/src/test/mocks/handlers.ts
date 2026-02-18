import { http, HttpResponse } from 'msw';
import type { Issue, AgentRun, ListResponse } from '@/lib/types';

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
];
