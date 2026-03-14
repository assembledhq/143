import { describe, it, expect, vi } from 'vitest';
import { act } from '@testing-library/react';
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

    expect(screen.getByText('New manual session')).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Tell the agent what to do...')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add files or photos' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Dictate' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Start session' })).toBeDisabled();
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
    await user.click(screen.getByRole('button', { name: 'Add' }));

    await user.type(screen.getByPlaceholderText('Tell the agent what to do...'), 'Investigate checkout timeout and propose a fix.');
    await user.click(screen.getByRole('button', { name: 'Start session' }));

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith('/sessions/session-manual-chat-1');
    });
  });

  it('shows dictation not supported error', async () => {
    const user = userEvent.setup();
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(screen.getByRole('button', { name: 'Dictate' }));

    expect(screen.getByText('Dictation is not supported in this browser.')).toBeInTheDocument();
  });

  it('shows dictation error when recognition fails', async () => {
    let capturedInstance: { onerror: (() => void) | null; onend: (() => void) | null };

    class MockSpeechRecognition {
      continuous = false;
      interimResults = false;
      lang = '';
      onresult: ((event: unknown) => void) | null = null;
      onerror: (() => void) | null = null;
      onend: (() => void) | null = null;
      start() { /* noop */ }
      stop() { /* noop */ }
      constructor() {
        // eslint-disable-next-line @typescript-eslint/no-this-alias
        capturedInstance = this;
      }
    }

    (window as unknown as Record<string, unknown>).SpeechRecognition = MockSpeechRecognition;

    const user = userEvent.setup();
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(screen.getByRole('button', { name: 'Dictate' }));

    // Trigger the error handler inside act to ensure React processes state updates
    act(() => {
      capturedInstance!.onerror!();
    });

    expect(screen.getByText('Dictation failed. Please type your request.')).toBeInTheDocument();

    delete (window as unknown as Record<string, unknown>).SpeechRecognition;
  });

  it('removes attachment when clicking remove button', async () => {
    const user = userEvent.setup();
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(screen.getByRole('button', { name: 'Add files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add image URL' }));
    await user.type(screen.getByPlaceholderText('https://example.com/screenshot.png'), 'https://example.com/test.png');
    await user.click(screen.getByRole('button', { name: 'Add' }));

    expect(screen.getByText('https://example.com/test.png')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Remove https://example.com/test.png' }));

    expect(screen.queryByText('https://example.com/test.png')).not.toBeInTheDocument();
  });

  it('does not add empty URL', async () => {
    const user = userEvent.setup();
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(screen.getByRole('button', { name: 'Add files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add image URL' }));
    await user.click(screen.getByRole('button', { name: 'Add' }));

    // The image URL input area should still be visible (not dismissed)
    // and no attachment badges should appear
    expect(screen.getByPlaceholderText('https://example.com/screenshot.png')).toBeInTheDocument();
  });

  it('shows error when session creation fails', async () => {
    const user = userEvent.setup();

    server.use(
      http.post('/api/v1/sessions/manual', () => {
        return HttpResponse.json(
          { error: { code: 'INTERNAL', message: 'server error' } },
          { status: 500 },
        );
      }),
    );

    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.type(screen.getByPlaceholderText('Tell the agent what to do...'), 'Test message');
    await user.click(screen.getByRole('button', { name: 'Start session' }));

    await waitFor(() => {
      expect(screen.getByText('Could not start session. Please try again.')).toBeInTheDocument();
    });
  });

  it('disables send button while pending', async () => {
    const user = userEvent.setup();

    server.use(
      http.post('/api/v1/sessions/manual', () => {
        return new Promise(() => {}); // Never resolves
      }),
    );

    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.type(screen.getByPlaceholderText('Tell the agent what to do...'), 'Test message');
    await user.click(screen.getByRole('button', { name: 'Start session' }));

    expect(screen.getByRole('button', { name: 'Start session' })).toBeDisabled();
  });
});
