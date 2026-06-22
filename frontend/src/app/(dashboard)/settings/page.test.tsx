import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderWithProviders, screen, waitFor, userEvent } from '@/test/test-utils';
import SettingsPage from './page';

const {
  settingsGetMock,
  settingsUpdateMock,
  settingsNetworkStatusMock,
  readinessPolicyGetMock,
  readinessPolicyUpdateMock,
  readinessCustomChecksListMock,
  readinessCustomCheckCreateMock,
  readinessCustomCheckUpdateMock,
  readinessCustomCheckDeleteMock,
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
  readinessPolicyGetMock: vi.fn().mockResolvedValue({
    data: {
      source: 'default',
      config: {
        enabled_for_builders: true,
        checks: {
          freshness: { enforcement: { builder: 'blocking', engineer: 'advisory', admin: 'advisory' } },
          agent_review_clean: { enforcement: { builder: 'blocking', engineer: 'advisory', admin: 'advisory' } },
        },
        bypass: {
          enabled: true,
          allowed_roles: ['admin', 'member', 'builder'],
          scopes: ['completed_blocking_checks'],
          non_bypassable_checks: [],
        },
        auto_run: { after_session_completion: false, on_create_pr: false },
        sensitive_paths: ['infra/**'],
        large_diff_file_threshold: 25,
        large_diff_line_threshold: 500,
      },
      bypass_counts: {
        total: 2,
        by_repository: [{ key: 'repo-1', count: 1 }],
        by_check: [{ key: 'agent_review_clean', count: 2 }],
        by_user: [{ key: 'user-1', count: 2 }],
      },
    },
  }),
  readinessPolicyUpdateMock: vi.fn().mockResolvedValue({ data: {} }),
  readinessCustomChecksListMock: vi.fn().mockResolvedValue({
    data: [{
      id: 'check-1',
      check_key: 'no_schema_drift',
      name: 'No schema drift',
      prompt: 'Check schema compatibility.',
      paths: { include: ['analytics/**'] },
      enforcement: { builder: 'blocking', engineer: 'advisory', admin: 'advisory' },
      source: 'repo_config',
      active: true,
    }],
  }),
  readinessCustomCheckCreateMock: vi.fn().mockResolvedValue({
    data: {
      id: 'check-2',
      check_key: 'review_docs',
      name: 'Review docs',
      prompt: 'Check docs.',
      paths: { include: ['docs/**'] },
      enforcement: { builder: 'advisory', engineer: 'advisory', admin: 'advisory' },
      source: 'org_settings',
      active: true,
    },
  }),
  readinessCustomCheckUpdateMock: vi.fn().mockResolvedValue({ data: {} }),
  readinessCustomCheckDeleteMock: vi.fn().mockResolvedValue(undefined),
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
      getPRReadinessPolicy: readinessPolicyGetMock,
      updatePRReadinessPolicy: readinessPolicyUpdateMock,
      listPRReadinessCustomChecks: readinessCustomChecksListMock,
      createPRReadinessCustomCheck: readinessCustomCheckCreateMock,
      updatePRReadinessCustomCheck: readinessCustomCheckUpdateMock,
      deletePRReadinessCustomCheck: readinessCustomCheckDeleteMock,
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
    readinessPolicyGetMock.mockClear();
    readinessPolicyUpdateMock.mockClear();
    readinessCustomChecksListMock.mockClear();
    readinessCustomCheckCreateMock.mockClear();
    readinessCustomCheckUpdateMock.mockClear();
    readinessCustomCheckDeleteMock.mockClear();
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
    readinessPolicyGetMock.mockResolvedValue({
      data: {
        source: 'default',
        config: {
          enabled_for_builders: true,
          checks: {
            freshness: { enforcement: { builder: 'blocking', engineer: 'advisory', admin: 'advisory' } },
            agent_review_clean: { enforcement: { builder: 'blocking', engineer: 'advisory', admin: 'advisory' } },
          },
          bypass: {
            enabled: true,
            allowed_roles: ['admin', 'member', 'builder'],
            scopes: ['completed_blocking_checks'],
            non_bypassable_checks: [],
          },
          auto_run: { after_session_completion: false, on_create_pr: false },
          sensitive_paths: ['infra/**'],
          large_diff_file_threshold: 25,
          large_diff_line_threshold: 500,
        },
        bypass_counts: {
          total: 2,
          by_repository: [{ key: 'repo-1', count: 1 }],
          by_check: [{ key: 'agent_review_clean', count: 2 }],
          by_user: [{ key: 'user-1', count: 2 }],
        },
      },
    });
    readinessPolicyUpdateMock.mockResolvedValue({ data: {} });
    readinessCustomChecksListMock.mockResolvedValue({
      data: [{
        id: 'check-1',
        check_key: 'no_schema_drift',
        name: 'No schema drift',
        prompt: 'Check schema compatibility.',
        paths: { include: ['analytics/**'] },
        enforcement: { builder: 'blocking', engineer: 'advisory', admin: 'advisory' },
        source: 'repo_config',
        active: true,
      }],
    });
    readinessCustomCheckCreateMock.mockResolvedValue({
      data: {
        id: 'check-2',
        check_key: 'review_docs',
        name: 'Review docs',
        prompt: 'Check docs.',
        paths: { include: ['docs/**'] },
        enforcement: { builder: 'advisory', engineer: 'advisory', admin: 'advisory' },
        source: 'org_settings',
        active: true,
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
        settings: { builder_permissions: { require_review_before_pr: true, extra_flag: true } },
        created_at: '2026-05-01T12:00:00Z',
        updated_at: '2026-05-01T12:00:00Z',
      },
    });
    settingsUpdateMock.mockResolvedValueOnce({
      data: {
        id: 'org-1',
        name: 'Trimmed Org',
        settings: { builder_permissions: { require_review_before_pr: true, extra_flag: true } },
        created_at: '2026-05-01T12:00:00Z',
        updated_at: '2026-05-06T15:30:00Z',
      },
    });
    settingsUpdateMock.mockResolvedValueOnce({
      data: {
        id: 'org-1',
        name: 'Trimmed Org',
        settings: { builder_permissions: { require_review_before_pr: false, extra_flag: true } },
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

    await user.click(screen.getByLabelText('Require builder review before PR'));

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { builder_permissions: { require_review_before_pr: false } },
      });
    });
    expect(settingsUpdateMock).toHaveBeenLastCalledWith({
      settings: { builder_permissions: { require_review_before_pr: false } },
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

  it('does not render sandbox runtime controls after they move to Runtime settings', async () => {
    renderWithProviders(<SettingsPage />);

    expect(await screen.findByRole('heading', { name: 'Organization', level: 1 })).toBeInTheDocument();
    expect(screen.queryByText('Network access')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Use static egress IP for sessions and previews')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Active previews per user')).not.toBeInTheDocument();
  });

  it('renders a compact PR readiness summary by default', async () => {
    renderWithProviders(<SettingsPage />);

    expect(await screen.findByText('PR readiness')).toBeInTheDocument();
    expect(screen.getByText('Organization default')).toBeInTheDocument();
    expect(await screen.findByText('2 total')).toBeInTheDocument();
    expect(await screen.findByText('1 custom check')).toBeInTheDocument();
    expect(screen.queryByText('Built-in checks')).not.toBeInTheDocument();
    expect(screen.queryByPlaceholderText('Prompt template')).not.toBeInTheDocument();
  });

  it('opens detailed PR readiness controls in the manage sheet', async () => {
    renderWithProviders(<SettingsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Manage readiness policy' }));

    expect(await screen.findByRole('dialog', { name: 'PR readiness policy' })).toBeInTheDocument();
    expect(screen.getByRole('table', { name: 'Built-in readiness checks' })).toBeInTheDocument();
    expect(screen.getByRole('row', { name: /Freshness/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'About Freshness' })).toBeInTheDocument();
    expect(screen.getAllByText('Blocking').length).toBeGreaterThan(0);
    expect(screen.queryByText('agent review clean')).not.toBeInTheDocument();
    expect(await screen.findByText('No schema drift')).toBeInTheDocument();
    expect(screen.getAllByText('2 total')).toHaveLength(2);
    expect(screen.getByText('repo-1: 1')).toBeInTheDocument();
    expect(screen.getByText('no_schema_drift · .143/config.json')).toBeInTheDocument();
  });

  it('applies PR readiness presets from the manage sheet', async () => {
    renderWithProviders(<SettingsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Manage readiness policy' }));
    await user.click(await screen.findByRole('combobox', { name: /Preset/i }));
    await user.click(screen.getByRole('option', { name: 'Strict' }));

    await waitFor(() => {
      expect(readinessPolicyUpdateMock).toHaveBeenCalledWith(
        expect.objectContaining({
          enabled_for_builders: true,
          checks: expect.objectContaining({
            freshness: expect.objectContaining({
              enforcement: expect.objectContaining({ builder: 'blocking' }),
            }),
            review_packet_draftable: expect.objectContaining({
              enforcement: expect.objectContaining({ builder: 'blocking' }),
            }),
          }),
        }),
        undefined,
      );
    });
  });

  it('lets admins disable advisory readiness for engineers as a group', async () => {
    renderWithProviders(<SettingsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Manage readiness policy' }));
    await user.click(await screen.findByLabelText('Engineer advisory checks'));

    await waitFor(() => {
      expect(readinessPolicyUpdateMock).toHaveBeenCalledWith(
        expect.objectContaining({
          checks: expect.objectContaining({
            freshness: expect.objectContaining({
              enforcement: expect.objectContaining({ engineer: 'off' }),
            }),
            agent_review_clean: expect.objectContaining({
              enforcement: expect.objectContaining({ engineer: 'off' }),
            }),
          }),
        }),
        undefined,
      );
    });
  });

  it('distinguishes repository settings checks from config-file checks', async () => {
    readinessCustomChecksListMock.mockResolvedValue({
      data: [
        {
          id: 'check-1',
          check_key: 'no_schema_drift',
          name: 'No schema drift',
          prompt: 'Check schema compatibility.',
          paths: { include: ['analytics/**'] },
          enforcement: { builder: 'blocking', engineer: 'advisory', admin: 'advisory' },
          source: 'repo_config',
          active: true,
        },
        {
          id: 'check-2',
          repository_id: 'repo-1',
          check_key: 'repo_docs',
          name: 'Repository docs',
          prompt: 'Check docs.',
          paths: { include: ['docs/**'] },
          enforcement: { builder: 'advisory', engineer: 'advisory', admin: 'advisory' },
          source: 'org_settings',
          active: true,
        },
      ],
    });

    renderWithProviders(<SettingsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Manage readiness policy' }));

    expect(await screen.findByText('No schema drift')).toBeInTheDocument();
    expect(await screen.findByText('Repository docs')).toBeInTheDocument();
    expect(screen.getByText('no_schema_drift · .143/config.json')).toBeInTheDocument();
    expect(screen.getByText('repo_docs · repo settings')).toBeInTheDocument();
  });

  it('keeps inherited custom checks read-only in repository readiness scope', async () => {
    readinessCustomChecksListMock.mockResolvedValue({
      data: [
        {
          id: 'org-check',
          check_key: 'org_policy',
          name: 'Org policy',
          prompt: 'Check org policy.',
          paths: { include: ['src/**'] },
          enforcement: { builder: 'advisory', engineer: 'advisory', admin: 'advisory' },
          source: 'org_settings',
          active: true,
        },
        {
          id: 'repo-check',
          repository_id: 'repo-1',
          check_key: 'repo_policy',
          name: 'Repo policy',
          prompt: 'Check repo policy.',
          paths: { include: ['repo/**'] },
          enforcement: { builder: 'advisory', engineer: 'advisory', admin: 'advisory' },
          source: 'org_settings',
          active: true,
        },
      ],
    });

    renderWithProviders(<SettingsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Manage readiness policy' }));
    await user.click(await screen.findByRole('combobox', { name: /Policy scope/i }));
    await user.click(screen.getByRole('option', { name: 'acme/app' }));

    expect(await screen.findByText('org_policy · org settings')).toBeInTheDocument();
    expect(screen.getByText('repo_policy · repo settings')).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: 'Edit' })).toHaveLength(1);
    expect(screen.getAllByRole('button', { name: 'Delete' })).toHaveLength(1);

    await user.click(screen.getByRole('button', { name: 'Delete' }));

    await waitFor(() => {
      expect(readinessCustomCheckDeleteMock).toHaveBeenCalledWith('repo-check');
    });
    expect(readinessCustomCheckDeleteMock).not.toHaveBeenCalledWith('org-check');
  });

  it('updates PR readiness auto-run policy controls', async () => {
    renderWithProviders(<SettingsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Manage readiness policy' }));
    await user.click(await screen.findByLabelText('Auto-run on Create PR'));

    await waitFor(() => {
      expect(readinessPolicyUpdateMock).toHaveBeenCalledWith(
        expect.objectContaining({
          auto_run: expect.objectContaining({ on_create_pr: true }),
        }),
        undefined,
      );
    });
  });

  it('creates custom PR readiness prompt checks with path filters', async () => {
    renderWithProviders(<SettingsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Manage readiness policy' }));
    await user.type(await screen.findByPlaceholderText('check_key'), 'review_docs');
    await user.type(screen.getByPlaceholderText('Check name'), 'Review docs');
    await user.type(screen.getByPlaceholderText('Include paths'), 'docs/**');
    await user.type(screen.getByPlaceholderText('Prompt template'), 'Check docs for reviewer context.');
    await user.click(screen.getByRole('button', { name: 'Add custom check' }));

    await waitFor(() => {
      expect(readinessCustomCheckCreateMock).toHaveBeenCalledWith(expect.objectContaining({
        check_key: 'review_docs',
        name: 'Review docs',
        prompt: 'Check docs for reviewer context.',
        repository_id: undefined,
        paths: expect.objectContaining({ include: ['docs/**'] }),
      }));
    });
  });
});
