import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { mockSessions } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

describe('SessionDetailPage', () => {
  it('shows loading state initially', () => {
    renderWithProviders(<SessionDetailContent id="session-plan-1" />);
    expect(screen.getByText('Loading session...')).toBeInTheDocument();
  });

  it('renders plan session with title', async () => {
    renderWithProviders(<SessionDetailContent id="session-plan-1" />);
    expect(
      await screen.findByText('Analyzed 5 open issues and delegated 2 tasks.'),
    ).toBeInTheDocument();
  });

  it('shows back to sessions link', async () => {
    renderWithProviders(<SessionDetailContent id="session-plan-1" />);
    await screen.findByText('Analyzed 5 open issues and delegated 2 tasks.');
    expect(screen.getByText('Back to sessions')).toBeInTheDocument();
  });

  it('shows PM Analysis badge for plan sessions', async () => {
    renderWithProviders(<SessionDetailContent id="session-plan-1" />);
    await screen.findByText('Analyzed 5 open issues and delegated 2 tasks.');
    expect(screen.getByText('PM Analysis')).toBeInTheDocument();
  });

  it('shows situation analysis for plan sessions', async () => {
    renderWithProviders(<SessionDetailContent id="session-plan-1" />);
    await screen.findByText('Analyzed 5 open issues and delegated 2 tasks.');
    expect(screen.getByText('Situation Analysis')).toBeInTheDocument();
    expect(
      screen.getByText('Found critical auth timeout and payment bug requiring immediate attention.'),
    ).toBeInTheDocument();
  });

  it('shows tasks with run status for plan sessions', async () => {
    renderWithProviders(<SessionDetailContent id="session-plan-1" />);
    await screen.findByText('Analyzed 5 open issues and delegated 2 tasks.');
    expect(screen.getByText('#1 · Fix auth timeout')).toBeInTheDocument();
    // "Completed" appears as both the session status badge and the run status badge
    expect(screen.getAllByText('Completed').length).toBeGreaterThanOrEqual(1);
  });

  it('shows run result summary in tasks', async () => {
    renderWithProviders(<SessionDetailContent id="session-plan-1" />);
    await screen.findByText('Analyzed 5 open issues and delegated 2 tasks.');
    expect(screen.getByText('Fixed TypeError by adding null check')).toBeInTheDocument();
  });

  it('shows view run details link for delegated tasks', async () => {
    renderWithProviders(<SessionDetailContent id="session-plan-1" />);
    await screen.findByText('Analyzed 5 open issues and delegated 2 tasks.');
    expect(screen.getByText('View run details')).toBeInTheDocument();
  });

  it('renders manual session', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: mockSessions[1] });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-manual-1" />);
    await screen.findByText('Run run-9876');
    expect(screen.getByText('Manual')).toBeInTheDocument();
  });

  it('shows error state when session not found', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'Session not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="nonexistent" />);
    expect(
      await screen.findByText('Failed to load session details.'),
    ).toBeInTheDocument();
  });

  it('shows issues reviewed count for plan sessions', async () => {
    renderWithProviders(<SessionDetailContent id="session-plan-1" />);
    await screen.findByText('Analyzed 5 open issues and delegated 2 tasks.');
    expect(screen.getByText('5 issues reviewed')).toBeInTheDocument();
  });
});
