import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { fireEvent, renderWithProviders, screen, userEvent, waitFor, within } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { mockSessions, mockMembers, mockIssues } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import type {
  Session,
  SessionMessage,
  SessionThread,
  SessionTimelineEntry,
  User,
  SingleResponse,
  ListResponse,
  ThreadMessageWindowResponse,
} from '@/lib/types';
import { installSessionDetailPageTestHooks, getChatScroller } from './session-detail-test-kit';

const { toast } = vi.hoisted(() => ({
  toast: {
    success: vi.fn(),
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

describe('SessionDetailPage transcript and scroll', () => {
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

  it('keeps failed and updated timestamps visually aligned', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[1],
            failure_explanation: 'Could not reproduce the error in test environment',
          },
        });
      }),
      http.get('/api/v1/audit-logs', () => {
        return HttpResponse.json({
          data: [{
            id: 'audit-1',
            actor_type: 'user',
            user_id: mockMembers[0].id,
            action: 'session.failed',
            created_at: new Date(Date.now() - 12 * 60000).toISOString(),
          }],
          meta: {},
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);

    const updatedTrigger = await screen.findByRole('button', { name: /Updated.*ago by/i });
    const metadataRow = updatedTrigger.parentElement;

    // The audit trigger now shares the timestamps flex row so "Failed X ago"
    // and "Updated X ago by Y" sit on the same baseline.
    expect(metadataRow?.className).toContain('flex');
    expect(metadataRow?.className).toContain('items-center');
    expect(metadataRow?.textContent).toMatch(/Failed.*ago/);

    // Inline variant: zero horizontal padding, muted color, preceded by a
    // decorative middle-dot separator marked aria-hidden.
    expect(updatedTrigger.className).toContain('px-0');
    expect(updatedTrigger.className).toContain('text-muted-foreground');
    expect(updatedTrigger.previousElementSibling?.getAttribute('aria-hidden')).toBe('true');
    expect(updatedTrigger.previousElementSibling?.textContent).toBe('·');
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
      await screen.findByText('Failed to load session'),
    ).toBeInTheDocument();
  });

  it('shows result summary card in overview', async () => {
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

    expect(await screen.findByText('Alice Smith')).toBeInTheDocument();
  });

  it('shows System when triggered_by_user_id is not set and no pm_plan_id', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('System')).toBeInTheDocument();
  });

  it('shows chat messages for idle multi-turn session', async () => {
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
      http.get('/api/v1/sessions/:id/timeline', () => {
        const timeline: SessionTimelineEntry[] = [
          {
            kind: 'message',
            created_at: '2026-02-17T07:01:00Z',
            message: { id: 1, session_id: idleSession.id, org_id: 'org-1', user_id: 'user-1', turn_number: 1, role: 'user', content: 'Fix the bug', created_at: '2026-02-17T07:01:00Z' },
          },
          {
            kind: 'message',
            created_at: '2026-02-17T07:02:00Z',
            message: { id: 2, session_id: idleSession.id, org_id: 'org-1', turn_number: 1, role: 'assistant', content: 'Done fixing', created_at: '2026-02-17T07:02:00Z' },
          },
        ];
        return HttpResponse.json({ data: timeline, meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={idleSession.id} />);
    expect(await screen.findByText('Fix the bug')).toBeInTheDocument();
    expect(screen.getByText('Done fixing')).toBeInTheDocument();
    expect(screen.queryByTestId('session-footer')).not.toBeInTheDocument();
  });

  it('suppresses duplicate final output log when timeline includes matching assistant transcript', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    const timeline: SessionTimelineEntry[] = [
      {
        kind: 'assistant_output',
        created_at: '2026-02-17T07:01:00Z',
        log: {
          id: 10,
          session_id: idleSession.id,
          level: 'output',
          message: 'I am checking the codebase first.',
          metadata: null,
          turn_number: 1,
          created_at: '2026-02-17T07:01:00Z',
          message_bytes: 'I am checking the codebase first.'.length,
          message_chars: 'I am checking the codebase first.'.length,
          message_truncated: false,
        },
      },
      {
        kind: 'log',
        created_at: '2026-02-17T07:01:30Z',
        log: {
          id: 11,
          session_id: idleSession.id,
          level: 'debug',
          message: 'hidden log 1',
          metadata: null,
          turn_number: 1,
          created_at: '2026-02-17T07:01:30Z',
          message_bytes: 'hidden log 1'.length,
          message_chars: 'hidden log 1'.length,
          message_truncated: false,
        },
      },
      {
        kind: 'log',
        created_at: '2026-02-17T07:01:40Z',
        log: {
          id: 12,
          session_id: idleSession.id,
          level: 'info',
          message: 'hidden log 2',
          metadata: null,
          turn_number: 1,
          created_at: '2026-02-17T07:01:40Z',
          message_bytes: 'hidden log 2'.length,
          message_chars: 'hidden log 2'.length,
          message_truncated: false,
        },
      },
      {
        kind: 'message',
        created_at: '2026-02-17T07:02:00Z',
        message: {
          id: 20,
          session_id: idleSession.id,
          org_id: 'org-1',
          turn_number: 1,
          role: 'assistant',
          content: 'Added the missing design doc.',
          created_at: '2026-02-17T07:02:00Z',
        },
      },
    ];

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/timeline', () => {
        return HttpResponse.json({ data: timeline, meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
      http.get('/api/v1/sessions/:id/messages', () => {
        return HttpResponse.json({ data: [] as SessionMessage[], meta: {} } satisfies ListResponse<SessionMessage>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={idleSession.id} />);

    expect(await screen.findByText('I am checking the codebase first.')).toBeInTheDocument();
    const duplicatedFinalMessages = await screen.findAllByText('Added the missing design doc.');
    expect(duplicatedFinalMessages).toHaveLength(1);
    expect(screen.getByText(/2 log entries/)).toBeInTheDocument();
  });

  it('shows loading skeleton when no messages and hides files-changed pill', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      diff_stats: { added: 2, removed: 5, files_changed: 2 },
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
      // Return issue without description so no synthetic message is added to timeline
      http.get('/api/v1/issues/:id', ({ params }) => {
        const issue = mockIssues.find((i) => i.id === params.id);
        if (!issue) {
          return HttpResponse.json(
            { error: { code: 'NOT_FOUND', message: 'Issue not found' } },
            { status: 404 },
          );
        }
        return HttpResponse.json({ data: { ...issue, description: '' } } satisfies SingleResponse<typeof issue>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={idleSession.id} />);
    expect(await screen.findByPlaceholderText('Send a follow-up message...')).toBeInTheDocument();
    expect(screen.getByTestId('session-timeline-skeleton')).toBeInTheDocument();
    expect(screen.queryByText('No activity yet')).not.toBeInTheDocument();
    expect(screen.queryByText(/files? changed/)).not.toBeInTheDocument();
  });

  it('shows loading skeleton for a running session before any timeline entries arrive', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      status: 'running',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/timeline', () => {
        return HttpResponse.json({ data: [] as SessionTimelineEntry[], meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
      http.get('/api/v1/issues/:id', ({ params }) => {
        const issue = mockIssues.find((i) => i.id === params.id);
        if (!issue) {
          return HttpResponse.json(
            { error: { code: 'NOT_FOUND', message: 'Issue not found' } },
            { status: 404 },
          );
        }
        return HttpResponse.json({ data: { ...issue, description: '' } } satisfies SingleResponse<typeof issue>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={runningSession.id} />);
    expect(await screen.findByPlaceholderText('Send a follow-up message...')).toBeInTheDocument();
    expect(screen.getByTestId('session-timeline-skeleton')).toBeInTheDocument();
    expect(screen.queryByText('Agent is working...')).not.toBeInTheDocument();
    expect(screen.queryByText(/files? changed/)).not.toBeInTheDocument();
  });

  it('does not shimmer forever for a terminal session with an empty timeline', async () => {
    const completedSession: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: completedSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/timeline', () => {
        return HttpResponse.json({ data: [] as SessionTimelineEntry[], meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
      http.get('/api/v1/issues/:id', ({ params }) => {
        const issue = mockIssues.find((i) => i.id === params.id);
        if (!issue) {
          return HttpResponse.json(
            { error: { code: 'NOT_FOUND', message: 'Issue not found' } },
            { status: 404 },
          );
        }
        return HttpResponse.json({ data: { ...issue, description: '' } } satisfies SingleResponse<typeof issue>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={completedSession.id} />);
    // Wait for the timeline query to settle by looking for the diff-summary
    // pill, which lives inside ChatTimeline (only rendered once we're past
    // the loading state).
    expect(await screen.findByText(/files? changed/)).toBeInTheDocument();
    expect(screen.queryByTestId('session-timeline-skeleton')).not.toBeInTheDocument();
  });

  it('clears the skeleton and enables the composer on an idle thread while a sibling thread is running', async () => {
    // Regression: in the user-reported scenario the session.status was
    // "running" (because a sibling thread was running) while the selected
    // thread was a freshly-created idle one. Two bugs combined: the skeleton
    // never cleared (because `activeSet.has("running")` was truthy) and the
    // composer was disabled by a leftover Phase 1 gate that required
    // session.status !== "running". Phase 2 supports concurrent threads, so
    // an idle thread must be sendable regardless of sibling activity.
    const sessionId = 'session-running-sibling';
    const threads: SessionThread[] = [
      {
        id: 'thread-main',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'codex',
        label: 'Main',
        status: 'running',
        current_turn: 1,
        created_at: '2026-05-04T07:00:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
      {
        id: 'thread-codex2',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'codex',
        label: 'Codex 2',
        status: 'idle',
        current_turn: 0,
        created_at: '2026-05-04T07:01:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
    ];

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            id: sessionId,
            status: 'running',
            sandbox_state: 'ready',
            threads,
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({
          data: [] as SessionMessage[],
          meta: {},
        } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    await user.click(await screen.findByRole('tab', { name: /Codex 2/ }));

    await waitFor(() => {
      expect(screen.queryByTestId('session-timeline-skeleton')).not.toBeInTheDocument();
    });
    const composer = screen.getByPlaceholderText('Send a message to Codex 2...');
    expect(composer).toBeEnabled();
  });

  it('does not shimmer forever for a freshly-created idle thread', async () => {
    // Regression: opening a brand-new secondary thread (idle, zero messages)
    // on a session whose overall status was still in `activeSet`
    // (e.g. "idle"/"running") used to keep the skeleton visible forever,
    // because the skeleton condition consulted session.status instead of the
    // selected thread's status. The skeleton must clear once the thread's
    // data has loaded so the empty-state composer becomes reachable.
    const sessionId = 'session-new-thread-skeleton';
    const threads: SessionThread[] = [
      {
        id: 'thread-main',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'codex',
        label: 'Main',
        status: 'idle',
        current_turn: 1,
        created_at: '2026-05-04T07:00:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
      {
        id: 'thread-codex2',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'codex',
        label: 'Codex 2',
        status: 'idle',
        current_turn: 0,
        created_at: '2026-05-04T07:01:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
    ];

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            id: sessionId,
            status: 'idle',
            sandbox_state: 'ready',
            threads,
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({
          data: [] as SessionMessage[],
          meta: {},
        } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    await user.click(await screen.findByRole('tab', { name: /Codex 2/ }));

    await waitFor(() => {
      expect(screen.queryByTestId('session-timeline-skeleton')).not.toBeInTheDocument();
    });
    const freshTabCard = screen.getByText('No context in this tab yet.').closest('[data-slot="card"]');
    expect(freshTabCard).not.toBeNull();
    expect(within(freshTabCard as HTMLElement).getByText('New tab')).toBeInTheDocument();
    expect(within(freshTabCard as HTMLElement).queryByText(/^Codex$/)).not.toBeInTheDocument();
    expect(screen.queryByText('Fresh tab')).not.toBeInTheDocument();
    expect(screen.queryByText('Fresh context')).not.toBeInTheDocument();
    expect(screen.getByText('No context in this tab yet.')).toBeInTheDocument();
    expect(screen.getByText('Send a task or add context to get started.')).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Send a message to Codex 2...')).toBeInTheDocument();
  });

  it('restores the saved scroll position when reopening an existing session', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      id: 'session-scroll-restore',
      status: 'idle',
      completed_at: undefined,
      current_turn: 2,
      sandbox_state: 'snapshotted',
    };

    window.localStorage.setItem(`session-scroll-position:org-1:user-1:${idleSession.id}`, '320');
    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(900);
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(200);

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/timeline', () => {
        const timeline: SessionTimelineEntry[] = [
          {
            kind: 'message',
            created_at: '2026-02-17T07:01:00Z',
            message: { id: 1, session_id: idleSession.id, org_id: 'org-1', user_id: 'user-1', turn_number: 1, role: 'user', content: 'Fix the bug', created_at: '2026-02-17T07:01:00Z' },
          },
          {
            kind: 'message',
            created_at: '2026-02-17T07:02:00Z',
            message: { id: 2, session_id: idleSession.id, org_id: 'org-1', turn_number: 1, role: 'assistant', content: 'Done fixing', created_at: '2026-02-17T07:02:00Z' },
          },
        ];
        return HttpResponse.json({ data: timeline, meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
    );

    const { container } = renderWithProviders(<SessionDetailContent id={idleSession.id} />);
    await screen.findByText('Done fixing');

    await waitFor(() => {
      expect(getChatScroller(container).scrollTop).toBe(320);
    });
  });

  it('restores a thread-specific saved scroll position when switching tabs in a session', async () => {
    const threadSession: Session = {
      ...mockSessions[0],
      id: 'session-thread-scroll-restore',
      status: 'idle',
      completed_at: undefined,
      sandbox_state: 'snapshotted',
      threads: [
        {
          id: 'thread-main',
          session_id: 'session-thread-scroll-restore',
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Main',
          status: 'idle',
          current_turn: 1,
          created_at: '2026-02-17T07:00:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
        {
          id: 'thread-codex-2',
          session_id: 'session-thread-scroll-restore',
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Codex 2',
          status: 'idle',
          current_turn: 1,
          created_at: '2026-02-17T07:03:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
      ],
    };

    window.localStorage.setItem(`session-scroll-position:org-1:user-1:${threadSession.id}:thread-main`, JSON.stringify({ version: 1, scrollTop: 120 }));
    window.localStorage.setItem(`session-scroll-position:org-1:user-1:${threadSession.id}:thread-codex-2`, JSON.stringify({ version: 1, scrollTop: 410 }));
    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(900);
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(200);

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: threadSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', ({ params }) => {
        const assistantContent = params.threadId === 'thread-main' ? 'Main reply' : 'Codex 2 reply';
        return HttpResponse.json({
          data: [
            {
              id: params.threadId === 'thread-main' ? 1 : 2,
              session_id: threadSession.id,
              org_id: 'org-1',
              thread_id: params.threadId as string,
              turn_number: 1,
              role: 'assistant',
              content: assistantContent,
              created_at: '2026-02-17T07:02:00Z',
            },
          ] as SessionMessage[],
          meta: {},
        } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    const user = userEvent.setup();
    const { container } = renderWithProviders(<SessionDetailContent id={threadSession.id} />);
    await screen.findByText('Main reply');

    await waitFor(() => {
      expect(getChatScroller(container).scrollTop).toBe(120);
    });

    await user.click(screen.getByRole('tab', { name: /Codex 2/ }));
    await screen.findByText('Codex 2 reply');

    await waitFor(() => {
      expect(getChatScroller(container).scrollTop).toBe(410);
    });
  });

  it('persists the last viewed thread when switching tabs in a session', async () => {
    const threadSession: Session = {
      ...mockSessions[0],
      id: 'session-thread-last-viewed',
      status: 'idle',
      completed_at: undefined,
      sandbox_state: 'snapshotted',
      threads: [
        {
          id: 'thread-main',
          session_id: 'session-thread-last-viewed',
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Main',
          status: 'idle',
          current_turn: 1,
          created_at: '2026-02-17T07:00:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
        {
          id: 'thread-codex-2',
          session_id: 'session-thread-last-viewed',
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Codex 2',
          status: 'idle',
          current_turn: 1,
          created_at: '2026-02-17T07:03:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
      ],
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: threadSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', ({ params }) => {
        const assistantContent = params.threadId === 'thread-main' ? 'Main reply' : 'Codex 2 reply';
        return HttpResponse.json({
          data: [
            {
              id: params.threadId === 'thread-main' ? 1 : 2,
              session_id: threadSession.id,
              org_id: 'org-1',
              thread_id: params.threadId as string,
              turn_number: 1,
              role: 'assistant',
              content: assistantContent,
              created_at: '2026-02-17T07:02:00Z',
            },
          ] as SessionMessage[],
          meta: {},
        } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={threadSession.id} />);
    await screen.findByText('Main reply');

    await user.click(screen.getByRole('tab', { name: /Codex 2/ }));
    await screen.findByText('Codex 2 reply');

    expect(window.localStorage.getItem(`session-active-thread:org-1:user-1:${threadSession.id}`)).toBe(
      JSON.stringify({ version: 1, threadId: 'thread-codex-2' }),
    );
  });

  it('reopens a threaded session on the last viewed tab and restores that tab scroll position', async () => {
    const threadSession: Session = {
      ...mockSessions[0],
      id: 'session-thread-reopen-last-viewed',
      status: 'idle',
      completed_at: undefined,
      sandbox_state: 'snapshotted',
      threads: [
        {
          id: 'thread-main',
          session_id: 'session-thread-reopen-last-viewed',
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Main',
          status: 'idle',
          current_turn: 1,
          created_at: '2026-02-17T07:00:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
        {
          id: 'thread-codex-2',
          session_id: 'session-thread-reopen-last-viewed',
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Codex 2',
          status: 'idle',
          current_turn: 1,
          created_at: '2026-02-17T07:03:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
      ],
    };

    window.localStorage.setItem(
      `session-active-thread:org-1:user-1:${threadSession.id}`,
      JSON.stringify({ version: 1, threadId: 'thread-codex-2' }),
    );
    window.localStorage.setItem(`session-scroll-position:org-1:user-1:${threadSession.id}:thread-main`, JSON.stringify({ version: 1, scrollTop: 120 }));
    window.localStorage.setItem(`session-scroll-position:org-1:user-1:${threadSession.id}:thread-codex-2`, JSON.stringify({ version: 1, scrollTop: 410 }));
    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(900);
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(200);

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: threadSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', ({ params }) => {
        const assistantContent = params.threadId === 'thread-main' ? 'Main reply' : 'Codex 2 reply';
        return HttpResponse.json({
          data: [
            {
              id: params.threadId === 'thread-main' ? 1 : 2,
              session_id: threadSession.id,
              org_id: 'org-1',
              thread_id: params.threadId as string,
              turn_number: 1,
              role: 'assistant',
              content: assistantContent,
              created_at: '2026-02-17T07:02:00Z',
            },
          ] as SessionMessage[],
          meta: {},
        } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    const { container } = renderWithProviders(<SessionDetailContent id={threadSession.id} />);
    await screen.findByText('Codex 2 reply');

    expect(screen.getByPlaceholderText('Send a message to Codex 2...')).toBeInTheDocument();
    expect(screen.queryByText('Main reply')).not.toBeInTheDocument();

    await waitFor(() => {
      expect(getChatScroller(container).scrollTop).toBe(410);
    });
  });

  it('loads a threaded session from the latest message window and prepends older messages', async () => {
    const threadSession: Session = {
      ...mockSessions[0],
      id: 'session-thread-window-pagination',
      status: 'idle',
      completed_at: undefined,
      sandbox_state: 'snapshotted',
      threads: [
        {
          id: 'thread-window',
          session_id: 'session-thread-window-pagination',
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Main',
          status: 'idle',
          current_turn: 2,
          created_at: '2026-02-17T07:00:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
      ],
    };
    const requestedUrls: string[] = [];

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: threadSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', ({ request, params }) => {
        requestedUrls.push(request.url);
        const url = new URL(request.url);
        if (url.searchParams.get('before') === '3') {
          return HttpResponse.json({
            data: [
              {
                id: 1,
                session_id: threadSession.id,
                org_id: 'org-1',
                thread_id: params.threadId as string,
                turn_number: 1,
                role: 'user',
                content: 'Older user prompt',
                created_at: '2026-02-17T07:01:00Z',
              },
              {
                id: 2,
                session_id: threadSession.id,
                org_id: 'org-1',
                thread_id: params.threadId as string,
                turn_number: 1,
                role: 'assistant',
                content: 'Older assistant reply',
                created_at: '2026-02-17T07:02:00Z',
              },
            ] as SessionMessage[],
            meta: { has_older: false, thread_status: 'idle' },
          } satisfies ThreadMessageWindowResponse);
        }
        return HttpResponse.json({
          data: [
            {
              id: 3,
              session_id: threadSession.id,
              org_id: 'org-1',
              thread_id: params.threadId as string,
              turn_number: 2,
              role: 'assistant',
              content: 'Latest assistant reply',
              created_at: '2026-02-17T07:03:00Z',
            },
          ] as SessionMessage[],
          meta: {
            next_older_cursor: '3',
            has_older: true,
            latest_assistant_message_id: 3,
            live_edge_message_id: 3,
            thread_status: 'idle',
          },
        } satisfies ThreadMessageWindowResponse);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={threadSession.id} />);

    await screen.findByText('Latest assistant reply');
    const firstRequest = new URL(requestedUrls[0]);
    expect(firstRequest.searchParams.get('position')).toBe('latest');
    expect(firstRequest.searchParams.get('limit')).toBe('60');

    await user.click(screen.getByRole('button', { name: /Load older/i }));

    const olderPrompt = await screen.findByText('Older user prompt');
    const latestReply = screen.getByText('Latest assistant reply');
    expect(olderPrompt.compareDocumentPosition(latestReply) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(requestedUrls.some((url) => new URL(url).searchParams.get('before') === '3')).toBe(true);
  });

  it('loads older thread pages before restoring a saved scroll position outside the latest window', async () => {
    const threadSession: Session = {
      ...mockSessions[0],
      id: 'session-thread-window-saved-scroll',
      status: 'idle',
      completed_at: undefined,
      sandbox_state: 'snapshotted',
      threads: [
        {
          id: 'thread-window-scroll',
          session_id: 'session-thread-window-saved-scroll',
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Main',
          status: 'idle',
          current_turn: 2,
          created_at: '2026-02-17T07:00:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
      ],
    };
    const requestedUrls: string[] = [];
    let transcriptScrollHeight = 500;
    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockImplementation(() => transcriptScrollHeight);
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(200);
    window.localStorage.setItem(
      `session-scroll-position:org-1:user-1:${threadSession.id}:thread-window-scroll`,
      JSON.stringify({ version: 1, scrollTop: 700 }),
    );

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: threadSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', ({ request, params }) => {
        requestedUrls.push(request.url);
        const url = new URL(request.url);
        if (url.searchParams.get('before') === '3') {
          transcriptScrollHeight = 1000;
          return HttpResponse.json({
            data: [
              {
                id: 1,
                session_id: threadSession.id,
                org_id: 'org-1',
                thread_id: params.threadId as string,
                turn_number: 1,
                role: 'user',
                content: 'Older saved-scroll prompt',
                created_at: '2026-02-17T07:01:00Z',
              },
              {
                id: 2,
                session_id: threadSession.id,
                org_id: 'org-1',
                thread_id: params.threadId as string,
                turn_number: 1,
                role: 'assistant',
                content: 'Older saved-scroll reply',
                created_at: '2026-02-17T07:02:00Z',
              },
            ] as SessionMessage[],
            meta: { has_older: false, thread_status: 'idle' },
          } satisfies ThreadMessageWindowResponse);
        }
        return HttpResponse.json({
          data: [
            {
              id: 3,
              session_id: threadSession.id,
              org_id: 'org-1',
              thread_id: params.threadId as string,
              turn_number: 2,
              role: 'assistant',
              content: 'Latest saved-scroll reply',
              created_at: '2026-02-17T07:03:00Z',
            },
          ] as SessionMessage[],
          meta: {
            next_older_cursor: '3',
            has_older: true,
            latest_assistant_message_id: 3,
            live_edge_message_id: 3,
            thread_status: 'idle',
          },
        } satisfies ThreadMessageWindowResponse);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    const { container } = renderWithProviders(<SessionDetailContent id={threadSession.id} />);

    await screen.findByText('Older saved-scroll prompt');
    await waitFor(() => {
      expect(requestedUrls.some((url) => new URL(url).searchParams.get('before') === '3')).toBe(true);
      expect(getChatScroller(container).scrollTop).toBe(700);
    });
  });

  it('opens a thread around a saved message anchor and loads newer messages downward', async () => {
    const threadSession: Session = {
      ...mockSessions[0],
      id: 'session-thread-anchor-restore',
      status: 'idle',
      completed_at: undefined,
      sandbox_state: 'snapshotted',
      threads: [
        {
          id: 'thread-anchor',
          session_id: 'session-thread-anchor-restore',
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Main',
          status: 'idle',
          current_turn: 3,
          created_at: '2026-02-17T07:00:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
      ],
    };
    const requestedUrls: string[] = [];

    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(1200);
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(300);
    window.localStorage.setItem(
      `session-scroll-position:org-1:user-1:${threadSession.id}:thread-anchor`,
      JSON.stringify({
        version: 2,
        anchor: { kind: 'message', id: 22 },
        offset_px: 18,
        scroll_top_fallback: 640,
      }),
    );

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: threadSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', ({ request, params }) => {
        requestedUrls.push(request.url);
        const url = new URL(request.url);
        if (url.searchParams.get('after') === '23') {
          return HttpResponse.json({
            data: [
              {
                id: 24,
                session_id: threadSession.id,
                org_id: 'org-1',
                thread_id: params.threadId as string,
                turn_number: 4,
                role: 'assistant',
                content: 'Newer anchored reply',
                created_at: '2026-02-17T07:04:00Z',
              },
            ] as SessionMessage[],
            meta: {
              has_older: false,
              has_newer: false,
              latest_assistant_message_id: 24,
              live_edge_message_id: 24,
              thread_status: 'idle',
              window_position: 'newer',
            },
          } satisfies ThreadMessageWindowResponse);
        }
        return HttpResponse.json({
          data: [
            {
              id: 21,
              session_id: threadSession.id,
              org_id: 'org-1',
              thread_id: params.threadId as string,
              turn_number: 2,
              role: 'user',
              content: 'Older anchored prompt',
              created_at: '2026-02-17T07:01:00Z',
            },
            {
              id: 22,
              session_id: threadSession.id,
              org_id: 'org-1',
              thread_id: params.threadId as string,
              turn_number: 2,
              role: 'assistant',
              content: 'Saved anchored reply',
              created_at: '2026-02-17T07:02:00Z',
            },
            {
              id: 23,
              session_id: threadSession.id,
              org_id: 'org-1',
              thread_id: params.threadId as string,
              turn_number: 3,
              role: 'user',
              content: 'Next anchored prompt',
              created_at: '2026-02-17T07:03:00Z',
            },
          ] as SessionMessage[],
          meta: {
            next_older_cursor: '21',
            has_older: true,
            next_newer_cursor: '23',
            has_newer: true,
            anchor_message_id: 22,
            anchor_found: true,
            latest_assistant_message_id: 22,
            live_edge_message_id: 24,
            thread_status: 'idle',
            window_position: 'around',
          },
        } satisfies ThreadMessageWindowResponse);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    const user = userEvent.setup();
    const { container } = renderWithProviders(<SessionDetailContent id={threadSession.id} />);

    await screen.findByText('Saved anchored reply');
    await waitFor(() => {
      const aroundUrl = requestedUrls.find((rawUrl) => new URL(rawUrl).searchParams.get('position') === 'around');
      expect(aroundUrl).toBeDefined();
      expect(new URL(aroundUrl!).searchParams.get('anchor_message_id')).toBe('22');
      expect(getChatScroller(container).scrollTop).toBe(18);
    });

    await user.click(screen.getByRole('button', { name: /Load newer/i }));

    expect(await screen.findByText('Newer anchored reply')).toBeInTheDocument();
    expect(requestedUrls.some((rawUrl) => new URL(rawUrl).searchParams.get('after') === '23')).toBe(true);
  });

  it('does not apply a delayed saved scroll restore after jumping to the latest message', async () => {
    const threadSession: Session = {
      ...mockSessions[0],
      id: 'session-thread-window-jump-latest',
      status: 'idle',
      completed_at: undefined,
      sandbox_state: 'snapshotted',
      threads: [
        {
          id: 'thread-window-scroll',
          session_id: 'session-thread-window-jump-latest',
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Main',
          status: 'idle',
          current_turn: 2,
          created_at: '2026-02-17T07:00:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
      ],
    };
    const requestedUrls: string[] = [];
    let transcriptScrollHeight = 500;
    let releaseOlderPage = () => {};
    const olderPageBlocked = new Promise<void>((resolve) => {
      releaseOlderPage = resolve;
    });

    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockImplementation(() => transcriptScrollHeight);
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(200);
    window.localStorage.setItem(
      `session-scroll-position:org-1:user-1:${threadSession.id}:thread-window-scroll`,
      JSON.stringify({ version: 1, scrollTop: 700 }),
    );

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: threadSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', async ({ request, params }) => {
        requestedUrls.push(request.url);
        const url = new URL(request.url);
        if (url.searchParams.get('before') === '3') {
          await olderPageBlocked;
          transcriptScrollHeight = 1000;
          return HttpResponse.json({
            data: [
              {
                id: 1,
                session_id: threadSession.id,
                org_id: 'org-1',
                thread_id: params.threadId as string,
                turn_number: 1,
                role: 'user',
                content: 'Older prompt after jump',
                created_at: '2026-02-17T07:01:00Z',
              },
              {
                id: 2,
                session_id: threadSession.id,
                org_id: 'org-1',
                thread_id: params.threadId as string,
                turn_number: 1,
                role: 'assistant',
                content: 'Older reply after jump',
                created_at: '2026-02-17T07:02:00Z',
              },
            ] as SessionMessage[],
            meta: { has_older: false, thread_status: 'idle' },
          } satisfies ThreadMessageWindowResponse);
        }
        return HttpResponse.json({
          data: [
            {
              id: 3,
              session_id: threadSession.id,
              org_id: 'org-1',
              thread_id: params.threadId as string,
              turn_number: 2,
              role: 'assistant',
              content: 'Latest reply before jump',
              created_at: '2026-02-17T07:03:00Z',
            },
          ] as SessionMessage[],
          meta: {
            next_older_cursor: '3',
            has_older: true,
            latest_assistant_message_id: 3,
            live_edge_message_id: 3,
            thread_status: 'idle',
          },
        } satisfies ThreadMessageWindowResponse);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    const { container } = renderWithProviders(<SessionDetailContent id={threadSession.id} />);

    await screen.findByText('Latest reply before jump');
    await waitFor(() => {
      expect(requestedUrls.some((url) => new URL(url).searchParams.get('before') === '3')).toBe(true);
    });

    const scroller = getChatScroller(container);
    fireEvent.keyDown(document, { key: 'End' });
    expect(scroller.scrollTop).toBe(500);

    releaseOlderPage();

    await screen.findByText('Older prompt after jump');
    await waitFor(() => {
      expect(scroller.scrollTop).toBe(1000);
    });
  });

  it('disables the composer while restoring the saved active thread on reopen', async () => {
    const threadSession: Session = {
      ...mockSessions[0],
      id: 'session-thread-restore-send-gate',
      status: 'idle',
      completed_at: undefined,
      sandbox_state: 'snapshotted',
      threads: [
        {
          id: 'thread-main',
          session_id: 'session-thread-restore-send-gate',
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Main',
          status: 'idle',
          current_turn: 1,
          created_at: '2026-02-17T07:00:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
        {
          id: 'thread-codex-2',
          session_id: 'session-thread-restore-send-gate',
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Codex 2',
          status: 'idle',
          current_turn: 1,
          created_at: '2026-02-17T07:03:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        },
      ],
    };

    let releaseAuth = () => {};
    const authReleased = new Promise<void>((resolve) => {
      releaseAuth = resolve;
    });

    window.localStorage.setItem(
      `session-active-thread:org-1:user-1:${threadSession.id}`,
      JSON.stringify({ version: 1, threadId: 'thread-codex-2' }),
    );

    server.use(
      http.get('/api/v1/auth/me', async () => {
        await authReleased;
        return HttpResponse.json({
          data: mockMembers[0],
        } satisfies SingleResponse<User>);
      }),
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: threadSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', ({ params }) => {
        const assistantContent = params.threadId === 'thread-main' ? 'Main reply' : 'Codex 2 reply';
        return HttpResponse.json({
          data: [
            {
              id: params.threadId === 'thread-main' ? 1 : 2,
              session_id: threadSession.id,
              org_id: 'org-1',
              thread_id: params.threadId as string,
              turn_number: 1,
              role: 'assistant',
              content: assistantContent,
              created_at: '2026-02-17T07:02:00Z',
            },
          ] as SessionMessage[],
          meta: {},
        } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    renderWithProviders(<SessionDetailContent id={threadSession.id} />);

    await screen.findByText('Loading thread...');
    expect(screen.getByRole('textbox')).toBeDisabled();

    releaseAuth();

    expect(await screen.findByText('Codex 2 reply')).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByPlaceholderText('Send a message to Codex 2...')).toBeEnabled();
    });
  });

  it('ignores a legacy saved top position and still opens a running session at the live edge', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      id: 'session-scroll-legacy-zero',
      status: 'running',
      completed_at: undefined,
      current_turn: 2,
      sandbox_state: 'running',
    };

    window.localStorage.setItem(`session-scroll-position:org-1:user-1:${runningSession.id}`, '0');
    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(900);
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(200);

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/timeline', () => {
        const timeline: SessionTimelineEntry[] = [
          {
            kind: 'message',
            created_at: '2026-02-17T07:01:00Z',
            message: { id: 1, session_id: runningSession.id, org_id: 'org-1', user_id: 'user-1', turn_number: 1, role: 'user', content: 'First request', created_at: '2026-02-17T07:01:00Z' },
          },
          {
            kind: 'message',
            created_at: '2026-02-17T07:02:00Z',
            message: { id: 2, session_id: runningSession.id, org_id: 'org-1', turn_number: 1, role: 'assistant', content: 'First response', created_at: '2026-02-17T07:02:00Z' },
          },
          {
            kind: 'message',
            created_at: '2026-02-17T07:03:00Z',
            message: { id: 3, session_id: runningSession.id, org_id: 'org-1', user_id: 'user-1', turn_number: 2, role: 'user', content: 'Second request', created_at: '2026-02-17T07:03:00Z' },
          },
          {
            kind: 'message',
            created_at: '2026-02-17T07:04:00Z',
            message: { id: 4, session_id: runningSession.id, org_id: 'org-1', turn_number: 2, role: 'assistant', content: 'Latest response', created_at: '2026-02-17T07:04:00Z' },
          },
        ];
        return HttpResponse.json({ data: timeline, meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
    );

    const { container } = renderWithProviders(<SessionDetailContent id={runningSession.id} />);
    await screen.findByText('Latest response');

    await waitFor(() => {
      expect(getChatScroller(container).scrollTop).toBe(900);
    });
  });

  it('opens active sessions at the live edge when there is no saved position', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      id: 'session-scroll-live-edge',
      status: 'running',
      completed_at: undefined,
      current_turn: 2,
      sandbox_state: 'running',
    };

    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(900);
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(200);

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/timeline', () => {
        const timeline: SessionTimelineEntry[] = [
          {
            kind: 'message',
            created_at: '2026-02-17T07:01:00Z',
            message: { id: 1, session_id: runningSession.id, org_id: 'org-1', user_id: 'user-1', turn_number: 1, role: 'user', content: 'Fix the bug', created_at: '2026-02-17T07:01:00Z' },
          },
        ];
        return HttpResponse.json({ data: timeline, meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
    );

    const { container } = renderWithProviders(<SessionDetailContent id={runningSession.id} />);
    await screen.findByText('Fix the bug');

    await waitFor(() => {
      expect(getChatScroller(container).scrollTop).toBe(900);
    });
  });

  it('does not persist a top-of-page scroll position when leaving before the initial anchor resolves', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      id: 'session-scroll-early-exit',
      status: 'idle',
      completed_at: undefined,
      current_turn: 2,
      sandbox_state: 'snapshotted',
    };

    let releaseTimeline = () => {};
    const timelineBlocked = new Promise<void>((resolve) => {
      releaseTimeline = resolve;
    });

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/timeline', async () => {
        await timelineBlocked;
        return HttpResponse.json({ data: [] as SessionTimelineEntry[], meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
    );

    const { container, unmount } = renderWithProviders(<SessionDetailContent id={idleSession.id} />);
    await screen.findByRole('heading', { name: 'Fixed TypeError by adding null check' });
    expect(getChatScroller(container).scrollTop).toBe(0);

    unmount();

    expect(window.localStorage.getItem(`session-scroll-position:org-1:user-1:${idleSession.id}`)).toBeNull();

    releaseTimeline();
  });
});
