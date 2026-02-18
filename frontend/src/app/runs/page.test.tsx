import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { RunsPageContent } from './runs-page-content';

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

describe('RunsPage', () => {
  it('shows loading state initially', () => {
    renderWithProviders(<RunsPageContent />);
    expect(screen.getByText('Loading runs...')).toBeInTheDocument();
  });

  it('renders runs returned from the API', async () => {
    renderWithProviders(<RunsPageContent />);

    expect(
      await screen.findByText('Fixed TypeError by adding null check'),
    ).toBeInTheDocument();
  });

  it('shows status labels for each run', async () => {
    renderWithProviders(<RunsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    // "Completed" and "Failed" appear in filter tabs, section headers, and status badges
    expect(screen.getAllByText('Completed').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Failed').length).toBeGreaterThanOrEqual(1);
  });

  it('shows agent type labels', async () => {
    renderWithProviders(<RunsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.getByText('Claude Code')).toBeInTheDocument();
    expect(screen.getByText('Codex')).toBeInTheDocument();
  });

  it('shows confidence score when present', async () => {
    renderWithProviders(<RunsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.getByText('Confidence: 92%')).toBeInTheDocument();
  });

  it('shows failure explanation for failed runs', async () => {
    renderWithProviders(<RunsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(
      screen.getByText('Could not reproduce the error in test environment'),
    ).toBeInTheDocument();
  });

  it('shows fallback run ID when result_summary is absent', async () => {
    renderWithProviders(<RunsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    // The second run has no result_summary, so it shows "Run " + id.slice(0, 8)
    expect(screen.getByText('Run run-9876')).toBeInTheDocument();
  });

  it('displays page header with title and description', async () => {
    renderWithProviders(<RunsPageContent />);

    expect(screen.getByText('Runs')).toBeInTheDocument();
    expect(
      screen.getByText('Each agent execution shows up as a run.'),
    ).toBeInTheDocument();
  });

  it('shows empty state when API returns no runs', async () => {
    server.use(
      http.get('/api/v1/runs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    renderWithProviders(<RunsPageContent />);

    expect(await screen.findByText('No runs yet')).toBeInTheDocument();
    expect(
      screen.getByText(
        'Runs are created automatically when 143 picks up an issue and starts working on a fix.',
      ),
    ).toBeInTheDocument();
  });

  it('shows error state when API request fails', async () => {
    server.use(
      http.get('/api/v1/runs', () => {
        return HttpResponse.json(
          { error: { code: 'INTERNAL', message: 'Server error' } },
          { status: 500 },
        );
      }),
    );

    renderWithProviders(<RunsPageContent />);

    expect(
      await screen.findByText(
        'Failed to load runs. Make sure the backend is running.',
      ),
    ).toBeInTheDocument();
  });

  it('shows duration for runs', async () => {
    renderWithProviders(<RunsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    // The first run: 5m 30s, the second run: 3m 0s
    expect(screen.getByText('Duration: 5m 30s')).toBeInTheDocument();
    expect(screen.getByText('Duration: 3m 0s')).toBeInTheDocument();
  });

  it('shows status filter tabs', async () => {
    renderWithProviders(<RunsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.getByText('All')).toBeInTheDocument();
    expect(screen.getByText('Active')).toBeInTheDocument();
    expect(screen.getByText('Needs Review')).toBeInTheDocument();
  });

  it('groups runs into sections by default', async () => {
    renderWithProviders(<RunsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    // The mock runs have one completed and one failed, so those sections should appear
    expect(screen.getAllByText('Failed').length).toBeGreaterThanOrEqual(2); // filter tab + section header + status badge
  });

  it('links run rows to detail pages', async () => {
    renderWithProviders(<RunsPageContent />);

    await screen.findByText('Fixed TypeError by adding null check');

    const links = screen.getAllByRole('link');
    const runLinks = links.filter((l) => l.getAttribute('href')?.startsWith('/runs/'));
    expect(runLinks.length).toBeGreaterThan(0);
  });
});
