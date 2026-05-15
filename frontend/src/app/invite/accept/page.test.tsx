import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithProviders, screen, userEvent, waitFor } from '@/test/test-utils';
import { getActiveOrgId, setActiveOrgId } from '@/lib/active-org';
import AcceptInvitationPage from './page';
import { QueryClient } from '@tanstack/react-query';

const pushMock = vi.hoisted(() => vi.fn());
const replaceMock = vi.hoisted(() => vi.fn());
const searchParamsMock = vi.hoisted(() => new URLSearchParams());
const meMock = vi.hoisted(() => vi.fn());
const claimInvitationMock = vi.hoisted(() => vi.fn());
const logoutMock = vi.hoisted(() => vi.fn());

vi.mock('@/lib/api', () => ({
  api: {
    auth: {
      me: meMock,
      claimInvitation: claimInvitationMock,
      logout: logoutMock,
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
    logoutMock.mockReset();
    searchParamsMock.forEach((_, key) => {
      searchParamsMock.delete(key);
    });
    setActiveOrgId(null);
    vi.restoreAllMocks();
  });

  it('shows error when no token is provided', () => {
    renderWithProviders(<AcceptInvitationPage />);
    expect(screen.getByText('No invitation token provided.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Go to sign in' })).toBeInTheDocument();
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

  it('passes GitHub acceptance context to login for GitHub-locked notification-email invites', async () => {
    searchParamsMock.set('token', 'invite-gh');
    meMock.mockRejectedValue({ code: 'UNAUTHORIZED' });
    vi.spyOn(global, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({
        data: {
          action: 'login',
          email: 'notify@example.com',
          github_username: 'octocat',
          acceptance_method: 'github',
          org_name: 'TestCo',
        },
      }),
    } as Response);

    const user = userEvent.setup();
    renderWithProviders(<AcceptInvitationPage />);

    await screen.findByRole('button', { name: 'Sign in to join TestCo' });
    expect(screen.getAllByText(/@octocat/).length).toBeGreaterThan(0);

    await user.click(screen.getByRole('button', { name: 'Sign in to join TestCo' }));

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith(
        '/login?invitation=invite-gh&email=notify%40example.com&github_username=octocat&acceptance_method=github&org=TestCo'
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
    expect(screen.getByRole('button', { name: 'Go to sign in' })).toBeInTheDocument();
  });

  it('logs out and uses switch-account login recovery after an invite mismatch', async () => {
    searchParamsMock.set('token', 'invite-mismatch');
    meMock.mockResolvedValue({
      data: {
        id: 'user-1',
        org_id: 'org-old',
        email: 'wrong-user@co.com',
        name: 'Wrong User',
        role: 'member',
        created_at: '2026-04-23T00:00:00Z',
      },
    });
    claimInvitationMock.mockRejectedValue(
      Object.assign(new Error('This invitation belongs to a different account.'), {
        code: 'INVITE_MISMATCH',
      })
    );
    logoutMock.mockResolvedValue({});
    vi.spyOn(global, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({
        data: { action: 'login', email: 'invitee@example.com', org_name: 'Acme' },
      }),
    } as Response);

    const user = userEvent.setup();
    renderWithProviders(<AcceptInvitationPage />);

    await screen.findByText('This invitation belongs to a different account.');
    await user.click(screen.getByRole('button', { name: 'Sign in with a different account' }));

    await waitFor(() => {
      expect(logoutMock).toHaveBeenCalledTimes(1);
      expect(pushMock).toHaveBeenCalledWith(
        '/login?invitation=invite-mismatch&email=invitee%40example.com&org=Acme&switch_account=1'
      );
    });
  });

  it('keeps the current session intact for non-mismatch invite claim failures', async () => {
    searchParamsMock.set('token', 'invite-invalid');
    setActiveOrgId('org-old');
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
    claimInvitationMock.mockRejectedValue(
      Object.assign(new Error('This invitation is no longer valid.'), {
        code: 'INVITE_INVALID',
      })
    );
    vi.spyOn(global, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({
        data: { action: 'login', email: 'invitee@example.com', org_name: 'Acme' },
      }),
    } as Response);

    const user = userEvent.setup();
    renderWithProviders(<AcceptInvitationPage />);

    await screen.findByText('This invitation is no longer valid.');
    await user.click(screen.getByRole('button', { name: 'Go to sign in' }));

    await waitFor(() => {
      expect(logoutMock).not.toHaveBeenCalled();
      expect(getActiveOrgId()).toBe('org-old');
      expect(pushMock).toHaveBeenCalledWith(
        '/login?invitation=invite-invalid&email=invitee%40example.com&org=Acme'
      );
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
    const clearMock = vi.spyOn(QueryClient.prototype, 'clear');
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
      expect(clearMock).toHaveBeenCalled();
      expect(replaceMock).toHaveBeenCalledWith('/onboarding');
    });
  });
});
