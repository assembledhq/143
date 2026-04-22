import { describe, it, expect, vi, beforeEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, waitFor, userEvent } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import AgentPage from './page';
import type { UserCredentialSummary, ResolvedCredential, CodexSubscription, ListResponse, Organization, SingleResponse } from '@/lib/types';

const { useAuthMock } = vi.hoisted(() => ({
  useAuthMock: vi.fn(),
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: useAuthMock,
}));

const mockTeamDefaults: UserCredentialSummary[] = [
  {
    provider: 'anthropic',
    configured: true,
    is_team_default: true,
    masked_key: 'sk-ant-...xyz',
    set_by_user_name: 'Alice Smith',
    status: 'active',
  },
];

const mockResolved: ResolvedCredential[] = [
  { provider: 'anthropic', source: 'personal', masked_key: 'sk-ant-...abc' },
  { provider: 'openai', source: 'team_default', masked_key: 'sk-...def' },
  { provider: 'gemini', source: 'none' },
];

const mockOrgSettings: SingleResponse<Organization> = {
  data: {
    id: 'org-1',
    name: 'Test Org',
    settings: {
      autonomy_level: 'auto_simple',
      execution_aggressiveness: 2,
      max_concurrent_runs: 5,
      default_agent_type: 'claude_code',
    },
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
};

function setupHandlers({
  team = mockTeamDefaults,
  resolved = mockResolved,
  subscriptions = [] as CodexSubscription[],
}: {
  team?: UserCredentialSummary[];
  resolved?: ResolvedCredential[];
  subscriptions?: CodexSubscription[];
} = {}) {
  server.use(
    http.get('/api/v1/settings/credentials/team', () => {
      return HttpResponse.json({ data: team, meta: {} } satisfies ListResponse<UserCredentialSummary>);
    }),
    http.get('/api/v1/settings/credentials/resolved', () => {
      return HttpResponse.json({ data: resolved, meta: {} } satisfies ListResponse<ResolvedCredential>);
    }),
    http.get('/api/v1/settings', () => {
      return HttpResponse.json(mockOrgSettings);
    }),
    http.get('/api/v1/settings/codex-auth/status', () => {
      return HttpResponse.json({ data: { status: 'none' } });
    }),
    http.get('/api/v1/settings/codex-auth/subscriptions', () => {
      return HttpResponse.json({ data: subscriptions, meta: {} } satisfies ListResponse<CodexSubscription>);
    }),
    http.get('/api/v1/settings/claude-code-auth/subscriptions', () => {
      return HttpResponse.json({ data: [], meta: {} });
    }),
  );
}

describe('AgentPage', () => {
  beforeEach(() => {
    useAuthMock.mockReturnValue({
      user: { id: 'user-1', name: 'Alice Smith', email: 'alice@example.com', role: 'admin' },
      isLoading: false,
      isAuthenticated: true,
    });
    setupHandlers();
  });

  it('renders page header', async () => {
    renderWithProviders(<AgentPage />);
    expect(screen.getByText('Coding agents')).toBeInTheDocument();
  });

  it('shows Organization coding agents section for admins', async () => {
    renderWithProviders(<AgentPage />);

    expect(await screen.findByText('Organization coding agents')).toBeInTheDocument();
    expect(screen.getByText('Default coding agent')).toBeInTheDocument();
  });

  it('shows only the selected agent config card in org section', async () => {
    renderWithProviders(<AgentPage />);

    // default_agent_type is claude_code, so only Claude Code settings should appear
    expect(await screen.findByText('Claude Code settings')).toBeInTheDocument();
  });

  it('shows Not configured badge in org section when no team default is set', async () => {
    setupHandlers({ team: [] });

    renderWithProviders(<AgentPage />);

    const orgSection = await screen.findByText('Organization coding agents');
    expect(orgSection).toBeInTheDocument();

    const notConfiguredBadges = await screen.findAllByText('Not configured');
    expect(notConfiguredBadges.length).toBeGreaterThanOrEqual(1);
  });

  it('shows Execution section for admins', async () => {
    renderWithProviders(<AgentPage />);

    expect(await screen.findByText('Execution')).toBeInTheDocument();
    expect(screen.getByText('Max concurrent runs')).toBeInTheDocument();
  });

  it('hides org and execution sections for non-admins', async () => {
    useAuthMock.mockReturnValue({
      user: { id: 'user-2', name: 'Bob', email: 'bob@example.com', role: 'member' },
      isLoading: false,
      isAuthenticated: true,
    });

    renderWithProviders(<AgentPage />);

    // Wait for page to render
    await waitFor(() => {
      expect(screen.getByText('Coding agents')).toBeInTheDocument();
    });
    expect(screen.queryByText('Organization coding agents')).not.toBeInTheDocument();
    expect(screen.queryByText('Autonomy level')).not.toBeInTheDocument();
    expect(screen.queryByText('Execution aggressiveness')).not.toBeInTheDocument();
    expect(screen.queryByText('Max concurrent runs')).not.toBeInTheDocument();
  });

  it('renders without a save button (autosaves)', async () => {
    renderWithProviders(<AgentPage />);

    await screen.findAllByText('Claude Code');
    expect(screen.queryByText('Save organization settings')).not.toBeInTheDocument();
  });

  it('shows Active subscription when a Codex subscription is connected', async () => {
    const subscription: CodexSubscription = {
      id: 'sub-1',
      label: 'Team A',
      status: 'active',
      account_type: 'pro',
    };
    setupHandlers({ team: [], resolved: [], subscriptions: [subscription] });
    server.use(
      http.get('/api/v1/settings/codex-auth/status', () => {
        return HttpResponse.json({ data: { status: 'completed' } });
      }),
      http.get('/api/v1/settings', () => {
        return HttpResponse.json({
          data: {
            ...mockOrgSettings.data,
            settings: { ...mockOrgSettings.data.settings, default_agent_type: 'codex' },
          },
        });
      }),
    );

    renderWithProviders(<AgentPage />);

    expect(await screen.findByText('Active')).toBeInTheDocument();
    expect(screen.getByText('Team A')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add subscription' })).toBeInTheDocument();
  });

  it('autosaves the default agent type when selection changes', async () => {
    let capturedBody: unknown;
    server.use(
      http.patch('/api/v1/settings', async ({ request }) => {
        capturedBody = await request.json();
        return HttpResponse.json(mockOrgSettings);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    // Switch from claude_code (default) to codex via the radio.
    await user.click(await screen.findByRole('radio', { name: /Codex/ }));

    await waitFor(() => {
      expect(capturedBody).toEqual({
        settings: { default_agent_type: 'codex' },
      });
    });
  });

  it('shows team default masked key and set_by_user_name', async () => {
    // Use claude_code as default agent so the anthropic team default is shown
    setupHandlers({ team: mockTeamDefaults, resolved: mockResolved });

    renderWithProviders(<AgentPage />);

    expect(await screen.findByText(/sk-ant-\.\.\.xyz/)).toBeInTheDocument();
    expect(screen.getByText(/Set by Alice Smith/)).toBeInTheDocument();
    expect(screen.getByText('Team default set')).toBeInTheDocument();
  });

  it('shows Remove team default button and opens confirmation dialog', async () => {
    setupHandlers({ team: mockTeamDefaults, resolved: mockResolved });

    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    const removeBtn = await screen.findByText('Remove team default');
    expect(removeBtn).toBeInTheDocument();

    await user.click(removeBtn);

    expect(await screen.findByText(/Are you sure you want to remove the team default/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Remove' })).toBeInTheDocument();
  });

  it('confirms removal of team default via dialog', async () => {
    let removeCalled = false;
    setupHandlers({ team: mockTeamDefaults, resolved: mockResolved });
    server.use(
      http.delete('/api/v1/settings/credentials/team/:provider', () => {
        removeCalled = true;
        return HttpResponse.json({ data: null });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    await user.click(await screen.findByText('Remove team default'));
    await user.click(await screen.findByRole('button', { name: 'Remove' }));

    await waitFor(() => {
      expect(removeCalled).toBe(true);
    });
  });

  it('renders Codex agent with credential method toggle when selected', async () => {
    setupHandlers({ team: [], resolved: [] });
    server.use(
      http.get('/api/v1/settings', () => {
        return HttpResponse.json({
          data: {
            ...mockOrgSettings.data,
            settings: { ...mockOrgSettings.data.settings, default_agent_type: 'codex' },
          },
        });
      }),
    );

    renderWithProviders(<AgentPage />);

    expect(await screen.findByText('Codex settings')).toBeInTheDocument();
    expect(screen.getByText('Credential method')).toBeInTheDocument();
    // "Sign in with ChatGPT" appears in both the RadioCard label and the auth button
    expect(screen.getAllByText('Sign in with ChatGPT').length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText('Use API key')).toBeInTheDocument();
  });

  it('shows hidden env vars message when ChatGPT method is selected for Codex', async () => {
    setupHandlers({ team: [], resolved: [] });
    server.use(
      http.get('/api/v1/settings', () => {
        return HttpResponse.json({
          data: {
            ...mockOrgSettings.data,
            settings: { ...mockOrgSettings.data.settings, default_agent_type: 'codex' },
          },
        });
      }),
      http.get('/api/v1/settings/codex-auth/status', () => {
        return HttpResponse.json({ data: { status: 'none' } });
      }),
    );

    renderWithProviders(<AgentPage />);

    // Default inferred method is "chatgpt" when no API key set and status is not completed
    expect(await screen.findByText('API key fields are hidden while ChatGPT sign-in is selected.')).toBeInTheDocument();
  });

  it('shows API key env var fields when api_key method is inferred for Codex', async () => {
    setupHandlers({ team: [], resolved: [] });
    server.use(
      http.get('/api/v1/settings', () => {
        return HttpResponse.json({
          data: {
            ...mockOrgSettings.data,
            settings: {
              ...mockOrgSettings.data.settings,
              default_agent_type: 'codex',
              agent_config: { codex: { OPENAI_API_KEY: 'sk-test' } },
            },
          },
        });
      }),
      http.get('/api/v1/settings/codex-auth/status', () => {
        return HttpResponse.json({ data: { status: 'none' } });
      }),
    );

    renderWithProviders(<AgentPage />);

    // hasCodexAPIKey is true and status is not completed => api_key method inferred
    expect(await screen.findByText('API Key')).toBeInTheDocument();
    expect(screen.queryByText('API key fields are hidden while ChatGPT sign-in is selected.')).not.toBeInTheDocument();
  });

  it('shows Advanced settings toggle for agents with advanced env vars', async () => {
    // claude_code has advanced env var (ANTHROPIC_BASE_URL). Env vars are
    // hidden under the subscription method, so switch to API key first.
    setupHandlers({ team: [], resolved: [] });

    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    await user.click(await screen.findByLabelText('Use Anthropic API key'));
    expect(await screen.findByText('Advanced settings')).toBeInTheDocument();
  });

  it('toggles advanced settings to show/hide advanced env vars', async () => {
    setupHandlers({ team: [], resolved: [] });

    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    await user.click(await screen.findByLabelText('Use Anthropic API key'));
    const advBtn = await screen.findByText('Advanced settings');
    await user.click(advBtn);

    expect(await screen.findByText('Base URL')).toBeInTheDocument();
    expect(screen.getByText('Hide advanced settings')).toBeInTheDocument();

    await user.click(screen.getByText('Hide advanced settings'));

    await waitFor(() => {
      expect(screen.queryByText('Base URL')).not.toBeInTheDocument();
    });
  });

  it('shows the saved indicator after an autosave succeeds', async () => {
    server.use(
      http.patch('/api/v1/settings', () => {
        return HttpResponse.json(mockOrgSettings);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    await user.click(await screen.findByRole('radio', { name: /Codex/ }));

    expect(await screen.findByText('Saved')).toBeInTheDocument();
  });

  it('shows the error indicator when an autosave fails', async () => {
    server.use(
      http.patch('/api/v1/settings', () => {
        return new HttpResponse(null, { status: 500 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    await user.click(await screen.findByRole('radio', { name: /Codex/ }));

    expect(await screen.findByText("Couldn't save")).toBeInTheDocument();
  });

  it('updates max concurrent runs input', async () => {
    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    const input = await screen.findByLabelText('Max concurrent runs');
    await user.clear(input);
    await user.type(input, '8');

    expect(input).toHaveValue(8);
  });

  it('renders Gemini CLI settings when gemini_cli is selected as default', async () => {
    setupHandlers({ team: [], resolved: [] });
    server.use(
      http.get('/api/v1/settings', () => {
        return HttpResponse.json({
          data: {
            ...mockOrgSettings.data,
            settings: { ...mockOrgSettings.data.settings, default_agent_type: 'gemini_cli' },
          },
        });
      }),
    );

    renderWithProviders(<AgentPage />);

    expect(await screen.findByText('Gemini CLI settings')).toBeInTheDocument();
  });

  it('does not show Advanced settings toggle for agents without advanced env vars', async () => {
    // gemini_cli has no advanced env vars
    setupHandlers({ team: [], resolved: [] });
    server.use(
      http.get('/api/v1/settings', () => {
        return HttpResponse.json({
          data: {
            ...mockOrgSettings.data,
            settings: { ...mockOrgSettings.data.settings, default_agent_type: 'gemini_cli' },
          },
        });
      }),
    );

    renderWithProviders(<AgentPage />);

    await screen.findByText('Gemini CLI settings');
    expect(screen.queryByText('Advanced settings')).not.toBeInTheDocument();
  });
});
