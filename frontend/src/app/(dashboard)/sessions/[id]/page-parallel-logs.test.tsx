// Transcript window rendering: the single /transcript endpoint returns both
// messages and tool-call logs for the loaded turns, so the legacy ChatTimeline
// renders both from one fetch (replacing the old parallel messages + logs
// two-query bootstrap).
import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { mockSessions } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import type { Session, SessionMessage, SingleResponse, SessionLog } from '@/lib/types';
import { installSessionDetailPageTestHooks, makeTranscriptWindow } from './session-detail-test-kit';

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

describe('SessionDetailPage transcript window', () => {
  it('renders both a message and a tool-call log from a single transcript fetch', async () => {
    const session = threadedSession('session-transcript-window');
    // Flip the thread to running so the transcript carries an interleaved
    // tool-call log alongside the assistant message, exercising the merged
    // message + log rendering that previously came from two separate queries.
    session.status = 'running';
    session.threads![0].status = 'running';
    let transcriptRequests = 0;

    const messages: SessionMessage[] = [
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
    ];
    const logs: SessionLog[] = [
      {
        id: 10,
        session_id: session.id,
        thread_id: threadId,
        level: 'tool_use',
        message: 'tool call',
        metadata: { tool: 'read', input: { path: 'src/app.ts' } },
        turn_number: 1,
        created_at: '2026-02-17T07:01:30Z',
        message_bytes: 9,
        message_chars: 9,
        message_truncated: false,
      },
    ];

    server.use(
      http.get(`/api/v1/sessions/${session.id}`, () => {
        return HttpResponse.json({ data: session } satisfies SingleResponse<Session>);
      }),
      http.get(`/api/v1/sessions/${session.id}/threads/:threadId/transcript`, () => {
        transcriptRequests += 1;
        return HttpResponse.json(
          makeTranscriptWindow(messages, logs, { thread_status: 'running' }),
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id={session.id} />);

    // Both the assistant message and the tool-call log render from the single
    // /transcript window — no separate logs round trip is involved.
    expect(await screen.findByText('Main reply')).toBeInTheDocument();
    expect(await screen.findByText('Read app.ts')).toBeInTheDocument();
    expect(transcriptRequests).toBeGreaterThan(0);
  });
});
