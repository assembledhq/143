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
}: {
  team?: UserCredentialSummary[];
  resolved?: ResolvedCredential[];
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

    // Wait for page to render
    await waitFor(() => {
      expect(screen.getByText('Coding agents')).toBeInTheDocument();
    });
    expect(screen.queryByText('Organization coding agents')).not.toBeInTheDocument();
    expect(screen.queryByText('Autonomy level')).not.toBeInTheDocument();
    expect(screen.queryByText('Execution aggressiveness')).not.toBeInTheDocument();
  });

  it('shows single save button for all org settings', async () => {
    renderWithProviders(<AgentPage />);

    await screen.findAllByText('Claude Code');
    expect(screen.getByText('Save organization settings')).toBeInTheDocument();
  });

  it('shows Connected badge when ChatGPT auth is completed', async () => {
    setupHandlers({ team: [], resolved: [] });
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

    expect(await screen.findByText('Connected')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Disconnect' })).toBeInTheDocument();
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
      expect(body.settings).toHaveProperty('default_agent_type');
      expect(body.settings).toHaveProperty('autonomy_level');
      expect(body.settings).toHaveProperty('execution_aggressiveness');
      expect(body.settings).toHaveProperty('max_concurrent_runs');
    });
  });
});
