import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, userEvent, waitFor } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { mockIssues } from '@/test/mocks/handlers';
import { IssuesPageContent } from './issues-page-content';

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

// Mock next/navigation
vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: vi.fn(), back: vi.fn() }),
  usePathname: () => '/issues',
}));

describe('IssuesPage', () => {
  it('shows loading state initially', () => {
    renderWithProviders(<IssuesPageContent />);
    expect(screen.getByText('Loading issues...')).toBeInTheDocument();
  });

  it('renders issues returned from the API', async () => {
    renderWithProviders(<IssuesPageContent />);

    expect(
      await screen.findByText('TypeError: Cannot read properties of undefined'),
    ).toBeInTheDocument();

    expect(
      screen.getByText('Null pointer exception in payment flow'),
    ).toBeInTheDocument();
  });

  it('shows severity badges for each issue', async () => {
    renderWithProviders(<IssuesPageContent />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('critical')).toBeInTheDocument();
    expect(screen.getByText('high')).toBeInTheDocument();
  });

  it('shows status badges for each issue', async () => {
    renderWithProviders(<IssuesPageContent />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('open')).toBeInTheDocument();
    expect(screen.getByText('triaged')).toBeInTheDocument();
  });

  it('shows source labels for each issue', async () => {
    renderWithProviders(<IssuesPageContent />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('Sentry')).toBeInTheDocument();
    expect(screen.getByText('Linear')).toBeInTheDocument();
  });

  it('shows occurrence count', async () => {
    renderWithProviders(<IssuesPageContent />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('142 occurrences')).toBeInTheDocument();
    expect(screen.getByText('37 occurrences')).toBeInTheDocument();
  });

  it('shows affected customer count when greater than zero', async () => {
    renderWithProviders(<IssuesPageContent />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('23 customers')).toBeInTheDocument();
    expect(screen.getByText('5 customers')).toBeInTheDocument();
  });

  it('shows the issue count header', async () => {
    renderWithProviders(<IssuesPageContent />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('2 issues')).toBeInTheDocument();
  });

  it('displays page header with title and description', async () => {
    renderWithProviders(<IssuesPageContent />);

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

    renderWithProviders(<IssuesPageContent />);

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

    renderWithProviders(<IssuesPageContent />);

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

    renderWithProviders(<IssuesPageContent />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('1 issue')).toBeInTheDocument();
  });

  it('does not show loading state once data is loaded', async () => {
    renderWithProviders(<IssuesPageContent />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.queryByText('Loading issues...')).not.toBeInTheDocument();
  });

  // Filter control tests
  it('renders filter controls for status, source, and severity', async () => {
    renderWithProviders(<IssuesPageContent />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('Status')).toBeInTheDocument();
    expect(screen.getByText('Source')).toBeInTheDocument();
    expect(screen.getByText('Severity')).toBeInTheDocument();
    expect(screen.getByText('All statuses')).toBeInTheDocument();
    expect(screen.getByText('All sources')).toBeInTheDocument();
    expect(screen.getByText('All severities')).toBeInTheDocument();
  });

  it('does not show clear filters button when no filters are active', async () => {
    renderWithProviders(<IssuesPageContent />);

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.queryByText('Clear filters')).not.toBeInTheDocument();
  });

  it('passes filter params to the API when URL search params are set', async () => {
    let capturedUrl = '';
    server.use(
      http.get('/api/v1/issues', ({ request }) => {
        capturedUrl = request.url;
        return HttpResponse.json({
          data: [mockIssues[0]],
          meta: {},
        });
      }),
    );

    renderWithProviders(<IssuesPageContent />, {
      searchParams: { status: 'open' },
    });

    await waitFor(() => {
      expect(capturedUrl).toContain('status=open');
    });
  });

  it('passes multiple filter params to the API', async () => {
    let capturedUrl = '';
    server.use(
      http.get('/api/v1/issues', ({ request }) => {
        capturedUrl = request.url;
        return HttpResponse.json({
          data: [mockIssues[0]],
          meta: {},
        });
      }),
    );

    renderWithProviders(<IssuesPageContent />, {
      searchParams: { status: 'open', severity: 'critical', source: 'sentry' },
    });

    await waitFor(() => {
      expect(capturedUrl).toContain('status=open');
      expect(capturedUrl).toContain('severity=critical');
      expect(capturedUrl).toContain('source=sentry');
    });
  });

  it('shows clear filters button when filters are set via URL params', async () => {
    renderWithProviders(<IssuesPageContent />, {
      searchParams: { status: 'open' },
    });

    await screen.findByText('TypeError: Cannot read properties of undefined');

    expect(screen.getByText('Clear filters')).toBeInTheDocument();
  });

  it('clears filters when clear filters button is clicked', async () => {
    let capturedUrls: string[] = [];
    server.use(
      http.get('/api/v1/issues', ({ request }) => {
        capturedUrls.push(request.url);
        return HttpResponse.json({
          data: mockIssues,
          meta: {},
        });
      }),
    );

    const user = userEvent.setup();

    renderWithProviders(<IssuesPageContent />, {
      searchParams: { status: 'open' },
    });

    // Wait for initial filtered load
    await waitFor(() => {
      expect(capturedUrls.some(url => url.includes('status=open'))).toBe(true);
    });

    const clearBtn = screen.getByText('Clear filters');
    capturedUrls = [];
    await user.click(clearBtn);

    // After clearing, API should be called without status param
    await waitFor(() => {
      expect(capturedUrls.some(url => !url.includes('status='))).toBe(true);
    });
  });

  it('shows selected filter value in the status trigger', async () => {
    renderWithProviders(<IssuesPageContent />, {
      searchParams: { status: 'open' },
    });

    await screen.findByText('TypeError: Cannot read properties of undefined');

    // The status trigger should show "Open" instead of "All statuses"
    expect(screen.getByText('Open')).toBeInTheDocument();
  });
});
