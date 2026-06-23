import { describe, it, expect, vi } from 'vitest';
import type { ReactNode } from 'react';
import { act } from '@testing-library/react';
import { fireEvent } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, userEvent, waitFor } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { ManualSessionCreatePageContent } from './manual-session-create-page-content';

const replaceMock = vi.fn();

vi.mock('next/navigation', () => ({
  useRouter: () => ({ replace: replaceMock, push: vi.fn(), back: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
}));

vi.mock('@/components/ui/dropdown-menu', () => ({
  DropdownMenu: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DropdownMenuTrigger: ({ children }: { children: ReactNode; asChild?: boolean }) => <>{children}</>,
  DropdownMenuContent: ({ children }: { children: ReactNode }) => <div role="menu">{children}</div>,
  DropdownMenuItem: ({ children, onClick, className }: { children: ReactNode; onClick?: () => void; className?: string }) => (
    <button type="button" role="menuitem" onClick={onClick} className={className}>
      {children}
    </button>
  ),
}));

describe('ManualSessionCreatePage', () => {
  it('renders centered composer controls', () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    expect(screen.getByText("Let's build")).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Tell the agent what to do...')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add files or photos' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Dictate' })).not.toBeInTheDocument();
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

    replaceMock.mockReset();
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(screen.getByRole('button', { name: 'Add files or photos' }));
    expect(screen.getByTestId('add-image-url-link-icon')).toBeInTheDocument();
    await user.click(screen.getByRole('menuitem', { name: 'Add image URL' }));
    await user.type(screen.getByPlaceholderText('https://example.com/screenshot.png'), 'https://example.com/checkout-timeout.png');
    await user.click(screen.getByRole('button', { name: 'Add' }));

    await user.type(screen.getByPlaceholderText('Tell the agent what to do...'), 'Investigate checkout timeout and propose a fix.');
    await user.click(screen.getByRole('button', { name: 'Start session' }));

    await waitFor(() => {
      expect(replaceMock).toHaveBeenCalledWith('/sessions/session-manual-chat-1');
    });
  }, 10000);

  it('inserts a selected mention and submits canonical references', async () => {
    const user = userEvent.setup();

    server.use(
      http.get('/api/v1/repositories', () => HttpResponse.json({
        data: [
          {
            id: 'repo-1',
            org_id: 'org-1',
            integration_id: 'int-1',
            github_id: 1,
            full_name: 'acme/repo',
            default_branch: 'main',
            private: false,
            clone_url: 'https://github.com/acme/repo.git',
            installation_id: 10,
            status: 'active',
            settings: {},
            created_at: '2026-03-05T12:00:00Z',
            updated_at: '2026-03-05T12:00:00Z',
          },
        ],
      })),
      http.get('/api/v1/repositories/:id/branches', () => HttpResponse.json({ data: [{ name: 'main', protected: true }] })),
      http.get('/api/v1/session-composer/files', ({ request }) => {
        const url = new URL(request.url);
        if (!url.searchParams.get('q')) {
          return HttpResponse.json({ data: [] });
        }

        return HttpResponse.json({
          data: [
            {
              kind: 'file',
              token: '@internal/api/handlers/sessions.go',
              path: 'internal/api/handlers/sessions.go',
              display: 'internal/api/handlers/sessions.go',
            },
          ],
        });
      }),
      http.post('/api/v1/sessions/manual', async ({ request }) => {
        const body = await request.json() as { message: string; references?: Array<{ path?: string; kind: string }> };
        expect(body.message).toContain('@internal/api/handlers/sessions.go');
        expect(body.references).toEqual([
          {
            kind: 'file',
            token: '@internal/api/handlers/sessions.go',
            path: 'internal/api/handlers/sessions.go',
            display: 'internal/api/handlers/sessions.go',
          },
        ]);

        return HttpResponse.json({ data: { id: 'session-with-reference' } }, { status: 201 });
      }),
    );

    replaceMock.mockReset();
    renderWithProviders(<ManualSessionCreatePageContent />);

    const textarea = screen.getByPlaceholderText('Tell the agent what to do...');
    await user.type(textarea, 'Inspect @sess');
    await user.click(await screen.findByRole('button', { name: 'internal/api/handlers/sessions.go' }));

    expect(screen.getByRole('button', { name: 'Remove internal/api/handlers/sessions.go' })).toBeInTheDocument();
    expect(textarea).toHaveValue('Inspect @internal/api/handlers/sessions.go ');

    await user.click(screen.getByRole('button', { name: 'Start session' }));

    await waitFor(() => {
      expect(replaceMock).toHaveBeenCalledWith('/sessions/session-with-reference');
    });
  });

  it('clears selected references when the repository changes', async () => {
    const user = userEvent.setup();

    server.use(
      http.get('/api/v1/repositories', () => HttpResponse.json({
        data: [
          {
            id: 'repo-a',
            org_id: 'org-1',
            integration_id: 'int-1',
            github_id: 1,
            full_name: 'acme/repo-a',
            default_branch: 'main',
            private: false,
            clone_url: 'https://github.com/acme/repo-a.git',
            installation_id: 10,
            status: 'active',
            settings: {},
            created_at: '2026-03-05T12:00:00Z',
            updated_at: '2026-03-05T12:00:00Z',
          },
          {
            id: 'repo-b',
            org_id: 'org-1',
            integration_id: 'int-1',
            github_id: 2,
            full_name: 'acme/repo-b',
            default_branch: 'main',
            private: false,
            clone_url: 'https://github.com/acme/repo-b.git',
            installation_id: 11,
            status: 'active',
            settings: {},
            created_at: '2026-03-05T12:00:00Z',
            updated_at: '2026-03-05T12:00:00Z',
          },
        ],
      })),
      http.get('/api/v1/repositories/:id/branches', () => HttpResponse.json({ data: [{ name: 'main', protected: true }] })),
      http.get('/api/v1/session-composer/files', ({ request }) => {
        const url = new URL(request.url);
        const repoID = url.searchParams.get('repository_id');
        return HttpResponse.json({
          data: repoID === 'repo-a'
            ? [{ kind: 'file', token: '@internal/a.ts', path: 'internal/a.ts', display: 'internal/a.ts' }]
            : [{ kind: 'file', token: '@internal/b.ts', path: 'internal/b.ts', display: 'internal/b.ts' }],
        });
      }),
    );

    renderWithProviders(<ManualSessionCreatePageContent />);

    const textarea = screen.getByPlaceholderText('Tell the agent what to do...');
    await user.type(textarea, 'Inspect @sess');
    await user.click(await screen.findByRole('button', { name: 'internal/a.ts' }));

    expect(textarea).toHaveValue('Inspect @internal/a.ts ');
    expect(screen.getByText('internal/a.ts')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /repo-a/i }));
    await user.click(screen.getByRole('menuitem', { name: /repo-b/i }));

    await waitFor(() => {
      expect(textarea).toHaveValue('Inspect');
    });
    expect(screen.queryByText('internal/a.ts')).not.toBeInTheDocument();
  });

  it('stops mention mode after moving the caret away from the @ token', async () => {
    const user = userEvent.setup();

    server.use(
      http.get('/api/v1/repositories', () => HttpResponse.json({
        data: [
          {
            id: 'repo-1',
            org_id: 'org-1',
            integration_id: 'int-1',
            github_id: 1,
            full_name: 'acme/repo',
            default_branch: 'main',
            private: false,
            clone_url: 'https://github.com/acme/repo.git',
            installation_id: 10,
            status: 'active',
            settings: {},
            created_at: '2026-03-05T12:00:00Z',
            updated_at: '2026-03-05T12:00:00Z',
          },
        ],
      })),
      http.get('/api/v1/repositories/:id/branches', () => HttpResponse.json({ data: [{ name: 'main', protected: true }] })),
      http.get('/api/v1/session-composer/files', () => HttpResponse.json({
        data: [
          {
            kind: 'file',
            token: '@internal/api/handlers/sessions.go',
            path: 'internal/api/handlers/sessions.go',
            display: 'internal/api/handlers/sessions.go',
          },
        ],
      })),
      http.post('/api/v1/sessions/manual', () => HttpResponse.json({ data: { id: 'noop-session' } }, { status: 201 })),
    );

    renderWithProviders(<ManualSessionCreatePageContent />);

    const textarea = screen.getByPlaceholderText('Tell the agent what to do...') as HTMLTextAreaElement;
    await user.type(textarea, 'Inspect @sess');
    expect(await screen.findByRole('button', { name: 'internal/api/handlers/sessions.go' })).toBeInTheDocument();

    textarea.focus();
    textarea.setSelectionRange(0, 0);
    fireEvent.select(textarea);

    await waitFor(() => {
      expect(screen.queryByRole('button', { name: 'internal/api/handlers/sessions.go' })).not.toBeInTheDocument();
    });
  });

  it('closes the mention picker when the user types a space after @ text', async () => {
    const user = userEvent.setup();

    server.use(
      http.get('/api/v1/repositories', () => HttpResponse.json({
        data: [
          {
            id: 'repo-1',
            org_id: 'org-1',
            integration_id: 'int-1',
            github_id: 1,
            full_name: 'acme/repo',
            default_branch: 'main',
            private: false,
            clone_url: 'https://github.com/acme/repo.git',
            installation_id: 10,
            status: 'active',
            settings: {},
            created_at: '2026-03-05T12:00:00Z',
            updated_at: '2026-03-05T12:00:00Z',
          },
        ],
      })),
      http.get('/api/v1/repositories/:id/branches', () => HttpResponse.json({ data: [{ name: 'main', protected: true }] })),
      http.get('/api/v1/session-composer/files', ({ request }) => {
        const url = new URL(request.url);
        if (!url.searchParams.get('q')) {
          return HttpResponse.json({ data: [] });
        }

        return HttpResponse.json({
          data: [
            {
              kind: 'file',
              token: '@internal/api/handlers/sessions.go',
              path: 'internal/api/handlers/sessions.go',
              display: 'internal/api/handlers/sessions.go',
            },
          ],
        });
      }),
    );

    renderWithProviders(<ManualSessionCreatePageContent />);

    const textarea = screen.getByPlaceholderText('Tell the agent what to do...');
    await user.type(textarea, 'Inspect @sess');
    expect(await screen.findByRole('button', { name: 'internal/api/handlers/sessions.go' })).toBeInTheDocument();

    await user.type(textarea, ' ');

    await waitFor(() => {
      expect(screen.queryByRole('button', { name: 'internal/api/handlers/sessions.go' })).not.toBeInTheDocument();
    });
  });

  it('renders the mention picker as an overlay outside the composer card flow', async () => {
    const user = userEvent.setup();

    server.use(
      http.get('/api/v1/repositories', () => HttpResponse.json({
        data: [
          {
            id: 'repo-1',
            org_id: 'org-1',
            integration_id: 'int-1',
            github_id: 1,
            full_name: 'acme/repo',
            default_branch: 'main',
            private: false,
            clone_url: 'https://github.com/acme/repo.git',
            installation_id: 10,
            status: 'active',
            settings: {},
            created_at: '2026-03-05T12:00:00Z',
            updated_at: '2026-03-05T12:00:00Z',
          },
        ],
      })),
      http.get('/api/v1/repositories/:id/branches', () => HttpResponse.json({ data: [{ name: 'main', protected: true }] })),
      http.get('/api/v1/session-composer/files', () => HttpResponse.json({
        data: [
          {
            kind: 'directory',
            token: '@internal/services',
            path: 'internal/services',
            display: 'internal/services',
          },
        ],
      })),
    );

    renderWithProviders(<ManualSessionCreatePageContent />);

    const composerCard = screen.getByTestId('manual-session-composer');
    vi.spyOn(composerCard, 'getBoundingClientRect').mockReturnValue({
      x: 0,
      y: 420,
      width: 600,
      height: 120,
      top: 420,
      right: 600,
      bottom: 540,
      left: 0,
      toJSON: () => ({}),
    });

    const textarea = screen.getByPlaceholderText('Tell the agent what to do...');
    await user.type(textarea, 'Inspect @serv');

    const overlay = await screen.findByTestId('mention-picker-overlay');

    expect(composerCard).not.toContainElement(overlay);
    expect((overlay as HTMLElement).style.bottom).not.toBe('');
    expect(screen.getByRole('button', { name: 'internal/services' })).toBeInTheDocument();
  });

  it('repositions the mention picker when the composer resizes', async () => {
    const user = userEvent.setup();

    server.use(
      http.get('/api/v1/repositories', () => HttpResponse.json({
        data: [
          {
            id: 'repo-1',
            org_id: 'org-1',
            integration_id: 'int-1',
            github_id: 1,
            full_name: 'acme/repo',
            default_branch: 'main',
            private: false,
            clone_url: 'https://github.com/acme/repo.git',
            installation_id: 10,
            status: 'active',
            settings: {},
            created_at: '2026-03-05T12:00:00Z',
            updated_at: '2026-03-05T12:00:00Z',
          },
        ],
      })),
      http.get('/api/v1/repositories/:id/branches', () => HttpResponse.json({ data: [{ name: 'main', protected: true }] })),
      http.get('/api/v1/session-composer/files', () => HttpResponse.json({
        data: [
          {
            kind: 'directory',
            token: '@internal/services',
            path: 'internal/services',
            display: 'internal/services',
          },
        ],
      })),
    );

    let resizeObserverCallback: ResizeObserverCallback | null = null;
    const disconnectMock = vi.fn();
    const originalResizeObserver = window.ResizeObserver;
    class MockResizeObserver {
      constructor(callback: ResizeObserverCallback) {
        resizeObserverCallback = callback;
      }
      observe() {}
      unobserve() {}
      disconnect() {
        disconnectMock();
      }
    }
    window.ResizeObserver = MockResizeObserver as typeof ResizeObserver;

    renderWithProviders(<ManualSessionCreatePageContent />);

    const composerCard = screen.getByTestId('manual-session-composer');
    let rect = {
      x: 0,
      y: 420,
      width: 600,
      height: 120,
      top: 420,
      right: 600,
      bottom: 540,
      left: 0,
      toJSON: () => ({}),
    };
    vi.spyOn(composerCard, 'getBoundingClientRect').mockImplementation(() => rect);

    const textarea = screen.getByPlaceholderText('Tell the agent what to do...');
    await user.type(textarea, 'Inspect @serv');

    const overlay = await screen.findByTestId('mention-picker-overlay');
    expect(overlay).toHaveStyle({ left: '0px', width: '600px' });

    rect = {
      ...rect,
      width: 720,
      right: 760,
      left: 40,
      x: 40,
    };

    await act(async () => {
      resizeObserverCallback?.([], {} as ResizeObserver);
    });

    await waitFor(() => {
      expect(overlay).toHaveStyle({ left: '40px', width: '720px' });
    });

    window.ResizeObserver = originalResizeObserver;
  }, 10_000);

  it('drops the selected reference chip when the inserted mention token is edited', async () => {
    const user = userEvent.setup();

    server.use(
      http.get('/api/v1/repositories', () => HttpResponse.json({
        data: [
          {
            id: 'repo-1',
            org_id: 'org-1',
            integration_id: 'int-1',
            github_id: 1,
            full_name: 'acme/repo',
            default_branch: 'main',
            private: false,
            clone_url: 'https://github.com/acme/repo.git',
            installation_id: 10,
            status: 'active',
            settings: {},
            created_at: '2026-03-05T12:00:00Z',
            updated_at: '2026-03-05T12:00:00Z',
          },
        ],
      })),
      http.get('/api/v1/repositories/:id/branches', () => HttpResponse.json({ data: [{ name: 'main', protected: true }] })),
      http.get('/api/v1/session-composer/files', () => HttpResponse.json({
        data: [
          {
            kind: 'file',
            token: '@internal/api/handlers/sessions.go',
            path: 'internal/api/handlers/sessions.go',
            display: 'internal/api/handlers/sessions.go',
          },
        ],
      })),
    );

    renderWithProviders(<ManualSessionCreatePageContent />);

    const textarea = screen.getByPlaceholderText('Tell the agent what to do...') as HTMLTextAreaElement;
    await user.type(textarea, 'Inspect @sess');
    await user.click(await screen.findByRole('button', { name: 'internal/api/handlers/sessions.go' }));

    expect(screen.getByText('internal/api/handlers/sessions.go')).toBeInTheDocument();
    await waitFor(() => {
      expect(textarea.value).toContain('@internal/api/handlers/sessions.go');
    });

    fireEvent.change(textarea, {
      target: { value: 'Inspect @internal/api/handlers ' },
    });

    await waitFor(() => {
      expect(screen.queryByRole('button', { name: 'Remove internal/api/handlers/sessions.go' })).not.toBeInTheDocument();
    });
  });

  it('does not render dictation controls or errors', () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    expect(screen.queryByRole('button', { name: 'Dictate' })).not.toBeInTheDocument();
    expect(screen.queryByText('Dictation is not supported in this browser.')).not.toBeInTheDocument();
    expect(screen.queryByText('Dictation failed. Please type your request.')).not.toBeInTheDocument();
  });

  it('removes attachment when clicking remove button', async () => {
    const user = userEvent.setup();
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(screen.getByRole('button', { name: 'Add files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add image URL' }));
    await user.type(screen.getByPlaceholderText('https://example.com/screenshot.png'), 'https://example.com/test.png');
    await user.click(screen.getByRole('button', { name: 'Add' }));

    expect(screen.getByAltText('test.png')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Remove test.png' }));

    expect(screen.queryByAltText('test.png')).not.toBeInTheDocument();
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

  it('adds a linked Linear issue as a chip via the Add linear issue menu item', async () => {
    const user = userEvent.setup();
    server.use(
      http.get('/api/v1/integrations', () => HttpResponse.json({
        data: [
          {
            id: 'integration-linear',
            provider: 'linear',
            status: 'active',
          },
        ],
        meta: {},
      })),
    );
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(screen.getByRole('button', { name: 'Add files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add linear issue' }));

    await user.type(screen.getByLabelText('Linear issue id or URL'), 'https://linear.app/acme/issue/ACS-1234');
    await user.click(screen.getByRole('button', { name: 'Add' }));

    expect(screen.getByText('ACS-1234')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Remove ACS-1234' })).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Tell the agent what to do...')).toHaveValue('');
    expect(screen.queryByLabelText('Linear issue id or URL')).not.toBeInTheDocument();
  });

  it('rejects free-text input in the Linear add affordance with an inline error', async () => {
    const user = userEvent.setup();
    server.use(
      http.get('/api/v1/integrations', () => HttpResponse.json({
        data: [
          {
            id: 'integration-linear',
            provider: 'linear',
            status: 'active',
          },
        ],
        meta: {},
      })),
    );
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(screen.getByRole('button', { name: 'Add files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add linear issue' }));

    await user.type(screen.getByLabelText('Linear issue id or URL'), 'just some text');
    await user.click(screen.getByRole('button', { name: 'Add' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Enter a Linear URL (https://linear.app/...) or key like ACS-1234',
    );
    expect(screen.getByLabelText('Linear issue id or URL')).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Tell the agent what to do...')).toHaveValue('');
  });

  it('uploads a file via the upload endpoint and shows thumbnail', async () => {
    const user = userEvent.setup();

    server.use(
      http.post('/api/v1/uploads', () => {
        return HttpResponse.json({
          url: '/api/v1/uploads/files/org-1/2026-03/test-uuid.png',
          file_name: 'photo.png',
          content_type: 'image/png',
        });
      }),
    );

    renderWithProviders(<ManualSessionCreatePageContent />);

    // Trigger upload via hidden file input.
    const file = new File(['fake-png'], 'photo.png', { type: 'image/png' });
    await user.click(screen.getByRole('button', { name: 'Add files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Upload files or photos' }));

    // The hidden file input should exist; simulate a file selection.
    const fileInput = document.querySelector('input[type="file"]') as HTMLInputElement;
    expect(fileInput).toBeTruthy();
    await user.upload(fileInput, file);

    // Should show the uploaded image thumbnail.
    await waitFor(() => {
      expect(screen.getByAltText('test-uuid.png')).toBeInTheDocument();
    });
  });

  it('opens an image lightbox from the composer attachment thumbnail', async () => {
    const user = userEvent.setup();

    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(screen.getByRole('button', { name: 'Add files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add image URL' }));
    await user.type(screen.getByPlaceholderText('https://example.com/screenshot.png'), 'https://example.com/test.png');
    await user.click(screen.getByRole('button', { name: 'Add' }));

    await user.click(screen.getByRole('button', { name: 'Preview test.png' }));

    expect(screen.getByRole('dialog', { name: 'Image preview' })).toBeInTheDocument();
    expect(screen.getByRole('img', { name: 'test.png' })).toBeInTheDocument();
  });

  it('shows error for oversized files', async () => {
    const user = userEvent.setup();
    renderWithProviders(<ManualSessionCreatePageContent />);

    // Create a file larger than 10 MB.
    const bigContent = new Uint8Array(11 * 1024 * 1024);
    const bigFile = new File([bigContent], 'huge.png', { type: 'image/png' });

    const fileInput = document.querySelector('input[type="file"]') as HTMLInputElement;
    expect(fileInput).toBeTruthy();
    await user.upload(fileInput, bigFile);

    await waitFor(() => {
      expect(screen.getByText(/too large/i)).toBeInTheDocument();
    });
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
      expect(screen.getByText('server error')).toBeInTheDocument();
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
