import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderWithProviders, screen, waitFor, userEvent } from '@/test/test-utils';
import SettingsPage from './page';

const {
  settingsGetMock,
  settingsUpdateMock,
  domainsListMock,
  domainsCreateMock,
  domainsVerifyMock,
  domainsDeleteMock,
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
  domainsListMock: vi.fn().mockResolvedValue({ data: [] }),
  domainsCreateMock: vi.fn().mockResolvedValue({
    data: {
      id: 'domain-1',
      org_id: 'org-1',
      domain: 'example.com',
      status: 'pending',
      verification_token: 'token',
      auto_join_enabled: true,
      auto_join_role: 'member',
      created_by: 'user-1',
      created_at: '2026-05-01T12:00:00Z',
      updated_at: '2026-05-01T12:00:00Z',
      verification_host: '_143-domain-verification.example.com',
      verification_record: '143-domain-verification=token',
    },
  }),
  domainsVerifyMock: vi.fn().mockResolvedValue({
    data: {
      id: 'domain-1',
      org_id: 'org-1',
      domain: 'example.com',
      status: 'verified',
      verification_token: 'token',
      auto_join_enabled: true,
      auto_join_role: 'member',
      created_by: 'user-1',
      created_at: '2026-05-01T12:00:00Z',
      updated_at: '2026-05-01T12:00:00Z',
      verification_host: '_143-domain-verification.example.com',
      verification_record: '143-domain-verification=token',
    },
  }),
  domainsDeleteMock: vi.fn().mockResolvedValue(undefined),
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
      domains: {
        list: domainsListMock,
        create: domainsCreateMock,
        verify: domainsVerifyMock,
        delete: domainsDeleteMock,
      },
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

vi.mock('@/lib/notify', () => ({
  notify: {
    success: vi.fn(),
    error: vi.fn(),
    info: vi.fn(),
  },
}));

describe('SettingsPage', () => {
  beforeEach(() => {
    settingsGetMock.mockClear();
    settingsUpdateMock.mockClear();
    domainsListMock.mockClear();
    domainsCreateMock.mockClear();
    domainsVerifyMock.mockClear();
    domainsDeleteMock.mockClear();
    domainsListMock.mockResolvedValue({ data: [] });
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

  it('shows and saves the active previews per user setting', async () => {
    settingsGetMock.mockResolvedValue({
      data: {
        id: 'org-1',
        name: 'Test Org',
        settings: { preview_max_previews_per_user: 7 },
        created_at: '2026-05-01T12:00:00Z',
        updated_at: '2026-05-01T12:00:00Z',
      },
    });

    renderWithProviders(<SettingsPage />);

    const input = await screen.findByLabelText('Active previews per user');
    await waitFor(() => {
      expect(input).toHaveValue(7);
    });

    const user = userEvent.setup();
    await user.click(input);
    await user.keyboard('{Control>}a{/Control}4');
    await user.tab();

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { preview_max_previews_per_user: 4 },
      });
    });
  });

  it('defaults the active previews per user setting to four', async () => {
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByLabelText('Active previews per user')).toHaveValue(4);
    });
  });

  it('lets admins create and verify a domain auto-join challenge', async () => {
    domainsListMock.mockResolvedValue({
      data: [
        {
          id: 'domain-1',
          org_id: 'org-1',
          domain: 'example.com',
          status: 'pending',
          verification_token: 'token',
          auto_join_enabled: true,
          auto_join_role: 'member',
          created_by: 'user-1',
          created_at: '2026-05-01T12:00:00Z',
          updated_at: '2026-05-01T12:00:00Z',
          verification_host: '_143-domain-verification.example.com',
          verification_record: '143-domain-verification=token',
        },
      ],
    });

    renderWithProviders(<SettingsPage />);

    expect(await screen.findByText('Domain access')).toBeInTheDocument();
    expect(await screen.findByText('_143-domain-verification.example.com')).toBeInTheDocument();
    expect(await screen.findByText('143-domain-verification=token')).toBeInTheDocument();

    const user = userEvent.setup();
    await user.type(screen.getByLabelText('Domain'), 'newco.com');
    await user.click(screen.getByRole('button', { name: /Add domain/i }));

    await waitFor(() => {
      expect(domainsCreateMock).toHaveBeenCalledWith({
        domain: 'newco.com',
        auto_join_role: 'member',
      });
    });

    await user.click(screen.getByRole('button', { name: /Verify/i }));
    await waitFor(() => {
      expect(domainsVerifyMock).toHaveBeenCalledWith('domain-1');
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
});
