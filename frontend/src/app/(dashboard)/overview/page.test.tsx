import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderWithProviders, screen, waitFor, userEvent } from '@/test/test-utils';
import Overview from './page';

const {
  loginMock,
  sentryLoginMock,
  codexStatusMock,
  codexInitiateMock,
  settingsGetMock,
  settingsUpdateMock,
  agentDefaultsMock,
} = vi.hoisted(() => ({
  loginMock: vi.fn(),
  sentryLoginMock: vi.fn(),
  codexStatusMock: vi.fn().mockResolvedValue({ data: { status: 'pending' } }),
  codexInitiateMock: vi.fn().mockResolvedValue({
    data: {
      user_code: 'ABCD-1234',
      verification_uri: 'https://auth.openai.com/codex/device',
      expires_in: 900,
    },
  }),
  settingsGetMock: vi.fn().mockResolvedValue({
    data: {
      name: 'Test Org',
      settings: {
        default_agent_type: 'codex',
        agent_config: {},
      },
    },
  }),
  settingsUpdateMock: vi.fn().mockResolvedValue({ data: {} }),
  agentDefaultsMock: vi.fn().mockResolvedValue({ data: {} }),
}));

vi.mock('@/lib/api', () => ({
  api: {
    auth: {
      login: loginMock,
      loginSentry: sentryLoginMock,
    },
    codexAuth: {
      status: codexStatusMock,
      initiate: codexInitiateMock,
    },
    settings: {
      get: settingsGetMock,
      update: settingsUpdateMock,
      getAgentDefaults: agentDefaultsMock,
    },
  },
}));

describe('OverviewPage', () => {
  beforeEach(() => {
    loginMock.mockReset();
    sentryLoginMock.mockReset();
    codexStatusMock.mockClear();
    codexStatusMock.mockResolvedValue({ data: { status: 'pending' } });
    settingsGetMock.mockClear();
    settingsUpdateMock.mockClear();
    agentDefaultsMock.mockClear();
  });

  it('starts GitHub onboarding directly from the dashboard', async () => {
    const user = userEvent.setup();

    renderWithProviders(<Overview />);

    await user.click(screen.getByRole('button', { name: 'Connect GitHub' }));

    expect(loginMock).toHaveBeenCalledTimes(1);
  });

  it('starts Sentry onboarding directly from the dashboard', async () => {
    const user = userEvent.setup();

    renderWithProviders(<Overview />);

    await user.click(screen.getByRole('button', { name: 'Connect Sentry' }));

    expect(sentryLoginMock).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('heading', { name: 'Integrations' })).not.toBeInTheDocument();
  });

  it('shows Linear integration on the dashboard', () => {
    renderWithProviders(<Overview />);

    expect(screen.getByText('Connect Linear')).toBeInTheDocument();
    expect(screen.getByText('Coming soon')).toBeInTheDocument();
  });

  it('shows the AgentSetupCard with connect prompt when not authenticated', async () => {
    renderWithProviders(<Overview />);

    await waitFor(() => {
      expect(screen.getByText('Connect your coding agent')).toBeInTheDocument();
    });

    expect(screen.getByRole('button', { name: 'Sign in with ChatGPT' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Settings' })).toBeInTheDocument();
  });

  it('opens agent settings modal from setup card and saves updates', async () => {
    const user = userEvent.setup();
    renderWithProviders(<Overview />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Settings' })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Settings' }));

    await waitFor(() => {
      expect(screen.getByText('Edit agent settings')).toBeInTheDocument();
    });

    expect(screen.getByText('For gpt-5.3-codex model access.')).toBeInTheDocument();
    expect(screen.getByText('Recommended')).toBeInTheDocument();

    await user.clear(screen.getByLabelText('Model'));
    await user.type(screen.getByLabelText('Model'), 'codex-mini');
    await user.click(screen.getByRole('button', { name: 'Save changes' }));

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledTimes(1);
    });

    expect(settingsUpdateMock).toHaveBeenCalledTimes(1);
  });

  it('shows the AgentSetupCard as connected when auth status is completed', async () => {
    codexStatusMock.mockResolvedValue({ data: { status: 'completed' } });

    renderWithProviders(<Overview />);

    await waitFor(() => {
      expect(screen.getByText('Codex is connected via ChatGPT.')).toBeInTheDocument();
    });

    expect(screen.getByText('Connected')).toBeInTheDocument();
  });

  it('opens device code modal when Sign in with ChatGPT is clicked', async () => {
    const user = userEvent.setup();
    renderWithProviders(<Overview />);

    await waitFor(() => {
      expect(screen.getByText('Sign in with ChatGPT')).toBeInTheDocument();
    });

    await user.click(screen.getByText('Sign in with ChatGPT'));

    await waitFor(() => {
      expect(screen.getByText('Connect your ChatGPT account')).toBeInTheDocument();
    });
  });

  it('renders the page description text', () => {
    renderWithProviders(<Overview />);

    expect(screen.getByText(/Once integrations are connected/)).toBeInTheDocument();
  });

  it('renders the page header', () => {
    renderWithProviders(<Overview />);

    expect(screen.getByText('Overview')).toBeInTheDocument();
    expect(screen.getByText('Get started by connecting your tools.')).toBeInTheDocument();
  });

  it('shows device code and verification URI in modal after initiation', async () => {
    const user = userEvent.setup();
    renderWithProviders(<Overview />);

    await waitFor(() => {
      expect(screen.getByText('Sign in with ChatGPT')).toBeInTheDocument();
    });

    await user.click(screen.getByText('Sign in with ChatGPT'));

    await waitFor(() => {
      expect(screen.getByText('ABCD-1234')).toBeInTheDocument();
    });

    expect(screen.getByText('https://auth.openai.com/codex/device')).toBeInTheDocument();
    expect(screen.getByText('Waiting for authentication...')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeInTheDocument();
  });

  it('closes modal when Cancel is clicked', async () => {
    const user = userEvent.setup();
    renderWithProviders(<Overview />);

    await waitFor(() => {
      expect(screen.getByText('Sign in with ChatGPT')).toBeInTheDocument();
    });

    await user.click(screen.getByText('Sign in with ChatGPT'));

    await waitFor(() => {
      expect(screen.getByText('Connect your ChatGPT account')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Cancel' }));

    await waitFor(() => {
      expect(screen.queryByText('Connect your ChatGPT account')).not.toBeInTheDocument();
    });
  });

  it('shows error state when auth initiation fails', async () => {
    codexInitiateMock.mockRejectedValueOnce(new Error('Network error'));
    const user = userEvent.setup();
    renderWithProviders(<Overview />);

    await waitFor(() => {
      expect(screen.getByText('Sign in with ChatGPT')).toBeInTheDocument();
    });

    await user.click(screen.getByText('Sign in with ChatGPT'));

    await waitFor(() => {
      expect(screen.getByText('Failed to start authentication. Please try again.')).toBeInTheDocument();
    });

    const cancelButton = screen.getByRole('button', { name: 'Cancel' });
    const tryAgainButton = screen.getByRole('button', { name: 'Try Again' });

    expect(cancelButton).toBeInTheDocument();
    expect(tryAgainButton).toBeInTheDocument();
    expect(cancelButton.parentElement).toBe(tryAgainButton.parentElement);
    expect(cancelButton.compareDocumentPosition(tryAgainButton) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('shows completed state when polling returns completed', async () => {
    // First call returns pending (for initial status check in AgentSetupCard)
    // Second call onwards returns completed (for polling inside modal)
    codexStatusMock
      .mockResolvedValueOnce({ data: { status: 'pending' } })
      .mockResolvedValue({ data: { status: 'completed' } });

    const user = userEvent.setup();
    renderWithProviders(<Overview />);

    await waitFor(() => {
      expect(screen.getByText('Sign in with ChatGPT')).toBeInTheDocument();
    });

    await user.click(screen.getByText('Sign in with ChatGPT'));

    await waitFor(() => {
      expect(screen.getByText('ABCD-1234')).toBeInTheDocument();
    });

    await waitFor(() => {
      expect(screen.getByText('Connected successfully!')).toBeInTheDocument();
    }, { timeout: 10000 });
  });

  it('shows expired state when polling returns expired', async () => {
    codexStatusMock
      .mockResolvedValueOnce({ data: { status: 'pending' } })
      .mockResolvedValue({ data: { status: 'expired' } });

    const user = userEvent.setup();
    renderWithProviders(<Overview />);

    await waitFor(() => {
      expect(screen.getByText('Sign in with ChatGPT')).toBeInTheDocument();
    });

    await user.click(screen.getByText('Sign in with ChatGPT'));

    await waitFor(() => {
      expect(screen.getByText('ABCD-1234')).toBeInTheDocument();
    });

    await waitFor(() => {
      expect(screen.getByText('Code expired. Please try again.')).toBeInTheDocument();
    }, { timeout: 10000 });

    expect(screen.getByRole('button', { name: 'Try Again' })).toBeInTheDocument();
  });

  it('shows error state when polling returns error', async () => {
    codexStatusMock
      .mockResolvedValueOnce({ data: { status: 'pending' } })
      .mockResolvedValue({ data: { status: 'error', message: 'Auth denied' } });

    const user = userEvent.setup();
    renderWithProviders(<Overview />);

    await waitFor(() => {
      expect(screen.getByText('Sign in with ChatGPT')).toBeInTheDocument();
    });

    await user.click(screen.getByText('Sign in with ChatGPT'));

    await waitFor(() => {
      expect(screen.getByText('ABCD-1234')).toBeInTheDocument();
    });

    await waitFor(() => {
      expect(screen.getByText('Auth denied')).toBeInTheDocument();
    }, { timeout: 10000 });
  });

  it('renders the expires timer text in the modal', async () => {
    const user = userEvent.setup();
    renderWithProviders(<Overview />);

    await waitFor(() => {
      expect(screen.getByText('Sign in with ChatGPT')).toBeInTheDocument();
    });

    await user.click(screen.getByText('Sign in with ChatGPT'));

    await waitFor(() => {
      expect(screen.getByText('ABCD-1234')).toBeInTheDocument();
    });

    // The timer should display the expires_in time (900 seconds = 15:00)
    expect(screen.getByText(/Expires in/)).toBeInTheDocument();
  });
});
