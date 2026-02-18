import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import userEvent from '@testing-library/user-event';
import { renderWithProviders, screen } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { mockRuns } from '@/test/mocks/handlers';
import { RunDetailContent } from './run-detail-content';

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

describe('RunDetailPage', () => {
  it('shows loading state initially', () => {
    renderWithProviders(<RunDetailContent id="run-abcdef12-3456-7890" />);
    expect(screen.getByText('Loading run...')).toBeInTheDocument();
  });

  it('renders run summary as page title', async () => {
    renderWithProviders(<RunDetailContent id="run-abcdef12-3456-7890" />);
    const heading = await screen.findByRole('heading', { name: 'Fixed TypeError by adding null check' });
    expect(heading).toBeInTheDocument();
  });

  it('shows back to runs link', async () => {
    renderWithProviders(<RunDetailContent id="run-abcdef12-3456-7890" />);
    await screen.findByRole('heading', { name: 'Fixed TypeError by adding null check' });
    expect(screen.getByText('Back to runs')).toBeInTheDocument();
  });

  it('renders all tab triggers', async () => {
    renderWithProviders(<RunDetailContent id="run-abcdef12-3456-7890" />);
    await screen.findByRole('heading', { name: 'Fixed TypeError by adding null check' });

    expect(screen.getByRole('tab', { name: 'Overview' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Logs' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Diff' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Validation' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'PR' })).toBeInTheDocument();
  });

  it('shows agent type in overview', async () => {
    renderWithProviders(<RunDetailContent id="run-abcdef12-3456-7890" />);
    await screen.findByRole('heading', { name: 'Fixed TypeError by adding null check' });
    expect(screen.getByText('Claude Code')).toBeInTheDocument();
  });

  it('shows confidence score in overview', async () => {
    renderWithProviders(<RunDetailContent id="run-abcdef12-3456-7890" />);
    await screen.findByRole('heading', { name: 'Fixed TypeError by adding null check' });
    expect(screen.getByText('92%')).toBeInTheDocument();
  });

  it('shows error state when run not found', async () => {
    server.use(
      http.get('/api/v1/runs/:id', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'Run not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<RunDetailContent id="nonexistent" />);
    expect(
      await screen.findByText('Failed to load run details.'),
    ).toBeInTheDocument();
  });

  it('shows failure details for failed runs', async () => {
    server.use(
      http.get('/api/v1/runs/:id', () => {
        return HttpResponse.json({ data: mockRuns[1] });
      }),
    );

    renderWithProviders(<RunDetailContent id="run-98765432-abcd-ef01" />);
    expect(
      await screen.findByText('Could not reproduce the error in test environment'),
    ).toBeInTheDocument();
    expect(screen.getByText('Failure Details')).toBeInTheDocument();
  });

  it('shows fallback run ID when result_summary is absent', async () => {
    server.use(
      http.get('/api/v1/runs/:id', () => {
        return HttpResponse.json({ data: mockRuns[1] });
      }),
    );

    renderWithProviders(<RunDetailContent id="run-98765432-abcd-ef01" />);
    expect(await screen.findByRole('heading', { name: 'Run run-9876' })).toBeInTheDocument();
  });

  it('renders validation details when validation tab is opened', async () => {
    const user = userEvent.setup();
    renderWithProviders(<RunDetailContent id="run-abcdef12-3456-7890" />);

    await screen.findByRole('heading', { name: 'Fixed TypeError by adding null check' });
    await user.click(screen.getByRole('tab', { name: 'Validation' }));

    expect(await screen.findByText('Overall:')).toBeInTheDocument();
    expect(screen.getByText('Direction Check')).toBeInTheDocument();
    expect(screen.getByText('Regression Test Check')).toBeInTheDocument();
  });

  it('renders PR details when PR tab is opened', async () => {
    const user = userEvent.setup();
    renderWithProviders(<RunDetailContent id="run-abcdef12-3456-7890" />);

    await screen.findByRole('heading', { name: 'Fixed TypeError by adding null check' });
    await user.click(screen.getByRole('tab', { name: 'PR' }));

    expect(await screen.findByText('Fix TypeError by adding null check')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'View on GitHub' })).toBeInTheDocument();
  });

  it('shows no diff message when run has no diff', async () => {
    const user = userEvent.setup();
    renderWithProviders(<RunDetailContent id="run-abcdef12-3456-7890" />);

    await screen.findByRole('heading', { name: 'Fixed TypeError by adding null check' });
    await user.click(screen.getByRole('tab', { name: 'Diff' }));

    expect(await screen.findByText('No diff available for this run.')).toBeInTheDocument();
  });
});
