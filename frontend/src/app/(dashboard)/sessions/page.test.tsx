import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { mockSessions, mockMembers } from '@/test/mocks/handlers';
import { SessionsPageContent } from './sessions-page-content';
import type { Session, User, ListResponse } from '@/lib/types';

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

// Mock next/navigation — SessionsPageContent uses useRouter for row clicks
vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), back: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => '/sessions',
}));

describe('SessionsPage', () => {
  it('shows loading state initially', () => {
    renderWithProviders(<SessionsPageContent />);
    expect(screen.getByText('Loading sessions...')).toBeInTheDocument();
  });

  it('renders sessions returned from the API', async () => {
    renderWithProviders(<SessionsPageContent />);

    expect(
      await screen.findByText('Fixed TypeError by adding null check'),
    ).toBeInTheDocument();
  });

  it('shows agent type badges', async () => {
    renderWithProviders(<SessionsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.getByText('claude code')).toBeInTheDocument();
    expect(screen.getByText('codex')).toBeInTheDocument();
  });

  it('displays page header with title and description', async () => {
    renderWithProviders(<SessionsPageContent />);

    expect(screen.getByText('Sessions')).toBeInTheDocument();
    expect(
      screen.getByText('Each agent execution creates a session.'),
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

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.getByRole('button', { name: 'All' })).toBeInTheDocument();
    expect(screen.getAllByText('Active').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Done').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Failed').length).toBeGreaterThanOrEqual(1);
  });

  it('shows status indicators for sessions', async () => {
    renderWithProviders(<SessionsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    // The mock sessions have one completed and one failed
    expect(screen.getAllByText('Completed').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Failed').length).toBeGreaterThanOrEqual(1);
  });

  it('shows Triggered by column header', async () => {
    renderWithProviders(<SessionsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.getByText('Triggered by')).toBeInTheDocument();
  });

  it('displays triggered-by user name for sessions with triggered_by_user_id', async () => {
    const sessionsWithTriggeredBy: Session[] = [
      {
        ...mockSessions[0],
        triggered_by_user_id: 'user-1',
      },
    ];

    server.use(
      http.get('/api/v1/sessions', () => {
        return HttpResponse.json({
          data: sessionsWithTriggeredBy,
          meta: {},
        } satisfies ListResponse<Session>);
      }),
      http.get('/api/v1/team/members', () => {
        return HttpResponse.json({
          data: mockMembers,
          meta: {},
        } satisfies ListResponse<User>);
      }),
    );

    renderWithProviders(<SessionsPageContent />);

    // Alice Smith -> shows first name "Alice"
    expect(await screen.findByText('Alice')).toBeInTheDocument();
  });

  it('shows dash when session has no triggered_by_user_id', async () => {
    renderWithProviders(<SessionsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    // Mock sessions don't have triggered_by_user_id, so should show dashes
    const dashes = screen.getAllByText('—');
    expect(dashes.length).toBeGreaterThanOrEqual(1);
  });

  it('shows sortable column headers', async () => {
    renderWithProviders(<SessionsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.getByText('Status')).toBeInTheDocument();
    expect(screen.getByText('Agent')).toBeInTheDocument();
    expect(screen.getByText('Confidence')).toBeInTheDocument();
    expect(screen.getByText('Last modified')).toBeInTheDocument();
  });

  it('shows session count', async () => {
    renderWithProviders(<SessionsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.getByText('2 sessions')).toBeInTheDocument();
  });
});
