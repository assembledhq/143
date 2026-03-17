import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { mockSessions, mockMembers } from '@/test/mocks/handlers';
import { SessionSidebar } from './session-sidebar';
import type { Session, User, ListResponse } from '@/lib/types';

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

// Mock next/navigation
vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), back: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => '/sessions',
  useParams: () => ({}),
}));

describe('SessionSidebar', () => {
  it('shows loading state initially', () => {
    renderWithProviders(<SessionSidebar />);
    expect(screen.getByText('Loading...')).toBeInTheDocument();
  });

  it('renders sessions returned from the API', async () => {
    renderWithProviders(<SessionSidebar />);

    expect(
      await screen.findByText('Fixed TypeError by adding null check'),
    ).toBeInTheDocument();
  });

  it('displays page header with Sessions title', async () => {
    renderWithProviders(<SessionSidebar />);

    expect(screen.getByText('Sessions')).toBeInTheDocument();
  });

  it('shows empty state when API returns no sessions', async () => {
    server.use(
      http.get('/api/v1/sessions', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    renderWithProviders(<SessionSidebar />);

    expect(await screen.findByText('No sessions yet')).toBeInTheDocument();
  });

  it('shows status filter tabs', async () => {
    renderWithProviders(<SessionSidebar />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.getByRole('button', { name: 'All' })).toBeInTheDocument();
    expect(screen.getAllByText('Active').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Done').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Failed').length).toBeGreaterThanOrEqual(1);
  });

  it('shows status indicators for sessions', async () => {
    renderWithProviders(<SessionSidebar />);

    await screen.findByText('Fixed TypeError by adding null check');

    // The mock sessions have one completed and one failed
    expect(screen.getAllByText('Completed').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Failed').length).toBeGreaterThanOrEqual(1);
  });

  it('has search input', async () => {
    renderWithProviders(<SessionSidebar />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.getByPlaceholderText('Search sessions...')).toBeInTheDocument();
  });

  it('has new session button', async () => {
    renderWithProviders(<SessionSidebar />);

    await screen.findByText('Fixed TypeError by adding null check');

    // Plus button links to /sessions/new
    const link = screen.getByRole('link', { name: '' });
    expect(link).toHaveAttribute('href', '/sessions/new');
  });
});
