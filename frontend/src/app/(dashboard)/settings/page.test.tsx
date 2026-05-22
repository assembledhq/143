import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderWithProviders, screen, waitFor, userEvent } from '@/test/test-utils';
import SettingsPage from './page';

const {
  settingsGetMock,
  settingsUpdateMock,
  settingsNetworkStatusMock,
  auditLogsListMock,
  teamListMembersMock,
  useAuthMock,
} = vi.hoisted(() => ({
  settingsGetMock: vi.fn().mockResolvedValue({
    data: {
      id: 'org-1',
      name: 'Test Org',
      settings: {},
      created_at: '2026-05-01T12:00:00Z',
      updated_at: '2026-05-01T12:00:00Z',
    },
  }),
  settingsUpdateMock: vi.fn().mockResolvedValue({
    data: {
      id: 'org-1',
      name: 'Updated Org',
      settings: {},
      created_at: '2026-05-01T12:00:00Z',
      updated_at: '2026-05-06T15:30:00Z',
    },
  }),
  settingsNetworkStatusMock: vi.fn().mockResolvedValue({
    data: {
      static_egress_available: true,
      static_egress_enabled: false,
      static_egress_public_ip: '203.0.113.10',
    },
  }),
  auditLogsListMock: vi.fn().mockResolvedValue({ data: [] }),
  teamListMembersMock: vi.fn().mockResolvedValue({ data: [] }),
  useAuthMock: vi.fn(() => ({
    user: { role: 'admin' },
  })),
}));

vi.mock('@/lib/api', () => ({
  api: {
    settings: {
      get: settingsGetMock,
      update: settingsUpdateMock,
      getNetworkStatus: settingsNetworkStatusMock,
    },
    auditLogs: {
      list: auditLogsListMock,
    },
    team: {
      listMembers: teamListMembersMock,
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
    settingsNetworkStatusMock.mockClear();
    useAuthMock.mockReset();
    useAuthMock.mockReturnValue({
      user: { role: 'admin' },
    });
    settingsGetMock.mockResolvedValue({
      data: {
        id: 'org-1',
        name: 'Test Org',
        settings: {},
        created_at: '2026-05-01T12:00:00Z',
        updated_at: '2026-05-01T12:00:00Z',
      },
    });
    settingsNetworkStatusMock.mockResolvedValue({
      data: {
        static_egress_available: true,
        static_egress_enabled: true,
        static_egress_public_ip: '203.0.113.10',
      },
    });
    auditLogsListMock.mockClear();
    teamListMembersMock.mockClear();
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
        id: 'org-1',
        name: 'My Org',
        settings: {},
        created_at: '2026-05-01T12:00:00Z',
        updated_at: '2026-05-01T12:00:00Z',
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

  it('updates the header timestamp after a successful settings save', async () => {
    settingsGetMock.mockResolvedValue({
      data: {
        id: 'org-1',
        name: 'Test Org',
        settings: {},
        created_at: '2026-05-01T12:00:00Z',
        updated_at: '2026-05-01T12:00:00Z',
      },
    });
    settingsUpdateMock.mockResolvedValue({
      data: {
        id: 'org-1',
        name: 'Updated Org',
        settings: {},
        created_at: '2026-05-01T12:00:00Z',
        updated_at: '2026-05-06T15:30:00Z',
      },
    });

    renderWithProviders(<SettingsPage />);

    expect(await screen.findByText(/Updated at .*May 1, 2026.*12:00 PM UTC/)).toBeInTheDocument();

    const input = screen.getByLabelText('Organization name');
    const user = userEvent.setup();
    await user.click(input);
    await user.keyboard('{Control>}a{/Control}Updated Org');
    await user.tab();

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({ name: 'Updated Org' });
    });
    await waitFor(() => {
      expect(screen.getByText(/Updated at .*May 6, 2026.*3:30 PM UTC/)).toBeInTheDocument();
    });
  });

  it('shows a saved indicator only on the pull requests section after PR changes', async () => {
    renderWithProviders(<SettingsPage />);

    const user = userEvent.setup();
    await user.click((await screen.findByText('App only')).closest('label') as HTMLElement);

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({ settings: { pr_authorship: 'app_only' } });
    });
    await waitFor(() => {
      expect(screen.getAllByText('Saved')).toHaveLength(1);
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

  it('shows static egress network access with copyable public IP', async () => {
    settingsGetMock.mockResolvedValue({
      data: {
        id: 'org-1',
        name: 'Test Org',
        settings: { sandbox_network: { static_egress_enabled: true } },
        created_at: '2026-05-01T12:00:00Z',
        updated_at: '2026-05-01T12:00:00Z',
      },
    });

    renderWithProviders(<SettingsPage />);

    expect(await screen.findByText('Network access')).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByLabelText('Use static egress IP for sessions and previews')).toBeChecked();
    });
    expect(screen.getByText('203.0.113.10')).toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByLabelText('Use static egress IP for sessions and previews'));

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { sandbox_network: { static_egress_enabled: false } },
      });
    });
  });

  it('allows admins to disable static egress when the gateway is unavailable', async () => {
    settingsGetMock.mockResolvedValue({
      data: {
        id: 'org-1',
        name: 'Test Org',
        settings: { sandbox_network: { static_egress_enabled: true } },
        created_at: '2026-05-01T12:00:00Z',
        updated_at: '2026-05-01T12:00:00Z',
      },
    });
    settingsNetworkStatusMock.mockResolvedValue({
      data: {
        static_egress_available: false,
        static_egress_enabled: true,
        static_egress_public_ip: '203.0.113.10',
        static_egress_unavailable_reason: 'no active static-egress-capable workers are available',
      },
    });

    renderWithProviders(<SettingsPage />);

    const toggle = await screen.findByLabelText('Use static egress IP for sessions and previews');
    await waitFor(() => {
      expect(toggle).toBeChecked();
      expect(toggle).not.toBeDisabled();
    });

    const user = userEvent.setup();
    await user.click(toggle);

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { sandbox_network: { static_egress_enabled: false } },
      });
    });
  });
});
