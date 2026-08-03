import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderWithProviders, screen, waitFor, userEvent } from '@/test/test-utils';
import SettingsPage from './page';

const {
  settingsGetMock,
  settingsUpdateMock,
  settingsNetworkStatusMock,
  repositoriesListMock,
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
  repositoriesListMock: vi.fn().mockResolvedValue({
    data: [{ id: 'repo-1', full_name: 'acme/app' }],
    meta: {},
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
    repositories: {
      list: repositoriesListMock,
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
    repositoriesListMock.mockClear();
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
    repositoriesListMock.mockResolvedValue({
      data: [{ id: 'repo-1', full_name: 'acme/app' }],
      meta: {},
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
      expect(screen.getByRole('heading', { name: 'Organization', level: 1 })).toBeInTheDocument();
    });

    expect(screen.getByLabelText('Organization name')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'General settings', level: 1 })).not.toBeInTheDocument();
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

  it('uses a low-priority activity footer as the only updated timestamp', async () => {
    settingsGetMock.mockResolvedValue({
      data: {
        id: 'org-1',
        name: 'Test Org',
        settings: {},
        created_at: '2026-05-01T12:00:00Z',
        updated_at: '2026-05-01T12:00:00Z',
      },
    });
    auditLogsListMock.mockResolvedValue({
      data: [{
        id: 1,
        org_id: 'org-1',
        actor_type: 'system',
        actor_id: 'system',
        action: 'settings.updated',
        resource_type: 'settings',
        created_at: new Date(Date.now() - 3 * 60000).toISOString(),
      }],
      meta: {},
    });

    renderWithProviders(<SettingsPage />);

    expect(await screen.findByText(/Last activity:/)).toBeInTheDocument();
    expect(screen.getByText(/Updated .* ago by System/)).toBeInTheDocument();
    expect(screen.queryByText(/Updated at .*May 1, 2026.*12:00 PM UTC/)).not.toBeInTheDocument();
  });

  it('uses the canonical organization returned by the server after saving settings', async () => {
    settingsGetMock.mockResolvedValue({
      data: {
        id: 'org-1',
        name: 'Test Org',
        settings: { pr_draft_default: false },
        created_at: '2026-05-01T12:00:00Z',
        updated_at: '2026-05-01T12:00:00Z',
      },
    });
    settingsUpdateMock.mockResolvedValueOnce({
      data: {
        id: 'org-1',
        name: 'Trimmed Org',
        settings: { pr_draft_default: false },
        created_at: '2026-05-01T12:00:00Z',
        updated_at: '2026-05-06T15:30:00Z',
      },
    });

    renderWithProviders(<SettingsPage />);

    const input = await screen.findByLabelText('Organization name');
    const user = userEvent.setup();
    await user.click(input);
    await user.keyboard('{Control>}a{/Control}  Trimmed Org  ');
    await user.tab();

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({ name: '  Trimmed Org  ' });
    });
    await waitFor(() => {
      expect(input).toHaveValue('Trimmed Org');
    });
    expect(screen.queryByLabelText('Require builder review before PR')).not.toBeInTheDocument();
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

  it('shows both default-on agent PR handoff controls and persists an explicit off value', async () => {
    renderWithProviders(<SettingsPage />);

    const createSwitch = await screen.findByRole('switch', { name: 'Create a PR when the coding agent is ready' });
    const reviewSwitch = screen.getByRole('switch', { name: 'Run a two-pass review/fix cycle before creating the PR' });
    expect(createSwitch).toBeChecked();
    expect(reviewSwitch).toBeChecked();

    await userEvent.click(createSwitch);

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: {
          session_automation: {
            automatic_follow_through: { create_pr_when_agent_ready: false },
          },
        },
      });
    });
  });

  it('sends sparse automation patches so rapid toggles cannot restore stale sibling values', async () => {
    settingsGetMock.mockResolvedValue({
      data: {
        id: 'org-1',
        name: 'Test Org',
        settings: {
          session_automation: {
            automatic_follow_through: {
              create_pr_when_agent_ready: false,
              resolve_conflicts_when_idle: false,
            },
          },
        },
        created_at: '2026-05-01T12:00:00Z',
        updated_at: '2026-05-01T12:00:00Z',
      },
    });
    renderWithProviders(<SettingsPage />);

    await userEvent.click(await screen.findByRole('switch', { name: 'Run a two-pass review/fix cycle before creating the PR' }));

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: {
          session_automation: {
            automatic_follow_through: { review_before_pr: false },
          },
        },
      });
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

  it('does not render sandbox runtime controls after they move to Runtime settings', async () => {
    renderWithProviders(<SettingsPage />);

    expect(await screen.findByRole('heading', { name: 'Organization', level: 1 })).toBeInTheDocument();
    expect(screen.queryByText('Network access')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Use static egress IP for sessions and previews')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Active previews per user')).not.toBeInTheDocument();
  });

});
