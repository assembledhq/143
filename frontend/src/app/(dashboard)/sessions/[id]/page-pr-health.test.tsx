import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { createTestQueryClient, renderWithProviders, screen, userEvent, waitFor, within } from '@/test/test-utils';
import { act } from '@testing-library/react';
import { server } from '@/test/mocks/server';
import { mockSessions, mockPR, mockPRHealth } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import type { PullRequest, Session, SessionTimelineEntry, SingleResponse, ListResponse } from '@/lib/types';
import { installSessionDetailPageTestHooks, MockEventSource } from './session-detail-test-kit';

const { toast } = vi.hoisted(() => ({
  toast: {
    success: vi.fn(),
    info: vi.fn(),
    error: vi.fn(),
  },
}));
const { routerPush } = vi.hoisted(() => ({
  routerPush: vi.fn(),
}));

vi.mock('@/lib/notify', () => ({
  notify: toast,
}));

vi.mock('@/components/markdown', () => ({
  MarkdownContent: ({ content, className }: { content: string; className?: string }) => (
    <div className={className}>{content}</div>
  ),
}));

vi.mock('@/components/session-keyboard-help-overlay', () => ({
  SessionKeyboardHelpOverlay: ({ open }: { open: boolean }) => (
    open ? <div role="dialog" aria-label="Session keyboard shortcuts" /> : null
  ),
}));

vi.mock('next/navigation', () => ({
  useRouter: () => ({
    push: routerPush,
  }),
  useSearchParams: () => new URLSearchParams(),
}));

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

// Mock next/image to render a plain img
vi.mock('next/image', () => ({
  default: ({ src, alt, className, width, height }: { src: string; alt: string; className?: string; width?: number; height?: number }) => (
    <span data-next-image={src} aria-label={alt} className={className} data-width={width} data-height={height} />
  ),
}));

installSessionDetailPageTestHooks({ toast, routerPush });

describe('SessionDetailPage PR health and merge', () => {
  it('shows running indicator for running session', async () => {
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
    expect(screen.getByPlaceholderText('Send a follow-up message...')).toBeEnabled();
  });

  it('disables input for pending session', async () => {
    const pendingSession: Session = {
      ...mockSessions[0],
      status: 'pending',
      completed_at: undefined,
      current_turn: 0,
      sandbox_state: 'none',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: pendingSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={pendingSession.id} />);
    expect(await screen.findByPlaceholderText('Session is not active')).toBeDisabled();
    expect(screen.getByText('Setting up environment')).toBeInTheDocument();
    expect(screen.getByText('Preparing the container and getting the agent ready to run.')).toBeInTheDocument();
  });

  it('keeps polling logs and reconnects after an SSE error while the session is active', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      id: 'session-running-reconnect',
      status: 'running',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'running',
    };

    let timelineFetchCount = 0;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/timeline', () => {
        timelineFetchCount += 1;
        return HttpResponse.json({
          data: timelineFetchCount >= 2
            ? [{
                kind: 'error',
                created_at: '2026-02-17T07:03:00Z',
                log: {
                  id: 101,
                  session_id: runningSession.id,
                  level: 'error',
                  message: 'late log after reconnect',
                  metadata: null,
                  turn_number: 1,
                  created_at: '2026-02-17T07:03:00Z',
                  message_bytes: 'late log after reconnect'.length,
                  message_chars: 'late log after reconnect'.length,
                  message_truncated: false,
                },
              }]
            : [],
          meta: {},
        } satisfies ListResponse<SessionTimelineEntry>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={runningSession.id} />);

    expect(await screen.findByText('Agent is working...')).toBeInTheDocument();
    const initialTimelineFetchCount = timelineFetchCount;
    expect(initialTimelineFetchCount).toBeGreaterThanOrEqual(1);
    expect(MockEventSource.instances).toHaveLength(1);

    MockEventSource.instances[0].emit('message', {
      id: 100,
      session_id: runningSession.id,
      level: 'info',
      message: 'cursor checkpoint',
      metadata: null,
      turn_number: 1,
      created_at: '2026-02-17T07:02:59Z',
    }, '100-0');

    MockEventSource.instances[0].onerror?.(new Event('error'));

    await waitFor(() => {
      expect(MockEventSource.instances).toHaveLength(2);
    }, { timeout: 2500 });
    expect(MockEventSource.instances[1].url).toContain('last_event_id=100-0');

    expect(await screen.findByText('late log after reconnect')).toBeInTheDocument();

    await waitFor(() => {
      expect(timelineFetchCount).toBeGreaterThan(initialTimelineFetchCount);
    });
  });

  it('does not open a per-page PR health stream now that the layout owns org events', async () => {
    const activeOrgId = '22222222-2222-2222-2222-222222222222';
    window.sessionStorage.setItem('active_org_id', activeOrgId);

    const queryClient = createTestQueryClient();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />, { queryClient });

    expect(await screen.findByText('PR health')).toBeInTheDocument();
    expect(MockEventSource.instances.some((source) => source.url.includes('/api/v1/pull-requests/stream'))).toBe(false);
  });

  it('leaves PR reconnect ownership outside the detail page', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findByText('PR health')).toBeInTheDocument();
    expect(MockEventSource.instances.some((source) => source.url.includes('/api/v1/pull-requests/stream'))).toBe(false);
  });

  it('preserves plan-mode styling for streamed output logs', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      id: 'session-running-plan-stream',
      agent_type: 'claude_code',
      status: 'running',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'running',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/timeline', () => {
        const timeline: SessionTimelineEntry[] = [
          {
            kind: 'message',
            created_at: '2026-02-17T07:01:00Z',
            message: {
              id: 101,
              session_id: runningSession.id,
              org_id: 'org-1',
              turn_number: 1,
              role: 'user',
              content: '[PLAN_MODE]\nPlease propose an implementation plan.',
              created_at: '2026-02-17T07:01:00Z',
            },
          },
        ];
        return HttpResponse.json({ data: timeline, meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={runningSession.id} />);

    expect(await screen.findByText('Agent is working...')).toBeInTheDocument();
    const sessionStream = MockEventSource.instances.find((instance) =>
      instance.url.includes(`/api/v1/sessions/${runningSession.id}/logs/stream`),
    );
    expect(sessionStream).toBeDefined();

    await act(async () => {
      sessionStream?.onmessage?.(
        new MessageEvent('message', {
          data: JSON.stringify({
            id: 501,
            session_id: runningSession.id,
            level: 'output',
            message: 'Plan step 1',
            metadata: null,
            turn_number: 1,
            created_at: '2026-02-17T07:02:00Z',
          }),
        }),
      );
    });

    expect(await screen.findByText('Implementation plan')).toBeInTheDocument();
    expect(screen.getByText('Plan step 1')).toBeInTheDocument();
  });

  it('does not show a validation tab for non-manual sessions', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.queryByRole('tab', { name: 'Validation' })).not.toBeInTheDocument();
  });

  it('does not show a validation tab for manual sessions', async () => {
    const manualSession: Session = {
      ...mockSessions[0],
      triggered_by_user_id: 'user-1',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: manualSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByRole('tab', { name: 'Overview' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: /^Changes/ })).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: 'Validation' })).not.toBeInTheDocument();
  });

  it('shows View PR button in tab bar when PR exists', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      // Default handler returns mockPR for GET /sessions/:id/pr
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(await screen.findByText('View PR')).toBeInTheDocument();
  });

  it('renders View PR as a real link instead of nesting a button inside a link', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const viewPRLink = await screen.findByRole('link', { name: 'View PR' });

    expect(viewPRLink).toHaveAttribute('href', 'https://github.com/example/repo/pull/42');
    expect(viewPRLink).toHaveAttribute('target', '_blank');
    expect(viewPRLink).toHaveAttribute('rel', expect.stringContaining('noopener'));
    expect(viewPRLink).toHaveAttribute('data-size', 'xs');
    expect(within(viewPRLink).queryByRole('button')).not.toBeInTheDocument();
  });

  it('keeps preview access in the tab instead of the session detail header actions', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            repository_id: 'repo-1',
            target_branch: 'main',
            working_branch: 'fix/type-error-null-check',
          },
        } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const actions = await screen.findByLabelText('Session detail actions');

    expect(screen.getByRole('tab', { name: 'Preview' })).toBeInTheDocument();
    expect(within(actions).queryByRole('link', { name: 'Preview' })).not.toBeInTheDocument();
    expect(within(actions).getByRole('link', { name: 'View PR' })).toBeInTheDocument();
    expect(within(actions).queryByRole('button', { name: 'Open preview' })).not.toBeInTheDocument();
  });

  it('keeps the tab rail scrollable while separating top-right actions', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const tabRail = await screen.findByLabelText('Session detail tabs');
    const headerBar = screen.getByTestId('session-detail-header-bar');
    const actions = screen.getByLabelText('Session detail actions');

    expect(headerBar).toHaveClass('items-center');
    expect(tabRail).toHaveClass('h-full');
    expect(tabRail).toHaveClass('items-center');
    expect(tabRail).toHaveClass('overflow-x-auto');
    expect(tabRail).toHaveClass('scrollbar-hide');
    expect(tabRail).toHaveClass('min-w-0');
    expect(actions).toHaveClass('shrink-0');
    expect(within(actions).getByRole('link', { name: 'View PR' })).toBeInTheDocument();
  });

  it('keeps the overflowing tab rail scrollbar hidden so actions stay vertically aligned', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const tabRail = await screen.findByLabelText('Session detail tabs');

    Object.defineProperty(tabRail, 'clientWidth', {
      configurable: true,
      value: 140,
    });
    Object.defineProperty(tabRail, 'scrollWidth', {
      configurable: true,
      value: 360,
    });

    await act(async () => {
      window.dispatchEvent(new Event('resize'));
    });

    await waitFor(() => {
      expect(tabRail).toHaveClass('scrollbar-hide');
    });
    expect(tabRail).toHaveClass('mask-fade-r');
  });

  it('renders the PR health banner at the top of Overview when a linked PR is open', async () => {
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            has_conflicts: true,
            can_resolve_conflicts: true,
            failing_test_count: 2,
            can_fix_tests: true,
            needs_agent_action: true,
            summary: 'PR #42 is blocked by conflicts and 2 failing test jobs.',
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findByText('PR health')).toBeInTheDocument();
    expect(screen.getByText('PR #42 is blocked by conflicts and 2 failing test jobs.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Resolve conflicts' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Fix tests' })).toBeInTheDocument();
  });

  it('shows a closed terminal state in the detail header and overview when a linked PR is closed', async () => {
    server.use(
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json({
          data: {
            id: 'pr-1',
            session_id: 'session-abcdef12-3456-7890',
            org_id: 'org-1',
            github_pr_number: 42,
            github_pr_url: 'https://github.com/example/repo/pull/42',
            github_repo: 'example/repo',
            title: 'Fix TypeError by adding null check',
            body: 'Adds a null check before accessing properties.',
            status: 'closed',
            branch_name: 'fix/type-error-null-check',
            review_status: null,
            ci_status: 'success',
            merged_at: null,
            closed_at: '2026-02-17T07:10:00Z',
            created_at: '2026-02-17T07:06:00Z',
            updated_at: '2026-02-17T07:10:00Z',
          },
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect((await screen.findAllByText('PR #42 closed')).length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText('PR #42 was closed without merging.')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'View PR' })).toBeInTheDocument();
    expect(screen.queryByText('PR health')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Resolve conflicts' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Fix tests' })).not.toBeInTheDocument();
  });

  it('shows the Merge button when the PR is mergeable, calls the merge API, and toasts on success', async () => {
    let mergeCalled = false;
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            can_merge: true,
            checks_confirmed: true,
            checks: [
              { name: 'unit tests', category: 'test' as const, status: 'passed' as const, summary: 'passed' },
            ],
            summary: 'PR #42 is mergeable and all required test checks are passing.',
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
      http.post('/api/v1/pull-requests/:id/merge', () => {
        mergeCalled = true;
        return HttpResponse.json({
          data: {
            merged: true,
            sha: 'merge-sha',
            message: 'Pull Request successfully merged',
            merge_method: 'squash' as const,
          },
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const mergeButton = await screen.findByRole('button', { name: /^Merge$/ });
    expect(mergeButton).not.toBeDisabled();

    await user.click(mergeButton);

    await waitFor(() => expect(mergeCalled).toBe(true));
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('PR #42 merged', expect.any(Object)));
  });

  it('reconciles open PR health when the PR stream opens after a missed update', async () => {
    const queryClient = createTestQueryClient();
    let healthRequestCount = 0;
    let prRequestCount = 0;
    server.use(
      http.get('/api/v1/sessions/:id/pr', () => {
        prRequestCount += 1;
        return HttpResponse.json({
          data: {
            ...mockPR,
            status: 'open',
          },
        } satisfies SingleResponse<PullRequest>);
      }),
      http.get('/api/v1/pull-requests/:id/health', () => {
        healthRequestCount += 1;
        if (healthRequestCount === 1) {
          return HttpResponse.json({
            data: {
              ...mockPRHealth,
              can_merge: false,
              checks_confirmed: false,
              checks: [
                { name: 'unit tests', category: 'test' as const, status: 'pending' as const, summary: 'running' },
              ],
              summary: 'PR #42 is waiting for required checks to report passing.',
            },
          } satisfies SingleResponse<typeof mockPRHealth>);
        }

        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            can_merge: true,
            checks_confirmed: true,
            checks: [
              { name: 'unit tests', category: 'test' as const, status: 'passed' as const, summary: 'passed' },
            ],
            summary: 'PR #42 is mergeable and all required test checks are passing.',
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />, { queryClient });

    expect(await screen.findByText('PR #42 is waiting for required checks to report passing.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^Merge$/ })).toBeDisabled();

    await act(async () => { await queryClient.refetchQueries({ type: 'active' }); });

    expect(await screen.findByRole('button', { name: /^Merge$/ })).toBeInTheDocument();
    expect(healthRequestCount).toBeGreaterThanOrEqual(2);
    expect(prRequestCount).toBeGreaterThanOrEqual(2);
  });

  it('renders external links for CI checks shown from the PR details hover card', async () => {
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            failing_test_count: 2,
            can_fix_tests: true,
            checks: [
              {
                name: 'unit tests',
                category: 'test' as const,
                status: 'failed' as const,
                details_url: 'https://ci.example.com/unit-tests',
              },
              {
                name: 'integration tests',
                category: 'test' as const,
                status: 'failed' as const,
                details_url: 'https://ci.example.com/integration-tests',
              },
            ],
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
    );

    const queryClient = createTestQueryClient();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />, { queryClient });

    const user = userEvent.setup();
    await user.hover(await screen.findByText('2/2 failed'));

    const unitLink = await screen.findByRole('link', { name: /unit tests/i });
    const integrationLink = screen.getByRole('link', { name: /integration tests/i });

    expect(unitLink).toHaveAttribute('href', 'https://ci.example.com/unit-tests');
    expect(unitLink).toHaveAttribute('target', '_blank');
    expect(unitLink).toHaveAttribute('rel', expect.stringContaining('noopener'));
    expect(integrationLink).toHaveAttribute('href', 'https://ci.example.com/integration-tests');
    expect(integrationLink).toHaveAttribute('target', '_blank');
    expect(integrationLink).toHaveAttribute('rel', expect.stringContaining('noreferrer'));
  });

  it('shows merged PR state when a linked PR has already been merged', async () => {
    const prCreatedSession: Session = {
      ...mockSessions[0],
      status: 'pr_created',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: prCreatedSession,
        } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json({
          data: {
            id: 'pr-1',
            session_id: 'session-abcdef12-3456-7890',
            org_id: 'org-1',
            github_pr_number: 42,
            github_pr_url: 'https://github.com/example/repo/pull/42',
            github_repo: 'example/repo',
            title: 'Fix TypeError by adding null check',
            body: 'Adds a null check before accessing properties.',
            status: 'merged',
            branch_name: 'fix/type-error-null-check',
            review_status: 'pending',
            ci_status: 'success',
            merged_at: '2026-02-17T07:10:00Z',
            closed_at: null,
            created_at: '2026-02-17T07:06:00Z',
            updated_at: '2026-02-17T07:10:00Z',
          },
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findAllByText('PR merged')).toHaveLength(2);
    expect(screen.getByText('PR #42 merged')).toBeInTheDocument();
    expect(screen.getByText('PR #42 was merged successfully.')).toHaveClass('text-xs');
    expect(screen.getByText('This change has landed. Open a follow-up session if you need to make another revision.')).toHaveClass('text-xs');
    expect(screen.getByRole('link', { name: 'View PR' })).toBeInTheDocument();
    expect(screen.getByLabelText('Merged PR status')).toHaveClass('text-success');
    expect(screen.queryAllByText('PR created')).toHaveLength(0);
    expect(within(screen.getByLabelText('Session detail actions')).queryByText('PR #42 merged')).not.toBeInTheDocument();
    expect(screen.queryByText('PR health')).not.toBeInTheDocument();
  });

  it('shows a closed PR with a neutral status tone', async () => {
    const prCreatedSession: Session = {
      ...mockSessions[0],
      status: 'pr_created',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: prCreatedSession,
        } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json({
          data: {
            ...mockPR,
            status: 'closed',
            merged_at: null,
            closed_at: '2026-02-17T07:10:00Z',
          },
        } satisfies SingleResponse<PullRequest>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const header = await screen.findByTestId('session-main-header');
    expect(within(header).getByText('PR closed')).toHaveClass('text-muted-foreground');
    expect(within(header).getByText('PR closed')).not.toHaveClass('text-success');
  });

  it('uses merged health as terminal while the cached PR row still says open', async () => {
    const prCreatedSession: Session = {
      ...mockSessions[0],
      status: 'pr_created',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: prCreatedSession,
        } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json({
          data: {
            ...mockPR,
            status: 'open',
            merged_at: null,
          },
        } satisfies SingleResponse<PullRequest>);
      }),
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            status: 'merged',
            can_merge: false,
            summary: 'PR #42 was merged successfully.',
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findAllByText('PR merged')).toHaveLength(2);
    expect(screen.getByText('PR #42 merged')).toBeInTheDocument();
    expect(screen.getByText('PR #42 was merged successfully.')).toBeInTheDocument();
    expect(screen.queryByText('PR health')).not.toBeInTheDocument();
  });

  it('updates the header status when the PR stream reports a merge', async () => {
    const queryClient = createTestQueryClient();
    const prCreatedSession: Session = {
      ...mockSessions[0],
      status: 'pr_created',
    };
    let currentPR: PullRequest = {
      ...mockPR,
      status: 'open',
      merged_at: null,
      updated_at: '2026-02-17T07:06:00Z',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: prCreatedSession,
        } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json({
          data: currentPR,
        } satisfies SingleResponse<typeof currentPR>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />, { queryClient });

    expect(await screen.findAllByText('PR created')).toHaveLength(2);
    currentPR = {
      ...currentPR,
      status: 'merged',
      merged_at: '2026-02-17T07:10:00Z',
      updated_at: '2026-02-17T07:10:00Z',
    };

    await act(async () => { await queryClient.refetchQueries({ type: 'active' }); });

    await waitFor(() => {
      const mergedBadges = screen.getAllByText('PR merged');
      expect(mergedBadges).toHaveLength(2);
      for (const badge of mergedBadges) {
        expect(badge).toHaveClass('text-success');
      }
    });
    expect(screen.queryAllByText('PR created')).toHaveLength(0);
  });

  it('keeps the Merge button visible but disabled while CI is still in flight', async () => {
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            can_merge: false,
            checks: [
              { name: 'unit tests', category: 'test' as const, status: 'pending' as const, summary: 'running' },
            ],
            summary: 'PR #42 has 1 failing test job.',
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
    );

    const queryClient = createTestQueryClient();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />, { queryClient });
    await screen.findByText('PR health');
    const mergeButton = screen.getByRole('button', { name: /^Merge$/ });
    expect(mergeButton).toBeDisabled();
    expect(mergeButton).toHaveAttribute('title', 'Checks are still running.');
  });

  it('shows a Merge button that opens GitHub auth when the org requires GitHub user auth and the user is disconnected', async () => {
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            can_merge: true,
            checks_confirmed: true,
            checks: [
              { name: 'unit tests', category: 'test' as const, status: 'passed' as const, summary: 'passed' },
            ],
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
      http.get('/api/v1/users/me/github-status', () => {
        return HttpResponse.json({
          connected: false,
          has_repo_scope: false,
          pr_authorship_mode: 'user_required',
          pr_draft_default: false,
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findByText('PR health');
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: /^Merge$/ }));

    expect(await screen.findByText('Merge this pull request as yourself?')).toBeInTheDocument();
    expect(screen.getByText('Connect your GitHub account to merge this pull request as yourself.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Connect your GitHub account' })).toBeInTheDocument();
  });

  it('toasts the API error message when the merge call fails', async () => {
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            can_merge: true,
            checks_confirmed: true,
            checks: [
              { name: 'unit tests', category: 'test' as const, status: 'passed' as const, summary: 'passed' },
            ],
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
      http.post('/api/v1/pull-requests/:id/merge', () => {
        return HttpResponse.json(
          {
            error: {
              code: 'PULL_REQUEST_MERGE_REJECTED',
              message: 'Head branch was modified. Review and try the merge again.',
            },
          },
          { status: 409 },
        );
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await user.click(await screen.findByRole('button', { name: /^Merge$/ }));

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Head branch was modified. Review and try the merge again.'),
    );
  });

  it('keeps the Merge button visible but disabled when checks have not yet confirmed a passing state', async () => {
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            can_merge: true,
            checks_confirmed: false,
            checks: [],
            summary: 'PR #42 is mergeable and all required test checks are passing.',
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findByText('PR health');
    const mergeButton = screen.getByRole('button', { name: /^Merge$/ });
    expect(mergeButton).toBeDisabled();
    expect(mergeButton).toHaveAttribute('title', 'Waiting for GitHub to confirm required checks.');
  });

  it('groups a stopped merge when ready state with its retry action', async () => {
    let retryRequested = false;

    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            merge_state: 'blocked' as const,
            can_merge: false,
            checks_confirmed: true,
            summary: 'PR #42 is blocked by GitHub merge requirements.',
            merge_when_ready: {
              state: 'failed' as const,
              last_error: 'Fix failing checks before enabling merge when ready.',
              requested_head_sha: 'head-sha',
              requested_health_version: 1,
            },
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
      http.post('/api/v1/pull-requests/:id/merge-when-ready', () => {
        retryRequested = true;
        return HttpResponse.json({
          data: { state: 'queued', requested_head_sha: 'head-sha', requested_health_version: 1 },
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const stoppedNotice = await screen.findByRole('status', { name: 'Merge when ready stopped' });
    expect(within(stoppedNotice).getByText(/Fix failing checks before enabling merge when ready\./)).toBeInTheDocument();

    await user.click(within(stoppedNotice).getByRole('button', { name: 'Retry merge when ready' }));

    await waitFor(() => {
      expect(retryRequested).toBe(true);
    });
  });

  it('shows the Merge button when GitHub has confirmed that the repo has no CI checks configured', async () => {
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            can_merge: true,
            checks_confirmed: true,
            checks: [],
            summary: 'PR #42 is mergeable. No CI checks are configured for this repository.',
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findByRole('button', { name: /^Merge$/ })).toBeInTheDocument();
  });

  it('does not show Resolve conflicts when GitHub reports a non-conflict blocked PR', async () => {
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            merge_state: 'blocked' as const,
            has_conflicts: false,
            can_resolve_conflicts: false,
            can_merge: false,
            checks_confirmed: true,
            checks: [
              { name: 'unit tests', category: 'test' as const, status: 'passed' as const, summary: 'passed' },
            ],
            summary: 'PR #42 is blocked by GitHub merge requirements.',
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findByText('PR #42 is blocked by GitHub merge requirements.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Resolve conflicts' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^Merge$/ })).toBeDisabled();
  });

  it('stays on the original session after starting a PR repair action', async () => {
    let repairRequested = false;
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            failing_test_count: 1,
            can_fix_tests: !repairRequested,
            needs_agent_action: !repairRequested,
            summary: 'PR #42 has 1 failing test job.',
            active_repairs: repairRequested ? [{
              action_type: 'fix_tests',
              session_id: 'session-abcdef12-3456-7890',
              session_status: 'running',
              health_version: 2,
            }] : [],
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
      http.post('/api/v1/pull-requests/:id/repair/fix-tests', () => {
        repairRequested = true;
        return HttpResponse.json({
          data: {
            session_id: 'session-abcdef12-3456-7890',
            mode: 'reconstructed',
            reused_in_flight: false,
            head_sha: 'head-sha',
            base_sha: 'base-sha',
            health_version: 2,
            repair_action_type: 'fix_tests',
          },
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await user.click(await screen.findByRole('button', { name: 'Fix tests' }));

    await waitFor(() => {
      expect(screen.getByText('Fix tests running')).toBeInTheDocument();
    });
    expect(routerPush).not.toHaveBeenCalled();
  });

  it('starts PR repair actions in the currently active thread', async () => {
    const threadedSession: Session = {
      ...mockSessions[0],
      threads: [
        {
          id: 'thread-main',
          session_id: mockSessions[0].id,
          org_id: 'org-1',
          agent_type: 'claude_code',
          label: 'Main',
          status: 'completed',
          current_turn: 1,
          created_at: '2026-02-17T07:00:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
        {
          id: 'thread-review',
          session_id: mockSessions[0].id,
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Review',
          status: 'idle',
          current_turn: 0,
          created_at: '2026-02-17T07:01:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
      ],
    };
    let repairBody: unknown;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: threadedSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            failing_test_count: 1,
            can_fix_tests: true,
            needs_agent_action: true,
            summary: 'PR #42 has 1 failing test job.',
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({
          data: [],
          meta: { has_older: false, thread_status: 'idle' },
        });
      }),
      http.post('/api/v1/pull-requests/:id/repair/fix-tests', async ({ request }) => {
        repairBody = await request.json();
        return HttpResponse.json({
          data: {
            session_id: threadedSession.id,
            thread_id: 'thread-review',
            mode: 'reconstructed',
            reused_in_flight: false,
            head_sha: 'head-sha',
            base_sha: 'base-sha',
            health_version: 1,
            repair_action_type: 'fix_tests',
          },
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={threadedSession.id} />);

    await user.click(await screen.findByRole('tab', { name: /Review/ }));
    await user.click(await screen.findByRole('button', { name: 'Fix tests' }));

    await waitFor(() => {
      expect(repairBody).toEqual({ thread_id: 'thread-review', push_changes: true });
    });
    expect(routerPush).not.toHaveBeenCalled();
  });

  it('starts no-push PR repair actions from the Fix tests dropdown', async () => {
    let repairBody: unknown;

    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            failing_test_count: 1,
            can_fix_tests: true,
            needs_agent_action: true,
            summary: 'PR #42 has 1 failing test job.',
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
      http.post('/api/v1/pull-requests/:id/repair/fix-tests', async ({ request }) => {
        repairBody = await request.json();
        return HttpResponse.json({
          data: {
            session_id: 'session-abcdef12-3456-7890',
            mode: 'reconstructed',
            reused_in_flight: false,
            head_sha: 'head-sha',
            base_sha: 'base-sha',
            health_version: 1,
            repair_action_type: 'fix_tests',
          },
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await user.click(await screen.findByRole('button', { name: 'More fix tests actions' }));
    await user.click(await screen.findByText('Fix without pushing changes'));

    await waitFor(() => {
      // mockSessions[0] has no threads, so activeThread is null and thread_id is absent from the body.
      expect(repairBody).toEqual({ push_changes: false });
    });
    expect(routerPush).not.toHaveBeenCalled();
  });

  it('includes both thread_id and push_changes:false when Fix without pushing is used from a threaded session', async () => {
    let repairBody: unknown;

    const threadedSession: Session = {
      ...mockSessions[0],
      threads: [
        {
          id: 'thread-main',
          session_id: mockSessions[0].id,
          org_id: 'org-1',
          agent_type: 'claude_code',
          label: 'Main',
          status: 'completed',
          current_turn: 1,
          created_at: '2026-02-17T07:00:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
        {
          id: 'thread-review',
          session_id: mockSessions[0].id,
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Review',
          status: 'idle',
          current_turn: 0,
          created_at: '2026-02-17T07:01:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
      ],
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: threadedSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            failing_test_count: 1,
            can_fix_tests: true,
            needs_agent_action: true,
            summary: 'PR #42 has 1 failing test job.',
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
      http.get('/api/v1/sessions/:id/messages', () => {
        return HttpResponse.json({
          data: [],
          meta: { has_older: false, thread_status: 'idle' },
        });
      }),
      http.post('/api/v1/pull-requests/:id/repair/fix-tests', async ({ request }) => {
        repairBody = await request.json();
        return HttpResponse.json({
          data: {
            session_id: threadedSession.id,
            thread_id: 'thread-review',
            mode: 'reconstructed',
            reused_in_flight: false,
            head_sha: 'head-sha',
            base_sha: 'base-sha',
            health_version: 1,
            repair_action_type: 'fix_tests',
          },
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={threadedSession.id} />);

    await user.click(await screen.findByRole('tab', { name: /Review/ }));
    await user.click(await screen.findByRole('button', { name: 'More fix tests actions' }));
    await user.click(await screen.findByText('Fix without pushing changes'));

    await waitFor(() => {
      expect(repairBody).toEqual({ thread_id: 'thread-review', push_changes: false });
    });
    expect(routerPush).not.toHaveBeenCalled();
  });

  it('shows an informational toast when the repair endpoint returns 409 REPAIR_ALREADY_IN_PROGRESS', async () => {
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            failing_test_count: 1,
            can_fix_tests: true,
            needs_agent_action: true,
            summary: 'PR #42 has 1 failing test job.',
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
      http.post('/api/v1/pull-requests/:id/repair/fix-tests', () => {
        return HttpResponse.json(
          { error: { code: 'REPAIR_ALREADY_IN_PROGRESS', message: 'a repair session is already in progress for this pull request' } },
          { status: 409 },
        );
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await user.click(await screen.findByRole('button', { name: 'Fix tests' }));

    await waitFor(() => {
      expect(toast.info).toHaveBeenCalledWith('Fix tests session is already in progress');
    });
    // Error banner should NOT be shown — only a toast.
    expect(screen.queryByText('a repair session is already in progress for this pull request')).not.toBeInTheDocument();
    expect(routerPush).not.toHaveBeenCalled();
  });

  it('shows an informational toast when the repair endpoint returns 409 REPAIR_SESSION_BUSY', async () => {
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            failing_test_count: 1,
            can_fix_tests: true,
            needs_agent_action: true,
            summary: 'PR #42 has 1 failing test job.',
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
      http.post('/api/v1/pull-requests/:id/repair/fix-tests', () => {
        return HttpResponse.json(
          { error: { code: 'REPAIR_SESSION_BUSY', message: 'a session is already running for this pull request' } },
          { status: 409 },
        );
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await user.click(await screen.findByRole('button', { name: 'Fix tests' }));

    await waitFor(() => {
      expect(toast.info).toHaveBeenCalledWith('Fix tests session is already in progress');
    });
    expect(screen.queryByText('a session is already running for this pull request')).not.toBeInTheDocument();
    expect(screen.queryByText('failed to start pull request repair')).not.toBeInTheDocument();
    expect(routerPush).not.toHaveBeenCalled();
  });

  it('keeps the repair CTA suppressed while the original-session repair starts', async () => {
    let repairRequested = false;
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            failing_test_count: 1,
            can_fix_tests: !repairRequested,
            needs_agent_action: !repairRequested,
            summary: 'PR #42 has 1 failing test job.',
            active_repairs: repairRequested ? [{
              action_type: 'fix_tests',
              session_id: 'session-abcdef12-3456-7890',
              session_status: 'running',
              health_version: 1,
            }] : [],
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
      http.post('/api/v1/pull-requests/:id/repair/fix-tests', () => {
        repairRequested = true;
        return HttpResponse.json({
          data: {
            session_id: 'session-abcdef12-3456-7890',
            mode: 'reconstructed',
            reused_in_flight: false,
            head_sha: 'head-sha',
            base_sha: 'base-sha',
            health_version: 1,
            repair_action_type: 'fix_tests',
          },
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await user.click(await screen.findByRole('button', { name: 'Fix tests' }));

    await waitFor(() => {
      expect(screen.getByText('Fix tests running')).toBeInTheDocument();
    });
    expect(routerPush).not.toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: 'Fix tests' })).not.toBeInTheDocument();
  });

  it('clears the running repair state after a pull request SSE health refresh', async () => {
    const queryClient = createTestQueryClient();
    let healthRequestCount = 0;
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        healthRequestCount += 1;
        return HttpResponse.json({
          data: healthRequestCount === 1
            ? {
                ...mockPRHealth,
                failing_test_count: 1,
                can_fix_tests: false,
                can_merge: false,
                needs_agent_action: true,
                summary: 'PR #42 has 1 failing test job.',
                active_repairs: [{
                  action_type: 'fix_tests' as const,
                  session_id: 'session-abcdef12-3456-7890',
                  session_status: 'running',
                  health_version: 1,
                }],
              }
            : {
                ...mockPRHealth,
                failing_test_count: 0,
                can_fix_tests: false,
                can_merge: true,
                needs_agent_action: false,
                checks_confirmed: true,
                summary: 'PR #42 is mergeable and all required test checks are passing.',
                active_repairs: [],
              },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />, { queryClient });

    expect(await screen.findByText('Fix tests running')).toBeInTheDocument();
    await act(async () => { await queryClient.refetchQueries({ type: 'active' }); });

    await waitFor(() => {
      expect(screen.queryByText('Fix tests running')).not.toBeInTheDocument();
      expect(screen.getByText('PR #42 is mergeable and all required test checks are passing.')).toBeInTheDocument();
    });
  });

  it('invalidates PR health when the current repair session finishes through session SSE', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      status: 'running',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'running',
    };
    let healthRequestCount = 0;
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/pull-requests/:id/health', () => {
        healthRequestCount += 1;
        return HttpResponse.json({
          data: healthRequestCount === 1
            ? {
                ...mockPRHealth,
                failing_test_count: 1,
                can_fix_tests: false,
                can_merge: false,
                needs_agent_action: true,
                summary: 'PR #42 has 1 failing test job.',
                active_repairs: [{
                  action_type: 'fix_tests' as const,
                  session_id: runningSession.id,
                  session_status: 'running',
                  health_version: 1,
                }],
              }
            : {
                ...mockPRHealth,
                failing_test_count: 0,
                can_fix_tests: false,
                can_merge: true,
                needs_agent_action: false,
                checks_confirmed: true,
                summary: 'PR #42 is mergeable and all required test checks are passing.',
                active_repairs: [],
              },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={runningSession.id} />);

    expect(await screen.findByText('Fix tests running')).toBeInTheDocument();
    await waitFor(() => {
      expect(MockEventSource.instances.some((source) => source.url.includes(`/api/v1/sessions/${runningSession.id}/logs/stream`))).toBe(true);
    });

    const sessionStream = MockEventSource.instances.find((source) => source.url.includes(`/api/v1/sessions/${runningSession.id}/logs/stream`));
    act(() => {
      sessionStream?.emit('done', {
        ...runningSession,
        status: 'completed',
        completed_at: '2026-02-17T07:10:00Z',
      });
    });

    await waitFor(() => {
      expect(screen.queryByText('Fix tests running')).not.toBeInTheDocument();
      expect(screen.getByText('PR #42 is mergeable and all required test checks are passing.')).toBeInTheDocument();
    });
  });

  it('replaces Fix tests with a durable running state after the repair launch succeeds', async () => {
    let healthRequestCount = 0;
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        healthRequestCount += 1;
        if (healthRequestCount === 1) {
          return HttpResponse.json({
            data: {
              ...mockPRHealth,
              failing_test_count: 1,
              can_fix_tests: true,
              needs_agent_action: true,
              summary: 'PR #42 has 1 failing test job.',
              active_repairs: [],
            },
          } satisfies SingleResponse<typeof mockPRHealth>);
        }

        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            failing_test_count: 1,
            can_fix_tests: false,
            can_merge: false,
            needs_agent_action: true,
            summary: 'PR #42 has 1 failing test job.',
            active_repairs: [{
              action_type: 'fix_tests' as const,
              session_id: 'session-repair-123',
              session_status: 'running',
              health_version: 1,
            }],
          },
        } satisfies SingleResponse<typeof mockPRHealth>);
      }),
      http.post('/api/v1/pull-requests/:id/repair/fix-tests', () => {
        return HttpResponse.json({
          data: {
            session_id: 'session-abcdef12-3456-7890',
            mode: 'existing',
            reused_in_flight: false,
            head_sha: 'head-sha',
            base_sha: 'base-sha',
            health_version: 1,
            repair_action_type: 'fix_tests',
          },
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await user.click(await screen.findByRole('button', { name: 'Fix tests' }));

    expect(await screen.findByText('Fix tests running')).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: 'Fix tests' })).not.toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: 'Open repair session' })).toBeInTheDocument();
  });
});
