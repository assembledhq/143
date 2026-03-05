import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { SessionsPageContent } from './sessions-page-content';

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

describe('SessionsPage', () => {
  it('shows loading state initially', () => {
    renderWithProviders(<SessionsPageContent />);
    expect(screen.getByText('Loading sessions...')).toBeInTheDocument();
  });

  it('renders sessions returned from the API', async () => {
    renderWithProviders(<SessionsPageContent />);

    expect(
      await screen.findByText('Analyzed 5 open issues and delegated 2 tasks.'),
    ).toBeInTheDocument();
  });

  it('shows session type badges', async () => {
    renderWithProviders(<SessionsPageContent />);

    await screen.findByText('Analyzed 5 open issues and delegated 2 tasks.');

    expect(screen.getByText('PM Analysis')).toBeInTheDocument();
    expect(screen.getByText('Manual')).toBeInTheDocument();
  });

  it('shows triggered_by labels', async () => {
    renderWithProviders(<SessionsPageContent />);

    await screen.findByText('Analyzed 5 open issues and delegated 2 tasks.');

    expect(screen.getByText('Scheduled')).toBeInTheDocument();
    expect(screen.getByText('Fix This')).toBeInTheDocument();
  });

  it('shows task counts', async () => {
    renderWithProviders(<SessionsPageContent />);

    await screen.findByText('Analyzed 5 open issues and delegated 2 tasks.');

    expect(screen.getAllByText('1 task').length).toBeGreaterThanOrEqual(1);
  });

  it('displays page header with title and description', async () => {
    renderWithProviders(<SessionsPageContent />);

    expect(screen.getByText('Sessions')).toBeInTheDocument();
    expect(
      screen.getByText('Each PM analysis cycle or manual fix creates a session.'),
    ).toBeInTheDocument();
  });

  it('shows empty state when API returns no sessions', async () => {
    server.use(
      http.get('/api/v1/sessions', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    renderWithProviders(<SessionsPageContent />);

    expect(await screen.findByText('No sessions yet')).toBeInTheDocument();
  });

  it('shows error state when API request fails', async () => {
    server.use(
      http.get('/api/v1/sessions', () => {
        return HttpResponse.json(
          { error: { code: 'INTERNAL', message: 'Server error' } },
          { status: 500 },
        );
      }),
    );

    renderWithProviders(<SessionsPageContent />);

    expect(
      await screen.findByText(
        'Failed to load sessions. Make sure the backend is running.',
      ),
    ).toBeInTheDocument();
  });

  it('shows status filter tabs', async () => {
    renderWithProviders(<SessionsPageContent />);

    await screen.findByText('Analyzed 5 open issues and delegated 2 tasks.');

    expect(screen.getByRole('button', { name: 'All' })).toBeInTheDocument();
    // "Active", "Completed", "Failed" also appear as section headers and status badges,
    // so use getAllByText to check they appear at least once.
    expect(screen.getAllByText('Active').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Completed').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Failed').length).toBeGreaterThanOrEqual(1);
  });

  it('groups sessions into sections by default', async () => {
    renderWithProviders(<SessionsPageContent />);

    await screen.findByText('Analyzed 5 open issues and delegated 2 tasks.');

    // The mock sessions have one completed and one failed
    expect(screen.getAllByText('Completed').length).toBeGreaterThanOrEqual(2);
    expect(screen.getAllByText('Failed').length).toBeGreaterThanOrEqual(2);
  });

  it('links session rows to detail pages', async () => {
    renderWithProviders(<SessionsPageContent />);

    await screen.findByText('Analyzed 5 open issues and delegated 2 tasks.');

    const links = screen.getAllByRole('link');
    const sessionLinks = links.filter((l) => l.getAttribute('href')?.startsWith('/sessions/'));
    expect(sessionLinks.length).toBeGreaterThan(0);
  });

  it('shows Analyze Issues button', async () => {
    renderWithProviders(<SessionsPageContent />);

    expect(screen.getByText('Analyze Issues')).toBeInTheDocument();
  });

  it('links New Manual Session action to dedicated page', async () => {
    renderWithProviders(<SessionsPageContent />);

    const link = await screen.findByRole('link', { name: 'New Manual Session' });
    expect(link).toHaveAttribute('href', '/sessions/new');
  });

  it('starts a one-off manual session from chat composer', async () => {
    const user = userEvent.setup();

    server.use(
      http.post('/api/v1/sessions/manual', async ({ request }) => {
        const body = await request.json() as { message: string; images?: string[] };
        if (!body.message.includes('Investigate checkout timeout')) {
          return HttpResponse.json({ error: { code: 'INVALID', message: 'bad body' } }, { status: 400 });
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

    renderWithProviders(<SessionsPageContent />);

    await user.click(await screen.findByRole('button', { name: 'New Manual Session' }));
    await user.type(screen.getByLabelText('Message'), 'Investigate checkout timeout and propose a fix.');
    await user.click(screen.getByRole('button', { name: 'Start Session' }));

    expect(await screen.findByText('Starting session...')).toBeInTheDocument();
  });
});
