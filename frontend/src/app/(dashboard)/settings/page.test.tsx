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
    agentDefaultsMock.mockClear();
    codexStatusMock.mockClear();
    codexStatusMock.mockResolvedValue({ data: { status: 'none' } });
    settingsUpdateMock.mockClear();
    codexDisconnectMock.mockClear();
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
    expect(screen.getByRole('button', { name: 'Try Again' })).toBeInTheDocument();
  });
});
