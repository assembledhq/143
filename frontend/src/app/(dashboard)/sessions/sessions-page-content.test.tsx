import { describe, it, expect, vi, beforeEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { SessionsPageContent } from './sessions-page-content';

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
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), back: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => '/sessions',
  useParams: () => ({}),
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: () => mockAuthState,
}));

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
});
