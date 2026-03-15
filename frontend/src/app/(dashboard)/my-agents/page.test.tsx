import { describe, it, expect, vi, beforeEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, waitFor, userEvent } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import MyAgentsPage from './page';
import type { UserCredentialSummary, ResolvedCredential, ListResponse } from '@/lib/types';

const { useAuthMock } = vi.hoisted(() => ({
  useAuthMock: vi.fn(),
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: useAuthMock,
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
  { provider: 'openrouter', source: 'none' },
];

function setupHandlers({
  personal = mockPersonalCreds,
  team = mockTeamDefaults,
  resolved = mockResolved,
}: {
  personal?: UserCredentialSummary[];
  team?: UserCredentialSummary[];
  resolved?: ResolvedCredential[];
} = {}) {
  server.use(
    http.get('/api/v1/settings/credentials/personal', () => {
      return HttpResponse.json({ data: personal, meta: {} } satisfies ListResponse<UserCredentialSummary>);
    }),
    http.get('/api/v1/settings/credentials/team', () => {
      return HttpResponse.json({ data: team, meta: {} } satisfies ListResponse<UserCredentialSummary>);
    }),
    http.get('/api/v1/settings/credentials/resolved', () => {
      return HttpResponse.json({ data: resolved, meta: {} } satisfies ListResponse<ResolvedCredential>);
    }),
  );
}

describe('MyAgentsPage', () => {
  beforeEach(() => {
    useAuthMock.mockReturnValue({
      user: { id: 'user-1', name: 'Alice Smith', email: 'alice@example.com', role: 'admin' },
      isLoading: false,
      isAuthenticated: true,
    });
    setupHandlers();
  });

  it('renders page header', async () => {
    renderWithProviders(<MyAgentsPage />);
    expect(screen.getByText('My Agents')).toBeInTheDocument();
  });

  it('shows all four provider cards in My Keys tab', async () => {
    renderWithProviders(<MyAgentsPage />);

    expect(await screen.findByText('Anthropic')).toBeInTheDocument();
    expect(screen.getByText('OpenAI')).toBeInTheDocument();
    expect(screen.getByText('Google Gemini')).toBeInTheDocument();
    expect(screen.getByText('OpenRouter')).toBeInTheDocument();
  });

  it('shows Configured badge for providers with keys', async () => {
    renderWithProviders(<MyAgentsPage />);

    expect(await screen.findByText('Configured')).toBeInTheDocument();
  });

  it('shows masked key for configured providers', async () => {
    renderWithProviders(<MyAgentsPage />);

    expect(await screen.findByText('Key: sk-ant-...abc')).toBeInTheDocument();
  });

  it('shows provider descriptions', async () => {
    renderWithProviders(<MyAgentsPage />);

    expect(await screen.findByText('Claude Code (Opus, Sonnet, Haiku)')).toBeInTheDocument();
    expect(screen.getByText('Codex (GPT-5 models)')).toBeInTheDocument();
  });

  it('shows Team Defaults tab for admins', async () => {
    renderWithProviders(<MyAgentsPage />);

    expect(await screen.findByText('Team Defaults')).toBeInTheDocument();
  });

  it('hides Team Defaults tab for non-admins', async () => {
    useAuthMock.mockReturnValue({
      user: { id: 'user-2', name: 'Bob', email: 'bob@example.com', role: 'member' },
      isLoading: false,
      isAuthenticated: true,
    });

    renderWithProviders(<MyAgentsPage />);

    await screen.findByText('Anthropic');
    expect(screen.queryByText('Team Defaults')).not.toBeInTheDocument();
  });

  it('shows Active Config tab', async () => {
    renderWithProviders(<MyAgentsPage />);

    expect(await screen.findByText('Active Config')).toBeInTheDocument();
  });

  it('renders resolved credentials in Active Config tab', async () => {
    const user = userEvent.setup();
    renderWithProviders(<MyAgentsPage />);

    await screen.findByText('Anthropic');
    await user.click(screen.getByText('Active Config'));

    expect(await screen.findByText('Your key')).toBeInTheDocument();
    expect(screen.getByText('Team default')).toBeInTheDocument();
  });

  it('shows Not configured for unconfigured resolved providers', async () => {
    const user = userEvent.setup();
    renderWithProviders(<MyAgentsPage />);

    await screen.findByText('Anthropic');
    await user.click(screen.getByText('Active Config'));

    const notConfigured = await screen.findAllByText('Not configured');
    expect(notConfigured.length).toBeGreaterThanOrEqual(1);
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
    renderWithProviders(<MyAgentsPage />);

    await screen.findByText('Anthropic');

    const inputs = screen.getAllByPlaceholderText('sk-ant-...');
    await user.type(inputs[0], 'sk-ant-newkey123');

    const saveButtons = screen.getAllByText('Save key');
    await user.click(saveButtons[0]);

    await waitFor(() => {
      expect(capturedBody).toBeDefined();
    });
  });

  it('disables Save key button when input is empty', async () => {
    renderWithProviders(<MyAgentsPage />);

    await screen.findByText('Anthropic');

    const saveButtons = screen.getAllByText('Save key');
    expect(saveButtons[0]).toBeDisabled();
  });

  it('shows Remove button for configured providers', async () => {
    renderWithProviders(<MyAgentsPage />);

    expect(await screen.findByText('Remove')).toBeInTheDocument();
  });

  it('shows remove confirmation dialog', async () => {
    const user = userEvent.setup();
    renderWithProviders(<MyAgentsPage />);

    await user.click(await screen.findByText('Remove'));

    expect(await screen.findByText('Remove API key')).toBeInTheDocument();
    expect(screen.getByText('Cancel')).toBeInTheDocument();
  });

  it('shows Set as team default button for admin with configured key', async () => {
    renderWithProviders(<MyAgentsPage />);

    expect(await screen.findByText('Set as team default')).toBeInTheDocument();
  });

  it('shows team default info in Team Defaults tab', async () => {
    const user = userEvent.setup();
    renderWithProviders(<MyAgentsPage />);

    await screen.findByText('Anthropic');
    await user.click(screen.getByText('Team Defaults'));

    expect(await screen.findByText('Active')).toBeInTheDocument();
    expect(screen.getByText('Set by Alice Smith')).toBeInTheDocument();
  });

  it('shows empty state for unconfigured providers without keys', async () => {
    setupHandlers({ personal: [], team: [], resolved: [] });

    renderWithProviders(<MyAgentsPage />);

    await screen.findByText('Anthropic');

    const saveButtons = screen.getAllByText('Save key');
    expect(saveButtons.length).toBe(4);
    saveButtons.forEach((btn) => expect(btn).toBeDisabled());
  });

  it('shows success message after saving key', async () => {
    server.use(
      http.put('/api/v1/settings/credentials/personal/:provider', () => {
        return HttpResponse.json({ data: mockPersonalCreds[0] });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<MyAgentsPage />);

    await screen.findByText('Anthropic');

    const inputs = screen.getAllByPlaceholderText('sk-ant-...');
    await user.type(inputs[0], 'sk-ant-newkey');

    const saveButtons = screen.getAllByText('Save key');
    await user.click(saveButtons[0]);

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
    renderWithProviders(<MyAgentsPage />);

    await screen.findByText('Anthropic');

    const inputs = screen.getAllByPlaceholderText('sk-ant-...');
    await user.type(inputs[0], 'sk-ant-badkey');

    const saveButtons = screen.getAllByText('Save key');
    await user.click(saveButtons[0]);

    expect(await screen.findByText('Failed to save key.')).toBeInTheDocument();
  });
});
