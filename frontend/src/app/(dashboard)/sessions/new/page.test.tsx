import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, userEvent, waitFor } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { ManualSessionCreatePageContent } from './manual-session-create-page-content';

const pushMock = vi.fn();

vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: pushMock, back: vi.fn() }),
}));

describe('ManualSessionCreatePage', () => {
  it('renders centered composer controls', () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    expect(screen.getByText('New Manual Session')).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Tell the agent what to do...')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add files or photos' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Dictate' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Start Session' })).toBeDisabled();
  });

  it('creates manual session and redirects to the session detail page', async () => {
    const user = userEvent.setup();

    server.use(
      http.post('/api/v1/sessions/manual', async ({ request }) => {
        const body = await request.json() as { message: string; images?: string[] };
        if (body.message !== 'Investigate checkout timeout and propose a fix.' || !body.images?.includes('https://example.com/checkout-timeout.png')) {
          return HttpResponse.json({ error: { code: 'INVALID', message: 'invalid payload' } }, { status: 400 });
        }

        return HttpResponse.json(
          {
            data: {
              id: 'session-manual-chat-1',
              type: 'manual',
              status: 'active',
              triggered_by: 'manual',
              title: 'Investigate checkout timeout and propose a fix',
              task_count: 1,
              active_run_count: 1,
              completed_run_count: 0,
              failed_run_count: 0,
              tasks: [
                {
                  rank: 1,
                  title: 'Investigate checkout timeout and propose a fix',
                  issue_ids: ['issue-manual-1'],
                  status: 'delegated',
                  agent_run_id: 'run-manual-chat-1',
                  run_status: 'running',
                },
              ],
              created_at: '2026-03-05T12:00:00Z',
            },
          },
          { status: 201 },
        );
      }),
    );

    pushMock.mockReset();
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(screen.getByRole('button', { name: 'Add files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add image URL' }));
    await user.type(screen.getByPlaceholderText('https://example.com/screenshot.png'), 'https://example.com/checkout-timeout.png');
    await user.click(screen.getByRole('button', { name: 'Add Image' }));

    await user.type(screen.getByPlaceholderText('Tell the agent what to do...'), 'Investigate checkout timeout and propose a fix.');
    await user.click(screen.getByRole('button', { name: 'Start Session' }));

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith('/sessions/session-manual-chat-1');
    });
  });
});
