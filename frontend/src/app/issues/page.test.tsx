import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { mockIssues } from '@/test/mocks/handlers';
import IssuesPage from './page';

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

describe('IssuesPage', () => {
  it('shows loading state initially', () => {
    renderWithProviders(<IssuesPage />);
    expect(screen.getByText('Loading issues...')).toBeInTheDocument();
  });

  it('renders issues returned from the API', async () => {
    renderWithProviders(<IssuesPage />);

    expect(
      await screen.findByText('TypeError: Cannot read properties of undefined'),
    ).toBeInTheDocument();

    expect(
      screen.getByText('Null pointer exception in payment flow'),
    ).toBeInTheDocument();
  });

  it('shows severity badges for each issue', async () => {
    renderWithProviders(<IssuesPage />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('critical')).toBeInTheDocument();
    expect(screen.getByText('high')).toBeInTheDocument();
  });

  it('shows status badges for each issue', async () => {
    renderWithProviders(<IssuesPage />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('open')).toBeInTheDocument();
    expect(screen.getByText('triaged')).toBeInTheDocument();
  });

  it('shows source labels for each issue', async () => {
    renderWithProviders(<IssuesPage />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('Sentry')).toBeInTheDocument();
    expect(screen.getByText('Linear')).toBeInTheDocument();
  });

  it('shows occurrence count', async () => {
    renderWithProviders(<IssuesPage />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('142 occurrences')).toBeInTheDocument();
    expect(screen.getByText('37 occurrences')).toBeInTheDocument();
  });

  it('shows affected customer count when greater than zero', async () => {
    renderWithProviders(<IssuesPage />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('23 customers')).toBeInTheDocument();
    expect(screen.getByText('5 customers')).toBeInTheDocument();
  });

  it('shows the issue count header', async () => {
    renderWithProviders(<IssuesPage />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('2 issues')).toBeInTheDocument();
  });

  it('displays page header with title and description', async () => {
    renderWithProviders(<IssuesPage />);

    expect(screen.getByText('Issues')).toBeInTheDocument();
    expect(
      screen.getByText('Issues from your connected trackers appear here.'),
    ).toBeInTheDocument();
  });

  it('shows empty state when API returns no issues', async () => {
    server.use(
      http.get('/api/v1/issues', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    renderWithProviders(<IssuesPage />);

    expect(await screen.findByText('No issues yet')).toBeInTheDocument();
    expect(
      screen.getByText(
        'Connect Sentry, Linear, or another issue tracker to start pulling in issues automatically.',
      ),
    ).toBeInTheDocument();
    expect(screen.getByText('Go to Settings')).toBeInTheDocument();
  });

  it('shows error state when API request fails', async () => {
    server.use(
      http.get('/api/v1/issues', () => {
        return HttpResponse.json(
          { error: { code: 'INTERNAL', message: 'Server error' } },
          { status: 500 },
        );
      }),
    );

    renderWithProviders(<IssuesPage />);

    expect(
      await screen.findByText(
        'Failed to load issues. Make sure the backend is running.',
      ),
    ).toBeInTheDocument();
  });

  it('renders singular issue count for a single issue', async () => {
    server.use(
      http.get('/api/v1/issues', () => {
        return HttpResponse.json({
          data: [mockIssues[0]],
          meta: {},
        });
      }),
    );

    renderWithProviders(<IssuesPage />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('1 issue')).toBeInTheDocument();
  });

  it('does not show loading state once data is loaded', async () => {
    renderWithProviders(<IssuesPage />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.queryByText('Loading issues...')).not.toBeInTheDocument();
  });
});
