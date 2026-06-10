// Warm-start prefetch behavior: the session detail page should start loading
// the stored active thread's message window in parallel with the session
// detail fetch, instead of serializing messages behind detail → thread
// selection → ChatPanel mount.
import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, waitFor } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { mockSessions, mockMembers } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import { writeStoredSessionActiveThread, writeStoredSessionAnchorPosition } from '@/lib/session-open-position';
import { writeCachedViewerScope } from '@/lib/viewer-scope-cache';
import type { Session, SessionMessage, SingleResponse, ListResponse } from '@/lib/types';
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

const sessionId = mockSessions[0].id;
const viewerScope = { userId: mockMembers[0].id, orgId: mockMembers[0].org_id ?? null };

function blockSessionDetail(): { detailRequests: () => number; releaseDetail: () => void } {
  let requests = 0;
  let release = () => {};
  const blocked = new Promise<void>((resolve) => {
    release = resolve;
  });
  server.use(
    http.get(`/api/v1/sessions/${sessionId}`, async () => {
      requests += 1;
      await blocked;
      return HttpResponse.json({
        data: { ...mockSessions[0], threads: [] },
      } satisfies SingleResponse<Session>);
    }),
  );
  return { detailRequests: () => requests, releaseDetail: release };
}

describe('SessionDetailPage warm-start message prefetch', () => {
  it('fetches the stored thread message window while session detail is still in flight', async () => {
    const threadId = 'thread-warm-1';
    writeCachedViewerScope(window.localStorage, viewerScope);
    writeStoredSessionActiveThread(window.localStorage, sessionId, viewerScope, threadId);

    const messageRequests: string[] = [];
    server.use(
      http.get(`/api/v1/sessions/${sessionId}/threads/${threadId}/messages`, ({ request }) => {
        messageRequests.push(new URL(request.url).searchParams.get('position') ?? '');
        return HttpResponse.json({
          data: [] as SessionMessage[],
          meta: {},
        } satisfies ListResponse<SessionMessage>);
      }),
    );
    const { detailRequests, releaseDetail } = blockSessionDetail();

    renderWithProviders(<SessionDetailContent id={sessionId} />);

    // The message window request fires while the detail response is still
    // blocked — proof the two round trips overlap instead of serializing.
    await waitFor(() => {
      expect(messageRequests).toEqual(['latest']);
    });
    expect(detailRequests()).toBe(1);

    releaseDetail();
    await screen.findAllByText('Fixed TypeError by adding null check');
  });

  it('anchors the prefetched window to the stored reading position when one exists', async () => {
    const threadId = 'thread-warm-2';
    writeCachedViewerScope(window.localStorage, viewerScope);
    writeStoredSessionActiveThread(window.localStorage, sessionId, viewerScope, threadId);
    writeStoredSessionAnchorPosition(
      window.localStorage,
      sessionId,
      viewerScope,
      { anchor: { kind: 'message', id: 42 }, offsetPx: 0, scrollTopFallback: 0 },
      threadId,
    );

    const anchorParams: Array<{ position: string | null; anchorMessageId: string | null }> = [];
    server.use(
      http.get(`/api/v1/sessions/${sessionId}/threads/${threadId}/messages`, ({ request }) => {
        const url = new URL(request.url);
        anchorParams.push({
          position: url.searchParams.get('position'),
          anchorMessageId: url.searchParams.get('anchor_message_id'),
        });
        return HttpResponse.json({
          data: [] as SessionMessage[],
          meta: {},
        } satisfies ListResponse<SessionMessage>);
      }),
    );
    const { releaseDetail } = blockSessionDetail();

    renderWithProviders(<SessionDetailContent id={sessionId} />);

    await waitFor(() => {
      expect(anchorParams).toEqual([{ position: 'around', anchorMessageId: '42' }]);
    });

    releaseDetail();
    await screen.findAllByText('Fixed TypeError by adding null check');
  });

  it('does not prefetch when no thread is stored for the session', async () => {
    writeCachedViewerScope(window.localStorage, viewerScope);

    let messageRequests = 0;
    server.use(
      http.get(`/api/v1/sessions/${sessionId}/threads/:threadId/messages`, () => {
        messageRequests += 1;
        return HttpResponse.json({
          data: [] as SessionMessage[],
          meta: {},
        } satisfies ListResponse<SessionMessage>);
      }),
    );
    const { releaseDetail } = blockSessionDetail();

    renderWithProviders(<SessionDetailContent id={sessionId} />);

    await new Promise((r) => setTimeout(r, 50));
    expect(messageRequests).toBe(0);

    releaseDetail();
    await screen.findAllByText('Fixed TypeError by adding null check');
  });
});
