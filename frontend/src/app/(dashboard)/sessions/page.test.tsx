import { describe, it, expect, vi, beforeEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { SessionSidebar } from './session-sidebar';

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

let mockPathname = '/sessions';

// Mock next/navigation
vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), back: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => mockPathname,
  useParams: () => ({}),
}));

describe('SessionSidebar', () => {
  beforeEach(() => {
    mockPathname = '/sessions';
  });

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

  it('displays New session link at top', async () => {
    renderWithProviders(<SessionSidebar />);

    const newSessionLink = screen.getByRole('link', { name: /New session/ });
    expect(newSessionLink).toHaveAttribute('href', '/sessions/new');
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

    expect(screen.getByRole('tab', { name: 'All' })).toBeInTheDocument();
    expect(screen.getAllByText('Needs attention').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Working').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Done').length).toBeGreaterThanOrEqual(1);
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

  it('shows ghost New session entry when on /sessions/new', async () => {
    mockPathname = '/sessions/new';

    renderWithProviders(<SessionSidebar />);

    await screen.findByText('Fixed TypeError by adding null check');

    // Ghost entry should be visible in the list (italic "New session" text)
    const newSessionTexts = screen.getAllByText('New session');
    // At least 2: the top "+ New session" link + the ghost entry in the list
    expect(newSessionTexts.length).toBeGreaterThanOrEqual(2);
  });
});
