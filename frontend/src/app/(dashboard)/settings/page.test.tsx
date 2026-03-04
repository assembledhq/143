import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderWithProviders, screen, waitFor, userEvent } from '@/test/test-utils';
import SettingsPage from './page';

const {
  settingsGetMock,
  agentDefaultsMock,
  codexStatusMock,
  settingsUpdateMock,
  codexDisconnectMock,
  codexInitiateMock,
  loginMock,
} = vi.hoisted(() => ({
  settingsGetMock: vi.fn().mockResolvedValue({
    data: {
      name: 'Test Org',
      settings: {
        default_agent_type: 'codex',
        autonomy_level: 'manual',
        execution_aggressiveness: 2,
        max_concurrent_runs: 3,
        confidence_thresholds: { auto_proceed: 0.8, human_review: 0.5 },
        priority_weights: { customer_impact: 0.35, severity: 0.25, recency: 0.2, revenue_risk: 0.2 },
        min_priority_threshold: 20,
        product_direction: '',
        agent_config: {},
      },
    },
  }),
  agentDefaultsMock: vi.fn().mockResolvedValue({ data: {} }),
  codexStatusMock: vi.fn().mockResolvedValue({ data: { status: 'none' } }),
  settingsUpdateMock: vi.fn().mockResolvedValue({ data: {} }),
  codexDisconnectMock: vi.fn().mockResolvedValue({}),
  codexInitiateMock: vi.fn().mockResolvedValue({
    data: {
      user_code: 'TEST-1234',
      verification_uri: 'https://auth.openai.com/codex/device',
      expires_in: 900,
    },
  }),
  loginMock: vi.fn(),
}));

vi.mock('@/lib/api', () => ({
  api: {
    settings: {
      get: settingsGetMock,
      getAgentDefaults: agentDefaultsMock,
      update: settingsUpdateMock,
    },
    codexAuth: {
      status: codexStatusMock,
      disconnect: codexDisconnectMock,
      initiate: codexInitiateMock,
    },
    auth: {
      login: loginMock,
    },
  },
}));

describe('SettingsPage', () => {
  beforeEach(() => {
    settingsGetMock.mockClear();
    settingsGetMock.mockResolvedValue({
      data: {
        name: 'Test Org',
        settings: {
          default_agent_type: 'codex',
          autonomy_level: 'manual',
          execution_aggressiveness: 2,
          max_concurrent_runs: 3,
          confidence_thresholds: { auto_proceed: 0.8, human_review: 0.5 },
          priority_weights: { customer_impact: 0.35, severity: 0.25, recency: 0.2, revenue_risk: 0.2 },
          min_priority_threshold: 20,
          product_direction: '',
          agent_config: {},
        },
      },
    });
    agentDefaultsMock.mockClear();
    agentDefaultsMock.mockResolvedValue({ data: {} });
    codexStatusMock.mockClear();
    codexStatusMock.mockResolvedValue({ data: { status: 'none' } });
    settingsUpdateMock.mockClear();
    settingsUpdateMock.mockResolvedValue({ data: {} });
    codexDisconnectMock.mockClear();
    codexDisconnectMock.mockResolvedValue({});
    codexInitiateMock.mockClear();
    codexInitiateMock.mockResolvedValue({
      data: {
        user_code: 'TEST-1234',
        verification_uri: 'https://auth.openai.com/codex/device',
        expires_in: 900,
      },
    });
    loginMock.mockClear();
  });

  it('renders the General section with organization name', async () => {
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('General')).toBeInTheDocument();
    });

    expect(screen.getByLabelText('Organization Name')).toBeInTheDocument();
  });

  it('renders the Integrations section', async () => {
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Integrations')).toBeInTheDocument();
    });
  });

  it('renders the Agent Setup section with agent type options', async () => {
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Agent Setup')).toBeInTheDocument();
    });

    expect(screen.getByText('Codex')).toBeInTheDocument();
    expect(screen.getByText('Claude Code')).toBeInTheDocument();
    expect(screen.getByText('Gemini CLI')).toBeInTheDocument();
  });

  it('shows ChatGPT sign-in when Codex is selected and not connected', async () => {
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Sign in with ChatGPT' })).toBeInTheDocument();
    });

    expect(screen.getByText('Recommended')).toBeInTheDocument();
  });

  it('shows Connected status when ChatGPT OAuth is completed', async () => {
    codexStatusMock.mockResolvedValue({ data: { status: 'completed' } });

    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Connected')).toBeInTheDocument();
    });

    expect(screen.getByText('Disconnect')).toBeInTheDocument();
  });

  it('renders the Agent Execution section with autonomy options', async () => {
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Agent Execution')).toBeInTheDocument();
    });

    expect(screen.getByText('Manual')).toBeInTheDocument();
    expect(screen.getByText('Auto (simple)')).toBeInTheDocument();
    expect(screen.getByText('Auto (all)')).toBeInTheDocument();
  });

  it('renders execution aggressiveness options', async () => {
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Conservative')).toBeInTheDocument();
    });

    expect(screen.getByText('Moderate')).toBeInTheDocument();
    expect(screen.getByText('Aggressive')).toBeInTheDocument();
    expect(screen.getByText('Maximum')).toBeInTheDocument();
  });

  it('renders the Save Settings button', async () => {
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Save Settings' })).toBeInTheDocument();
    });
  });

  it('shows Advanced Settings when toggled', async () => {
    const user = userEvent.setup();
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Advanced Settings' })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Advanced Settings' }));

    await waitFor(() => {
      expect(screen.getByText('Confidence Thresholds')).toBeInTheDocument();
    });

    expect(screen.getByText('Prioritization')).toBeInTheDocument();
    expect(screen.getByText('Other Agent Configuration')).toBeInTheDocument();
  });

  it('calls settings update on save', async () => {
    const user = userEvent.setup();
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Save Settings' })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Save Settings' }));

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledTimes(1);
    });
  });

  it('renders API Key section for selected agent', async () => {
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getAllByText('API Key').length).toBeGreaterThanOrEqual(1);
    });
  });

  it('opens device code modal when Sign in with ChatGPT button is clicked', async () => {
    const user = userEvent.setup();
    renderWithProviders(<SettingsPage />);

    const btn = await screen.findByRole('button', { name: 'Sign in with ChatGPT' });
    await user.click(btn);

    expect(await screen.findByText('Connect your ChatGPT account')).toBeInTheDocument();

    // Wait for device code to appear after async initiation
    expect(await screen.findByText('TEST-1234')).toBeInTheDocument();
    expect(screen.getByText('https://auth.openai.com/codex/device')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy' })).toBeInTheDocument();
  });

  it('shows Disconnect button when connected and calls disconnect on click', async () => {
    codexStatusMock.mockResolvedValue({ data: { status: 'completed' } });
    const user = userEvent.setup();

    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Connected')).toBeInTheDocument();
    });

    await user.click(screen.getByText('Disconnect'));

    await waitFor(() => {
      expect(codexDisconnectMock).toHaveBeenCalledTimes(1);
    });
  });

  it('renders model and base URL fields for selected agent', async () => {
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Model')).toBeInTheDocument();
    });

    expect(screen.getByText('Base URL')).toBeInTheDocument();
  });

  it('renders Max Concurrent Runs input', async () => {
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByLabelText('Max Concurrent Runs')).toBeInTheDocument();
    });
  });

  it('shows error state in device code modal when initiation fails', async () => {
    codexInitiateMock.mockRejectedValueOnce(new Error('fail'));
    const user = userEvent.setup();

    renderWithProviders(<SettingsPage />);

    const btn = await screen.findByRole('button', { name: 'Sign in with ChatGPT' });
    await user.click(btn);

    expect(await screen.findByText('Failed to start authentication. Please try again.')).toBeInTheDocument();
    const cancelButton = screen.getByRole('button', { name: 'Cancel' });
    const tryAgainButton = screen.getByRole('button', { name: 'Try Again' });

    expect(cancelButton).toBeInTheDocument();
    expect(tryAgainButton).toBeInTheDocument();
    expect(cancelButton.parentElement).toBe(tryAgainButton.parentElement);
    expect(cancelButton.compareDocumentPosition(tryAgainButton) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('syncs server settings into form state on load', async () => {
    settingsGetMock.mockResolvedValue({
      data: {
        name: 'My Org',
        settings: {
          default_agent_type: 'claude_code',
          autonomy_level: 'auto_simple',
          execution_aggressiveness: 3,
          max_concurrent_runs: 5,
          confidence_thresholds: { auto_proceed: 0.9, human_review: 0.3 },
          priority_weights: { customer_impact: 0.4, severity: 0.3, recency: 0.15, revenue_risk: 0.15 },
          min_priority_threshold: 30,
          product_direction: 'Focus on mobile',
          product_context: {
            philosophy: 'Move fast',
            direction: 'Focus on mobile',
            focus_areas: ['mobile', 'performance'],
            avoid_areas: ['legacy'],
          },
          agent_config: {},
        },
      },
    });

    renderWithProviders(<SettingsPage />);

    // Wait for the org name to be synced
    await waitFor(() => {
      expect(screen.getByLabelText('Organization Name')).toHaveValue('My Org');
    });

    // Max concurrent should be synced from server
    expect(screen.getByLabelText('Max Concurrent Runs')).toHaveValue(5);
  });

  it('saves settings with correct payload structure', async () => {
    const user = userEvent.setup();
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Save Settings' })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Save Settings' }));

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledTimes(1);
    });

    const payload = settingsUpdateMock.mock.calls[0][0];
    expect(payload).toHaveProperty('settings');
    expect(payload.settings).toHaveProperty('autonomy_level');
    expect(payload.settings).toHaveProperty('execution_aggressiveness');
    expect(payload.settings).toHaveProperty('confidence_thresholds');
    expect(payload.settings).toHaveProperty('priority_weights');
    expect(payload.settings).toHaveProperty('product_context');
    expect(payload.settings).toHaveProperty('default_agent_type');
  });

  it('shows success message after successful save', async () => {
    const user = userEvent.setup();
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Save Settings' })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Save Settings' }));

    await waitFor(() => {
      expect(screen.getByText('Settings saved.')).toBeInTheDocument();
    });
  });

  it('shows error message after failed save', async () => {
    settingsUpdateMock.mockRejectedValueOnce(new Error('fail'));
    const user = userEvent.setup();
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Save Settings' })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Save Settings' }));

    await waitFor(() => {
      expect(screen.getByText('Failed to save settings.')).toBeInTheDocument();
    });
  });

  it('shows advanced settings with PM Agent section', async () => {
    const user = userEvent.setup();
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Advanced Settings' })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Advanced Settings' }));

    await waitFor(() => {
      expect(screen.getByText('PM Agent')).toBeInTheDocument();
    });

    expect(screen.getByLabelText('Schedule (hours)')).toBeInTheDocument();
    expect(screen.getByLabelText('PM Model')).toBeInTheDocument();
  });

  it('shows prioritization section with philosophy and direction fields in advanced settings', async () => {
    const user = userEvent.setup();
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Advanced Settings' })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Advanced Settings' }));

    await waitFor(() => {
      expect(screen.getByLabelText('Philosophy')).toBeInTheDocument();
    });

    expect(screen.getByLabelText('Current Direction')).toBeInTheDocument();
    expect(screen.getByLabelText('Focus Areas')).toBeInTheDocument();
    expect(screen.getByLabelText('Avoid Areas')).toBeInTheDocument();
  });

  it('shows priority weights with sum validation in advanced settings', async () => {
    const user = userEvent.setup();
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Advanced Settings' })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Advanced Settings' }));

    await waitFor(() => {
      expect(screen.getByText('Priority Weights')).toBeInTheDocument();
    });

    expect(screen.getByText(/Sum:/)).toBeInTheDocument();
    expect(screen.getByText('Customer Impact')).toBeInTheDocument();
    expect(screen.getByText('Severity')).toBeInTheDocument();
    expect(screen.getByText('Recency')).toBeInTheDocument();
    expect(screen.getByText('Revenue Risk')).toBeInTheDocument();
    expect(screen.getByText('Minimum Score Threshold')).toBeInTheDocument();
  });

  it('shows auto-proceed and human review threshold labels in advanced settings', async () => {
    const user = userEvent.setup();
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Advanced Settings' })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Advanced Settings' }));

    await waitFor(() => {
      expect(screen.getByText('Auto-proceed Threshold')).toBeInTheDocument();
    });

    expect(screen.getByText('Human Review Threshold')).toBeInTheDocument();
  });

  it('shows other agent configuration in advanced settings', async () => {
    const user = userEvent.setup();
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Advanced Settings' })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Advanced Settings' }));

    await waitFor(() => {
      expect(screen.getByText('Other Agent Configuration')).toBeInTheDocument();
    });

    // With codex as default, Claude Code and Gemini CLI should be in "Other" section
    expect(screen.getByText('Configure credentials for agents other than your default.')).toBeInTheDocument();
  });

  it('syncs settings with product_context fields from server', async () => {
    settingsGetMock.mockResolvedValue({
      data: {
        name: 'Context Org',
        settings: {
          default_agent_type: 'codex',
          autonomy_level: 'manual',
          execution_aggressiveness: 2,
          max_concurrent_runs: 3,
          confidence_thresholds: { auto_proceed: 0.8, human_review: 0.5 },
          priority_weights: { customer_impact: 0.35, severity: 0.25, recency: 0.2, revenue_risk: 0.2 },
          min_priority_threshold: 20,
          product_direction: 'Quarterly focus',
          product_context: {
            philosophy: 'User first',
            direction: 'Quarterly focus',
            focus_areas: ['auth', 'performance'],
            avoid_areas: ['legacy'],
          },
          agent_config: { claude_code: { ANTHROPIC_API_KEY: 'sk-test' } },
        },
      },
    });

    const user = userEvent.setup();
    renderWithProviders(<SettingsPage />);

    // When agent_config has values, advanced settings should open automatically
    await waitFor(() => {
      expect(screen.getByText('Other Agent Configuration')).toBeInTheDocument();
    });

    // Focus areas and avoid areas should render as badges
    expect(screen.getByText('auth')).toBeInTheDocument();
    expect(screen.getByText('performance')).toBeInTheDocument();
    expect(screen.getByText('legacy')).toBeInTheDocument();
  });

  it('closes device code modal when Cancel is clicked', async () => {
    const user = userEvent.setup();
    renderWithProviders(<SettingsPage />);

    const btn = await screen.findByRole('button', { name: 'Sign in with ChatGPT' });
    await user.click(btn);

    expect(await screen.findByText('Connect your ChatGPT account')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Cancel' }));

    await waitFor(() => {
      expect(screen.queryByText('Connect your ChatGPT account')).not.toBeInTheDocument();
    });
  });

  it('adds focus area tags via Enter key', async () => {
    const user = userEvent.setup();
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Advanced Settings' })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Advanced Settings' }));

    const focusInput = await screen.findByPlaceholderText('Add focus area and press Enter');
    await user.type(focusInput, 'security{Enter}');

    await waitFor(() => {
      expect(screen.getByText('security')).toBeInTheDocument();
    });
  });

  it('adds avoid area tags via Enter key', async () => {
    const user = userEvent.setup();
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Advanced Settings' })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Advanced Settings' }));

    const avoidInput = await screen.findByPlaceholderText('Add avoid area and press Enter');
    await user.type(avoidInput, 'refactoring{Enter}');

    await waitFor(() => {
      expect(screen.getByText('refactoring')).toBeInTheDocument();
    });
  });

  it('shows server default label when agent has server-side defaults', async () => {
    agentDefaultsMock.mockResolvedValue({
      data: {
        codex: { OPENAI_API_KEY: 'sk-server-key', OPENAI_MODEL: 'gpt-4' },
      },
    });

    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getAllByText('server default').length).toBeGreaterThanOrEqual(1);
    });
  });
});
