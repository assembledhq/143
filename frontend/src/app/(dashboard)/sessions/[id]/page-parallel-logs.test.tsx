// Parallel logs bootstrap: thread logs should start loading (latest-turns
// window) alongside the message window instead of waiting for messages to
// finish, then converge on the precise visible-turns window.
import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, waitFor } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { mockSessions } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import type { Session, SessionMessage, SingleResponse, ListResponse, SessionLog } from '@/lib/types';
import { installSessionDetailPageTestHooks } from './session-detail-test-kit';

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

vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

vi.mock('next/image', () => ({
  default: ({ src, alt, className, width, height }: { src: string; alt: string; className?: string; width?: number; height?: number }) => (
    <span data-next-image={src} aria-label={alt} className={className} data-width={width} data-height={height} />
  ),
}));

installSessionDetailPageTestHooks({ toast, routerPush });

const threadId = 'thread-main';

function threadedSession(id: string): Session {
  return {
    ...mockSessions[0],
    id,
    status: 'idle',
    completed_at: undefined,
    sandbox_state: 'snapshotted',
    threads: [
      {
        id: threadId,
        session_id: id,
        org_id: 'org-1',
        agent_type: 'codex',
        label: 'Main',
        status: 'idle',
        current_turn: 1,
        created_at: '2026-02-17T07:00:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
    ],
  };
}

type LoggedLogRequest = { latestTurns: string | null; turnNumbers: string | null };

describe('SessionDetailPage parallel thread logs', () => {
  it('fetches a latest-turns logs window while messages are still loading, then converges on visible turns', async () => {
    const session = threadedSession('session-parallel-logs');

    let releaseMessages = () => {};
    const messagesBlocked = new Promise<void>((resolve) => {
      releaseMessages = resolve;
    });
    const logRequests: LoggedLogRequest[] = [];

    server.use(
      http.get(`/api/v1/sessions/${session.id}`, () => {
        return HttpResponse.json({ data: session } satisfies SingleResponse<Session>);
      }),
      http.get(`/api/v1/sessions/${session.id}/threads/:threadId/messages`, async () => {
        await messagesBlocked;
        return HttpResponse.json({
          data: [
            {
              id: 1,
              session_id: session.id,
              org_id: 'org-1',
              thread_id: threadId,
              turn_number: 1,
              role: 'assistant',
              content: 'Main reply',
              created_at: '2026-02-17T07:02:00Z',
            },
          ] as SessionMessage[],
          meta: {},
        } satisfies ListResponse<SessionMessage>);
      }),
      http.get(`/api/v1/sessions/${session.id}/threads/:threadId/logs`, ({ request }) => {
        const url = new URL(request.url);
        logRequests.push({
          latestTurns: url.searchParams.get('latest_turns'),
          turnNumbers: url.searchParams.get('turn_numbers'),
        });
        return HttpResponse.json({ data: [] as SessionLog[], meta: {} } satisfies ListResponse<SessionLog>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={session.id} />);

    // The bootstrap logs window goes out while the messages response is
    // still blocked — the two round trips overlap instead of serializing.
    await waitFor(() => {
      expect(logRequests).toEqual([{ latestTurns: '60', turnNumbers: null }]);
    });

    releaseMessages();
    await screen.findByText('Main reply');

    // Once the loaded messages pin down the visible turns, the query
    // converges on the precise window.
    await waitFor(() => {
      expect(logRequests.at(-1)).toEqual({ latestTurns: null, turnNumbers: '1' });
    });
  });

  it('uses only the precise turn window once messages are already loaded', async () => {
    const session = threadedSession('session-precise-logs');
    const logRequests: LoggedLogRequest[] = [];

    server.use(
      http.get(`/api/v1/sessions/${session.id}`, () => {
        return HttpResponse.json({ data: session } satisfies SingleResponse<Session>);
      }),
      http.get(`/api/v1/sessions/${session.id}/threads/:threadId/messages`, () => {
        return HttpResponse.json({
          data: [
            {
              id: 1,
              session_id: session.id,
              org_id: 'org-1',
              thread_id: threadId,
              turn_number: 1,
              role: 'assistant',
              content: 'Main reply',
              created_at: '2026-02-17T07:02:00Z',
            },
          ] as SessionMessage[],
          meta: {},
        } satisfies ListResponse<SessionMessage>);
      }),
      http.get(`/api/v1/sessions/${session.id}/threads/:threadId/logs`, ({ request }) => {
        const url = new URL(request.url);
        logRequests.push({
          latestTurns: url.searchParams.get('latest_turns'),
          turnNumbers: url.searchParams.get('turn_numbers'),
        });
        return HttpResponse.json({ data: [] as SessionLog[], meta: {} } satisfies ListResponse<SessionLog>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={session.id} />);
    await screen.findByText('Main reply');

    await waitFor(() => {
      expect(logRequests.at(-1)).toEqual({ latestTurns: null, turnNumbers: '1' });
    });
    // The bootstrap window may or may not have fired depending on timing, but
    // it must never fire again after the precise window is established.
    const precise = logRequests.filter((request) => request.turnNumbers !== null);
    const bootstrapAfterPrecise = logRequests.indexOf(precise[0]) >= 0
      ? logRequests.slice(logRequests.indexOf(precise[0])).filter((request) => request.latestTurns !== null)
      : [];
    expect(bootstrapAfterPrecise).toEqual([]);
  });
});
