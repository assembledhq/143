import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithProviders, screen, userEvent, waitFor } from '@/test/test-utils';
import AcceptInvitationPage from './page';

const pushMock = vi.hoisted(() => vi.fn());
const replaceMock = vi.hoisted(() => vi.fn());
const searchParamsMock = vi.hoisted(() => new URLSearchParams());

vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: pushMock, replace: replaceMock }),
  useSearchParams: () => searchParamsMock,
}));

describe('AcceptInvitationPage', () => {
  beforeEach(() => {
    pushMock.mockReset();
    replaceMock.mockReset();
    searchParamsMock.forEach((_, key) => {
      searchParamsMock.delete(key);
    });
    vi.restoreAllMocks();
  });

  it('shows error when no token is provided', () => {
    renderWithProviders(<AcceptInvitationPage />);
    expect(screen.getByText('No invitation token provided.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Go to Login' })).toBeInTheDocument();
  });

  it('shows loading state when token is present', () => {
    searchParamsMock.set('token', 'abc123');
    vi.spyOn(global, 'fetch').mockReturnValue(new Promise(() => {})); // never resolves

    renderWithProviders(<AcceptInvitationPage />);
    expect(screen.getByText('Verifying invitation...')).toBeInTheDocument();
  });

  it('shows login state with sign in button', async () => {
    searchParamsMock.set('token', 'invite-login');
    vi.spyOn(global, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({
        data: { action: 'login', email: 'user@co.com', org_name: 'TestCo' },
      }),
    } as Response);

    const user = userEvent.setup();
    renderWithProviders(<AcceptInvitationPage />);

    await screen.findByRole('button', { name: 'Sign in' });
    expect(screen.getByText(/user@co.com/)).toBeInTheDocument();
    expect(screen.getByText('TestCo')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith(
        '/login?invitation=invite-login&email=user%40co.com&org=TestCo'
      );
    });
  });

  it('shows error when API returns error', async () => {
    searchParamsMock.set('token', 'expired-token');
    vi.spyOn(global, 'fetch').mockResolvedValue({
      ok: false,
      json: async () => ({ error: { message: 'Invitation expired' } }),
    } as Response);

    renderWithProviders(<AcceptInvitationPage />);

    await waitFor(() => {
      expect(screen.getByText('Invitation expired')).toBeInTheDocument();
    });
  });

  it('redirects to /autopilot when no action is returned', async () => {
    searchParamsMock.set('token', 'auto-token');
    vi.spyOn(global, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({ data: { org_name: 'Acme' } }),
    } as Response);

    renderWithProviders(<AcceptInvitationPage />);

    await waitFor(() => {
      expect(replaceMock).toHaveBeenCalledWith('/autopilot');
    });
  });

  it('shows register state with login alternative', async () => {
    searchParamsMock.set('token', 'reg-no-email');
    vi.spyOn(global, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({
        data: { action: 'register', org_name: 'Acme' },
      }),
    } as Response);

    const user = userEvent.setup();
    renderWithProviders(<AcceptInvitationPage />);

    await screen.findByRole('button', { name: 'Create account' });
    expect(screen.getByRole('button', { name: 'Sign in to existing account' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Sign in to existing account' }));

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith('/login?invitation=reg-no-email&org=Acme');
    });
  });

  it('passes invited email and org to sign-up flow', async () => {
    searchParamsMock.set('token', 'invite-token-123');
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

    await screen.findByRole('button', { name: 'Create account' });
    expect(screen.getByText(/invitee@example.com/i)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Create account' }));

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith('/login?tab=signup&invitation=invite-token-123&email=invitee%40example.com&org=Acme');
    });
  });
});
