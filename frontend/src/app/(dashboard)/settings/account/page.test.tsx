import { describe, it, expect, vi, beforeEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, waitFor, userEvent } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import AccountPage from './page';
import type { UserCredentialSummary, ResolvedCredential, ListResponse } from '@/lib/types';

const { useAuthMock } = vi.hoisted(() => ({
  useAuthMock: vi.fn(),
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: useAuthMock,
}));

vi.mock('next-themes', () => ({
  useTheme: () => ({ theme: 'system', setTheme: vi.fn() }),
}));

const mockPersonalCreds: UserCredentialSummary[] = [
  {
    provider: 'anthropic',
    configured: true,
    is_team_default: false,
    masked_key: 'sk-ant-...abc',
    status: 'active',
  },
];

const mockResolved: ResolvedCredential[] = [
  { provider: 'anthropic', source: 'personal', masked_key: 'sk-ant-...abc' },
  { provider: 'openai', source: 'team_default', masked_key: 'sk-...def' },
  { provider: 'gemini', source: 'none' },
];

function setupHandlers({
  personal = mockPersonalCreds,
  resolved = mockResolved,
}: {
  personal?: UserCredentialSummary[];
  resolved?: ResolvedCredential[];
} = {}) {
  server.use(
    http.get('/api/v1/settings/credentials/personal', () => {
      return HttpResponse.json({ data: personal, meta: {} } satisfies ListResponse<UserCredentialSummary>);
    }),
    http.get('/api/v1/settings/credentials/resolved', () => {
      return HttpResponse.json({ data: resolved, meta: {} } satisfies ListResponse<ResolvedCredential>);
    }),
    http.get('/api/v1/github/status', () => {
      return HttpResponse.json({ connected: false, has_repo_scope: false });
    }),
    http.get('/api/v1/settings/codex-auth/status', () => {
      return HttpResponse.json({ data: { status: 'none' } });
    }),
  );
}

describe('AccountPage', () => {
  beforeEach(() => {
    useAuthMock.mockReturnValue({
      user: { id: 'user-1', name: 'Alice Smith', email: 'alice@example.com', role: 'admin' },
      isLoading: false,
      isAuthenticated: true,
    });
    setupHandlers();
  });

  it('renders page header', async () => {
    renderWithProviders(<AccountPage />);
    expect(screen.getByText('Account')).toBeInTheDocument();
    expect(screen.getByText('Your personal preferences and credentials.')).toBeInTheDocument();
  });

  it('renders the Appearance section with theme selector', async () => {
    renderWithProviders(<AccountPage />);

    await waitFor(() => {
      expect(screen.getByText('Appearance')).toBeInTheDocument();
    });

    expect(screen.getByText('Theme')).toBeInTheDocument();
    expect(screen.getByText('Select your preferred color scheme')).toBeInTheDocument();
  });

  it('renders the GitHub PR connection section', async () => {
    renderWithProviders(<AccountPage />);

    expect(await screen.findByText('GitHub connection for PRs')).toBeInTheDocument();
    expect(screen.getByText('Connect GitHub')).toBeInTheDocument();
  });

  it('renders the coding agent credentials section', async () => {
    renderWithProviders(<AccountPage />);

    expect(await screen.findByText('Coding agent credentials')).toBeInTheDocument();
    expect(screen.getByText('Your personal API keys. Personal keys are used first, falling back to organization defaults.')).toBeInTheDocument();
  });

  it('shows 3 agent type radio cards', async () => {
    renderWithProviders(<AccountPage />);

    const claudeLabels = await screen.findAllByText('Claude Code');
    expect(claudeLabels.length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText('Codex')).toBeInTheDocument();
    expect(screen.getByText('Gemini CLI')).toBeInTheDocument();
  });

  it('shows Configured badge for providers with keys', async () => {
    renderWithProviders(<AccountPage />);

    expect(await screen.findByText('Configured')).toBeInTheDocument();
  });

  it('shows masked key for configured providers', async () => {
    renderWithProviders(<AccountPage />);

    expect(await screen.findByText('Key: sk-ant-...abc')).toBeInTheDocument();
  });

  it('shows resolution source badges', async () => {
    renderWithProviders(<AccountPage />);

    expect(await screen.findByText('Your key')).toBeInTheDocument();
  });

  it('saves a new API key', async () => {
    let capturedBody: unknown;
    server.use(
      http.put('/api/v1/settings/credentials/personal/:provider', async ({ request }) => {
        capturedBody = await request.json();
        return HttpResponse.json({ data: mockPersonalCreds[0] });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<AccountPage />);

    await screen.findByText('Key: sk-ant-...abc');

    const input = screen.getByPlaceholderText('Replace existing key...');
    await user.type(input, 'sk-ant-newkey123');

    const saveButton = screen.getByText('Save key');
    await user.click(saveButton);

    await waitFor(() => {
      expect(capturedBody).toBeDefined();
    });
  });

  it('disables Save key button when input is empty', async () => {
    renderWithProviders(<AccountPage />);

    const saveButton = await screen.findByText('Save key');
    expect(saveButton).toBeDisabled();
  });

  it('shows Remove button for configured providers', async () => {
    renderWithProviders(<AccountPage />);

    expect(await screen.findByText('Remove')).toBeInTheDocument();
  });

  it('shows remove confirmation dialog', async () => {
    const user = userEvent.setup();
    renderWithProviders(<AccountPage />);

    await user.click(await screen.findByText('Remove'));

    expect(await screen.findByText('Remove API key')).toBeInTheDocument();
    expect(screen.getByText('Cancel')).toBeInTheDocument();
  });

  it('shows Set as team default button for admin with configured key', async () => {
    renderWithProviders(<AccountPage />);

    expect(await screen.findByText('Set as team default')).toBeInTheDocument();
  });

  it('shows success message after saving key', async () => {
    server.use(
      http.put('/api/v1/settings/credentials/personal/:provider', () => {
        return HttpResponse.json({ data: mockPersonalCreds[0] });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<AccountPage />);

    await screen.findByText('Key: sk-ant-...abc');

    const input = screen.getByPlaceholderText('Replace existing key...');
    await user.type(input, 'sk-ant-newkey');

    const saveButton = screen.getByText('Save key');
    await user.click(saveButton);

    expect(await screen.findByText('Key saved successfully.')).toBeInTheDocument();
  });

  it('shows error message when save fails', async () => {
    server.use(
      http.put('/api/v1/settings/credentials/personal/:provider', () => {
        return HttpResponse.json(
          { error: { code: 'INTERNAL', message: 'Server error' } },
          { status: 500 },
        );
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<AccountPage />);

    await screen.findByText('Key: sk-ant-...abc');

    const input = screen.getByPlaceholderText('Replace existing key...');
    await user.type(input, 'sk-ant-badkey');

    const saveButton = screen.getByText('Save key');
    await user.click(saveButton);

    expect(await screen.findByText('Failed to save key.')).toBeInTheDocument();
  });

  it('switches agent credential card when selecting a different radio', async () => {
    renderWithProviders(<AccountPage />);

    expect(await screen.findByText('Key: sk-ant-...abc')).toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByText('Codex'));

    await waitFor(() => {
      expect(screen.queryByText('Key: sk-ant-...abc')).not.toBeInTheDocument();
    });
    expect(screen.getByPlaceholderText('sk-...')).toBeInTheDocument();
  });

  it('auto-defaults to the first configured provider', async () => {
    setupHandlers({
      personal: [
        { provider: 'openai', configured: true, is_team_default: false, masked_key: 'sk-...xyz', status: 'active' },
      ],
      resolved: [
        { provider: 'openai', source: 'personal', masked_key: 'sk-...xyz' },
      ],
    });

    renderWithProviders(<AccountPage />);

    expect(await screen.findByText('Key: sk-...xyz')).toBeInTheDocument();
  });

  it('defaults to Codex when no provider has a configured key', async () => {
    setupHandlers({ personal: [], resolved: [] });

    renderWithProviders(<AccountPage />);

    expect(await screen.findByText('Credential method')).toBeInTheDocument();
    expect(screen.getByLabelText('Use API key')).toBeInTheDocument();
    expect(screen.getByLabelText('Sign in with ChatGPT')).toBeInTheDocument();
  });

  it('hides API key input when ChatGPT method is selected for Codex', async () => {
    setupHandlers({ personal: [], resolved: [] });

    renderWithProviders(<AccountPage />);

    const apiKeyInput = await screen.findByPlaceholderText('sk-...');
    expect(apiKeyInput).toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByLabelText('Sign in with ChatGPT'));

    await waitFor(() => {
      expect(screen.queryByPlaceholderText('sk-...')).not.toBeInTheDocument();
    });
    expect(screen.getByText('API key fields are hidden while ChatGPT sign-in is selected.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Sign in with ChatGPT' })).toBeInTheDocument();
  });

  it('shows Pi card with Ready-to-run badge when an inherited provider is configured', async () => {
    const user = userEvent.setup();
    renderWithProviders(<AccountPage />);

    const piLabels = await screen.findAllByText('Pi');
    expect(piLabels.length).toBeGreaterThanOrEqual(1);

    await user.click(piLabels[0]);

    expect(await screen.findByText('Ready to run')).toBeInTheDocument();
    // Anthropic row shows the masked key from mockPersonalCreds with its own Remove.
    expect(screen.getByText('sk-ant-...abc')).toBeInTheDocument();
    // OpenAI and Gemini rows render inline API key inputs (Codex has no
    // personal key in the default fixture, so a Save button is present).
    expect(screen.getAllByRole('button', { name: /^Save$/i }).length).toBeGreaterThanOrEqual(1);
  });

  it('lets the user save an inherited provider key inline from the Pi card', async () => {
    setupHandlers({ personal: [], resolved: [] });
    let capturedProvider: string | null = null;
    server.use(
      http.put('/api/v1/settings/credentials/personal/:provider', async ({ params }) => {
        capturedProvider = params.provider as string;
        return HttpResponse.json({
          data: { provider: capturedProvider, configured: true, is_team_default: false, masked_key: 'sk-ant-new', status: 'active' },
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<AccountPage />);

    const piLabels = await screen.findAllByText('Pi');
    await user.click(piLabels[0]);

    const anthropicInput = await screen.findByLabelText('Claude Code API key');
    await user.type(anthropicInput, 'sk-ant-typed');

    // Save the Anthropic-row key (the first Save is the Claude Code row).
    const saveButtons = screen.getAllByRole('button', { name: /^Save$/i });
    await user.click(saveButtons[0]);

    await waitFor(() => {
      expect(capturedProvider).toBe('anthropic');
    });
  });

  it('warns on Pi card when no inherited provider keys are configured', async () => {
    setupHandlers({ personal: [], resolved: [] });
    const user = userEvent.setup();
    renderWithProviders(<AccountPage />);

    const piLabels = await screen.findAllByText('Pi');
    await user.click(piLabels[0]);

    expect(await screen.findByText('Add a key to run')).toBeInTheDocument();
    // All three provider rows should render their API key inputs inline.
    expect(screen.getByLabelText('Claude Code API key')).toBeInTheDocument();
    expect(screen.getByLabelText('Codex API key')).toBeInTheDocument();
    expect(screen.getByLabelText('Gemini CLI API key')).toBeInTheDocument();
  });

  it('hides Set as team default for non-admin users', async () => {
    useAuthMock.mockReturnValue({
      user: { id: 'user-2', name: 'Bob', email: 'bob@example.com', role: 'member' },
      isLoading: false,
      isAuthenticated: true,
    });

    renderWithProviders(<AccountPage />);

    await screen.findByText('Key: sk-ant-...abc');
    expect(screen.queryByText('Set as team default')).not.toBeInTheDocument();
  });
});
