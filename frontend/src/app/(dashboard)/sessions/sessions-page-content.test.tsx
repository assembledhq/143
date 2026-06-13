import { describe, it, expect, vi, beforeEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { fireEvent, renderWithProviders, screen } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { SessionsPageContent } from './sessions-page-content';
import type { SessionListItem } from '@/lib/types';

const mockAuthState: {
  isAuthenticated: boolean;
  user: { id: string; role: string } | null;
  isLoading: boolean;
  logout: ReturnType<typeof vi.fn>;
} = {
  isAuthenticated: true,
  user: { id: 'user-1', role: 'member' },
  isLoading: false,
  logout: vi.fn(),
};

vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), back: vi.fn(), prefetch: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => '/sessions',
  useParams: () => ({}),
}));

const preloadSessionDetailContent = vi.hoisted(() => vi.fn());

vi.mock('./[id]/session-detail-page-client', () => ({
  preloadSessionDetailContent,
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: () => mockAuthState,
}));

function makeSession(overrides: Partial<SessionListItem> = {}): SessionListItem {
  return {
    id: 'sess-1',
    org_id: 'org-1',
    agent_type: 'claude_code',
    status: 'completed',
    autonomy_level: 'full',
    token_mode: 'standard',
    current_turn: 1,
    sandbox_state: 'snapshotted',
    pr_creation_state: 'idle',
    pr_push_state: 'idle',
    created_at: '2026-02-17T07:00:00Z',
    started_at: '2026-02-17T07:00:00Z',
    completed_at: '2026-02-17T07:05:00Z',
    last_activity_at: '2026-02-17T07:05:00Z',
    result_summary: 'Test session',
    ...overrides,
  };
}

describe('SessionsPageContent', () => {
  beforeEach(() => {
    mockAuthState.isAuthenticated = true;
    mockAuthState.user = { id: 'user-1', role: 'member' };
    mockAuthState.isLoading = false;
    mockAuthState.logout = vi.fn();
  });

  it('shows loading while Mine is waiting on auth resolution', async () => {
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

    renderWithProviders(<SessionsPageContent />);

    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(requestCount).toBe(0);
    expect(screen.getByText('Loading sessions...')).toBeInTheDocument();
  });

  it('does not request the team roster for builders', async () => {
    mockAuthState.user = { id: 'user-1', role: 'builder' };
    let teamRequestCount = 0;

    server.use(
      http.get('/api/v1/team/members', () => {
        teamRequestCount += 1;
        return HttpResponse.json({ error: { code: 'FORBIDDEN', message: 'insufficient permissions' } }, { status: 403 });
      }),
    );

    renderWithProviders(<SessionsPageContent />);

    expect(await screen.findByText(/Fixed TypeError by adding null check/)).toBeInTheDocument();
    expect(teamRequestCount).toBe(0);
  });

  it('shows in-flight PR creation and push statuses in the sessions table', async () => {
    server.use(
      http.get('/api/v1/sessions', () => {
        return HttpResponse.json({
          data: [
            makeSession({ id: 's1', result_summary: 'Opening a pull request', pr_creation_state: 'queued' }),
            makeSession({ id: 's2', result_summary: 'Updating an existing pull request', pr_push_state: 'pushing' }),
          ],
          meta: {},
        });
      }),
      http.get('/api/v1/sessions/counts', () => {
        return HttpResponse.json({ data: { all: 2, active: 0, archived: 0, cap: 100 } });
      }),
    );

    renderWithProviders(<SessionsPageContent />);

    expect(await screen.findByText('Opening a pull request')).toBeInTheDocument();
    expect(screen.getByText('Creating PR')).toBeInTheDocument();
    expect(screen.getByText('Pushing changes')).toBeInTheDocument();
  });

  it('warms the session detail chunk when hovering a session row', async () => {
    preloadSessionDetailContent.mockClear();

    renderWithProviders(<SessionsPageContent />);

    const row = (await screen.findByText(/Fixed TypeError by adding null check/)).closest('tr');
    expect(row).not.toBeNull();
    fireEvent.mouseEnter(row!);

    expect(preloadSessionDetailContent).toHaveBeenCalled();
  });
});
