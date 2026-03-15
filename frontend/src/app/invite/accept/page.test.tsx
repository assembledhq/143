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
