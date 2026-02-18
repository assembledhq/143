import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { mockRuns } from '@/test/mocks/handlers';
import RunsPage from './page';

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

describe('RunsPage', () => {
  it('shows loading state initially', () => {
    renderWithProviders(<RunsPage />);
    expect(screen.getByText('Loading runs...')).toBeInTheDocument();
  });

  it('renders runs returned from the API', async () => {
    renderWithProviders(<RunsPage />);

    expect(
      await screen.findByText('Fixed TypeError by adding null check'),
    ).toBeInTheDocument();
  });

  it('shows status labels for each run', async () => {
    renderWithProviders(<RunsPage />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.getByText('Completed')).toBeInTheDocument();
    expect(screen.getByText('Failed')).toBeInTheDocument();
  });

  it('shows agent type labels', async () => {
    renderWithProviders(<RunsPage />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.getByText('Claude Code')).toBeInTheDocument();
    expect(screen.getByText('Codex')).toBeInTheDocument();
  });

  it('shows confidence score when present', async () => {
    renderWithProviders(<RunsPage />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.getByText('Confidence: 92%')).toBeInTheDocument();
  });

  it('shows failure explanation for failed runs', async () => {
    renderWithProviders(<RunsPage />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(
      screen.getByText('Could not reproduce the error in test environment'),
    ).toBeInTheDocument();
  });

  it('shows fallback run ID when result_summary is absent', async () => {
    renderWithProviders(<RunsPage />);

    await screen.findByText('Fixed TypeError by adding null check');

    // The second run has no result_summary, so it shows "Run " + id.slice(0, 8)
    expect(screen.getByText('Run run-9876')).toBeInTheDocument();
  });

  it('shows the run count header', async () => {
    renderWithProviders(<RunsPage />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.getByText('2 runs')).toBeInTheDocument();
  });

  it('displays page header with title and description', async () => {
    renderWithProviders(<RunsPage />);

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

    renderWithProviders(<RunsPage />);

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

    renderWithProviders(<RunsPage />);

    expect(
      await screen.findByText(
        'Failed to load runs. Make sure the backend is running.',
      ),
    ).toBeInTheDocument();
  });

  it('renders singular run count for a single run', async () => {
    server.use(
      http.get('/api/v1/runs', () => {
        return HttpResponse.json({
          data: [mockRuns[0]],
          meta: {},
        });
      }),
    );

    renderWithProviders(<RunsPage />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.getByText('1 run')).toBeInTheDocument();
  });

  it('does not show loading state once data is loaded', async () => {
    renderWithProviders(<RunsPage />);

    await screen.findByText('Fixed TypeError by adding null check');

    expect(screen.queryByText('Loading runs...')).not.toBeInTheDocument();
  });

  it('shows duration for runs', async () => {
    renderWithProviders(<RunsPage />);

    await screen.findByText('Fixed TypeError by adding null check');

    // The first run: 5m 30s, the second run: 3m 0s
    expect(screen.getByText('Duration: 5m 30s')).toBeInTheDocument();
    expect(screen.getByText('Duration: 3m 0s')).toBeInTheDocument();
  });
});
