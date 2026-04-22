import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderWithProviders, screen, waitFor, userEvent } from '@/test/test-utils';
import SettingsPage from './page';

const {
  settingsGetMock,
  settingsUpdateMock,
  useAuthMock,
} = vi.hoisted(() => ({
  settingsGetMock: vi.fn().mockResolvedValue({
    data: {
      name: 'Test Org',
      settings: {},
    },
  }),
  settingsUpdateMock: vi.fn().mockResolvedValue({
    data: {
      name: 'Updated Org',
      settings: {},
    },
  }),
  useAuthMock: vi.fn(() => ({
    user: { role: 'admin' },
  })),
}));

vi.mock('@/lib/api', () => ({
  api: {
    settings: {
      get: settingsGetMock,
      update: settingsUpdateMock,
    },
  },
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: useAuthMock,
}));

describe('SettingsPage', () => {
  beforeEach(() => {
    settingsGetMock.mockClear();
    settingsUpdateMock.mockClear();
    useAuthMock.mockReset();
    useAuthMock.mockReturnValue({
      user: { role: 'admin' },
    });
    settingsGetMock.mockResolvedValue({
      data: {
        name: 'Test Org',
        settings: {},
      },
    });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('renders the Organization section with organization name', async () => {
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Organization')).toBeInTheDocument();
    });

    expect(screen.getByLabelText('Organization name')).toBeInTheDocument();
  });

  it('displays the organization name from server', async () => {
    settingsGetMock.mockResolvedValue({
      data: {
        name: 'My Org',
        settings: {},
      },
    });

    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByLabelText('Organization name')).toHaveValue('My Org');
    });
  });

  it('lets admins edit the organization name', async () => {
    renderWithProviders(<SettingsPage />);

    const input = await screen.findByLabelText('Organization name');
    expect(input).not.toBeDisabled();

    const user = userEvent.setup();
    await user.click(input);
    await user.keyboard('{Control>}a{/Control}Updated Org');

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({ name: 'Updated Org' });
    });
  });

  it('keeps the organization name field disabled for non-admins', async () => {
    useAuthMock.mockReturnValue({
      user: { role: 'member' },
    });

    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByLabelText('Organization name')).toBeDisabled();
    });
  });
});
