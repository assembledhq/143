import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, userEvent, waitFor, within } from '@/test/test-utils';
import { act } from '@testing-library/react';
import { server } from '@/test/mocks/server';
import { mockSessions } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import type { Session, SessionMessage, SessionThread, SingleResponse } from '@/lib/types';
import { installSessionDetailPageTestHooks, MockEventSource, changeFieldValue, makeTranscriptWindow } from './session-detail-test-kit';

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

describe('SessionDetailPage agent tabs and threads', () => {
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
      http.get('/api/v1/sessions/:id/threads/:threadId/transcript', ({ params }) => {
        return HttpResponse.json(
          makeTranscriptWindow(messagesByThread[params.threadId as string] ?? [], []),
        );
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

    changeFieldValue(screen.getByPlaceholderText('Send a message to Claude Code 3...'), 'Run the frontend checks.');
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
      http.get('/api/v1/sessions/:id/threads/:threadId/transcript', () => {
        return HttpResponse.json(makeTranscriptWindow([], []));
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

  it('only shows the desktop add-tab action in the agent tab strip', async () => {
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
      http.get('/api/v1/sessions/:id/threads/:threadId/transcript', () => {
        return HttpResponse.json(makeTranscriptWindow([], []));
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
    expect(within(headerActions).queryByRole('button', { name: 'Add agent tab' })).not.toBeInTheDocument();

    const stripAddButton = screen.getByRole('button', { name: 'Add agent tab' });
    await user.click(stripAddButton);

    await waitFor(() => {
      expect(createdThread).toBe(true);
    });
    expect(await screen.findByRole('tab', { name: /Codex 2/ })).toBeInTheDocument();
  });

  it('keeps the desktop session header and detail panel header on the same border-box height', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const mainHeader = await screen.findByTestId('session-main-header');
    const detailHeader = screen.getByTestId('session-detail-header');
    const detailHeaderBar = screen.getByTestId('session-detail-header-bar');

    expect(mainHeader).toHaveClass('h-14', 'border-b');
    expect(detailHeader).toHaveClass('h-14', 'border-b');
    expect(detailHeaderBar).toHaveClass('h-14');
    expect(detailHeaderBar).not.toHaveClass('h-full');
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
      http.get('/api/v1/sessions/:id/threads/:threadId/transcript', () => {
        return HttpResponse.json(makeTranscriptWindow([], []));
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

  it('does not refetch the whole session after creating a blank tab', async () => {
    const sessionId = 'session-create-tab-no-detail-refetch';
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
    let sessionDetailRequests = 0;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        sessionDetailRequests += 1;
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
      http.get('/api/v1/sessions/:id/threads/:threadId/transcript', () => {
        return HttpResponse.json(makeTranscriptWindow([], []));
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
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    expect(await screen.findByPlaceholderText('Send a message to Claude Code...')).toBeInTheDocument();
    expect(sessionDetailRequests).toBe(1);

    const addTabButtons = screen.getAllByRole('button', { name: 'Add agent tab' });
    await user.click(addTabButtons[addTabButtons.length - 1] as HTMLButtonElement);

    expect(await screen.findByPlaceholderText('Send a message to Claude Code 2...')).toBeInTheDocument();
    await waitFor(() => {
      expect(sessionDetailRequests).toBe(1);
    });
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
    let postedMessageBody: { message: string; client_message_id?: string } | null = null;

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
      http.get('/api/v1/sessions/:id/threads/:threadId/transcript', () => {
        return HttpResponse.json(makeTranscriptWindow([], []));
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
        postedMessageBody = await request.json() as { message: string; client_message_id?: string };
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
    changeFieldValue(screen.getByPlaceholderText('Send a message to Codex 2...'), 'Use the selected model.');
    await user.click(screen.getByRole('button', { name: 'Send message' }));

    await waitFor(() => {
      expect(patchedBodies).toContainEqual({ label: 'Codex 2', model: 'gpt-5.4-mini' });
    });
    await waitFor(() => {
      // Send carries a generated client_message_id (durable inbox
      // idempotency token). Assert the user-meaningful payload here and
      // verify the idempotency token shape separately so the test is not
      // coupled to crypto.randomUUID output.
      expect(postedMessageBody).toMatchObject({ message: 'Use the selected model.' });
    });
    // TS narrows postedMessageBody to `null` outside the msw closure (it
    // doesn't follow the closure assignment), so refer to the captured
    // value through a typed local that preserves the declared shape.
    const sentBody = postedMessageBody as { message: string; client_message_id?: string } | null;
    expect(sentBody?.client_message_id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
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
      http.get('/api/v1/sessions/:id/threads/:threadId/transcript', () => {
        return HttpResponse.json(makeTranscriptWindow([], []));
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
      http.get('/api/v1/sessions/:id/threads/:threadId/transcript', () => {
        return HttpResponse.json(makeTranscriptWindow([], []));
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

  it('refreshes thread state when session status SSE payload omits thread detail', async () => {
    const sessionId = 'session-status-refreshes-thread-state';
    const thread: SessionThread = {
      id: 'thread-main',
      session_id: sessionId,
      org_id: 'org-1',
      agent_type: 'codex',
      label: 'Main',
      status: 'idle',
      current_turn: 1,
      created_at: '2026-02-17T07:00:00Z',
      cost_cents: 0,
      pending_message_count: 0,
    };
    let sessionFetchCount = 0;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        sessionFetchCount += 1;
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            id: sessionId,
            status: sessionFetchCount >= 2 ? 'running' : 'idle',
            sandbox_state: sessionFetchCount >= 2 ? 'running' : 'snapshotted',
            threads: [{
              ...thread,
              status: sessionFetchCount >= 2 ? 'running' : 'idle',
            }],
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/transcript', () => {
        return HttpResponse.json(
          makeTranscriptWindow(
            [{
              id: 1,
              session_id: sessionId,
              org_id: 'org-1',
              thread_id: thread.id,
              turn_number: 1,
              role: 'user',
              content: 'Start work',
              created_at: '2026-02-17T07:00:00Z',
            }],
            [],
          ),
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id={sessionId} />);

    expect(await screen.findByText('Start work')).toBeInTheDocument();
    expect(screen.queryByText('Agent is working...')).not.toBeInTheDocument();
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

    expect(await screen.findByText('Agent is working...')).toBeInTheDocument();
    await waitFor(() => {
      expect(sessionFetchCount).toBeGreaterThanOrEqual(2);
    });
  });

  it('refreshes thread state when the session SSE stream opens', async () => {
    const sessionId = 'session-open-refreshes-thread-state';
    const thread: SessionThread = {
      id: 'thread-main',
      session_id: sessionId,
      org_id: 'org-1',
      agent_type: 'codex',
      label: 'Main',
      status: 'idle',
      current_turn: 1,
      created_at: '2026-02-17T07:00:00Z',
      cost_cents: 0,
      pending_message_count: 0,
    };
    let sessionFetchCount = 0;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        sessionFetchCount += 1;
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            id: sessionId,
            status: sessionFetchCount >= 2 ? 'running' : 'idle',
            sandbox_state: sessionFetchCount >= 2 ? 'running' : 'snapshotted',
            threads: [{
              ...thread,
              status: sessionFetchCount >= 2 ? 'running' : 'idle',
            }],
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/transcript', () => {
        return HttpResponse.json(
          makeTranscriptWindow(
            [{
              id: 1,
              session_id: sessionId,
              org_id: 'org-1',
              thread_id: thread.id,
              turn_number: 1,
              role: 'user',
              content: 'Start work',
              created_at: '2026-02-17T07:00:00Z',
            }],
            [],
          ),
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id={sessionId} />);

    expect(await screen.findByText('Start work')).toBeInTheDocument();
    expect(screen.queryByText('Agent is working...')).not.toBeInTheDocument();
    await waitFor(() => {
      expect(MockEventSource.instances.length).toBeGreaterThan(0);
    });

    act(() => {
      MockEventSource.instances[0].onopen?.(new Event('open'));
    });

    expect(await screen.findByText('Agent is working...')).toBeInTheDocument();
    await waitFor(() => {
      expect(sessionFetchCount).toBeGreaterThanOrEqual(2);
    });
  });

  it('polls active session detail so missed status events self-heal', async () => {
    const sessionId = 'session-active-detail-polling';
    const thread: SessionThread = {
      id: 'thread-main',
      session_id: sessionId,
      org_id: 'org-1',
      agent_type: 'codex',
      label: 'Main',
      status: 'running',
      current_turn: 1,
      created_at: '2026-02-17T07:00:00Z',
      cost_cents: 0,
      pending_message_count: 0,
    };
    let sessionFetchCount = 0;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        sessionFetchCount += 1;
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            id: sessionId,
            status: sessionFetchCount >= 2 ? 'completed' : 'running',
            sandbox_state: sessionFetchCount >= 2 ? 'snapshotted' : 'running',
            threads: [{
              ...thread,
              status: sessionFetchCount >= 2 ? 'completed' : 'running',
            }],
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/transcript', () => {
        return HttpResponse.json(
          makeTranscriptWindow(
            [{
              id: 1,
              session_id: sessionId,
              org_id: 'org-1',
              thread_id: thread.id,
              turn_number: 1,
              role: 'user',
              content: 'Start work',
              created_at: '2026-02-17T07:00:00Z',
            }],
            [],
          ),
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id={sessionId} />);

    expect(await screen.findByText('Agent is working...')).toBeInTheDocument();

    await waitFor(() => {
      expect(sessionFetchCount).toBeGreaterThanOrEqual(2);
    }, { timeout: 5000 });
    expect(screen.queryByText('Agent is working...')).not.toBeInTheDocument();
  }, 10000);

  it('clears stale running thread UI when a cancelled session status omits thread detail', async () => {
    const sessionId = 'session-cancelled-thread-omitted';
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
      http.get('/api/v1/sessions/:id/threads/:threadId/transcript', () => {
        return HttpResponse.json(
          makeTranscriptWindow(
            [{
              id: 1,
              session_id: sessionId,
              org_id: 'org-1',
              thread_id: thread.id,
              turn_number: 1,
              role: 'user',
              content: 'Stop this run',
              created_at: '2026-02-17T07:00:00Z',
            }],
            [],
          ),
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id={sessionId} />);

    expect(await screen.findByText('Agent is working...')).toBeInTheDocument();
    await waitFor(() => {
      expect(MockEventSource.instances.length).toBeGreaterThan(0);
    });

    act(() => {
      MockEventSource.instances[0].emit('done', {
        ...mockSessions[0],
        id: sessionId,
        status: 'cancelled',
        sandbox_state: 'none',
        completed_at: '2026-02-17T07:05:00Z',
      });
    });

    await waitFor(() => {
      expect(screen.queryByText('Agent is working...')).not.toBeInTheDocument();
    });
    expect(screen.getByText('Session stopped')).toBeInTheDocument();
  });

  it('clears stale running thread UI when an idle session status omits thread detail', async () => {
    const sessionId = 'session-idle-thread-omitted';
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
      http.get('/api/v1/sessions/:id/threads/:threadId/transcript', () => {
        return HttpResponse.json(
          makeTranscriptWindow(
            [{
              id: 1,
              session_id: sessionId,
              org_id: 'org-1',
              thread_id: thread.id,
              turn_number: 1,
              role: 'user',
              content: 'Finish this run',
              created_at: '2026-02-17T07:00:00Z',
            }],
            [],
          ),
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id={sessionId} />);

    expect(await screen.findByText('Agent is working...')).toBeInTheDocument();
    await waitFor(() => {
      expect(MockEventSource.instances.length).toBeGreaterThan(0);
    });

    act(() => {
      MockEventSource.instances[0].emit('status', {
        ...mockSessions[0],
        id: sessionId,
        status: 'idle',
        sandbox_state: 'snapshotted',
        snapshot_key: 'snapshots/session-idle-thread-omitted.tar',
      });
    });

    await waitFor(() => {
      expect(screen.queryByText('Agent is working...')).not.toBeInTheDocument();
    });
    expect(screen.getByTitle('Send message')).toBeInTheDocument();
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
      http.get('/api/v1/sessions/:id/threads/:threadId/transcript', () => {
        return HttpResponse.json(makeTranscriptWindow([], []));
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
});
