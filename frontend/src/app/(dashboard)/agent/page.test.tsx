import { describe, it, expect, vi, beforeEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, waitFor, userEvent } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import AgentPage from './page';
import type { UserCredentialSummary, ResolvedCredential, ListResponse, Organization, SingleResponse } from '@/lib/types';

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
    http.get('/api/v1/settings', () => {
      return HttpResponse.json(mockOrgSettings);
    }),
    http.get('/api/v1/settings/agent-defaults', () => {
      return HttpResponse.json({ data: {} });
    }),
    http.get('/api/v1/settings/codex-auth/status', () => {
      return HttpResponse.json({ data: { status: 'none' } });
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

  it('shows 3 agent type radio cards in My coding agents section', async () => {
    renderWithProviders(<AgentPage />);

    // Both personal and org sections use the same 3 agent types
    const claudeLabels = await screen.findAllByText('Claude Code');
    expect(claudeLabels.length).toBeGreaterThanOrEqual(1);
    const codexLabels = screen.getAllByText('Codex');
    expect(codexLabels.length).toBeGreaterThanOrEqual(1);
    const geminiLabels = screen.getAllByText('Gemini CLI');
    expect(geminiLabels.length).toBeGreaterThanOrEqual(1);
  });

  it('shows Configured badge for providers with keys', async () => {
    renderWithProviders(<AgentPage />);

    expect(await screen.findByText('Configured')).toBeInTheDocument();
  });

  it('shows masked key for configured providers', async () => {
    renderWithProviders(<AgentPage />);

    expect(await screen.findByText('Key: sk-ant-...abc')).toBeInTheDocument();
  });

  it('shows resolution source badges', async () => {
    renderWithProviders(<AgentPage />);

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
    renderWithProviders(<AgentPage />);

    // Wait for personal creds to load
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
    renderWithProviders(<AgentPage />);

    await screen.findAllByText('Claude Code');

    // Default view shows Claude Code, which has a configured key
    const saveButton = screen.getByText('Save key');
    expect(saveButton).toBeDisabled();
  });

  it('shows Remove button for configured providers', async () => {
    renderWithProviders(<AgentPage />);

    expect(await screen.findByText('Remove')).toBeInTheDocument();
  });

  it('shows remove confirmation dialog', async () => {
    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    await user.click(await screen.findByText('Remove'));

    expect(await screen.findByText('Remove API key')).toBeInTheDocument();
    expect(screen.getByText('Cancel')).toBeInTheDocument();
  });

  it('shows Set as team default button for admin with configured key', async () => {
    renderWithProviders(<AgentPage />);

    expect(await screen.findByText('Set as team default')).toBeInTheDocument();
  });

  it('shows success message after saving key', async () => {
    server.use(
      http.put('/api/v1/settings/credentials/personal/:provider', () => {
        return HttpResponse.json({ data: mockPersonalCreds[0] });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    // Wait for personal creds to load
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
    renderWithProviders(<AgentPage />);

    // Wait for personal creds to load
    await screen.findByText('Key: sk-ant-...abc');

    const input = screen.getByPlaceholderText('Replace existing key...');
    await user.type(input, 'sk-ant-badkey');

    const saveButton = screen.getByText('Save key');
    await user.click(saveButton);

    expect(await screen.findByText('Failed to save key.')).toBeInTheDocument();
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

  it('shows Execution section for admins', async () => {
    renderWithProviders(<AgentPage />);

    expect(await screen.findByText('Autonomy level')).toBeInTheDocument();
    expect(screen.getByText('Execution aggressiveness')).toBeInTheDocument();
    expect(screen.getByText('Max concurrent runs')).toBeInTheDocument();
  });

  it('hides org and execution sections for non-admins', async () => {
    useAuthMock.mockReturnValue({
      user: { id: 'user-2', name: 'Bob', email: 'bob@example.com', role: 'member' },
      isLoading: false,
      isAuthenticated: true,
    });

    renderWithProviders(<AgentPage />);

    await screen.findAllByText('Claude Code');
    expect(screen.queryByText('Organization coding agents')).not.toBeInTheDocument();
    expect(screen.queryByText('Autonomy level')).not.toBeInTheDocument();
    expect(screen.queryByText('Execution aggressiveness')).not.toBeInTheDocument();
  });

  it('shows single save button for all org settings', async () => {
    renderWithProviders(<AgentPage />);

    await screen.findAllByText('Claude Code');
    expect(screen.getByText('Save organization settings')).toBeInTheDocument();
  });

  it('shows empty state for unconfigured providers', async () => {
    setupHandlers({ personal: [], team: [], resolved: [] });

    renderWithProviders(<AgentPage />);

    await screen.findAllByText('Claude Code');

    // Only 1 save button visible (for the selected agent in personal section)
    const saveButton = screen.getByText('Save key');
    expect(saveButton).toBeDisabled();
  });

  it('saves org settings with single mutation', async () => {
    let capturedBody: unknown;
    server.use(
      http.patch('/api/v1/settings', async ({ request }) => {
        capturedBody = await request.json();
        return HttpResponse.json(mockOrgSettings);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    await screen.findByText('Save organization settings');
    await user.click(screen.getByText('Save organization settings'));

    await waitFor(() => {
      expect(capturedBody).toBeDefined();
      const body = capturedBody as Record<string, Record<string, unknown>>;
      // Should contain both agent config and execution settings in one payload
      expect(body.settings).toHaveProperty('default_agent_type');
      expect(body.settings).toHaveProperty('autonomy_level');
      expect(body.settings).toHaveProperty('execution_aggressiveness');
      expect(body.settings).toHaveProperty('max_concurrent_runs');
    });
  });
});
