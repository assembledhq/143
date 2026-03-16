import { describe, it, expect, vi, beforeAll } from 'vitest';
import { fireEvent } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { mockSessions, mockMembers } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import type { Session, SessionMessage, User, SingleResponse, ListResponse } from '@/lib/types';

// Mock EventSource (not available in jsdom)
beforeAll(() => {
  global.EventSource = vi.fn().mockImplementation(() => ({
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    close: vi.fn(),
    onerror: null,
  })) as unknown as typeof EventSource;
});

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

describe('SessionDetailPage', () => {
  it('shows loading state initially', () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    expect(screen.getByText('Loading session...')).toBeInTheDocument();
  });

  it('renders session with result summary as title', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const elements = await screen.findAllByText('Fixed TypeError by adding null check');
    expect(elements.length).toBeGreaterThanOrEqual(1);
  });

  it('shows back to sessions link', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('Back to sessions')).toBeInTheDocument();
  });

  it('shows agent type label', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('Claude Code session')).toBeInTheDocument();
  });

  it('shows overview tab with status and confidence', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getAllByText('Completed').length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText('92%')).toBeInTheDocument();
  });

  it('shows tabs for Overview, Logs, Changes, Validation', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByRole('tab', { name: 'Overview' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Logs' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Changes' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Validation' })).toBeInTheDocument();
  });

  it('renders failed session with failure details', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[1],
            failure_explanation: 'Could not reproduce the error in test environment',
          },
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');
    expect(screen.getByText('Could not reproduce the error in test environment')).toBeInTheDocument();
  });

  it('shows error state when session not found', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'Session not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="nonexistent" />);
    expect(
      await screen.findByText('Failed to load session details.'),
    ).toBeInTheDocument();
  });

  it('shows result summary card', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('Result')).toBeInTheDocument();
  });

  it('shows triggered by user name when triggered_by_user_id is set', async () => {
    const sessionWithTrigger: Session = {
      ...mockSessions[0],
      triggered_by_user_id: 'user-1',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithTrigger } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/team/members', () => {
        return HttpResponse.json({
          data: mockMembers,
          meta: {},
        } satisfies ListResponse<User>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findByText('Triggered by')).toBeInTheDocument();
    expect(screen.getByText('Alice Smith')).toBeInTheDocument();
  });

  it('does not show triggered by when triggered_by_user_id is not set', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.queryByText('Triggered by')).not.toBeInTheDocument();
  });

  it('shows Chat tab and defaults to it for idle multi-turn session', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 2,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/messages', () => {
        const msgs: SessionMessage[] = [
          { id: 1, session_id: idleSession.id, org_id: 'org-1', user_id: 'user-1', turn_number: 1, role: 'user', content: 'Fix the bug', created_at: '2026-02-17T07:01:00Z' },
          { id: 2, session_id: idleSession.id, org_id: 'org-1', turn_number: 1, role: 'assistant', content: 'Done fixing', created_at: '2026-02-17T07:02:00Z' },
        ];
        return HttpResponse.json({ data: msgs, meta: {} } satisfies ListResponse<SessionMessage>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={idleSession.id} />);
    expect(await screen.findByRole('tab', { name: 'Chat' })).toBeInTheDocument();
    expect(await screen.findByText('Fix the bug')).toBeInTheDocument();
    expect(screen.getByText('Done fixing')).toBeInTheDocument();
    // Turn indicator shown in subtitle
    expect(screen.getByText(/Turn 2/)).toBeInTheDocument();
  });

  it('shows empty message state in Chat tab when no messages', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={idleSession.id} />);
    expect(await screen.findByText('No messages yet. The session is processing its initial turn.')).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Send a follow-up message...')).toBeInTheDocument();
  });

  it('shows running indicator in Chat tab for running session', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      status: 'running',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'running',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={runningSession.id} />);
    expect(await screen.findByText('Agent is working...')).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Agent is working...')).toBeDisabled();
  });

  it('shows validation tab with check results', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    // Click the Validation tab
    const validationTab = screen.getByRole('tab', { name: 'Validation' });
    fireEvent.click(validationTab);
    expect(await screen.findByText('Direction check')).toBeInTheDocument();
    expect(screen.getByText('Correctness check')).toBeInTheDocument();
    expect(screen.getByText('Changes align with issue description')).toBeInTheDocument();
  });

  it('shows changes tab with PR info and diff', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    const changesTab = screen.getByRole('tab', { name: 'Changes' });
    fireEvent.click(changesTab);
    expect(await screen.findByText('View on GitHub')).toBeInTheDocument();
    expect(screen.getByText('example/repo #42')).toBeInTheDocument();
  });

  it('shows failure next steps and retry button', async () => {
    const failedSession: Session = {
      ...mockSessions[1],
      failure_explanation: 'Something broke',
      failure_category: 'test_failure',
      failure_next_steps: ['Check logs', 'Retry with debug'],
      failure_retry_advised: true,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: failedSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');
    expect(screen.getByText('test_failure')).toBeInTheDocument();
    expect(screen.getByText('Check logs')).toBeInTheDocument();
    expect(screen.getByText('Retry with debug')).toBeInTheDocument();
    expect(screen.getByText('Retry')).toBeInTheDocument();
  });

  it('shows duration for completed session', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('Duration')).toBeInTheDocument();
    // 5m 30s duration between started_at and completed_at
    expect(screen.getByText('5m 30s')).toBeInTheDocument();
  });

  it('shows PM context when pm_plan_id is set', async () => {
    const sessionWithPM: Session = {
      ...mockSessions[0],
      pm_plan_id: 'plan-1',
      pm_reasoning: 'High impact bug',
      pm_approach: 'Quick null check fix',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithPM } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByText('PM context');
    expect(screen.getByText('High impact bug')).toBeInTheDocument();
    expect(screen.getByText('Quick null check fix')).toBeInTheDocument();
  });
});
