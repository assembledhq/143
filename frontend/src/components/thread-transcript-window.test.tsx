import { http, HttpResponse } from 'msw';
import { describe, expect, it, vi } from 'vitest';
import { ThreadTranscriptWindow } from '@/components/thread-transcript-window';
import { server } from '@/test/mocks/server';
import { renderWithProviders, screen, waitFor } from '@/test/test-utils';
import type { SessionTranscriptWindowResponse } from '@/lib/types';

vi.mock('@tanstack/react-virtual', () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getTotalSize: () => count * 180,
    getVirtualItems: () =>
      Array.from({ length: count }, (_, index) => ({
        index,
        key: index,
        start: index * 180,
      })),
    measureElement: vi.fn(),
  }),
}));

function transcriptResponse(overrides: Partial<SessionTranscriptWindowResponse['meta']> = {}): SessionTranscriptWindowResponse {
  return {
    data: [
      {
        turn_number: 7,
        started_at: '2026-06-14T12:00:00Z',
        entries: [
          {
            id: 'msg_101',
            kind: 'message',
            message_id: 101,
            role: 'user',
            content: 'Please run the focused tests.',
            created_at: '2026-06-14T12:00:00Z',
          },
          {
            id: 'log_44',
            kind: 'tool_use',
            log_id: 44,
            tool_name: 'exec_command',
            summary: 'npm run test:ci',
            created_at: '2026-06-14T12:00:10Z',
          },
          {
            id: 'msg_102',
            kind: 'message',
            message_id: 102,
            role: 'assistant',
            content: 'The focused tests pass.',
            created_at: '2026-06-14T12:01:00Z',
          },
        ],
      },
    ],
    meta: {
      position: 'latest',
      has_older: false,
      has_newer: false,
      anchor_found: true,
      thread_status: 'idle',
      ...overrides,
    },
  };
}

describe('ThreadTranscriptWindow', () => {
  it('renders backend-grouped transcript turns from the transcript endpoint', async () => {
    server.use(
      http.get('/api/v1/sessions/session-1/threads/thread-1/transcript', () => {
        return HttpResponse.json(transcriptResponse());
      }),
    );

    renderWithProviders(
      <ThreadTranscriptWindow sessionId="session-1" threadId="thread-1" />,
    );

    expect(await screen.findByText('Please run the focused tests.')).toBeInTheDocument();
    expect(screen.getByText('The focused tests pass.')).toBeInTheDocument();
    expect(screen.getByText('exec_command')).toBeInTheDocument();
    expect(screen.getByRole('group', { name: 'Turn 7' })).toBeInTheDocument();
  });

  it('opens around an initial message anchor when one is provided', async () => {
    let queryString = '';
    server.use(
      http.get('/api/v1/sessions/session-1/threads/thread-1/transcript', ({ request }) => {
        queryString = new URL(request.url).searchParams.toString();
        return HttpResponse.json(transcriptResponse({
          position: 'around',
          anchor_entry_id: 'msg_101',
        }));
      }),
    );

    renderWithProviders(
      <ThreadTranscriptWindow
        sessionId="session-1"
        threadId="thread-1"
        initialAnchorMessageId={101}
      />,
    );

    await screen.findByText('Please run the focused tests.');
    await waitFor(() => {
      expect(queryString).toContain('position=around');
      expect(queryString).toContain('anchor_message_id=101');
    });
  });

  it('renders attachment-only transcript messages', async () => {
    server.use(
      http.get('/api/v1/sessions/session-1/threads/thread-1/transcript', () => {
        return HttpResponse.json({
          data: [
            {
              turn_number: 8,
              started_at: '2026-06-14T12:05:00Z',
              entries: [
                {
                  id: 'msg_201',
                  kind: 'message',
                  message_id: 201,
                  role: 'user',
                  content: '',
                  created_at: '2026-06-14T12:05:00Z',
                  message: {
                    id: 201,
                    session_id: 'session-1',
                    org_id: 'org-1',
                    thread_id: 'thread-1',
                    turn_number: 8,
                    role: 'user',
                    content: '',
                    attachments: ['/uploads/org-1/debug.txt'],
                    created_at: '2026-06-14T12:05:00Z',
                  },
                },
              ],
            },
          ],
          meta: {
            position: 'latest',
            has_older: false,
            has_newer: false,
            anchor_found: true,
            thread_status: 'idle',
          },
        });
      }),
    );

    renderWithProviders(
      <ThreadTranscriptWindow sessionId="session-1" threadId="thread-1" />,
    );

    expect(await screen.findByText('debug.txt')).toBeInTheDocument();
    expect(screen.getByText('debug.txt').closest('a')).toHaveAttribute('href', '/uploads/org-1/debug.txt');
  });
});
