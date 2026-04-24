import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithProviders, screen, userEvent, waitFor } from '@/test/test-utils';
import { getActiveOrgId, setActiveOrgId } from '@/lib/active-org';
import AcceptInvitationPage from './page';

const pushMock = vi.hoisted(() => vi.fn());
const replaceMock = vi.hoisted(() => vi.fn());
const searchParamsMock = vi.hoisted(() => new URLSearchParams());
const meMock = vi.hoisted(() => vi.fn());
const claimInvitationMock = vi.hoisted(() => vi.fn());

vi.mock('@/lib/api', () => ({
  api: {
    auth: {
      me: meMock,
      claimInvitation: claimInvitationMock,
    },
  },
}));

vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: pushMock, replace: replaceMock }),
  useSearchParams: () => searchParamsMock,
}));

describe('AcceptInvitationPage', () => {
  beforeEach(() => {
    pushMock.mockReset();
    replaceMock.mockReset();
    meMock.mockReset();
    claimInvitationMock.mockReset();
    searchParamsMock.forEach((_, key) => {
      searchParamsMock.delete(key);
    });
    setActiveOrgId(null);
    vi.restoreAllMocks();
  });

  it('shows error when no token is provided', () => {
    renderWithProviders(<AcceptInvitationPage />);
    expect(screen.getByText('No invitation token provided.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Sign in with a different account' })).toBeInTheDocument();
  });

  it('shows loading state when token is present', () => {
    searchParamsMock.set('token', 'abc123');
    meMock.mockRejectedValue({ code: 'UNAUTHORIZED' });
    vi.spyOn(global, 'fetch').mockReturnValue(new Promise(() => {})); // never resolves

    renderWithProviders(<AcceptInvitationPage />);
    expect(screen.getByText('Verifying invitation...')).toBeInTheDocument();
  });

  it('shows login state with sign in button', async () => {
    searchParamsMock.set('token', 'invite-login');
    meMock.mockRejectedValue({ code: 'UNAUTHORIZED' });
    vi.spyOn(global, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({
        data: { action: 'login', email: 'user@co.com', org_name: 'TestCo' },
      }),
    } as Response);

    const user = userEvent.setup();
    renderWithProviders(<AcceptInvitationPage />);

    await screen.findByRole('button', { name: 'Sign in to join TestCo' });
    expect(screen.getByText('Join TestCo')).toBeInTheDocument();
    expect(screen.getAllByText(/user@co.com/)).toHaveLength(2);

    await user.click(screen.getByRole('button', { name: 'Sign in to join TestCo' }));

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith(
        '/login?invitation=invite-login&email=user%40co.com&org=TestCo'
      );
    });
  });

  it('shows error when API returns error', async () => {
    searchParamsMock.set('token', 'expired-token');
    meMock.mockRejectedValue({ code: 'UNAUTHORIZED' });
    vi.spyOn(global, 'fetch').mockResolvedValue({
      ok: false,
      json: async () => ({ error: { message: 'Invitation expired' } }),
    } as Response);

    renderWithProviders(<AcceptInvitationPage />);

    await waitFor(() => {
      expect(screen.getByText('Invitation expired')).toBeInTheDocument();
    });
  });

  it('redirects to /onboarding when no action is returned', async () => {
    searchParamsMock.set('token', 'auto-token');
    meMock.mockRejectedValue({ code: 'UNAUTHORIZED' });
    vi.spyOn(global, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({ data: { org_name: 'Acme' } }),
    } as Response);

    renderWithProviders(<AcceptInvitationPage />);

    await waitFor(() => {
      expect(replaceMock).toHaveBeenCalledWith('/onboarding');
    });
  });

  it('shows register state with login alternative', async () => {
    searchParamsMock.set('token', 'reg-no-email');
    meMock.mockRejectedValue({ code: 'UNAUTHORIZED' });
    vi.spyOn(global, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({
        data: { action: 'register', org_name: 'Acme' },
      }),
    } as Response);

    const user = userEvent.setup();
    renderWithProviders(<AcceptInvitationPage />);

    await screen.findByRole('button', { name: 'Create account to join Acme' });
    expect(screen.getByRole('button', { name: 'I already have an account' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'I already have an account' }));

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith('/login?invitation=reg-no-email&org=Acme');
    });
  });

  it('passes invited email and org to sign-up flow', async () => {
    searchParamsMock.set('token', 'invite-token-123');
    meMock.mockRejectedValue({ code: 'UNAUTHORIZED' });
    vi.spyOn(global, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({
        data: {
          action: 'register',
          email: 'invitee@example.com',
          org_name: 'Acme',
        },
      }),
    } as Response);

    const user = userEvent.setup();
    renderWithProviders(<AcceptInvitationPage />);

    await screen.findByRole('button', { name: 'Create account to join Acme' });
    expect(screen.getByText('Join Acme')).toBeInTheDocument();
    expect(screen.getAllByText(/invitee@example.com/i)).toHaveLength(2);

    await user.click(screen.getByRole('button', { name: 'Create account to join Acme' }));

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith('/login?tab=signup&invitation=invite-token-123&email=invitee%40example.com&org=Acme');
    });
  });

  it('claims the invitation immediately when the user is already authenticated', async () => {
    searchParamsMock.set('token', 'invite-authenticated');
    meMock.mockResolvedValue({
      data: {
        id: 'user-1',
        org_id: 'org-old',
        email: 'user@co.com',
        name: 'User',
        role: 'member',
        created_at: '2026-04-23T00:00:00Z',
      },
    });
    claimInvitationMock.mockResolvedValue({
      data: { org_id: 'org-new', role: 'member' },
    });
    vi.spyOn(global, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({
        data: { action: 'login', email: 'user@co.com', org_name: 'TestCo' },
      }),
    } as Response);

    renderWithProviders(<AcceptInvitationPage />);

    await waitFor(() => {
      expect(claimInvitationMock).toHaveBeenCalledWith('invite-authenticated');
    });
    await waitFor(() => {
      expect(getActiveOrgId()).toBe('org-new');
      expect(replaceMock).toHaveBeenCalledWith('/onboarding');
    });
  });
});
