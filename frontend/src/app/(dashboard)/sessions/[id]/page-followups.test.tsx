import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { fireEvent, renderWithProviders, screen, userEvent, waitFor } from '@/test/test-utils';
import { act } from '@testing-library/react';
import { server } from '@/test/mocks/server';
import { mockSessions, mockMembers } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import { api } from '@/lib/api';
import type {
  Session,
  SessionMessage,
  SessionReviewComment,
  SessionTimelineEntry,
  SingleResponse,
  ListResponse,
} from '@/lib/types';
import {
  installSessionDetailPageTestHooks,
  MockEventSource,
  getChatScroller,
  changeFieldValue,
  submitFieldWithEnter,
  mockSessionDetailWithLazyDiff,
} from './session-detail-test-kit';

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

describe('SessionDetailPage follow-up messages', () => {
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

    changeFieldValue(textarea, 'Hello agent');
    submitFieldWithEnter(textarea);

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

    renderWithProviders(<SessionDetailContent id={idleSession.id} />);

    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    changeFieldValue(textarea, 'Show this immediately');
    submitFieldWithEnter(textarea);

    expect(await screen.findByText('Show this immediately')).toBeInTheDocument();
    expect(textarea).toHaveValue('');

    releaseResponse();

    await waitFor(() => {
      expect(screen.getByText('Show this immediately')).toBeInTheDocument();
    });
  });

  it('does not fast-poll or double-render an optimistic follow-up before POST resolves', async () => {
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

    renderWithProviders(<SessionDetailContent id={idleSession.id} />);

    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    changeFieldValue(textarea, 'Show once');
    submitFieldWithEnter(textarea);

    expect(await screen.findByText('Show once')).toBeInTheDocument();

    expect(timelineFetchCount).toBe(1);
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
    const planTextarea = screen.getByPlaceholderText('Describe what you want to plan...');
    changeFieldValue(planTextarea, 'Plan this change');
    submitFieldWithEnter(planTextarea);

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

    expect(await screen.findByText('Implementation plan')).toBeInTheDocument();
    expect(screen.getByText('Plan step 1')).toBeInTheDocument();
    expect(textarea).toHaveValue('');

    releaseResponse();
  });

  it('clears attached review comments after sending them to the agent', async () => {
    let postedMessage = '';
    let postedResolveIDs: string[] | undefined;
    const user = userEvent.setup();
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

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    await user.click(screen.getAllByTitle('View changes')[0]);
    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
    expect(await screen.findByText('1 comment attached')).toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: 'Overview' }));
    expect(await screen.findByText('1 comment attached')).toBeInTheDocument();
    expect(screen.getAllByText('Handle the null edge case').length).toBeGreaterThan(0);

    changeFieldValue(textarea, 'Hello agent');
    submitFieldWithEnter(textarea);

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

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    expect(await screen.findByText('50 comments attached')).toBeInTheDocument();

    changeFieldValue(textarea, 'Please handle these');
    submitFieldWithEnter(textarea);

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

    changeFieldValue(textarea, 'Hello agent');
    submitFieldWithEnter(textarea);

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
    expect(screen.getByText('Plan mode')).toBeInTheDocument();

    // Exit plan mode
    const exitButton = screen.getByTitle('Exit plan mode');
    await user.click(exitButton);
    expect(screen.queryByText('Plan mode')).not.toBeInTheDocument();
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

  it('uploads an image dropped onto the follow-up input surface and shows it in the attachment strip', async () => {
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
      url: 'https://example.com/dropped-follow-up.png',
      file_name: 'dropped-follow-up.png',
      content_type: 'image/png',
    });

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    const inputSurface = screen.getByTestId('session-composer-input-surface');
    const file = new File(['image-bytes'], 'dropped-follow-up.png', { type: 'image/png' });

    fireEvent.dragEnter(inputSurface, {
      dataTransfer: {
        files: [file],
        items: [{ kind: 'file', type: 'image/png', getAsFile: () => file }],
        types: ['Files'],
      },
    });

    expect(inputSurface).toHaveAttribute('data-drag-active', 'true');

    fireEvent.drop(inputSurface, {
      dataTransfer: {
        files: [file],
        items: [{ kind: 'file', type: 'image/png', getAsFile: () => file }],
        types: ['Files'],
      },
    });

    await waitFor(() => {
      expect(uploadSpy).toHaveBeenCalledWith(file);
    });
    expect(await screen.findByRole('button', { name: 'Preview dropped-follow-up.png' })).toBeInTheDocument();
    await waitFor(() => {
      expect(textarea).toHaveFocus();
    });
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
    changeFieldValue(screen.getByRole('textbox', { name: 'Image URL' }), 'https://example.com/follow-up-shot.png');
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
    changeFieldValue(linearInput, 'ACS-1234');
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
    changeFieldValue(linearInput, 'fix the bug');
    await user.click(screen.getByRole('button', { name: 'Add' }));

    // The ref-validation error must surface so the user knows why nothing
    // happened; without this, an invalid input silently swallowed the click.
    expect(await screen.findByRole('alert')).toHaveTextContent(/Linear URL/);
    expect(screen.getByLabelText('Linear issue id or URL')).toBeInTheDocument();
    expect(composer).toHaveValue('');
  });
});
