import { describe, it, expect, vi, beforeEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, waitFor } from '@/test/test-utils';
import userEvent from '@testing-library/user-event';
import { server } from '@/test/mocks/server';
import { SessionSidebar } from './session-sidebar';
import type { SessionListItem } from '@/lib/types';

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

let mockPathname = '/sessions';
let mockParams: Record<string, string> = {};
const mockAuthState: {
  isAuthenticated: boolean;
  user: { id: string } | null;
  isLoading: boolean;
  logout: ReturnType<typeof vi.fn>;
} = {
  isAuthenticated: true,
  user: { id: 'user-1' },
  isLoading: false,
  logout: vi.fn(),
};

vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), back: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => mockPathname,
  useParams: () => mockParams,
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: () => mockAuthState,
}));

const mockOptimisticSessions: { id: string; title: string; status: 'pending'; created_at: string; resolvedId?: string }[] = [];

vi.mock('@/contexts/optimistic-sessions', () => ({
  useOptimisticSessions: () => ({
    optimisticSessions: mockOptimisticSessions,
    addOptimisticSession: vi.fn(),
    removeOptimisticSession: vi.fn(),
    markOptimisticResolved: vi.fn(),
  }),
  OptimisticSessionsProvider: ({ children }: { children: React.ReactNode }) => children,
}));

// Helper to create a session with overrides
function makeSession(overrides: Partial<SessionListItem> = {}): SessionListItem {
  return {
    id: 'sess-1',
    primary_issue_id: 'issue-1',
    org_id: 'org-1',
    agent_type: 'claude_code',
    status: 'completed',
    autonomy_level: 'full',
    token_mode: 'standard',
    current_turn: 0,
    sandbox_state: 'none',
    pr_creation_state: 'idle',
    created_at: '2026-02-17T07:00:00Z',
    started_at: '2026-02-17T07:00:00Z',
    completed_at: '2026-02-17T07:05:00Z',
    last_activity_at: '2026-02-17T07:05:00Z',
    result_summary: 'Test session',
    ...overrides,
  };
}

function serveSessions(sessions: SessionListItem[]) {
  server.use(
    http.get('/api/v1/sessions', () => {
      return HttpResponse.json({ data: sessions, meta: {} });
    }),
  );
}

describe('SessionSidebar', () => {
  beforeEach(() => {
    mockPathname = '/sessions';
    mockParams = {};
    mockOptimisticSessions.length = 0;
    mockAuthState.isAuthenticated = true;
    mockAuthState.user = { id: 'user-1' };
    mockAuthState.isLoading = false;
    mockAuthState.logout = vi.fn();
  });

  it('defaults the owner scope to Mine', async () => {
    let capturedUserId: string | null = null;
    server.use(
      http.get('/api/v1/sessions', ({ request }) => {
        capturedUserId = new URL(request.url).searchParams.get('triggered_by_user_id');
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    renderWithProviders(<SessionSidebar />);

    await screen.findByRole('radio', { name: 'Mine' });
    expect(capturedUserId).toBe('user-1');
  });

  it('does not fetch sessions until the Mine scope can resolve the current user', async () => {
    mockAuthState.isAuthenticated = false;
    mockAuthState.user = null;
    mockAuthState.isLoading = true;

    let requestCount = 0;
    server.use(
      http.get('/api/v1/sessions', () => {
        requestCount += 1;
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get('/api/v1/sessions/counts', () => {
        requestCount += 1;
        return HttpResponse.json({ data: { all: 0, active: 0, archived: 0, cap: 0 } });
      }),
    );

    renderWithProviders(<SessionSidebar />);

    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(requestCount).toBe(0);
    expect(screen.getByText('Loading...')).toBeInTheDocument();
  });

  // -----------------------------------------------------------------------
  // Search filtering
  // -----------------------------------------------------------------------

  it('filters sessions by search input', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Alpha fix' }),
      makeSession({ id: 's2', result_summary: 'Beta update' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Alpha fix');

    const input = screen.getByPlaceholderText('Search sessions...');
    await userEvent.type(input, 'Beta');

    await waitFor(() => {
      expect(screen.queryByText('Alpha fix')).not.toBeInTheDocument();
    });
    expect(screen.getByText('Beta update')).toBeInTheDocument();
  });

  it('clears search on Escape key', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Alpha fix' }),
      makeSession({ id: 's2', result_summary: 'Beta update' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Alpha fix');

    const input = screen.getByPlaceholderText('Search sessions...');
    await userEvent.type(input, 'Beta');
    await waitFor(() => {
      expect(screen.queryByText('Alpha fix')).not.toBeInTheDocument();
    });

    await userEvent.keyboard('{Escape}');
    await waitFor(() => {
      expect(screen.getByText('Alpha fix')).toBeInTheDocument();
    });
  });

  // -----------------------------------------------------------------------
  // "No sessions match this filter" vs "No sessions yet"
  // -----------------------------------------------------------------------

  it('shows "No sessions match this filter" when search yields no results', async () => {
    serveSessions([makeSession({ id: 's1', result_summary: 'Only session' })]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Only session');

    const input = screen.getByPlaceholderText('Search sessions...');
    await userEvent.type(input, 'zzz-nonexistent');

    await waitFor(() => {
      expect(screen.getByText('No sessions match this filter.')).toBeInTheDocument();
    });
  });

  it('shows a GitHub setup notice when no repository integration is connected', async () => {
    serveSessions([]);
    server.use(
      http.get('/api/v1/integrations', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get('/api/v1/repositories', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    renderWithProviders(<SessionSidebar />);

    expect(await screen.findByText('GitHub setup required')).toBeInTheDocument();
    expect(
      screen.getByText(/connect github before creating sessions or projects/i),
    ).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open integrations' })).toHaveAttribute(
      'href',
      '/settings/integrations',
    );
  });

  // -----------------------------------------------------------------------
  // Optimistic session rows
  // -----------------------------------------------------------------------

  it('renders optimistic sessions when on "all" filter', async () => {
    serveSessions([]);
    mockOptimisticSessions.push({
      id: 'opt-1',
      title: 'Creating sandbox...',
      status: 'pending',
      created_at: new Date().toISOString(),
    });

    renderWithProviders(<SessionSidebar />);

    await waitFor(() => {
      expect(screen.getByText('Creating sandbox...')).toBeInTheDocument();
    });
    expect(screen.getByText('Pending')).toBeInTheDocument();
  });

  it('hides a resolved optimistic row once its real session appears in the list', async () => {
    // Simulate the create flow: the optimistic has already been marked resolved
    // to real session id "s-real". The real row is served by the API.
    serveSessions([makeSession({ id: 's-real', result_summary: 'Real session' })]);
    mockOptimisticSessions.push({
      id: 'opt-1',
      title: 'Optimistic placeholder',
      status: 'pending',
      created_at: new Date().toISOString(),
      resolvedId: 's-real',
    });

    renderWithProviders(<SessionSidebar />);

    await screen.findByText('Real session');
    expect(screen.queryByText('Optimistic placeholder')).not.toBeInTheDocument();
  });

  it('keeps a resolved optimistic row visible when the real session is not yet in the list', async () => {
    serveSessions([]);
    mockOptimisticSessions.push({
      id: 'opt-1',
      title: 'Still waiting',
      status: 'pending',
      created_at: new Date().toISOString(),
      resolvedId: 's-not-yet-fetched',
    });

    renderWithProviders(<SessionSidebar />);

    await waitFor(() => {
      expect(screen.getByText('Still waiting')).toBeInTheDocument();
    });
  });

  // -----------------------------------------------------------------------
  // PR status badge variants
  // -----------------------------------------------------------------------

  it('shows PR badge with "Merged" status', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'pr_created',
        result_summary: 'PR session',
        pr_summary: { status: 'merged', ci_status: '', number: 1, url: '#' },
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('PR session');
    expect(screen.getByText('PR')).toBeInTheDocument();
    expect(screen.getByTitle('Merged')).toBeInTheDocument();
  });

  it('shows PR badge with "Closed" status even when CI previously passed', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'pr_created',
        result_summary: 'Closed PR session',
        pr_summary: { status: 'closed', ci_status: 'success', number: 5, url: '#' },
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Closed PR session');
    expect(screen.getByTitle('Closed')).toBeInTheDocument();
    expect(screen.queryByTitle('CI passed')).not.toBeInTheDocument();
  });

  it('shows PR badge with "CI passed" status', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'pr_created',
        result_summary: 'CI pass session',
        pr_summary: { status: 'open', ci_status: 'success', number: 2, url: '#' },
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('CI pass session');
    expect(screen.getByTitle('CI passed')).toBeInTheDocument();
  });

  it('shows PR badge with "CI failed" status', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'pr_created',
        result_summary: 'CI fail session',
        pr_summary: { status: 'open', ci_status: 'failure', number: 3, url: '#' },
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('CI fail session');
    expect(screen.getByTitle('CI failed')).toBeInTheDocument();
  });

  it('shows PR badge with "CI pending" status', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'pr_created',
        result_summary: 'CI pending session',
        pr_summary: { status: 'open', ci_status: 'pending', number: 4, url: '#' },
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('CI pending session');
    expect(screen.getByTitle('CI pending')).toBeInTheDocument();
  });

  // -----------------------------------------------------------------------
  // Diff stats badge
  // -----------------------------------------------------------------------

  it('shows diff stats badge when session has changes', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        result_summary: 'Diff session',
        diff_stats: { added: 10, removed: 3, files_changed: 2 },
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Diff session');
    // DiffStatsBadge renders +10 / -3 style content
    expect(screen.getByText('+10')).toBeInTheDocument();
  });

  it('does not show diff stats badge when added and removed are zero', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        result_summary: 'No diff session',
        diff_stats: { added: 0, removed: 0, files_changed: 0 },
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('No diff session');
    expect(screen.queryByText('+0')).not.toBeInTheDocument();
  });

  // -----------------------------------------------------------------------
  // Unread / working indicator
  // -----------------------------------------------------------------------

  it('shows animated dot for running sessions', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'running',
        result_summary: 'Running session',
        started_at: '2026-02-17T07:00:00Z',
        completed_at: undefined,
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Running session');
    expect(screen.getByText('Running')).toBeInTheDocument();
  });

  it('marks unread sessions when last_activity_at is after last_viewed_at', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'completed',
        result_summary: 'Unread session',
        last_activity_at: '2026-02-17T09:00:00Z',
        last_viewed_at: '2026-02-17T08:00:00Z',
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Unread session');
    // The unread session title gets "text-foreground" class
    const titleEl = screen.getByText('Unread session');
    expect(titleEl.className).toContain('text-foreground');
  });

  it('marks sessions with activity but no last_viewed_at as unread', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'completed',
        result_summary: 'Never viewed session',
        last_activity_at: '2026-02-17T09:00:00Z',
        // last_viewed_at is undefined -> unread
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Never viewed session');
    const titleEl = screen.getByText('Never viewed session');
    expect(titleEl.className).toContain('text-foreground');
  });

  // -----------------------------------------------------------------------
  // Failed session error message
  // -----------------------------------------------------------------------

  it('shows failure explanation for failed sessions', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'failed',
        result_summary: 'Failed session',
        failure_explanation: 'Could not connect to service',
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Failed session');
    expect(screen.getByText('Could not connect to service')).toBeInTheDocument();
  });

  it('shows error field when failure_explanation is absent', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'failed',
        result_summary: 'Error session',
        error: 'timeout exceeded',
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Error session');
    expect(screen.getByText('timeout exceeded')).toBeInTheDocument();
  });

  // -----------------------------------------------------------------------
  // PM badge
  // -----------------------------------------------------------------------

  it('shows PM badge for PM-triggered sessions without user', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        result_summary: 'PM session',
        pm_plan_id: 'plan-123',
        triggered_by_user_id: undefined,
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('PM session');
    expect(screen.getByText('PM')).toBeInTheDocument();
  });

  it('does not show PM badge when triggered_by_user_id is set', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        result_summary: 'User PM session',
        pm_plan_id: 'plan-123',
        triggered_by_user_id: 'user-1',
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('User PM session');
    expect(screen.queryByText('PM')).not.toBeInTheDocument();
  });

  // -----------------------------------------------------------------------
  // Selected session highlighting
  // -----------------------------------------------------------------------

  it('highlights the selected session', async () => {
    mockParams = { id: 's1' };
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Selected session' }),
      makeSession({ id: 's2', result_summary: 'Other session' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Selected session');

    const selectedLink = screen.getByText('Selected session').closest('a');
    expect(selectedLink?.className).toContain('bg-background');
    expect(selectedLink?.className).toContain('shadow-sm');
  });

  // -----------------------------------------------------------------------
  // Filter preservation in detail links
  // -----------------------------------------------------------------------

  it('preserves the user/status/repo filters in session detail links', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Linked session' }),
    ]);

    renderWithProviders(<SessionSidebar />, {
      searchParams: { user: 'all', status: 'active', repo: 'repo-1' },
    });
    await screen.findByText('Linked session');

    const link = screen.getByText('Linked session').closest('a');
    expect(link).toHaveAttribute(
      'href',
      '/sessions/s1?user=all&status=active&repo=repo-1',
    );
  });

  it('preserves a member-id user filter (not just "all")', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Member-scoped session' }),
    ]);

    renderWithProviders(<SessionSidebar />, {
      searchParams: { user: 'user-2' },
    });
    await screen.findByText('Member-scoped session');

    const link = screen.getByText('Member-scoped session').closest('a');
    expect(link).toHaveAttribute('href', '/sessions/s1?user=user-2');
  });

  it('only serializes the filters that are actually set', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Status-only session' }),
    ]);

    renderWithProviders(<SessionSidebar />, {
      searchParams: { status: 'archived' },
    });
    await screen.findByText('Status-only session');

    const link = screen.getByText('Status-only session').closest('a');
    expect(link).toHaveAttribute('href', '/sessions/s1?status=archived');
  });

  it('omits the query suffix when no filters are active', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Plain session' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Plain session');

    const link = screen.getByText('Plain session').closest('a');
    expect(link).toHaveAttribute('href', '/sessions/s1');
  });

});
