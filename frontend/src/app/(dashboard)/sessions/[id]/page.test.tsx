import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { fireEvent, renderWithProviders, screen, userEvent, waitFor, within } from '@/test/test-utils';
import { act } from '@testing-library/react';
import { server } from '@/test/mocks/server';
import { mockSessions, mockMembers, mockIssues, mockPR, mockPRHealth } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import { api } from '@/lib/api';
import type { Issue, PullRequest, Session, SessionDiff, SessionMessage, SessionReviewComment, SessionReviewLoop, SessionThread, SessionTimelineEntry, User, SingleResponse, ListResponse } from '@/lib/types';

const { toast } = vi.hoisted(() => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));
const { routerPush } = vi.hoisted(() => ({
  routerPush: vi.fn(),
}));

function sessionWithoutRawDiff(session: Session): Session {
  const copy = { ...session };
  delete copy.diff;
  delete copy.diff_history;
  return copy;
}

function mockSessionDetailWithLazyDiff(session: Session) {
  server.use(
    http.get('/api/v1/sessions/:id', () => {
      return HttpResponse.json({ data: sessionWithoutRawDiff(session) } satisfies SingleResponse<Session>);
    }),
    http.get('/api/v1/sessions/:id/diff', () => {
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
  );
}

vi.mock('@/lib/notify', () => ({
  notify: toast,
}));

vi.mock('next/navigation', () => ({
  useRouter: () => ({
    push: routerPush,
  }),
  useSearchParams: () => new URLSearchParams(),
}));

// Mock EventSource (not available in jsdom)
class MockEventSource {
  static instances: MockEventSource[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSED = 2;
  readyState = 0;
  url: string;
  withCredentials = false;
  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  private listeners = new Map<string, Array<(ev: MessageEvent) => void>>();
  constructor(url: string | URL) {
    this.url = String(url);
    MockEventSource.instances.push(this);
  }
  addEventListener = vi.fn((event: string, handler: EventListenerOrEventListenerObject) => {
    const fn = typeof handler === 'function'
      ? handler as (ev: MessageEvent) => void
      : (ev: MessageEvent) => handler.handleEvent(ev);
    this.listeners.set(event, [...(this.listeners.get(event) ?? []), fn]);
  });
  removeEventListener = vi.fn((event: string, handler: EventListenerOrEventListenerObject) => {
    const existing = this.listeners.get(event) ?? [];
    this.listeners.set(event, existing.filter((fn) => fn !== handler));
  });
  close = vi.fn();
  dispatchEvent = vi.fn(() => true);
  emit(event: string, data: unknown) {
    const message = { data: JSON.stringify(data) } as MessageEvent;
    for (const listener of this.listeners.get(event) ?? []) {
      listener(message);
    }
  }
}

function setMobileViewport(matches: boolean) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: query === "(max-width: 767px)" ? matches : false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  });
}

beforeAll(() => {
  global.EventSource = MockEventSource as unknown as typeof EventSource;
  setMobileViewport(false);
});

afterEach(() => {
  MockEventSource.instances = [];
  toast.success.mockReset();
  toast.error.mockReset();
  routerPush.mockReset();
  vi.useRealTimers();
  window.localStorage.clear();
  vi.restoreAllMocks();
  setMobileViewport(false);
});

function getChatScroller(container: HTMLElement): HTMLDivElement {
  const scroller = container.querySelector('div.flex-1.overflow-y-auto.space-y-2.p-4');
  expect(scroller).toBeInstanceOf(HTMLDivElement);
  return scroller as HTMLDivElement;
}

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

  it('shows agent type label', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getAllByText(/Claude Code/).length).toBeGreaterThanOrEqual(1);
  });

  it('renders the session Linear chip as an outbound link when only linear_identifier_hint is available', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            linked_issues: [],
            linear_identifier_hint: 'ENG-1234',
          },
        } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const link = await screen.findByRole('link', { name: 'ENG-1234' });
    expect(link).toHaveAttribute('href', 'https://linear.app/issue/ENG-1234');
    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
  });

  it('shows a disabled review-loop action when no session snapshot is available', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Code review' })).not.toBeInTheDocument();
  });

  it('shows the review loop action after a PR exists when a snapshot is available', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            snapshot_key: 'snapshot-post-pr-review',
            sandbox_state: 'snapshotted',
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/review-loops', () => {
        return HttpResponse.json({
          data: [] as SessionReviewLoop[],
          meta: {},
        } satisfies ListResponse<SessionReviewLoop>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findByRole('button', { name: 'Review' })).toBeInTheDocument();
  });

  it('starts a manual review loop with the selected pass count', async () => {
    const user = userEvent.setup();
    let postedMaxPasses = 0;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[1],
            status: 'completed',
            snapshot_key: 'snapshot-manual-review',
            sandbox_state: 'snapshotted',
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/review-loops', () => {
        return HttpResponse.json({
          data: [] as SessionReviewLoop[],
          meta: {},
        } satisfies ListResponse<SessionReviewLoop>);
      }),
      http.post('/api/v1/sessions/:id/review-loops', async ({ request, params }) => {
        const body = await request.json() as { max_passes: number };
        postedMaxPasses = body.max_passes;
        return HttpResponse.json({
          data: {
            id: 'review-loop-selected-passes',
            org_id: 'org-1',
            session_id: params.id as string,
            status: 'running',
            source: 'manual',
            agent_type: 'codex',
            max_passes: body.max_passes,
            completed_passes: 0,
            review_required: false,
            started_at: '2026-02-17T07:12:00Z',
          },
        } satisfies SingleResponse<SessionReviewLoop>, { status: 201 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);

    await user.click(await screen.findByRole('button', { name: 'Review' }));
    await user.click(await screen.findByRole('button', { name: 'Increase review passes' }));
    await user.click(screen.getByRole('button', { name: 'Start review' }));

    await waitFor(() => {
      expect(postedMaxPasses).toBe(3);
    });
  });

  it('does not show a dedicated self-review button for viewers', async () => {
    server.use(
      http.get('/api/v1/auth/me', () => {
        return HttpResponse.json({
          data: {
            ...mockMembers[0],
            role: 'viewer',
          },
        } satisfies SingleResponse<User>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    await waitFor(() => {
      expect(screen.queryByRole('button', { name: 'Review' })).not.toBeInTheDocument();
    });
  });

  it('renders the session header title at text-sm size', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const headerTitle = await screen.findByRole('heading', {
      level: 1,
      name: 'Fixed TypeError by adding null check',
    });

    expect(headerTitle.className).toContain('text-sm');
    expect(headerTitle.className).not.toContain('text-xs');
  });

  it('lets the user edit the session title inline', async () => {
    const updatedTitle = 'Renamed session title';
    let currentTitle = 'Original editable title';

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            title: currentTitle,
            result_summary: undefined,
          },
        } satisfies SingleResponse<Session>);
      }),
      http.patch('/api/v1/sessions/:id', async ({ request }) => {
        const body = await request.json() as { title: string };
        currentTitle = body.title;
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            title: currentTitle,
            result_summary: undefined,
          },
        } satisfies SingleResponse<Session>);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findByRole('heading', { level: 1, name: currentTitle });
    await user.click(screen.getByRole('button', { name: 'Edit session title' }));

    const input = screen.getByDisplayValue(currentTitle);
    await user.clear(input);
    await user.type(input, updatedTitle);
    await user.click(screen.getByRole('button', { name: 'Save title' }));

    await waitFor(() => {
      expect(screen.getByRole('heading', { level: 1, name: updatedTitle })).toBeInTheDocument();
    });
  });

  it('shows a hover tooltip when Save title is disabled', async () => {
    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findByRole('heading', { level: 1, name: 'Fixed TypeError by adding null check' });
    await user.click(screen.getByRole('button', { name: 'Edit session title' }));

    const saveButton = screen.getByRole('button', { name: 'Save title' });
    expect(saveButton).toBeDisabled();

    await user.hover(saveButton.parentElement as HTMLElement);

    expect(await screen.findByRole('tooltip', { name: 'Enter a different title to save your changes.' })).toBeInTheDocument();
  });

  it('seeds the title editor from the same title shown in the header', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            title: undefined,
            pm_approach: 'Quick null check fix',
            result_summary: 'Fixed TypeError by adding null check',
          },
        } satisfies SingleResponse<Session>);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findByRole('heading', { level: 1, name: 'Quick null check fix' });
    await user.click(screen.getByRole('button', { name: 'Edit session title' }));

    expect(screen.getByDisplayValue('Quick null check fix')).toBeInTheDocument();
  });

  it('shows overview tab with status in detail panel', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getAllByText('Completed').length).toBeGreaterThanOrEqual(1);
  });

  it('renders the desktop detail panel as an opaque surface above neighboring content', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const detailPanel = screen.getByTestId('session-detail-panel');

    expect(detailPanel).toHaveClass('relative');
    expect(detailPanel).toHaveClass('z-10');
    expect(detailPanel).toHaveClass('bg-background');
  });

  it('shows detail panel tabs for Overview and Changes', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByRole('tab', { name: 'Overview' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Changes' })).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: 'Validation' })).not.toBeInTheDocument();
  });

  it('uses the same desktop header bar height for the conversation and detail panels', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.getByTestId('session-main-header')).toHaveClass('h-14');
    expect(screen.getByTestId('session-detail-header-bar')).toHaveClass('h-14');
  });

  it('uses a dedicated mobile close button that does not compete with PR actions', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findAllByText('Fixed TypeError by adding null check');
    await user.click(screen.getByRole('button', { name: 'Open session details' }));

    // panelTabsEl is rendered both inline (desktop) and inside the Sheet
    // (mobile), so we scope to the dialog Radix opens for the sheet to
    // assert on the mobile-visible instance specifically.
    const sheet = await screen.findByRole('dialog');
    const closeBtn = within(sheet).getByRole('button', { name: 'Close details' });
    expect(closeBtn).toBeInTheDocument();
    const viewPRLink = within(sheet).getByRole('link', { name: 'View PR' });
    expect(viewPRLink).toBeInTheDocument();
    expect(viewPRLink.className).not.toContain('w-full');
    expect(within(sheet).queryByRole('button', { name: 'Close' })).not.toBeInTheDocument();

    await user.click(closeBtn);

    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    });
  });

  it('switches between sandbox agent tabs and sends through the active thread', async () => {
    const sessionId = 'session-abcdef12-3456-7890';
    const threads: SessionThread[] = [
      {
        id: 'thread-codex',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'codex',
        label: 'Codex',
        status: 'idle',
        current_turn: 1,
        created_at: '2026-02-17T07:00:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
      {
        id: 'thread-claude',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'claude_code',
        label: 'Claude review',
        status: 'running',
        current_turn: 1,
        created_at: '2026-02-17T07:01:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
    ];
    const messagesByThread: Record<string, SessionMessage[]> = {
      'thread-codex': [
        {
          id: 10,
          session_id: sessionId,
          org_id: 'org-1',
          thread_id: 'thread-codex',
          turn_number: 1,
          role: 'assistant',
          content: 'Codex implemented the export endpoint.',
          created_at: '2026-02-17T07:02:00Z',
        },
      ],
      'thread-claude': [
        {
          id: 11,
          session_id: sessionId,
          org_id: 'org-1',
          thread_id: 'thread-claude',
          turn_number: 1,
          role: 'assistant',
          content: 'Claude found a missing pagination cap.',
          created_at: '2026-02-17T07:03:00Z',
        },
      ],
    };
    let createdThread = false;
    let sessionMessagePosted = false;
    let postedThreadID: string | null = null;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            status: 'idle',
            agent_type: 'codex',
            sandbox_state: 'ready',
            threads,
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.post('/api/v1/sessions/:id/messages', () => {
        sessionMessagePosted = true;
        return HttpResponse.json({ data: {} });
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', ({ params }) => {
        return HttpResponse.json({
          data: messagesByThread[params.threadId as string] ?? [],
          meta: {},
        } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({
          data: [],
          meta: {},
        });
      }),
      http.get('/api/v1/sessions/:id/thread-file-events', () => {
        return HttpResponse.json({
          data: [],
          meta: {},
        });
      }),
      http.post('/api/v1/sessions/:id/threads', async ({ request, params }) => {
        const body = await request.json() as { label: string; agent_type: string };
        createdThread = true;
        const thread: SessionThread = {
          id: 'thread-new',
          session_id: params.id as string,
          org_id: 'org-1',
          agent_type: body.agent_type,
          label: body.label,
          status: 'idle',
          current_turn: 0,
          created_at: '2026-02-17T07:04:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        };
        threads.push(thread);
        messagesByThread[thread.id] = [];
        return HttpResponse.json({ data: thread } satisfies SingleResponse<SessionThread>, { status: 201 });
      }),
      http.post('/api/v1/sessions/:id/threads/:threadId/messages', async ({ request, params }) => {
        const body = await request.json() as { message: string };
        postedThreadID = params.threadId as string;
        return HttpResponse.json({
          data: {
            id: 12,
            session_id: sessionId,
            org_id: 'org-1',
            thread_id: params.threadId as string,
            turn_number: 2,
            role: 'user',
            content: body.message,
            created_at: '2026-02-17T07:05:00Z',
          },
        } satisfies SingleResponse<SessionMessage>, { status: 201 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    expect(await screen.findByText('Codex implemented the export endpoint.')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: /Codex/ })).toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: /Claude review/ }));
    expect(await screen.findByText('Claude found a missing pagination cap.')).toBeInTheDocument();

    const addTabButtons = screen.getAllByRole('button', { name: 'Add agent tab' });
    const stripAddButton = addTabButtons[addTabButtons.length - 1] as HTMLButtonElement;
    await user.click(stripAddButton);

    await waitFor(() => {
      expect(createdThread).toBe(true);
    });
    expect(screen.queryByRole('dialog', { name: 'Add agent tab' })).not.toBeInTheDocument();
    expect(await screen.findByRole('tab', { name: /Claude Code 3/ })).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Send a message to Claude Code 3...')).toBeInTheDocument();

    await user.type(screen.getByPlaceholderText('Send a message to Claude Code 3...'), 'Run the frontend checks.');
    await user.click(screen.getByRole('button', { name: 'Send message' }));

    await waitFor(() => {
      expect(postedThreadID).toBe('thread-new');
    });
    expect(sessionMessagePosted).toBe(false);
  }, 10000);

  it('lets a blank new tab switch from Claude Code to Codex before the first message', async () => {
    const sessionId = 'session-switch-agent-before-send';
    const threads: SessionThread[] = [
      {
        id: 'thread-main',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'claude_code',
        label: 'Claude Code',
        status: 'idle',
        current_turn: 1,
        created_at: '2026-02-17T07:00:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
    ];
    let patchBody: { agent_type?: string; label?: string; model?: string } | null = null;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            id: sessionId,
            status: 'idle',
            agent_type: 'claude_code',
            sandbox_state: 'ready',
            threads,
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({ data: [] as SessionMessage[], meta: {} } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get('/api/v1/sessions/:id/thread-file-events', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.post('/api/v1/sessions/:id/threads', async ({ request, params }) => {
        const body = await request.json() as { label: string; agent_type: string };
        const thread: SessionThread = {
          id: 'thread-new',
          session_id: params.id as string,
          org_id: 'org-1',
          agent_type: body.agent_type as SessionThread['agent_type'],
          label: body.label,
          status: 'idle',
          current_turn: 0,
          created_at: '2026-02-17T07:04:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        };
        threads.push(thread);
        return HttpResponse.json({ data: thread } satisfies SingleResponse<SessionThread>, { status: 201 });
      }),
      http.patch('/api/v1/sessions/:id/threads/:threadId', async ({ request, params }) => {
        patchBody = await request.json() as { agent_type?: string; label?: string; model?: string };
        const updatedThread: SessionThread = {
          id: params.threadId as string,
          session_id: sessionId,
          org_id: 'org-1',
          agent_type: (patchBody.agent_type ?? 'codex') as SessionThread['agent_type'],
          model_override: patchBody.model,
          label: patchBody.label ?? 'Codex 2',
          status: 'idle',
          current_turn: 0,
          created_at: '2026-02-17T07:04:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        };
        threads[1] = updatedThread;
        return HttpResponse.json({ data: updatedThread } satisfies SingleResponse<SessionThread>);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    expect(await screen.findByPlaceholderText('Send a message to Claude Code...')).toBeInTheDocument();

    const addTabButtons = screen.getAllByRole('button', { name: 'Add agent tab' });
    const stripAddButton = addTabButtons[addTabButtons.length - 1] as HTMLButtonElement;
    await user.click(stripAddButton);
    expect(await screen.findByPlaceholderText('Send a message to Claude Code 2...')).toBeInTheDocument();

    await user.click(screen.getByLabelText('Agent'));
    await user.click(await screen.findByRole('option', { name: 'Codex' }));

    await waitFor(() => {
      expect(patchBody).toEqual({ agent_type: 'codex', label: 'Codex 2' });
    });
    expect(screen.getByRole('tab', { name: /Codex 2/ })).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Send a message to Codex 2...')).toBeInTheDocument();
  });

  it('shows a desktop header add-tab action that creates a tab directly', async () => {
    const sessionId = 'session-header-new-tab';
    const threads: SessionThread[] = [
      {
        id: 'thread-main',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'codex',
        label: 'Codex',
        status: 'idle',
        current_turn: 1,
        created_at: '2026-02-17T07:00:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
    ];
    let createdThread = false;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            id: sessionId,
            status: 'idle',
            agent_type: 'codex',
            sandbox_state: 'ready',
            threads,
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({ data: [] as SessionMessage[], meta: {} } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get('/api/v1/sessions/:id/thread-file-events', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.post('/api/v1/sessions/:id/threads', async ({ request, params }) => {
        const body = await request.json() as { label: string; agent_type: string };
        createdThread = true;
        const thread: SessionThread = {
          id: 'thread-new',
          session_id: params.id as string,
          org_id: 'org-1',
          agent_type: body.agent_type as SessionThread['agent_type'],
          label: body.label,
          status: 'idle',
          current_turn: 0,
          created_at: '2026-02-17T07:04:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        };
        threads.push(thread);
        return HttpResponse.json({ data: thread } satisfies SingleResponse<SessionThread>, { status: 201 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    await screen.findByPlaceholderText('Send a message to Codex...');

    const headerActions = screen.getByTestId('session-header-actions');
    const newTabButton = within(headerActions).getByRole('button', { name: 'Add agent tab' });
    await user.click(newTabButton);

    await waitFor(() => {
      expect(createdThread).toBe(true);
    });
    expect(await screen.findByRole('tab', { name: /Codex 2/ })).toBeInTheDocument();
  });

  it('shows the desktop agent tab row as soon as a second tab is being created', async () => {
    const sessionId = 'session-show-tab-row-while-creating';
    const threads: SessionThread[] = [
      {
        id: 'thread-main',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'codex',
        label: 'Codex',
        status: 'idle',
        current_turn: 1,
        created_at: '2026-02-17T07:00:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
    ];
    let resolveCreateThread: ((thread: SessionThread) => void) | null = null;
    const createThreadResponse = new Promise<SingleResponse<SessionThread>>((resolve) => {
      resolveCreateThread = (thread) => resolve({ data: thread });
    });

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            id: sessionId,
            status: 'idle',
            agent_type: 'codex',
            sandbox_state: 'ready',
            threads,
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({ data: [] as SessionMessage[], meta: {} } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get('/api/v1/sessions/:id/thread-file-events', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.post('/api/v1/sessions/:id/threads', async ({ params }) => {
        const thread: SessionThread = {
          id: 'thread-new',
          session_id: params.id as string,
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Codex 2',
          status: 'idle',
          current_turn: 0,
          created_at: '2026-02-17T07:04:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        };
        threads.push(thread);
        const response = await createThreadResponse;
        return HttpResponse.json(response, { status: 201 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    const addTabButtons = await screen.findAllByRole('button', { name: 'Add agent tab' });
    const stripAddButton = addTabButtons[addTabButtons.length - 1] as HTMLButtonElement;
    await user.click(stripAddButton);

    expect(await screen.findByRole('tablist', { name: 'Agent tabs' })).toBeInTheDocument();
    const pendingTab = screen.getByRole('tab', { name: /Codex 2/ });
    expect(pendingTab).toBeDisabled();

    await user.click(pendingTab);

    expect(screen.getByPlaceholderText('Send a message to Codex...')).toBeInTheDocument();
    expect(screen.queryByText('Loading thread...')).not.toBeInTheDocument();

    expect(resolveCreateThread).not.toBeNull();
    resolveCreateThread!({
      id: 'thread-new',
      session_id: sessionId,
      org_id: 'org-1',
      agent_type: 'codex',
      label: 'Codex 2',
      status: 'idle',
      current_turn: 0,
      created_at: '2026-02-17T07:04:00Z',
      cost_cents: 0,
      pending_message_count: 0,
    });

    expect(await screen.findByPlaceholderText('Send a message to Codex 2...')).toBeInTheDocument();
  });

  it('persists the selected model on a blank tab before the first thread send', async () => {
    const sessionId = 'session-persist-model-before-first-send';
    const threads: SessionThread[] = [
      {
        id: 'thread-main',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'codex',
        label: 'Codex',
        status: 'idle',
        current_turn: 1,
        created_at: '2026-02-17T07:00:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
    ];
    const patchedBodies: Array<{ agent_type?: string; label?: string; model?: string }> = [];
    let postedMessageBody: { message: string } | null = null;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            id: sessionId,
            status: 'idle',
            agent_type: 'codex',
            sandbox_state: 'ready',
            threads,
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({ data: [] as SessionMessage[], meta: {} } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get('/api/v1/sessions/:id/thread-file-events', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.post('/api/v1/sessions/:id/threads', async ({ request, params }) => {
        const body = await request.json() as { label: string; agent_type: string; model?: string };
        const thread: SessionThread = {
          id: 'thread-new',
          session_id: params.id as string,
          org_id: 'org-1',
          agent_type: body.agent_type as SessionThread['agent_type'],
          model_override: body.model,
          label: body.label,
          status: 'idle',
          current_turn: 0,
          created_at: '2026-02-17T07:04:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        };
        threads.push(thread);
        return HttpResponse.json({ data: thread } satisfies SingleResponse<SessionThread>, { status: 201 });
      }),
      http.patch('/api/v1/sessions/:id/threads/:threadId', async ({ request, params }) => {
        const body = await request.json() as { agent_type?: string; label?: string; model?: string };
        patchedBodies.push(body);
        const updatedThread: SessionThread = {
          id: params.threadId as string,
          session_id: sessionId,
          org_id: 'org-1',
          agent_type: (body.agent_type ?? 'codex') as SessionThread['agent_type'],
          model_override: body.model,
          label: body.label ?? 'Codex 2',
          status: 'idle',
          current_turn: 0,
          created_at: '2026-02-17T07:04:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        };
        threads[1] = updatedThread;
        return HttpResponse.json({ data: updatedThread } satisfies SingleResponse<SessionThread>);
      }),
      http.post('/api/v1/sessions/:id/threads/:threadId/messages', async ({ request, params }) => {
        postedMessageBody = await request.json() as { message: string };
        return HttpResponse.json({
          data: {
            id: 101,
            session_id: params.id as string,
            org_id: 'org-1',
            thread_id: params.threadId as string,
            turn_number: 1,
            role: 'user',
            content: postedMessageBody.message,
            created_at: '2026-02-17T07:05:00Z',
          },
        } satisfies SingleResponse<SessionMessage>, { status: 201 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    expect(await screen.findByPlaceholderText('Send a message to Codex...')).toBeInTheDocument();

    const addTabButtons = screen.getAllByRole('button', { name: 'Add agent tab' });
    const stripAddButton = addTabButtons[addTabButtons.length - 1] as HTMLButtonElement;
    await user.click(stripAddButton);
    expect(await screen.findByPlaceholderText('Send a message to Codex 2...')).toBeInTheDocument();

    await user.click(screen.getByLabelText('Model override'));
    await user.click(await screen.findByRole('option', { name: 'gpt-5.4-mini' }));
    await user.type(screen.getByPlaceholderText('Send a message to Codex 2...'), 'Use the selected model.');
    await user.click(screen.getByRole('button', { name: 'Send message' }));

    await waitFor(() => {
      expect(patchedBodies).toContainEqual({ label: 'Codex 2', model: 'gpt-5.4-mini' });
    });
    await waitFor(() => {
      expect(postedMessageBody).toEqual({ message: 'Use the selected model.' });
    });
  });

  it('shows the agent selector before model override on a new blank tab composer', async () => {
    const sessionId = 'session-new-tab-agent-before-model';
    const threads: SessionThread[] = [
      {
        id: 'thread-main',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'codex',
        label: 'Codex',
        status: 'idle',
        current_turn: 1,
        created_at: '2026-02-17T07:00:00Z',
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
            agent_type: 'codex',
            sandbox_state: 'ready',
            threads,
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({ data: [] as SessionMessage[], meta: {} } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.post('/api/v1/sessions/:id/threads', async ({ params }) => {
        const thread: SessionThread = {
          id: 'thread-new',
          session_id: params.id as string,
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Codex 2',
          status: 'idle',
          current_turn: 0,
          created_at: '2026-02-17T07:04:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        };
        threads.push(thread);
        return HttpResponse.json({ data: thread } satisfies SingleResponse<SessionThread>, { status: 201 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    expect(await screen.findByPlaceholderText('Send a message to Codex...')).toBeInTheDocument();

    const addTabButtons = screen.getAllByRole('button', { name: 'Add agent tab' });
    const stripAddButton = addTabButtons[addTabButtons.length - 1] as HTMLButtonElement;
    await user.click(stripAddButton);

    expect(await screen.findByPlaceholderText('Send a message to Codex 2...')).toBeInTheDocument();

    const inputSurface = screen.getByTestId('session-composer-input-surface');
    const agentSelector = within(inputSurface).getByLabelText('Agent');
    const modelSelector = within(inputSurface).getByLabelText('Model override');

    expect(agentSelector.compareDocumentPosition(modelSelector) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('preserves thread tabs when session status SSE payload omits thread detail', async () => {
    const sessionId = 'session-abcdef12-3456-7890';
    const thread: SessionThread = {
      id: 'thread-codex',
      session_id: sessionId,
      org_id: 'org-1',
      agent_type: 'codex',
      label: 'Codex',
      status: 'running',
      current_turn: 1,
      created_at: '2026-02-17T07:00:00Z',
      cost_cents: 0,
      pending_message_count: 0,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            id: sessionId,
            status: 'running',
            sandbox_state: 'running',
            threads: [thread],
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    renderWithProviders(<SessionDetailContent id={sessionId} />);

    expect(await screen.findByRole('group', { name: /Codex/ })).toBeInTheDocument();
    await waitFor(() => {
      expect(MockEventSource.instances.length).toBeGreaterThan(0);
    });

    act(() => {
      MockEventSource.instances[0].emit('status', {
        ...mockSessions[0],
        id: sessionId,
        status: 'running',
        sandbox_state: 'running',
      });
    });

    expect(screen.getByRole('group', { name: /Codex/ })).toBeInTheDocument();
  });

  it('archives a closed thread and switches focus to a remaining tab', async () => {
    const sessionId = 'session-archive-thread';
    let threads: SessionThread[] = [
      {
        id: 'thread-main',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'codex',
        label: 'Main tab',
        status: 'running',
        current_turn: 1,
        created_at: '2026-02-17T07:00:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
      {
        id: 'thread-review',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'claude_code',
        label: 'Review',
        status: 'completed',
        current_turn: 1,
        created_at: '2026-02-17T07:02:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
    ];
    let archivedThreadId: string | null = null;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            id: sessionId,
            status: 'running',
            sandbox_state: 'running',
            agent_type: 'codex',
            threads,
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({ data: [] as SessionMessage[], meta: {} } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get('/api/v1/sessions/:id/thread-file-events', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.post('/api/v1/sessions/:id/threads/:threadId/archive', ({ params }) => {
        archivedThreadId = params.threadId as string;
        threads = threads.filter((thread) => thread.id !== archivedThreadId);
        return HttpResponse.json({
          data: {
            id: archivedThreadId,
            session_id: sessionId,
            org_id: 'org-1',
            agent_type: 'claude_code',
            label: 'Review',
            status: 'completed',
            current_turn: 1,
            created_at: '2026-02-17T07:02:00Z',
            archived_at: '2026-02-17T07:05:00Z',
            cost_cents: 0,
            pending_message_count: 0,
          },
        } satisfies SingleResponse<SessionThread>);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    expect(await screen.findByRole('tab', { name: 'Review' })).toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: 'Review' }));
    await user.click(screen.getByRole('button', { name: 'Close Review tab' }));

    await waitFor(() => {
      expect(archivedThreadId).toBe('thread-review');
    });
    await waitFor(() => {
      expect(screen.queryByRole('tab', { name: 'Review' })).not.toBeInTheDocument();
    });
    expect(screen.getByPlaceholderText('Send a message to Main tab...')).toBeInTheDocument();
  });

  it('does not hide vertical overflow on the detail tablist', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const tabList = screen.getByRole('tablist');
    expect(tabList.className).not.toContain('overflow-y-hidden');
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
    expect(await screen.findByTestId('session-timeline-skeleton')).toBeInTheDocument();
    expect(screen.queryByText('No activity yet')).not.toBeInTheDocument();
    expect(screen.queryByText(/files? changed/)).not.toBeInTheDocument();
    expect(screen.getByPlaceholderText('Send a follow-up message...')).toBeInTheDocument();
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
    expect(await screen.findByTestId('session-timeline-skeleton')).toBeInTheDocument();
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
                },
              }]
            : [],
          meta: {},
        } satisfies ListResponse<SessionTimelineEntry>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={runningSession.id} />);

    expect(await screen.findByText('Agent is working...')).toBeInTheDocument();
    expect(timelineFetchCount).toBe(1);
    expect(MockEventSource.instances).toHaveLength(1);

    MockEventSource.instances[0].onerror?.(new Event('error'));

    await waitFor(() => {
      expect(MockEventSource.instances).toHaveLength(2);
    }, { timeout: 2500 });

    expect(await screen.findByText('late log after reconnect')).toBeInTheDocument();

    await waitFor(() => {
      expect(timelineFetchCount).toBeGreaterThanOrEqual(2);
    });
  });

  it('includes the active org in the PR health stream URL', async () => {
    const activeOrgId = '22222222-2222-2222-2222-222222222222';
    window.sessionStorage.setItem('active_org_id', activeOrgId);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findByText('PR health')).toBeInTheDocument();
    expect(MockEventSource.instances).toHaveLength(1);
    expect(MockEventSource.instances[0].url).toContain(`/api/v1/pull-requests/stream?org_id=${activeOrgId}`);
  });

  it('reconnects the PR health stream after an SSE error', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findByText('PR health')).toBeInTheDocument();
    expect(MockEventSource.instances).toHaveLength(1);

    MockEventSource.instances[0].onerror?.(new Event('error'));

    await waitFor(() => {
      expect(MockEventSource.instances).toHaveLength(2);
    }, { timeout: 2500 });
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

    expect(await screen.findByText('Implementation Plan')).toBeInTheDocument();
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
    expect(within(viewPRLink).queryByRole('button')).not.toBeInTheDocument();
  });

  it('keeps the tab rail scrollable while separating top-right actions', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const tabRail = await screen.findByLabelText('Session detail tabs');
    const actions = screen.getByLabelText('Session detail actions');

	expect(tabRail).toHaveClass('overflow-x-auto');
	expect(tabRail).toHaveClass('scrollbar-hide');
	expect(tabRail).toHaveClass('min-w-0');
	expect(actions).toHaveClass('shrink-0');
    expect(within(actions).getByRole('link', { name: 'View PR' })).toBeInTheDocument();
  });

  it('shows the horizontal tab scrollbar only when tabs run into the action buttons', async () => {
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
      expect(tabRail).not.toHaveClass('scrollbar-hide');
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

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findByText('PR #42 is waiting for required checks to report passing.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Merge$/ })).not.toBeInTheDocument();

    await waitFor(() => {
      expect(MockEventSource.instances.some((source) => source.url.includes('/api/v1/pull-requests/stream'))).toBe(true);
    });
    const prStream = MockEventSource.instances.find((source) => source.url.includes('/api/v1/pull-requests/stream'));

    act(() => {
      prStream?.onopen?.(new Event('open'));
    });

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

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

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
    expect(screen.getByLabelText('Merged PR status')).toHaveClass('text-violet-700', 'dark:text-violet-400');
    expect(screen.queryAllByText('PR created')).toHaveLength(0);
    expect(within(screen.getByLabelText('Session detail actions')).queryByText('PR #42 merged')).not.toBeInTheDocument();
    expect(screen.queryByText('PR health')).not.toBeInTheDocument();
  });

  it('updates the header status when the PR stream reports a merge', async () => {
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

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findAllByText('PR created')).toHaveLength(2);
    await waitFor(() => {
      expect(MockEventSource.instances.some((source) => source.url.includes('/api/v1/pull-requests/stream'))).toBe(true);
    });

    currentPR = {
      ...currentPR,
      status: 'merged',
      merged_at: '2026-02-17T07:10:00Z',
      updated_at: '2026-02-17T07:10:00Z',
    };

    const prStream = MockEventSource.instances.find((source) => source.url.includes('/api/v1/pull-requests/stream'));
    expect(prStream).toBeDefined();

    act(() => {
      prStream?.emit('pull_request.updated', {
        pull_request_id: 'pr-1',
        version: 2,
        head_sha: 'head-sha',
        base_sha: 'base-sha',
        synced_at: '2026-02-17T07:10:00Z',
      });
    });

    await waitFor(() => {
      const mergedBadges = screen.getAllByText('PR merged');
      expect(mergedBadges).toHaveLength(2);
      for (const badge of mergedBadges) {
        expect(badge).toHaveClass('text-violet-700', 'dark:text-violet-400');
      }
    });
    expect(screen.queryAllByText('PR created')).toHaveLength(0);
  });

  it('hides the Merge button while CI is still in flight', async () => {
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

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByText('PR health');
    expect(screen.queryByRole('button', { name: /^Merge$/ })).not.toBeInTheDocument();
  });

  it('shows a Merge button that opens GitHub auth when the org requires GitHub user auth and the user is disconnected', async () => {
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            can_merge: true,
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
    expect(screen.getByRole('button', { name: 'Continue with GitHub' })).toBeInTheDocument();
  });

  it('toasts the API error message when the merge call fails', async () => {
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
        return HttpResponse.json({
          data: {
            ...mockPRHealth,
            can_merge: true,
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

  it('hides the Merge button when checks have not yet confirmed a passing state', async () => {
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
    expect(screen.queryByRole('button', { name: /^Merge$/ })).not.toBeInTheDocument();
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
    expect(screen.queryByRole('button', { name: /^Merge$/ })).not.toBeInTheDocument();
  });

  it('routes to a new revision session after starting a PR repair action', async () => {
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
        return HttpResponse.json({
          data: {
            session_id: 'session-revision-123',
            mode: 'revision',
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
      expect(routerPush).toHaveBeenCalledWith('/sessions/session-revision-123');
    });
  });

  it('keeps the repair CTA suppressed while navigating to a different repair session', async () => {
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => {
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
      }),
      http.post('/api/v1/pull-requests/:id/repair/fix-tests', () => {
        return HttpResponse.json({
          data: {
            session_id: 'session-revision-123',
            mode: 'revision',
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
      expect(routerPush).toHaveBeenCalledWith('/sessions/session-revision-123');
    });
    expect(screen.getByRole('button', { name: 'Opening repair session…' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Fix tests' })).not.toBeInTheDocument();
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
    // 5m 30s duration between started_at and completed_at (shown in timestamp row)
    expect(screen.getByText('5m 30s')).toBeInTheDocument();
  });

  it('shows pass selector when session has diff_history with multiple passes', async () => {
    const pass1Diff = 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);';
    const pass2Diff = 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);\ndiff --git a/src/new.ts b/src/new.ts\n--- /dev/null\n+++ b/src/new.ts\n@@ -0,0 +1 @@\n+export const x = 1;';

    const sessionWithHistory: Session = {
      ...mockSessions[0],
      diff: pass2Diff,
      diff_stats: { added: 2, removed: 0, files_changed: 2 },
      diff_history: [
        { pass: 1, diff: pass1Diff, diff_stats: { added: 1, removed: 0, files_changed: 1 }, created_at: '2026-03-19T10:00:00Z' },
        { pass: 2, diff: pass2Diff, diff_stats: { added: 2, removed: 0, files_changed: 2 }, created_at: '2026-03-19T10:05:00Z' },
      ],
      current_turn: 2,
    };

    mockSessionDetailWithLazyDiff(sessionWithHistory);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const changesTab = screen.getByRole('tab', { name: /^Changes/ });
    await user.click(changesTab);

    // Pass selector should be visible with "All changes" label
    expect(await screen.findByText('All changes')).toBeInTheDocument();
  });

  it('shows Create PR button for completed session with diff and no existing PR', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(await screen.findByRole('button', { name: /Create PR/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Create PR/ })).not.toBeDisabled();
  });

  it('shows Create PR button for completed session with snapshot even when diff stats are missing', async () => {
    const sessionWithSnapshotOnly: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff_stats: undefined,
      snapshot_key: 'snap-abc',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithSnapshotOnly } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(await screen.findByRole('button', { name: /Create PR/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Create PR/ })).not.toBeDisabled();
  });

  it('hides PR mutation controls and skips the team roster lookup for builders', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
    };
    let teamRequestCount = 0;

    server.use(
      http.get('/api/v1/auth/me', () => {
        return HttpResponse.json({
          data: {
            ...mockMembers[0],
            role: 'builder',
          },
        } satisfies SingleResponse<User>);
      }),
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.get('/api/v1/team/members', () => {
        teamRequestCount += 1;
        return HttpResponse.json({ error: { code: 'FORBIDDEN', message: 'insufficient permissions' } }, { status: 403 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findAllByText('Fixed TypeError by adding null check');
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /Create PR/ })).not.toBeInTheDocument();
    });
    expect(teamRequestCount).toBe(0);
  });

  it('does not show Create PR button when PR already exists', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      // Default handler already returns mockPR for GET /sessions/:id/pr
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    // Wait for the PR query to resolve before asserting
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /Create PR/ })).not.toBeInTheDocument();
    });
  });

  it('shows Push changes only when the session has unpushed changes', async () => {
    const sessionWithUnpushedChanges: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      has_unpushed_changes: true,
      pr_push_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithUnpushedChanges } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(await screen.findByRole('button', { name: 'Push changes' })).toBeInTheDocument();
  });

  it('hides Push changes when the PR already matches the latest session head', async () => {
    const sessionWithoutUnpushedChanges: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      has_unpushed_changes: false,
      pr_push_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithoutUnpushedChanges } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    await waitFor(() => {
      expect(screen.queryByRole('button', { name: 'Push changes' })).not.toBeInTheDocument();
    });
  });

  it('does not show a PR snapshot error when the PR already exists', async () => {
    const sessionWithStalePRError: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: undefined,
      pr_creation_state: 'failed',
      pr_creation_error: 'session state expired — re-run to create a PR',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithStalePRError } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.getByRole('link', { name: /View PR/ })).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('does not show Create PR button when session has no diff', async () => {
    // Default mockSessions[0] has no diff_stats
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.queryByRole('button', { name: /Create PR/ })).not.toBeInTheDocument();
  });

  it('shows checkpoint-missing notice when diff exists but no reusable snapshot was saved', async () => {
    const sessionWithMissingSnapshot: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      sandbox_state: 'none',
      snapshot_key: undefined,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithMissingSnapshot } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('No reusable checkpoint saved');
    expect(alert).toHaveTextContent('This session finished without saving a reusable checkpoint for PR creation. Send a new message to rebuild the sandbox, then create the PR again.');
    expect(within(alert).queryByRole('button')).not.toBeInTheDocument();
  });

  it('shows snapshot-expired notice when the saved checkpoint was reaped', async () => {
    const expiredSession: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      sandbox_state: 'destroyed',
      snapshot_key: undefined,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: expiredSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Session snapshot expired');
    expect(alert).toHaveTextContent('This session snapshot expired before a PR could be created. Send a new message to rebuild the sandbox, then create the PR again.');
  });

  it('shows a hover tooltip when Create PR is disabled', async () => {
    const sessionWithSnapshot: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      sandbox_state: 'snapshotted',
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithSnapshot } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', async () => {
        await new Promise((resolve) => setTimeout(resolve, 1000));
        return HttpResponse.json({ data: { status: 'queued' } });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const createPRButton = await screen.findByRole('button', { name: 'Create PR' });
    await user.click(createPRButton);

    const queueingButton = await screen.findByRole('button', { name: 'Queueing PR…' });
    expect(queueingButton).toBeDisabled();

    await user.hover(queueingButton.parentElement as HTMLElement);

    expect(await screen.findByRole('tooltip', { name: 'Sending the PR request to the queue' })).toBeInTheDocument();
  });

  it('matches the snapshot expiry notice horizontal margins to overview cards', async () => {
    const sessionWithMissingSnapshot: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: undefined,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithMissingSnapshot } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const alert = await screen.findByRole('alert');
    expect(alert.className).toContain('mx-2');
  });

  it('shows a resume-specific PR error instead of session expiry when the GitHub resume token is stale', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.get('/api/v1/users/me/github-status', () => {
        return HttpResponse.json({
          connected: true,
          has_repo_scope: true,
          github_login: 'alice',
          pr_authorship_mode: 'user_preferred',
          pr_draft_default: false,
        });
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          {
            error: {
              code: 'PR_RESUME_EXPIRED',
              message: 'GitHub authorization completed, but the PR resume request expired. Please click Create PR again.',
            },
          },
          { status: 409 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />, {
      searchParams: { github_pr: 'connected', resume_pr: 'resume-123' },
    });
    await screen.findAllByText('Fixed TypeError by adding null check');

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent("Couldn't resume PR creation");
    expect(alert).toHaveTextContent('GitHub authorization completed, but the PR resume request expired. Please click Create PR again.');
    expect(alert).not.toHaveTextContent('PR session expired');
  });

  it('does not show Create PR button when session is running', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      status: 'running',
      completed_at: undefined,
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      sandbox_state: 'running',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByText('Agent is working...');
    expect(screen.queryByRole('button', { name: /Create PR/ })).not.toBeInTheDocument();
  });

  it('does not show a checkpoint-missing notice for an active run before a checkpoint is expected', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      status: 'running',
      completed_at: undefined,
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      sandbox_state: 'running',
      snapshot_key: undefined,
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByText('Agent is working...');

    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.queryByText('No reusable checkpoint saved')).not.toBeInTheDocument();
  });

  it('does not show a checkpoint-missing notice for an idle interactive session before PR creation starts', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      sandbox_state: 'snapshotted',
      snapshot_key: undefined,
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.queryByText('No reusable checkpoint saved')).not.toBeInTheDocument();
  });

  it('does not show a checkpoint-missing notice while awaiting user input', async () => {
    const awaitingInputSession: Session = {
      ...mockSessions[0],
      status: 'awaiting_input',
      completed_at: undefined,
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      sandbox_state: 'snapshotted',
      snapshot_key: undefined,
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: awaitingInputSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.queryByText('No reusable checkpoint saved')).not.toBeInTheDocument();
  });

  it('calls createPR API when Create PR button is clicked', async () => {
    let createPRCalled = false;

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        createPRCalled = true;
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const createPRButton = await screen.findByRole('button', { name: /Create PR/ });
    await user.click(createPRButton);

    await waitFor(() => {
      expect(createPRCalled).toBe(true);
    });
  });

  it('does not queue duplicate create PR requests from the keyboard while one is pending', async () => {
    let createPRCalls = 0;

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        createPRCalls += 1;
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByRole('button', { name: /Create PR/ });
    act(() => {
      (document.activeElement as HTMLElement | null)?.blur();
    });

    await userEvent.keyboard('pc');
    await waitFor(() => {
      expect(createPRCalls).toBe(1);
    });
    await userEvent.keyboard('pc');

    expect(createPRCalls).toBe(1);
  });

  it('keeps the button in a pending state after create PR starts without server PR state fields', async () => {
    const legacySession: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: undefined,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: legacySession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    expect(await screen.findByRole('button', { name: /Creating PR…/ })).toBeDisabled();
  });

  it('shows an immediate queueing state while the create PR request is still being sent', async () => {
    let resolveCreatePR: (() => void) | undefined;

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', async () => {
        await new Promise<void>((resolve) => {
          resolveCreatePR = resolve;
        });
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    expect(await screen.findByRole('button', { name: /Queueing PR…/ })).toBeDisabled();

    resolveCreatePR?.();
  });

  it('shows the immediate create PR error in a 10-second toast', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'PUSH_FAILED', message: 'GitHub rejected the branch push.' } },
          { status: 500 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith('PR creation failed', { duration: 10000 });
    });
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent("Couldn't create the PR");
    expect(alert).toHaveTextContent('GitHub rejected the branch push.');
    expect(within(alert).getByRole('button', { name: 'Retry' })).toBeInTheDocument();
  });

  it('shows the PR authorship modal and falls back to app mode when requested', async () => {
    const requestBodies: unknown[] = [];

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.get('/api/v1/users/me/github-status', () => {
        return HttpResponse.json({
          connected: false,
          has_repo_scope: false,
          pr_authorship_mode: 'user_preferred',
          pr_draft_default: false,
        });
      }),
      http.post('/api/v1/sessions/:id/pr', async ({ request }) => {
        requestBodies.push(await request.json().catch(() => undefined));
        if (requestBodies.length === 1) {
          return HttpResponse.json(
            {
              error: {
                code: 'GITHUB_PR_AUTHORSHIP_REQUIRED',
                message: 'Authorize GitHub to create this pull request as you.',
                details: {
                  connect_url: '/api/v1/users/me/github/connect?flow=pr_authorship',
                  resume_token: 'resume-123',
                  can_fallback_to_app: true,
                },
              },
            },
            { status: 409 },
          );
        }
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    expect(await screen.findByText('Open this pull request as yourself?')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Create as 143' }));

    await waitFor(() => {
      expect(requestBodies).toEqual([undefined, { author_mode: 'app' }]);
    });
  });

  it('opens the GitHub auth prompt when pressing p c and PR authorship is required', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
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
    await screen.findByRole('button', { name: /Create PR/ });
    act(() => {
      (document.activeElement as HTMLElement | null)?.blur();
    });

    await userEvent.keyboard('pc');

    expect(await screen.findByText('Open this pull request as yourself?')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Continue with GitHub' })).toBeInTheDocument();
  });

  it('auto-resumes PR creation after GitHub auth callback', async () => {
    const requestBodies: unknown[] = [];

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.get('/api/v1/users/me/github-status', () => {
        return HttpResponse.json({
          connected: true,
          has_repo_scope: true,
          github_login: 'alice',
          pr_authorship_mode: 'user_preferred',
          pr_draft_default: false,
        });
      }),
      http.post('/api/v1/sessions/:id/pr', async ({ request }) => {
        requestBodies.push(await request.json().catch(() => undefined));
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />, {
      searchParams: { github_pr: 'connected', resume_pr: 'resume-123' },
    });
    await screen.findAllByText('Fixed TypeError by adding null check');

    await waitFor(() => {
      expect(requestBodies).toEqual([{ author_mode: 'user', resume_token: 'resume-123' }]);
    });
  });

  // Regression: when the OAuth callback signs an `Action` claim into the
  // resume token, the redirect forwards it as resume_action. The frontend
  // must dispatch the matching mutation (push vs create) deterministically,
  // even when the current PR state would otherwise lead to the opposite
  // branch — e.g. another tab created the PR during the OAuth round-trip,
  // so by the time we replay there's a PR but the original click was
  // "Create PR". We trust the signed action over the live state.
  it('auto-resumes Push changes when resume_action=push_changes is in the URL', async () => {
    const createBodies: unknown[] = [];
    const pushBodies: unknown[] = [];

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'succeeded',
      has_unpushed_changes: true,
      pr_push_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json({
          data: {
            id: 'pr-1',
            session_id: 'session-abcdef12-3456-7890',
            org_id: sessionWithDiff.org_id,
            github_pr_number: 42,
            github_pr_url: 'https://github.com/example/repo/pull/42',
            github_repo: 'example/repo',
            title: 'Fix bug',
            status: 'open',
            review_status: 'pending',
            authored_by: 'app',
            ci_status: 'pending',
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
          },
        });
      }),
      http.get('/api/v1/users/me/github-status', () => {
        return HttpResponse.json({
          connected: true,
          has_repo_scope: true,
          github_login: 'alice',
          pr_authorship_mode: 'user_preferred',
          pr_draft_default: false,
        });
      }),
      http.post('/api/v1/sessions/:id/pr', async ({ request }) => {
        createBodies.push(await request.json().catch(() => undefined));
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
      http.post('/api/v1/sessions/:id/pr/push', async ({ request }) => {
        pushBodies.push(await request.json().catch(() => undefined));
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />, {
      searchParams: { github_pr: 'connected', resume_pr: 'resume-push-1', resume_action: 'push_changes' },
    });
    await screen.findAllByText('Fixed TypeError by adding null check');

    await waitFor(() => {
      expect(pushBodies).toEqual([{ author_mode: 'user', resume_token: 'resume-push-1' }]);
    });
    expect(createBodies).toEqual([]);
  });

  // Mirror of the push case: an explicit resume_action=create_pr must dispatch
  // the create mutation even if a PR somehow appeared during the OAuth
  // round-trip. The state-based fallback would route to push in that
  // scenario; the signed action overrides.
  it('auto-resumes Create PR when resume_action=create_pr is in the URL', async () => {
    const createBodies: unknown[] = [];
    const pushBodies: unknown[] = [];

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.get('/api/v1/users/me/github-status', () => {
        return HttpResponse.json({
          connected: true,
          has_repo_scope: true,
          github_login: 'alice',
          pr_authorship_mode: 'user_preferred',
          pr_draft_default: false,
        });
      }),
      http.post('/api/v1/sessions/:id/pr', async ({ request }) => {
        createBodies.push(await request.json().catch(() => undefined));
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
      http.post('/api/v1/sessions/:id/pr/push', async ({ request }) => {
        pushBodies.push(await request.json().catch(() => undefined));
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />, {
      searchParams: { github_pr: 'connected', resume_pr: 'resume-create-1', resume_action: 'create_pr' },
    });
    await screen.findAllByText('Fixed TypeError by adding null check');

    await waitFor(() => {
      expect(createBodies).toEqual([{ author_mode: 'user', resume_token: 'resume-create-1' }]);
    });
    expect(pushBodies).toEqual([]);
  });

  it('keeps a creating state until the pull request exists, then swaps to View PR', async () => {
    let sessionFetchCount = 0;
    const queuedSession: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'queued',
    };
    const pushingSession: Session = {
      ...queuedSession,
      pr_creation_state: 'pushing',
    };
    const succeededSession: Session = {
      ...queuedSession,
      pr_creation_state: 'succeeded',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        sessionFetchCount += 1;
        if (sessionFetchCount <= 1) {
          return HttpResponse.json({
            data: { ...queuedSession, pr_creation_state: 'idle' },
          } satisfies SingleResponse<Session>);
        }
        if (sessionFetchCount === 2) {
          return HttpResponse.json({ data: queuedSession } satisfies SingleResponse<Session>);
        }
        if (sessionFetchCount === 3) {
          return HttpResponse.json({ data: pushingSession } satisfies SingleResponse<Session>);
        }
        return HttpResponse.json({ data: succeededSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        if (sessionFetchCount >= 4) {
          return HttpResponse.json({
            data: {
              id: 'pr-1',
              session_id: 'session-abcdef12-3456-7890',
              org_id: queuedSession.org_id,
              github_pr_number: 42,
              github_pr_url: 'https://github.com/example/repo/pull/42',
              github_repo: 'example/repo',
              title: 'Fix bug',
              status: 'open',
              review_status: 'pending',
              authored_by: 'app',
              ci_status: 'pending',
              created_at: new Date().toISOString(),
              updated_at: new Date().toISOString(),
            },
          });
        }
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    expect(await screen.findByRole('button', { name: /Creating PR…/ })).toBeDisabled();

    await waitFor(
      async () => {
        expect(await screen.findByRole('link', { name: /View PR/ })).toBeInTheDocument();
      },
      { timeout: 12000 },
    );
    expect(screen.queryByRole('button', { name: /Create PR/ })).not.toBeInTheDocument();
  }, 15000);

  it('shows a clear toast and retry button when background PR creation fails', async () => {
    let sessionFetchCount = 0;
    const queuedSession: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'queued',
    };
    const failedSession: Session = {
      ...queuedSession,
      pr_creation_state: 'failed',
      pr_creation_error: 'GitHub rejected the branch push.',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        sessionFetchCount += 1;
        if (sessionFetchCount <= 1) {
          return HttpResponse.json({
            data: { ...queuedSession, pr_creation_state: 'idle' },
          } satisfies SingleResponse<Session>);
        }
        return HttpResponse.json({ data: failedSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    await waitFor(
      () => {
        expect(toast.error).toHaveBeenCalledWith('PR creation failed', { duration: 10000 });
      },
      { timeout: 5000 },
    );
    await waitFor(
      () => {
        expect(screen.getByRole('button', { name: /Retry/ })).toBeInTheDocument();
      },
      { timeout: 5000 },
    );
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent("Couldn't create the PR");
    expect(alert).toHaveTextContent('GitHub rejected the branch push.');
    expect(within(alert).getByRole('button', { name: 'Retry' })).toBeInTheDocument();
  }, 8000);

  it('shows snapshot-unavailable guidance without a retry action when direct PR creation loses the snapshot', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          {
            error: {
              code: 'SNAPSHOT_NOT_CAPTURED',
              message: 'This session finished without saving a reusable checkpoint for PR creation. Send a new message to rebuild the sandbox, then create the PR again.',
            },
          },
          { status: 400 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    await waitFor(
      () => {
        expect(toast.error).toHaveBeenCalledWith('PR creation failed', { duration: 10000 });
      },
      { timeout: 5000 },
    );

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('No reusable checkpoint saved');
    expect(alert).toHaveTextContent('This session finished without saving a reusable checkpoint for PR creation. Send a new message to rebuild the sandbox, then create the PR again.');
    expect(within(alert).queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument();
  });

  it('shows file count badge on Changes tab when session has diff', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);\ndiff --git a/src/new.ts b/src/new.ts\n--- /dev/null\n+++ b/src/new.ts\n@@ -0,0 +1 @@\n+export const x = 1;',
      diff_stats: { added: 2, removed: 0, files_changed: 2 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    // Changes tab should show file count badge
    const changesTab = screen.getByRole('tab', { name: /^Changes/ });
    await waitFor(() => {
      expect(changesTab).toHaveTextContent('Changes2');
    });
  });

  it('reserves bottom space for the active Changes underline when the file count badge is shown', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);\ndiff --git a/src/new.ts b/src/new.ts\n--- /dev/null\n+++ b/src/new.ts\n@@ -0,0 +1 @@\n+export const x = 1;',
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.getByLabelText('Session detail tabs')).toHaveClass('pb-1');
  });

  it('does not show file count badge on Changes tab when session has no diff', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const changesTab = screen.getByRole('tab', { name: 'Changes' });
    expect(changesTab).toHaveTextContent('Changes');
    expect(changesTab).not.toHaveTextContent(/\d/);
  });

  it('shows diff stats badge with file count in header when session has diff', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    // Header and footer should show clickable diff stats badges
    const viewChangesButtons = await screen.findAllByTitle('View changes');
    expect(viewChangesButtons.length).toBeGreaterThanOrEqual(1);
    expect(viewChangesButtons[0]).toHaveTextContent('+1');
  });

  it('clicking diff stats badge in header opens Changes tab', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const viewChangesButtons = await screen.findAllByTitle('View changes');
    await user.click(viewChangesButtons[0]);

    // Should show the diff content in the Changes tab
    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
  });

  it('shows contextual empty state for completed session with no changes', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const changesTab = screen.getByRole('tab', { name: 'Changes' });
    await user.click(changesTab);

    expect(await screen.findByText('No changes yet')).toBeInTheDocument();
    expect(screen.getByText('This session did not produce any file changes.')).toBeInTheDocument();
  });

  it('shows contextual empty state for running session with no changes', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      status: 'running',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const changesTab = screen.getByRole('tab', { name: 'Changes' });
    await user.click(changesTab);

    expect(await screen.findByText('No changes yet')).toBeInTheDocument();
    expect(screen.getByText('Changes will appear here as the agent modifies files.')).toBeInTheDocument();
  });

  it('shows stop button instead of send button when session is running', async () => {
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
    await screen.findByText('Agent is working...');
    expect(screen.getByTitle('Cancel session')).toBeInTheDocument();
    expect(screen.getByTitle('Send message')).toBeInTheDocument();
  });

  it('keeps the composer enabled and sends follow-up messages while the session is running', async () => {
    let postedMessage = '';
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
      http.post('/api/v1/sessions/:id/messages', async ({ request }) => {
        const body = await request.json() as { message: string };
        postedMessage = body.message;
        return HttpResponse.json({
          data: {
            id: 99,
            session_id: runningSession.id,
            org_id: 'org-1',
            user_id: 'user-1',
            turn_number: 2,
            role: 'user' as const,
            content: body.message,
            created_at: '2026-02-17T07:10:00Z',
          },
        } satisfies SingleResponse<SessionMessage>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={runningSession.id} />);
    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    expect(textarea).toBeEnabled();

    const user = userEvent.setup();
    await user.type(textarea, 'Queue this behind the current work');
    await user.click(screen.getByTitle('Send message'));

    await waitFor(() => {
      expect(postedMessage).toBe('Queue this behind the current work');
    });
  });

  it('shows send button instead of stop button when session is idle', async () => {
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
    await screen.findByPlaceholderText('Send a follow-up message...');
    expect(screen.getByTitle('Send message')).toBeInTheDocument();
    expect(screen.queryByTitle('Cancel session')).not.toBeInTheDocument();
  });

  it('shows send button for completed session (not stop)', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByTitle('Send message')).toBeInTheDocument();
    expect(screen.queryByTitle('Cancel session')).not.toBeInTheDocument();
  });

  it('calls cancel session API when cancel button is clicked during running state', async () => {
    let cancelSessionCalled = false;

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
      http.post('/api/v1/sessions/:id/cancel', () => {
        cancelSessionCalled = true;
        return HttpResponse.json({ data: { ...runningSession, status: 'cancelled' } });
      }),
    );

    renderWithProviders(<SessionDetailContent id={runningSession.id} />);
    await screen.findByText('Agent is working...');

    const user = userEvent.setup();
    const cancelButton = screen.getByTitle('Cancel session');
    await user.click(cancelButton);

    await waitFor(() => {
      expect(cancelSessionCalled).toBe(true);
    });
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
    expect(screen.getAllByText('Quick null check fix').length).toBeGreaterThanOrEqual(1);
  });

  it('shows Preview tab and renders PreviewPanel when clicked', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const previewTab = screen.getByRole('tab', { name: /Preview/ });
    expect(previewTab).toBeInTheDocument();
    expect(previewTab.querySelector('svg')).not.toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(previewTab);

    // PreviewPanel is rendered inside the preview tab content
    // It will show some loading/content from the preview component
    await waitFor(() => {
      expect(previewTab).toHaveAttribute('data-state', 'active');
    });
  });

  it('shows snapshot expired banner for destroyed sandbox', async () => {
    const expiredSession: Session = {
      ...mockSessions[0],
      status: 'completed',
      sandbox_state: 'destroyed',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: expiredSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    expect(
      await screen.findByText(/environment has expired/),
    ).toBeInTheDocument();
  });

  it('disables input for destroyed sandbox session', async () => {
    const expiredSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      sandbox_state: 'destroyed',
      current_turn: 1,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: expiredSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const textarea = await screen.findByPlaceholderText('Session environment has expired and can no longer be continued');
    expect(textarea).toBeDisabled();
  });

  it('hides input bar for pm_agent sessions', async () => {
    const pmSession: Session = {
      ...mockSessions[0],
      agent_type: 'pm_agent',
      status: 'running',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'running',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: pmSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByText('Agent is working...');
    // No textarea should be present for pm_agent
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
  });

  it('shows PM Agent as trigger label when pm_plan_id is set without user', async () => {
    const pmTriggeredSession: Session = {
      ...mockSessions[0],
      pm_plan_id: 'plan-1',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: pmTriggeredSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('PM Agent')).toBeInTheDocument();
  });

  it('shows "Started" timestamp for in-progress session without completed_at', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      status: 'running',
      completed_at: undefined,
      started_at: '2026-02-17T07:00:00Z',
      current_turn: 1,
      sandbox_state: 'running',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByText('Agent is working...');
    const startedLabel = screen.getByText(/Started/);
    expect(startedLabel).toBeInTheDocument();

    const metadataRow = startedLabel.closest('div');
    expect(metadataRow?.textContent?.trim().startsWith('Started')).toBe(true);
  });

  it('shows raw agent type when not in known labels', async () => {
    const customSession: Session = {
      ...mockSessions[0],
      agent_type: 'my_custom_agent',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: customSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('my_custom_agent')).toBeInTheDocument();
  });

  it('shows codex auth failure with re-authenticate button', async () => {
    const codexAuthSession: Session = {
      ...mockSessions[1],
      failure_category: 'codex_auth_expired',
      failure_explanation: 'Codex token expired',
      agent_type: 'codex',
    };
    let statusScope: string | null = null;
    let initiateBody: Record<string, unknown> | null = null;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: codexAuthSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/settings/codex-auth/status', ({ request }) => {
        statusScope = new URL(request.url).searchParams.get('scope');
        return HttpResponse.json({ data: { status: 'none' } });
      }),
      http.post('/api/v1/settings/codex-auth/initiate', async ({ request }) => {
        initiateBody = await request.json() as Record<string, unknown>;
        return HttpResponse.json({
          data: {
            user_code: 'TEST-CODE',
            verification_uri: 'https://auth.openai.com/codex/device',
            expires_in: 900,
          },
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');
    expect(screen.getByText('codex_auth_expired')).toBeInTheDocument();
    await waitFor(() => {
      expect(statusScope).toBe('personal');
    });
    const user = userEvent.setup();
    await user.click(screen.getByText('Re-authenticate with ChatGPT'));
    await waitFor(() => {
      expect(initiateBody).toMatchObject({ scope: 'personal' });
    });
    // Should NOT show failure_next_steps for codex auth failures
    expect(screen.queryByText('Next steps')).not.toBeInTheDocument();
  });

  it('shows connected message when codex auth is completed after failure', async () => {
    const codexAuthSession: Session = {
      ...mockSessions[1],
      failure_category: 'codex_auth_expired',
      failure_explanation: 'Codex token expired',
      agent_type: 'codex',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: codexAuthSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/settings/codex-auth/status', ({ request }) => {
        expect(new URL(request.url).searchParams.get('scope')).toBe('personal');
        return HttpResponse.json({ data: { status: 'completed' } });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');
    expect(
      await screen.findByText(/ChatGPT connected/),
    ).toBeInTheDocument();
  });

  it('can toggle the detail panel visibility', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    // Detail panel should be visible initially (has Overview tab)
    expect(screen.getByRole('tab', { name: 'Overview' })).toBeInTheDocument();

    const user = userEvent.setup();
    // Find the toggle button by its title
    const hideButton = screen.getByTitle('Hide details');
    await user.click(hideButton);

    // After hiding, Overview tab should no longer be in the document
    expect(screen.queryByRole('tab', { name: 'Overview' })).not.toBeInTheDocument();

    // Show details button should now be available
    const showButton = screen.getByTitle('Show details');
    await user.click(showButton);

    // Tabs should be back
    expect(screen.getByRole('tab', { name: 'Overview' })).toBeInTheDocument();
  });

  it('shows pr_created status badge', async () => {
    const prCreatedSession: Session = {
      ...mockSessions[0],
      status: 'pr_created',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: prCreatedSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getAllByText('PR created').length).toBeGreaterThanOrEqual(1);
  });

  it('shows skipped session with disabled input', async () => {
    const skippedSession: Session = {
      ...mockSessions[0],
      status: 'skipped',
      started_at: undefined,
      completed_at: undefined,
      current_turn: 0,
      sandbox_state: 'none',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: skippedSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    expect(await screen.findByPlaceholderText('Session is not active')).toBeDisabled();
    // Status badge should show "Skipped"
    expect(screen.getAllByText('Skipped').length).toBeGreaterThanOrEqual(1);
  });

  it('shows cancelled session status', async () => {
    const cancelledSession: Session = {
      ...mockSessions[0],
      status: 'cancelled',
      completed_at: '2026-02-17T07:03:00Z',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: cancelledSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getAllByText('Cancelled').length).toBeGreaterThanOrEqual(1);
  });

  it('shows issue description as synthetic message in timeline', async () => {
    server.use(
      http.get('/api/v1/issues/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockIssues[0],
            description: 'This is the issue description for the agent',
          },
        } satisfies SingleResponse<Issue>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    expect(
      await screen.findByText('This is the issue description for the agent'),
    ).toBeInTheDocument();
  });

  it('shows awaiting_input session status with active indicator', async () => {
    const awaitingSession: Session = {
      ...mockSessions[0],
      status: 'awaiting_input',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: awaitingSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getAllByText('Awaiting input').length).toBeGreaterThanOrEqual(1);
  });

  it('shows needs_human_guidance status', async () => {
    const guidanceSession: Session = {
      ...mockSessions[0],
      status: 'needs_human_guidance',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: guidanceSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getAllByText('Needs guidance').length).toBeGreaterThanOrEqual(1);
  });

  it('shows plan mode toggle for claude_code idle sessions', async () => {
    const idleClaudeSession: Session = {
      ...mockSessions[0],
      agent_type: 'claude_code',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleClaudeSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');
    // Plan button should be present for claude_code
    expect(screen.getByTitle('Switch to plan mode (Shift+Tab)')).toBeInTheDocument();
  });

  it('shows failed session with error field when no failure_explanation', async () => {
    const failedSession: Session = {
      ...mockSessions[1],
      failure_explanation: undefined,
      error: 'Internal server error during execution',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: failedSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');
    expect(screen.getByText('Internal server error during execution')).toBeInTheDocument();
  });

  it('shows plan mode indicator when plan button is clicked', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      agent_type: 'claude_code',
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

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');

    const user = userEvent.setup();
    const planButton = screen.getByTitle('Switch to plan mode (Shift+Tab)');
    await user.click(planButton);

    // Plan mode indicator should appear
    expect(screen.getByText('Plan Mode')).toBeInTheDocument();
    expect(screen.getByText('Agent will create a plan for review before making changes')).toBeInTheDocument();
    // Placeholder should change
    expect(screen.getByPlaceholderText('Describe what you want to plan...')).toBeInTheDocument();
    // Plan mode exit button should be visible
    expect(screen.getByTitle('Exit plan mode')).toBeInTheDocument();
  });

  it('sends message when Enter key is pressed with content', async () => {
    let messageSent = false;

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
      http.post('/api/v1/sessions/:id/messages', () => {
        messageSent = true;
        return HttpResponse.json({
          data: {
            id: 99,
            session_id: idleSession.id,
            org_id: 'org-1',
            user_id: 'user-1',
            turn_number: 2,
            role: 'user' as const,
            content: 'Hello agent',
            created_at: '2026-02-17T07:10:00Z',
          },
        } satisfies SingleResponse<SessionMessage>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');

    const user = userEvent.setup();
    await user.type(textarea, 'Hello agent');
    await user.keyboard('{Enter}');

    await waitFor(() => {
      expect(messageSent).toBe(true);
    });
  });

  it('renders a follow-up message in the transcript immediately before the backend responds', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };
    let releaseResponse!: () => void;
    const responseReleased = new Promise<void>((resolve) => {
      releaseResponse = resolve;
    });

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
      http.post('/api/v1/sessions/:id/messages', async ({ request }) => {
        const body = await request.json() as { message: string };
        await responseReleased;
        return HttpResponse.json({
          data: {
            id: 99,
            session_id: idleSession.id,
            org_id: 'org-1',
            user_id: 'user-1',
            turn_number: 2,
            role: 'user' as const,
            content: body.message,
            created_at: '2026-02-17T07:10:00Z',
          },
        } satisfies SingleResponse<SessionMessage>);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={idleSession.id} />);

    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    await user.type(textarea, 'Show this immediately');
    await user.keyboard('{Enter}');

    expect(await screen.findByText('Show this immediately')).toBeInTheDocument();
    expect(textarea).toHaveValue('');

    releaseResponse();

    await waitFor(() => {
      expect(screen.getByText('Show this immediately')).toBeInTheDocument();
    });
  });

  it('does not double-render a follow-up when the timeline poll sees the real message before POST resolves', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };
    let timelineEntries: SessionTimelineEntry[] = [];
    let timelineFetchCount = 0;
    let releaseResponse!: () => void;
    const responseReleased = new Promise<void>((resolve) => {
      releaseResponse = resolve;
    });

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/timeline', () => {
        timelineFetchCount += 1;
        return HttpResponse.json({
          data: timelineEntries,
          meta: {},
        } satisfies ListResponse<SessionTimelineEntry>);
      }),
      http.post('/api/v1/sessions/:id/messages', async ({ request }) => {
        const body = await request.json() as { message: string };
        const realMessage: SessionMessage = {
          id: 99,
          session_id: idleSession.id,
          org_id: 'org-1',
          user_id: 'user-1',
          turn_number: 2,
          role: 'user',
          content: body.message,
          created_at: '2026-02-17T07:10:00Z',
        };
        timelineEntries = [
          {
            kind: 'message',
            created_at: '2026-02-17T07:10:00Z',
            message: realMessage,
          },
        ];
        await responseReleased;
        return HttpResponse.json({
          data: realMessage,
        } satisfies SingleResponse<SessionMessage>);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={idleSession.id} />);

    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    await user.type(textarea, 'Show once');
    await user.keyboard('{Enter}');

    expect(await screen.findByText('Show once')).toBeInTheDocument();

    await waitFor(() => {
      expect(timelineFetchCount).toBeGreaterThanOrEqual(2);
    }, { timeout: 4500 });

    expect(screen.getAllByText('Show once')).toHaveLength(1);

    releaseResponse();
  });

  it('treats an optimistic plan-mode follow-up as a plan turn for streamed output', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      agent_type: 'claude_code',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };
    let releaseResponse!: () => void;
    const responseReleased = new Promise<void>((resolve) => {
      releaseResponse = resolve;
    });

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
      http.post('/api/v1/sessions/:id/messages', async ({ request }) => {
        const body = await request.json() as { message: string; plan_mode?: boolean };
        await responseReleased;
        return HttpResponse.json({
          data: {
            id: 100,
            session_id: idleSession.id,
            org_id: 'org-1',
            user_id: 'user-1',
            turn_number: 2,
            role: 'user' as const,
            content: body.plan_mode ? `[PLAN_MODE]\n${body.message}` : body.message,
            created_at: '2026-02-17T07:10:00Z',
          },
        } satisfies SingleResponse<SessionMessage>);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={idleSession.id} />);

    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    await user.click(screen.getByTitle('Switch to plan mode (Shift+Tab)'));
    await user.type(screen.getByPlaceholderText('Describe what you want to plan...'), 'Plan this change');
    await user.keyboard('{Enter}');

    const sessionStream = MockEventSource.instances.find((instance) =>
      instance.url.includes(`/api/v1/sessions/${idleSession.id}/logs/stream`),
    );
    expect(sessionStream).toBeDefined();

    await act(async () => {
      sessionStream?.onmessage?.(
        new MessageEvent('message', {
          data: JSON.stringify({
            id: 501,
            session_id: idleSession.id,
            level: 'output',
            message: 'Plan step 1',
            metadata: null,
            turn_number: 2,
            created_at: '2026-02-17T07:10:30Z',
          }),
        }),
      );
    });

    expect(await screen.findByText('Implementation Plan')).toBeInTheDocument();
    expect(screen.getByText('Plan step 1')).toBeInTheDocument();
    expect(textarea).toHaveValue('');

    releaseResponse();
  });

  it('clears attached review comments after sending them to the agent', async () => {
    let postedMessage = '';
    let postedResolveIDs: string[] | undefined;
    const idleSessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };
    const comment: SessionReviewComment = {
      id: 'comment-1',
      session_id: idleSessionWithDiff.id,
      org_id: 'org-1',
      user_id: mockMembers[0].id,
      file_path: 'src/app.ts',
      line_number: 2,
      diff_side: 'new',
      body: 'Handle the null edge case',
      resolved: false,
      pass_number: 1,
      created_at: '2026-02-17T07:10:00Z',
      updated_at: '2026-02-17T07:10:00Z',
    };
    // Mutable backing store: GET returns whatever state POST /messages
    // transitions the comment to. Mirrors the real backend, which resolves
    // attached comments in the same transaction as the message create.
    let comments: SessionReviewComment[] = [comment];

    mockSessionDetailWithLazyDiff(idleSessionWithDiff);
    server.use(
      http.get('/api/v1/sessions/:id/review-comments', () => {
        return HttpResponse.json({
          data: comments,
          meta: {},
        } satisfies ListResponse<SessionReviewComment>);
      }),
      http.post('/api/v1/sessions/:id/messages', async ({ request }) => {
        const body = await request.json() as { message: string; resolve_review_comment_ids?: string[] };
        postedMessage = body.message;
        postedResolveIDs = body.resolve_review_comment_ids;
        if (postedResolveIDs && postedResolveIDs.length > 0) {
          const resolved = new Set(postedResolveIDs);
          comments = comments.map((c) => (resolved.has(c.id) ? { ...c, resolved: true } : c));
        }
        return HttpResponse.json({
          data: {
            id: 99,
            session_id: idleSessionWithDiff.id,
            org_id: 'org-1',
            user_id: 'user-1',
            turn_number: 2,
            role: 'user' as const,
            content: body.message,
            created_at: '2026-02-17T07:10:00Z',
          },
        } satisfies SingleResponse<SessionMessage>);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    await user.click(screen.getAllByTitle('View changes')[0]);
    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
    expect(await screen.findByText('1 comment attached')).toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: 'Overview' }));
    expect(await screen.findByText('1 comment attached')).toBeInTheDocument();
    expect(screen.getAllByText('Handle the null edge case').length).toBeGreaterThan(0);

    await user.type(textarea, 'Hello agent');
    await user.keyboard('{Enter}');

    await waitFor(() => {
      expect(postedMessage).toContain('Please address the following code review comments:');
      expect(postedMessage).toContain('src/app.ts:2');
      expect(postedMessage).toContain('"Handle the null edge case"');
      expect(postedMessage).toContain('Hello agent');
    });
    // The send must include the comment ID so the backend can resolve it
    // atomically with the message create. Without this, a page refresh
    // would resurrect the attached comment.
    expect(postedResolveIDs).toEqual([comment.id]);

    await waitFor(() => {
      expect(screen.queryByText('1 comment attached')).not.toBeInTheDocument();
    });
    expect(screen.queryByText('Handle the null edge case')).not.toBeInTheDocument();
  });

  it('caps attached review comments to the backend per-message resolve limit', async () => {
    let postedResolveIDs: string[] | undefined;
    const idleSessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };
    const originalComments: SessionReviewComment[] = Array.from({ length: 51 }, (_, index) => ({
      id: `00000000-0000-4000-8000-${index.toString().padStart(12, '0')}`,
      session_id: idleSessionWithDiff.id,
      org_id: 'org-1',
      user_id: mockMembers[0].id,
      file_path: 'src/app.ts',
      line_number: index + 1,
      diff_side: 'new',
      body: `Review comment ${index + 1}`,
      resolved: false,
      pass_number: 1,
      created_at: '2026-02-17T07:10:00Z',
      updated_at: '2026-02-17T07:10:00Z',
    }));
    let comments = originalComments;

    mockSessionDetailWithLazyDiff(idleSessionWithDiff);
    server.use(
      http.get('/api/v1/sessions/:id/review-comments', () => {
        return HttpResponse.json({
          data: comments,
          meta: {},
        } satisfies ListResponse<SessionReviewComment>);
      }),
      http.post('/api/v1/sessions/:id/messages', async ({ request }) => {
        const body = await request.json() as { message: string; resolve_review_comment_ids?: string[] };
        postedResolveIDs = body.resolve_review_comment_ids;
        if (postedResolveIDs && postedResolveIDs.length > 0) {
          const resolved = new Set(postedResolveIDs);
          comments = comments.map((comment) => (resolved.has(comment.id) ? { ...comment, resolved: true } : comment));
        }
        return HttpResponse.json({
          data: {
            id: 99,
            session_id: idleSessionWithDiff.id,
            org_id: 'org-1',
            user_id: 'user-1',
            turn_number: 2,
            role: 'user' as const,
            content: body.message,
            created_at: '2026-02-17T07:10:00Z',
          },
        } satisfies SingleResponse<SessionMessage>);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    expect(await screen.findByText('50 comments attached')).toBeInTheDocument();

    await user.type(textarea, 'Please handle these');
    await user.keyboard('{Enter}');

    await waitFor(() => {
      expect(postedResolveIDs).toHaveLength(50);
    });
    expect(postedResolveIDs).toEqual(originalComments.slice(0, 50).map((comment) => comment.id));

    await waitFor(() => {
      expect(screen.getByText('1 comment attached')).toBeInTheDocument();
    });
  });

  it('scrolls the chat transcript back to the live edge after sending a follow-up message', async () => {
    let messageSent = false;
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(900);
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(200);

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
      http.post('/api/v1/sessions/:id/messages', async ({ request }) => {
        const body = await request.json() as { message: string };
        messageSent = body.message === 'Hello agent';
        return HttpResponse.json({
          data: {
            id: 99,
            session_id: idleSession.id,
            org_id: 'org-1',
            user_id: 'user-1',
            turn_number: 2,
            role: 'user' as const,
            content: body.message,
            created_at: '2026-02-17T07:10:00Z',
          },
        } satisfies SingleResponse<SessionMessage>);
      }),
    );

    const { container } = renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    const scroller = getChatScroller(container);
    await act(async () => {
      scroller.scrollTop = 0;
      scroller.dispatchEvent(new Event('scroll'));
    });

    const user = userEvent.setup();
    await user.type(textarea, 'Hello agent');
    await user.keyboard('{Enter}');

    await waitFor(() => {
      expect(messageSent).toBe(true);
      expect(scroller.scrollTop).toBe(900);
    });
  });

  it('scrolls the transcript with keyboard shortcuts immediately after loading', async () => {
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

    const { container } = renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');
    const scroller = getChatScroller(container);
    const scrollBy = vi.fn();
    Object.defineProperty(scroller, 'clientHeight', { configurable: true, value: 400 });
    Object.defineProperty(scroller, 'scrollBy', { configurable: true, value: scrollBy });
    act(() => {
      scroller.focus();
    });

    await userEvent.keyboard('{PageDown}');

    expect(scrollBy).toHaveBeenCalledWith({ top: 340, behavior: 'smooth' });
  });

  it('clears the jump-to-latest affordance when the viewed session changes', async () => {
    const idleSessionA: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };
    const idleSessionB: Session = {
      ...mockSessions[1],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      result_summary: 'Second session title',
    };

    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(900);
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(200);

    server.use(
      http.get('/api/v1/sessions/:id', ({ params }) => {
        const session = params.id === idleSessionA.id ? idleSessionA : idleSessionB;
        return HttpResponse.json({ data: session } satisfies SingleResponse<Session>);
      }),
    );

    const { container, rerender } = renderWithProviders(
      <SessionDetailContent id="session-abcdef12-3456-7890" />,
    );

    await screen.findAllByText('Fixed TypeError by adding null check');
    const scroller = getChatScroller(container);
    await act(async () => {
      scroller.scrollTop = 0;
      scroller.dispatchEvent(new Event('scroll'));
    });

    expect(await screen.findByRole('button', { name: /Jump to latest/i })).toBeInTheDocument();

    rerender(<SessionDetailContent id="session-98765432-abcd-ef01" />);

    await screen.findByRole('heading', { level: 1, name: 'Second session title' });
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /Jump to latest/i })).not.toBeInTheDocument();
    });
  });

  it('positions the jump-to-latest affordance close to the composer', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(900);
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(200);

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    const { container } = renderWithProviders(<SessionDetailContent id={idleSession.id} />);

    await screen.findAllByText('Fixed TypeError by adding null check');
    const scroller = getChatScroller(container);
    await act(async () => {
      scroller.scrollTop = 0;
      scroller.dispatchEvent(new Event('scroll'));
    });

    const jumpButton = await screen.findByRole('button', { name: /Jump to latest/i });
    const jumpContainer = jumpButton.parentElement;

    expect(jumpContainer).not.toBeNull();
    expect(jumpContainer).toHaveClass('bottom-4');
    expect(jumpContainer).not.toHaveClass('bottom-24');
  });

  it('opens review mode when clicking diff stats badge in footer', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    // Click the diff stats badge to enter review mode
    const viewChangesButtons = screen.getAllByTitle('View changes');
    await user.click(viewChangesButtons[0]);

    // After entering review mode, the review diff view should be shown
    // and the file should be visible
    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
  });

  it('exits plan mode when exit button is clicked', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      agent_type: 'claude_code',
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

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');

    const user = userEvent.setup();
    // Enter plan mode
    const planButton = screen.getByTitle('Switch to plan mode (Shift+Tab)');
    await user.click(planButton);
    expect(screen.getByText('Plan Mode')).toBeInTheDocument();

    // Exit plan mode
    const exitButton = screen.getByTitle('Exit plan mode');
    await user.click(exitButton);
    expect(screen.queryByText('Plan Mode')).not.toBeInTheDocument();
    expect(screen.getByPlaceholderText('Send a follow-up message...')).toBeInTheDocument();
  });

  it('shows send plan request button title when in plan mode', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      agent_type: 'claude_code',
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

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');

    const user = userEvent.setup();
    // Enter plan mode
    const planButton = screen.getByTitle('Switch to plan mode (Shift+Tab)');
    await user.click(planButton);

    // Send button title should change to "Send plan request"
    expect(screen.getByTitle('Send plan request')).toBeInTheDocument();
    // Plan button should be hidden when in plan mode
    expect(screen.queryByTitle('Switch to plan mode (Shift+Tab)')).not.toBeInTheDocument();
  });

  it('shows duration in seconds for short sessions', async () => {
    const quickSession: Session = {
      ...mockSessions[0],
      started_at: '2026-02-17T07:00:00Z',
      completed_at: '2026-02-17T07:00:45Z',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: quickSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('45s')).toBeInTheDocument();
  });

  it('hides plan button for non-claude_code agents', async () => {
    const codexSession: Session = {
      ...mockSessions[0],
      agent_type: 'codex',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: codexSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');
    // Plan button should not exist for codex
    expect(screen.queryByTitle('Switch to plan mode (Shift+Tab)')).not.toBeInTheDocument();
  });

  it('shows file upload button for active sessions', async () => {
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

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');
    expect(screen.getByTitle('Add files, photos, or a Linear issue')).toBeInTheDocument();
    expect(screen.getByTitle('Add files, photos, or a Linear issue')).not.toBeDisabled();
  });

  it('shows the shared add menu items in the continue-session composer', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      snapshot_key: 'snapshot/test',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const user = userEvent.setup();

    await screen.findByPlaceholderText('Send a follow-up message...');
    await user.click(screen.getByTitle('Add files, photos, or a Linear issue'));

    expect(await screen.findByRole('menuitem', { name: 'Upload files or photos' })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'Add image URL' })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'Add linear issue' })).toBeInTheDocument();
  });

  it('uploads an image pasted into the follow-up prompt and shows it in the attachment strip', async () => {
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

    const uploadSpy = vi.spyOn(api.uploads, 'upload').mockResolvedValue({
      url: 'https://example.com/pasted-follow-up.png',
      file_name: 'pasted-follow-up.png',
      content_type: 'image/png',
    });

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    const file = new File(['image-bytes'], 'pasted-follow-up.png', { type: 'image/png' });

    fireEvent.paste(textarea, {
      clipboardData: {
        files: [file],
        items: [{ kind: 'file', type: 'image/png', getAsFile: () => file }],
        types: ['Files'],
      },
    });

    await waitFor(() => {
      expect(uploadSpy).toHaveBeenCalledWith(file);
    });
    expect(await screen.findByRole('button', { name: 'Preview pasted-follow-up.png' })).toBeInTheDocument();
  });

  it('adds an image URL from the continue-session dropdown and shows it in the attachment strip', async () => {
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

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const user = userEvent.setup();

    await screen.findByPlaceholderText('Send a follow-up message...');
    await user.click(screen.getByTitle('Add files, photos, or a Linear issue'));
    await user.click(await screen.findByRole('menuitem', { name: 'Add image URL' }));
    await user.type(screen.getByRole('textbox', { name: 'Image URL' }), 'https://example.com/follow-up-shot.png');
    await user.click(screen.getByRole('button', { name: 'Add' }));

    expect(await screen.findByRole('button', { name: 'Preview follow-up-shot.png' })).toBeInTheDocument();
  });

  it('shows Codex agent type label', async () => {
    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');
    expect(screen.getAllByText('Codex').length).toBeGreaterThanOrEqual(1);
  });

  it('disables file upload button when session is not active', async () => {
    const skippedSession: Session = {
      ...mockSessions[0],
      status: 'skipped',
      current_turn: 0,
      sandbox_state: 'none',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: skippedSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Session is not active');
    expect(screen.getByTitle('Add files, photos, or a Linear issue')).toBeDisabled();
  });

  it('appends a Linear identifier to the follow-up message via the dropdown', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      current_turn: 1,
      sandbox_state: 'snapshotted',
      snapshot_key: 'snapshot/test',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const composer = await screen.findByPlaceholderText('Send a follow-up message...');
    const user = userEvent.setup();

    await user.click(screen.getByTitle('Add files, photos, or a Linear issue'));
    await user.click(await screen.findByRole('menuitem', { name: 'Add linear issue' }));

    const linearInput = await screen.findByLabelText('Linear issue id or URL');
    await user.type(linearInput, 'ACS-1234');
    await user.click(screen.getByRole('button', { name: 'Add' }));

    expect(composer).toHaveValue('ACS-1234');
    // Submitting the ref must close the input so the user can keep typing
    // their message in the textarea — leaving it open would steal the next
    // keystroke.
    expect(screen.queryByLabelText('Linear issue id or URL')).not.toBeInTheDocument();
  });

  it('shows an inline error and keeps the input open when the Linear ref is malformed', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      current_turn: 1,
      sandbox_state: 'snapshotted',
      snapshot_key: 'snapshot/test',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const composer = await screen.findByPlaceholderText('Send a follow-up message...');
    const user = userEvent.setup();

    await user.click(screen.getByTitle('Add files, photos, or a Linear issue'));
    await user.click(await screen.findByRole('menuitem', { name: 'Add linear issue' }));

    const linearInput = await screen.findByLabelText('Linear issue id or URL');
    await user.type(linearInput, 'fix the bug');
    await user.click(screen.getByRole('button', { name: 'Add' }));

    // The ref-validation error must surface so the user knows why nothing
    // happened; without this, an invalid input silently swallowed the click.
    expect(await screen.findByRole('alert')).toHaveTextContent(/Linear URL/);
    expect(screen.getByLabelText('Linear issue id or URL')).toBeInTheDocument();
    expect(composer).toHaveValue('');
  });

  it('enters review mode and shows review diff view with file tree', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();

    // Click Changes tab first
    const changesTab = screen.getByRole('tab', { name: /^Changes/ });
    await user.click(changesTab);

    // Click "Review 1 file" button to enter review mode
    const reviewButton = await screen.findByText(/Review 1 file/);
    await user.click(reviewButton);

    // Should show the file content in the review diff view
    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);

    // The detail panel toggle should be disabled during review
    const toggleButton = screen.getByTitle('File tree required during review');
    expect(toggleButton).toBeDisabled();

    await user.hover(toggleButton.parentElement as HTMLElement);

    expect(await screen.findByRole('tooltip', { name: 'File tree required during review' })).toBeInTheDocument();
  });

  it('opens the mobile diff view immediately when the chat files-changed summary is clicked', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByText('1 file changed'));

    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('uses a single mobile top bar and moves thread controls into the session actions sheet', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const threads: SessionThread[] = [
      {
        id: 'thread-1',
        session_id: 'session-abcdef12-3456-7890',
        org_id: 'org-1',
        agent_type: 'codex',
        label: 'Main tab',
        status: 'running',
        current_turn: 1,
        diff: 'diff --git a/src/app.ts b/src/app.ts',
        created_at: '2026-02-17T07:00:00Z',
        cost_cents: 15,
        pending_message_count: 0,
      },
      {
        id: 'thread-2',
        session_id: 'session-abcdef12-3456-7890',
        org_id: 'org-1',
        agent_type: 'claude_code',
        label: 'Review',
        status: 'awaiting_input',
        created_at: '2026-02-17T07:02:00Z',
        current_turn: 1,
        cost_cents: 10,
        pending_message_count: 1,
      },
    ];

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            threads,
          },
        } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.getByRole('button', { name: 'Open session details' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Open session actions' })).toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Open session actions' }));

    const actionsSheet = await screen.findByRole('dialog', { name: 'Session actions' });
    expect(within(actionsSheet).getByRole('button', { name: 'Switch to Main tab' })).toBeInTheDocument();
    expect(within(actionsSheet).getByRole('button', { name: 'Switch to Review' })).toBeInTheDocument();
    expect(within(actionsSheet).getByRole('button', { name: 'Add agent tab' })).toBeInTheDocument();
    expect(within(actionsSheet).getByRole('button', { name: 'Rename session' })).toBeInTheDocument();
  });

  it('opens a full-screen mobile diff when a file is selected from the Changes sheet', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Open session details' }));

    const detailSheet = await screen.findByRole('dialog');
    await user.click(within(detailSheet).getByRole('tab', { name: /^Changes/ }));
    await user.click(within(detailSheet).getByRole('button', { name: /app\.ts/ }));

    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('reopens the mobile files list from the diff reader', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);\ndiff --git a/src/new.ts b/src/new.ts\n--- /dev/null\n+++ b/src/new.ts\n@@ -0,0 +1 @@\n+export const x = 1;',
      diff_stats: { added: 2, removed: 0, files_changed: 2 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByText('2 files changed'));

    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
    expect(screen.getByText('1 of 2')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Open files list' }));
    const detailSheet = await screen.findByRole('dialog');
    expect(within(detailSheet).getByText('2 files changed')).toBeInTheDocument();
    expect(within(detailSheet).getByText('Browse session details, changed files, and preview on mobile.')).toBeInTheDocument();
  });

  it('uses the Changes sheet as a mobile file index instead of showing a review-all action', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);\ndiff --git a/src/new.ts b/src/new.ts\n--- /dev/null\n+++ b/src/new.ts\n@@ -0,0 +1 @@\n+export const x = 1;',
      diff_stats: { added: 2, removed: 0, files_changed: 2 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Open session details' }));

    const detailSheet = await screen.findByRole('dialog');
    await user.click(within(detailSheet).getByRole('tab', { name: /^Changes/ }));

    expect(within(detailSheet).queryByText(/Review 2 files/)).not.toBeInTheDocument();
    expect(within(detailSheet).getByText('2 files changed')).toBeInTheDocument();
    expect(within(detailSheet).getByPlaceholderText('Filter files...')).toBeInTheDocument();
  });

  it('keeps the shared session composer off-canvas but available from the dedicated mobile diff reader', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const idleSessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      snapshot_key: 'snapshot/test',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(idleSessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');

    const user = userEvent.setup();
    await user.click(await screen.findByText('1 file changed'));

    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
    expect(screen.queryByPlaceholderText('Send a follow-up message...')).not.toBeInTheDocument();
    expect(screen.queryByTitle('Send message')).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Message agent' }));

    expect(await screen.findByPlaceholderText('Send a follow-up message...')).toBeInTheDocument();
    expect(screen.getByTitle('Send message')).toBeInTheDocument();
  });

  it('shows the session warning state inside the mobile composer sheet while reviewing', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const destroyedSessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'destroyed',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(destroyedSessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Session environment has expired and can no longer be continued');

    const user = userEvent.setup();
    await user.click(await screen.findByText('1 file changed'));
    await user.click(screen.getByRole('button', { name: 'Message agent' }));

    expect(await screen.findByText(/This session's environment has expired/i)).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Session environment has expired and can no longer be continued')).toBeInTheDocument();
  });

  it('opens mobile review comment edits in a sheet instead of inline in the diff row', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    const comments: SessionReviewComment[] = [{
      id: 'comment-mobile-edit',
      session_id: 'session-abcdef12-3456-7890',
      org_id: 'org-1',
      user_id: 'user-1',
      file_path: 'src/app.ts',
      line_number: 2,
      diff_side: 'new',
      body: 'Add a guard before using this import.',
      resolved: false,
      pass_number: 0,
      created_at: '2026-02-17T07:04:00Z',
      updated_at: '2026-02-17T07:04:00Z',
    }];

    mockSessionDetailWithLazyDiff(sessionWithDiff);
    server.use(
      http.get('/api/v1/sessions/:id/review-comments', () => {
        return HttpResponse.json({
          data: comments,
          meta: {},
        } satisfies ListResponse<SessionReviewComment>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByText('1 file changed'));

    expect((await screen.findAllByText('Add a guard before using this import.')).length).toBeGreaterThan(0);
    await user.click(screen.getByTitle('Edit'));

    expect(await screen.findByText('Edit review comment')).toBeInTheDocument();
    expect(screen.getByDisplayValue('Add a guard before using this import.')).toBeInTheDocument();
    expect(screen.queryByTestId('inline-comment-composer-anchor')).not.toBeInTheDocument();
  });

  it('exits review mode when clicking a non-changes tab', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();

    // Enter review mode via diff stats badge
    const viewChangesButtons = screen.getAllByTitle('View changes');
    await user.click(viewChangesButtons[0]);
    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);

    // Click Overview tab — should exit review mode
    const overviewTab = screen.getByRole('tab', { name: 'Overview' });
    await user.click(overviewTab);

    // Review mode should be exited — chat panel should be visible again
    await waitFor(() => {
      expect(screen.getByTitle('Hide details')).toBeInTheDocument();
    });
  });

  it('exits review mode when browser history removes the review query param', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(screen.getAllByTitle('View changes')[0]);
    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
    expect(screen.getByTitle('File tree required during review')).toBeInTheDocument();

    act(() => {
      window.history.pushState(null, '', '/sessions/session-abcdef12-3456-7890?review=active');
      window.history.pushState(null, '', '/sessions/session-abcdef12-3456-7890');
      window.dispatchEvent(new PopStateEvent('popstate'));
    });

    await waitFor(() => {
      expect(screen.getByTitle('Hide details')).toBeInTheDocument();
    });
  });

  it('shows review comment input in review mode for active session', async () => {
    const idleSessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(idleSessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');

    const user = userEvent.setup();
    // Enter review mode
    const viewChangesButtons = screen.getAllByTitle('View changes');
    await user.click(viewChangesButtons[0]);

    // The standard shared composer should remain present in review mode.
    expect(await screen.findByPlaceholderText('Send a follow-up message...')).toBeInTheDocument();
    expect(screen.getByTitle('Send message')).toBeInTheDocument();
  });

  it('shows hover tooltips for disabled composer actions when the session environment has expired', async () => {
    const destroyedSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'destroyed',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: destroyedSession } satisfies SingleResponse<Session>);
      }),
    );

    const user = userEvent.setup();
    const { container } = renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findByPlaceholderText('Session environment has expired and can no longer be continued');

    const attachButton = container.querySelector('button[title="Add files, photos, or a Linear issue"]') as HTMLButtonElement | null;
    expect(attachButton).not.toBeNull();
    expect(attachButton).toBeDisabled();
    await user.hover(attachButton?.parentElement as HTMLElement);
    expect(await screen.findByRole('tooltip', { name: 'Session environment has expired and can no longer be continued.' })).toBeInTheDocument();

    const sendButton = container.querySelector('button[title="Send message"]') as HTMLButtonElement | null;
    expect(sendButton).not.toBeNull();
    expect(sendButton).toBeDisabled();
    await user.hover(sendButton?.parentElement as HTMLElement);
    expect(await screen.findByRole('tooltip', { name: 'Session environment has expired and can no longer be continued.' })).toBeInTheDocument();
  });

  it('keeps the expired sandbox warning visible in review mode with the shared composer', async () => {
    const destroyedSessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'destroyed',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(destroyedSessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Session environment has expired and can no longer be continued');

    const user = userEvent.setup();
    await user.click(screen.getAllByTitle('View changes')[0]);

    expect(await screen.findByText(/environment has expired/i)).toBeVisible();
  });

  it('keeps the no-headless-resume warning visible in review mode with the shared composer', async () => {
    const ampSessionWithDiff: Session = {
      ...mockSessions[0],
      agent_type: 'amp',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(ampSessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByText(/doesn't support headless conversation resume/i);

    const user = userEvent.setup();
    await user.click(screen.getAllByTitle('View changes')[0]);

    expect(await screen.findByText(/doesn't support headless conversation resume/i)).toBeVisible();
  });

  it('shares composer draft state and review comment attachments between chat and review mode', async () => {
    const idleSessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };
    const comments: SessionReviewComment[] = [{
      id: 'comment-1',
      session_id: 'session-abcdef12-3456-7890',
      org_id: 'org-1',
      user_id: mockMembers[0].id,
      file_path: 'src/app.ts',
      line_number: 2,
      diff_side: 'new',
      body: 'Handle the null edge case',
      resolved: false,
      pass_number: 1,
      created_at: '2026-02-17T07:10:00Z',
      updated_at: '2026-02-17T07:10:00Z',
    }];

    mockSessionDetailWithLazyDiff(idleSessionWithDiff);
    server.use(
      http.get('/api/v1/sessions/:id/review-comments', () => {
        return HttpResponse.json({
          data: comments,
          meta: {},
        } satisfies ListResponse<SessionReviewComment>);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const composer = await screen.findByPlaceholderText('Send a follow-up message...') as HTMLTextAreaElement;
    await user.type(composer, 'Please fix this and add tests');
    expect(composer.value).toBe('Please fix this and add tests');

    await user.click(screen.getAllByTitle('View changes')[0]);
    const sharedComposerInReview = await screen.findByDisplayValue('Please fix this and add tests');
    expect(sharedComposerInReview).toBeInTheDocument();

    expect(await screen.findByText('1 comment attached')).toBeInTheDocument();
    expect(screen.getAllByText('Handle the null edge case').length).toBeGreaterThan(0);

    await user.click(screen.getByRole('tab', { name: 'Overview' }));

    expect(await screen.findByDisplayValue('Please fix this and add tests')).toBeInTheDocument();
    expect(screen.getByText('1 comment attached')).toBeInTheDocument();
    expect(screen.getAllByText('Handle the null edge case').length).toBeGreaterThan(0);
  });

  it('returns to the main chat view after sending from review mode', async () => {
    const idleSessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(idleSessionWithDiff);
    server.use(
      http.post('/api/v1/sessions/:id/messages', async ({ request }) => {
        const body = await request.json() as { message: string };
        return HttpResponse.json({
          data: {
            id: 99,
            session_id: idleSessionWithDiff.id,
            org_id: 'org-1',
            user_id: 'user-1',
            turn_number: 2,
            role: 'user' as const,
            content: body.message,
            created_at: '2026-02-17T07:10:00Z',
          },
        } satisfies SingleResponse<SessionMessage>);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    await user.click(screen.getAllByTitle('View changes')[0]);
    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);

    await user.type(textarea, 'Hello from review');
    await user.keyboard('{Enter}');

    await waitFor(() => {
      expect(screen.queryByText('src/app.ts')).not.toBeInTheDocument();
    });
    expect(screen.getByTitle('Hide details')).toBeInTheDocument();
  });

  it('shows review file count in Changes tab and file click works', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);\ndiff --git a/src/new.ts b/src/new.ts\n--- /dev/null\n+++ b/src/new.ts\n@@ -0,0 +1 @@\n+export const x = 1;',
      diff_stats: { added: 2, removed: 0, files_changed: 2 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const changesTab = screen.getByRole('tab', { name: /^Changes/ });
    await user.click(changesTab);

    // Should show "Review 2 files" button
    expect(await screen.findByText(/Review 2 files/)).toBeInTheDocument();
  });

  it('keeps the review diff file set aligned with the Changes tab attribution filter', async () => {
    const sessionId = 'session-abcdef12-3456-7890';
    const codexThread: SessionThread = {
      id: 'thread-codex',
      session_id: sessionId,
      org_id: 'org-1',
      agent_type: 'codex',
      label: 'Codex',
      status: 'completed',
      current_turn: 1,
      created_at: '2026-02-17T07:00:00Z',
      cost_cents: 0,
      pending_message_count: 0,
    };
    const claudeThread: SessionThread = {
      id: 'thread-claude',
      session_id: sessionId,
      org_id: 'org-1',
      agent_type: 'claude_code',
      label: 'Claude review',
      status: 'completed',
      current_turn: 1,
      created_at: '2026-02-17T07:01:00Z',
      cost_cents: 0,
      pending_message_count: 0,
    };
    const sessionWithThreadsAndDiff: Session = {
      ...mockSessions[0],
      id: sessionId,
      threads: [codexThread, claudeThread],
      diff: [
        'diff --git a/frontend/src/app.ts b/frontend/src/app.ts',
        '--- a/frontend/src/app.ts',
        '+++ b/frontend/src/app.ts',
        '@@ -1 +1,2 @@',
        ' export const app = true;',
        '+export const codex = true;',
        'diff --git a/frontend/src/lib/helpers.ts b/frontend/src/lib/helpers.ts',
        '--- a/frontend/src/lib/helpers.ts',
        '+++ b/frontend/src/lib/helpers.ts',
        '@@ -1 +1,2 @@',
        ' export const helper = true;',
        '+export const shared = true;',
        'diff --git a/frontend/src/components/automation-model-select.tsx b/frontend/src/components/automation-model-select.tsx',
        '--- a/frontend/src/components/automation-model-select.tsx',
        '+++ b/frontend/src/components/automation-model-select.tsx',
        '@@ -1 +1,2 @@',
        ' export function AutomationModelSelect() {',
        '+  return null;',
        ' }',
      ].join('\n'),
      diff_stats: { added: 3, removed: 0, files_changed: 3 },
    };

    mockSessionDetailWithLazyDiff(sessionWithThreadsAndDiff);
    server.use(
      http.get('/api/v1/sessions/:id/thread-file-events', () => {
        return HttpResponse.json({
          data: [
            {
              id: 1,
              org_id: 'org-1',
              session_id: sessionId,
              thread_id: codexThread.id,
              turn: 1,
              path: 'frontend/src/app.ts',
              event_type: 'modified',
              observed_at: '2026-02-17T07:02:00Z',
            },
            {
              id: 2,
              org_id: 'org-1',
              session_id: sessionId,
              thread_id: codexThread.id,
              turn: 1,
              path: 'frontend/src/lib/helpers.ts',
              event_type: 'modified',
              observed_at: '2026-02-17T07:02:30Z',
            },
            {
              id: 3,
              org_id: 'org-1',
              session_id: sessionId,
              thread_id: claudeThread.id,
              turn: 1,
              path: 'frontend/src/lib/helpers.ts',
              event_type: 'modified',
              observed_at: '2026-02-17T07:03:00Z',
            },
            {
              id: 4,
              org_id: 'org-1',
              session_id: sessionId,
              thread_id: claudeThread.id,
              turn: 1,
              path: 'frontend/src/components/automation-model-select.tsx',
              event_type: 'modified',
              observed_at: '2026-02-17T07:03:30Z',
            },
          ],
          meta: {},
        } satisfies ListResponse<import('@/lib/types').SessionThreadFileEvent>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({ data: [] as SessionMessage[], meta: {} } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    await screen.findAllByText('Fixed TypeError by adding null check');
    await user.click(screen.getByRole('tab', { name: /^Changes/ }));

    const changesPanel = screen.getByRole('tabpanel', { name: /^Changes/ });
    await user.click(within(changesPanel).getByRole('combobox'));
    await user.click(await screen.findByRole('option', { name: 'Touched by Codex' }));

    expect(await screen.findByText('Review 2 files')).toBeInTheDocument();
    expect(screen.queryByText(/^automation-model-select\.tsx$/)).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Review 2 files' }));

    expect(await screen.findByText('frontend/src/app.ts')).toBeInTheDocument();
    expect(screen.getByText('frontend/src/lib/helpers.ts')).toBeInTheDocument();
    expect(
      screen.queryByText('frontend/src/components/automation-model-select.tsx')
    ).not.toBeInTheDocument();
  });

  it('shows model selector for agents with available models', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      agent_type: 'claude_code',
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

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');
    // Model override selector should be present
    expect(screen.getByLabelText('Model override')).toBeInTheDocument();
  });

  it('uses a mobile settings sheet for the resumed-session composer on small screens', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      agent_type: 'claude_code',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    setMobileViewport(true);
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');

    expect(screen.getByRole('button', { name: 'Session settings' })).toBeInTheDocument();
    expect(screen.queryByLabelText('Model override')).not.toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Session settings' }));

    expect(await screen.findByRole('dialog', { name: 'Session settings' })).toBeInTheDocument();
    expect(screen.getByLabelText('Model override')).toBeInTheDocument();
  });

  it('does not render the session footer on mobile conversation view', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 3,
      sandbox_state: 'snapshotted',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    setMobileViewport(true);
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-mobile-footer-hidden" />);
    await screen.findByPlaceholderText('Send a follow-up message...');

    expect(screen.queryByTestId('session-footer')).not.toBeInTheDocument();
  });

  it('keeps the mobile follow-up textarea collapsed until focused', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    setMobileViewport(true);
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-mobile-composer-height" />);
    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');

    expect(textarea).toHaveAttribute('data-mobile-composer-state', 'collapsed');
    expect(textarea).toHaveAttribute('rows', '1');

    const user = userEvent.setup();
    await user.click(textarea);

    expect(textarea).toHaveAttribute('data-mobile-composer-state', 'expanded');

    fireEvent.blur(textarea);

    await waitFor(() => {
      expect(textarea).toHaveAttribute('data-mobile-composer-state', 'collapsed');
    });
  });

  it('autofocuses the follow-up textarea on desktop session detail pages', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    setMobileViewport(false);
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-desktop-autofocus" />);
    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');

    await waitFor(() => {
      expect(textarea).toHaveFocus();
    });
  });

  it('focuses the continue-session textarea after creating a new tab', async () => {
    const sessionId = 'session-create-tab-autofocus';
    const threads: SessionThread[] = [
      {
        id: 'thread-main',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'codex',
        label: 'Codex',
        status: 'idle',
        current_turn: 1,
        created_at: '2026-02-17T07:00:00Z',
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
            sandbox_state: 'snapshotted',
            threads,
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.post('/api/v1/sessions/:id/threads', async () => {
        const thread: SessionThread = {
          id: 'thread-new',
          session_id: sessionId,
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Codex 2',
          status: 'idle',
          current_turn: 0,
          created_at: '2026-02-17T07:04:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        };
        threads.push(thread);
        return HttpResponse.json({ data: thread } satisfies SingleResponse<SessionThread>, { status: 201 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    const addTabButtons = await screen.findAllByRole('button', { name: 'Add agent tab' });
    const stripAddButton = addTabButtons[addTabButtons.length - 1] as HTMLButtonElement;
    await user.click(stripAddButton);

    const textarea = await screen.findByPlaceholderText('Send a message to Codex 2...');

    await waitFor(() => {
      expect(textarea).toHaveFocus();
    });
  });

  it('keeps focus on the add-tab trigger after creating a tab when no composer is rendered', async () => {
    const sessionId = 'session-create-tab-no-composer';
    const threads: SessionThread[] = [
      {
        id: 'thread-main',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'pm_agent',
        label: 'Planner',
        status: 'idle',
        current_turn: 1,
        created_at: '2026-02-17T07:00:00Z',
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
            agent_type: 'pm_agent',
            status: 'idle',
            sandbox_state: 'snapshotted',
            threads,
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.post('/api/v1/sessions/:id/threads', async () => {
        const thread: SessionThread = {
          id: 'thread-new',
          session_id: sessionId,
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Codex 2',
          status: 'idle',
          current_turn: 0,
          created_at: '2026-02-17T07:04:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        };
        threads.push(thread);
        return HttpResponse.json({ data: thread } satisfies SingleResponse<SessionThread>, { status: 201 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    const addTabButtons = await screen.findAllByRole('button', { name: 'Add agent tab' });
    const stripAddButton = addTabButtons[addTabButtons.length - 1] as HTMLButtonElement;
    await user.click(stripAddButton);

    await waitFor(() => {
      expect(stripAddButton).toHaveFocus();
    });
  });

  it('returns focus to the desktop header add-tab trigger when a new tab has no composer', async () => {
    const sessionId = 'session-header-create-tab-no-composer';
    const threads: SessionThread[] = [
      {
        id: 'thread-main',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'pm_agent',
        label: 'Planner',
        status: 'idle',
        current_turn: 1,
        created_at: '2026-02-17T07:00:00Z',
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
            agent_type: 'pm_agent',
            status: 'idle',
            sandbox_state: 'snapshotted',
            threads,
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.post('/api/v1/sessions/:id/threads', async () => {
        const thread: SessionThread = {
          id: 'thread-new',
          session_id: sessionId,
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Codex 2',
          status: 'idle',
          current_turn: 0,
          created_at: '2026-02-17T07:04:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        };
        threads.push(thread);
        return HttpResponse.json({ data: thread } satisfies SingleResponse<SessionThread>, { status: 201 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    const headerActions = await screen.findByTestId('session-header-actions');
    const headerAddButton = within(headerActions).getByRole('button', { name: 'Add agent tab' });
    await user.click(headerAddButton);

    await waitFor(() => {
      expect(headerAddButton).toHaveFocus();
    });
  });

  it('matches both trigger menus to the continue-session input width', async () => {
    const resumableSession: Session = {
      ...mockSessions[0],
      agent_type: 'codex',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      repository_id: 'repo-1',
      target_branch: 'main',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: resumableSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/composer/files', ({ params }) => {
        expect(params.id).toBe('session-abcdef12-3456-7890');
        return HttpResponse.json({
          data: [
            {
              kind: 'directory',
              token: '@internal/services',
              path: 'internal/services',
              display: 'internal/services',
            },
          ],
          meta: {},
        } satisfies ListResponse<{ kind: 'file' | 'directory'; token?: string; path?: string; id?: string; display: string }>);
      }),
      http.get('/api/v1/session-composer/slash-commands', () => {
        return HttpResponse.json({
          groups: [
            {
              source: 'builtin',
              label: 'Codex commands',
              items: [
                {
                  kind: 'command',
                  agent_type: 'codex',
                  name: 'review',
                  token: '/review',
                  display: '/review',
                  description: 'Review pending changes',
                  source: 'builtin',
                },
              ],
            },
          ],
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const composer = await screen.findByPlaceholderText('Send a follow-up message...') as HTMLTextAreaElement;
    const composerShell = screen.getByTestId('session-composer-shell');
    const inputSurface = screen.getByTestId('session-composer-input-surface');

    vi.spyOn(composerShell, 'getBoundingClientRect').mockReturnValue({
      x: 0,
      y: 680,
      width: 760,
      height: 132,
      top: 680,
      right: 760,
      bottom: 812,
      left: 0,
      toJSON: () => ({}),
    });
    vi.spyOn(inputSurface, 'getBoundingClientRect').mockReturnValue({
      x: 48,
      y: 692,
      width: 640,
      height: 108,
      top: 692,
      right: 688,
      bottom: 800,
      left: 48,
      toJSON: () => ({}),
    });

    await user.type(composer, 'Inspect @serv');

    const mentionOverlay = await screen.findByTestId('trigger-picker-overlay');
    expect(mentionOverlay).toHaveStyle({ left: '48px', width: '640px' });

    await user.clear(composer);
    await user.type(composer, '/rev');

    expect(await screen.findByText('/review')).toBeInTheDocument();

    const commandOverlay = screen.getByTestId('trigger-picker-overlay');
    expect(commandOverlay).toHaveStyle({ left: '48px', width: '640px' });
  });

  it('renders slash command chips without a leading slash icon', async () => {
    const resumableSession: Session = {
      ...mockSessions[0],
      agent_type: 'codex',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      repository_id: 'repo-1',
      target_branch: 'main',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: resumableSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/session-composer/slash-commands', () => {
        return HttpResponse.json({
          groups: [
            {
              source: 'builtin',
              label: 'Codex commands',
              items: [
                {
                  kind: 'command',
                  agent_type: 'codex',
                  name: 'review',
                  token: '/review',
                  display: '/review',
                  description: 'Review pending changes',
                  source: 'builtin',
                },
              ],
            },
          ],
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const composer = await screen.findByPlaceholderText('Send a follow-up message...') as HTMLTextAreaElement;
    await user.type(composer, '/rev');
    await user.click((await screen.findByText('/review')).closest('button') as HTMLButtonElement);

    const chips = screen.getByLabelText('Selected references and commands');
    expect(within(chips).getByText('/review')).toBeInTheDocument();
    expect(chips.querySelector('svg.lucide-slash')).toBeNull();
  });

  it('shows Gemini CLI agent type label', async () => {
    const geminiSession: Session = {
      ...mockSessions[0],
      agent_type: 'gemini_cli',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: geminiSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('Gemini CLI')).toBeInTheDocument();
  });

  it('renders Unknown user when triggered_by_user_id does not match any member', async () => {
    const sessionWithUnknownUser: Session = {
      ...mockSessions[0],
      triggered_by_user_id: 'user-nonexistent',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithUnknownUser } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('Unknown user')).toBeInTheDocument();
  });

  it('shows automation provenance in the overview tab for automation-created sessions', async () => {
    const automationSession: Session = {
      ...mockSessions[0],
      origin: 'automation',
      automation_run_id: 'automation-run-1',
      triggered_by_user_id: undefined,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: automationSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('Created by automation')).toBeInTheDocument();
    expect(screen.getByText('Automation run')).toBeInTheDocument();
  });

  it('does not show automation provenance for manually created sessions', async () => {
    const manualSession: Session = {
      ...mockSessions[0],
      origin: 'manual',
      automation_run_id: undefined,
      triggered_by_user_id: mockMembers[0].id,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: manualSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.queryByText('Created by automation')).not.toBeInTheDocument();
    expect(screen.queryByText('Automation run')).not.toBeInTheDocument();
  });

  it('falls back to github_login when triggering member has no display name', async () => {
    const memberWithoutName: User = {
      id: 'user-no-name',
      org_id: 'org-1',
      email: '249349663+nisarg-assembled@users.noreply.github.com',
      name: '',
      role: 'admin',
      github_login: 'nisarg-assembled',
      created_at: '2026-01-01T00:00:00Z',
    };
    const sessionWithNamelessTrigger: Session = {
      ...mockSessions[0],
      triggered_by_user_id: memberWithoutName.id,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithNamelessTrigger } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/team/members', () => {
        return HttpResponse.json({
          data: [memberWithoutName],
          meta: {},
        } satisfies ListResponse<User>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    expect(await screen.findByText('nisarg-assembled')).toBeInTheDocument();
    expect(screen.queryByText('Unknown user')).not.toBeInTheDocument();
  });

  it('calls retry API when Retry button is clicked on failed session', async () => {
    let retryCalled = false;

    const failedSession: Session = {
      ...mockSessions[1],
      failure_explanation: 'Something broke',
      failure_retry_advised: true,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: failedSession } satisfies SingleResponse<Session>);
      }),
      http.post('/api/v1/sessions/:id/retry', () => {
        retryCalled = true;
        return HttpResponse.json({ data: { ...failedSession, status: 'pending' } });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');

    const user = userEvent.setup();
    const retryButton = screen.getByText('Retry');
    await user.click(retryButton);

    await waitFor(() => {
      expect(retryCalled).toBe(true);
    });
  });

  it('does not render the session footer for multi-turn sessions', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 3,
      sandbox_state: 'snapshotted',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');
    expect(screen.queryByTestId('session-footer')).not.toBeInTheDocument();
  });

  it('shows Shift+Tab toggle for plan mode in claude_code session', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      agent_type: 'claude_code',
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

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');

    const user = userEvent.setup();
    // Shift+Tab should toggle plan mode
    await user.click(textarea);
    await user.keyboard('{Shift>}{Tab}{/Shift}');

    // Plan mode should now be active
    expect(screen.getByText('Plan Mode')).toBeInTheDocument();

    // Shift+Tab again should exit plan mode
    await user.keyboard('{Shift>}{Tab}{/Shift}');
    expect(screen.queryByText('Plan Mode')).not.toBeInTheDocument();
  });

  it('shows idle status badge', async () => {
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

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');
    expect(screen.getAllByText('Idle').length).toBeGreaterThanOrEqual(1);
  });

  it('shows session title from title field when available', async () => {
    const sessionWithTitle: Session = {
      ...mockSessions[0],
      title: 'Custom session title',
      result_summary: undefined,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithTitle } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    expect(await screen.findByRole('heading', { level: 1, name: 'Custom session title' })).toBeInTheDocument();
  });

  it('shows only pm_approach without pm_reasoning in PM context', async () => {
    const sessionWithPM: Session = {
      ...mockSessions[0],
      pm_plan_id: 'plan-1',
      pm_reasoning: undefined,
      pm_approach: 'Direct fix approach',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithPM } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByText('PM context');
    expect(screen.getAllByText('Direct fix approach').length).toBeGreaterThanOrEqual(1);
    expect(screen.queryByText('Why this was prioritized')).not.toBeInTheDocument();
  });

  it('clears message after successful send', async () => {
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

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const textarea = await screen.findByPlaceholderText('Send a follow-up message...') as HTMLTextAreaElement;

    const user = userEvent.setup();
    await user.type(textarea, 'Test message');
    expect(textarea.value).toBe('Test message');

    // Click send button
    const sendButton = screen.getByTitle('Send message');
    await user.click(sendButton);

    // After send, the textarea should be cleared
    await waitFor(() => {
      expect(textarea.value).toBe('');
    });
  });

});
