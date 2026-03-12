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

  it('groups sessions into sections by status', async () => {
    renderWithProviders(<SessionsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    // The mock sessions have one completed and one failed
    expect(screen.getAllByText('Completed').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Failed').length).toBeGreaterThanOrEqual(1);
  });

  it('links session rows to detail pages', async () => {
    renderWithProviders(<SessionsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    const links = screen.getAllByRole('link');
    const sessionLinks = links.filter((l) => l.getAttribute('href')?.startsWith('/sessions/'));
    expect(sessionLinks.length).toBeGreaterThan(0);
  });

});
