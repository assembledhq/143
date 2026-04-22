import { beforeEach, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import AgentPage from "./page";
import type {
  ClaudeCodeSubscription,
  CodexSubscription,
  ListResponse,
  Organization,
  ResolvedCredential,
  SingleResponse,
  UserCredentialSummary,
} from "@/lib/types";

const { useAuthMock } = vi.hoisted(() => ({
  useAuthMock: vi.fn(),
}));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: useAuthMock,
}));

const mockTeamDefaults: UserCredentialSummary[] = [
  {
    provider: "anthropic",
    configured: true,
    is_team_default: true,
    masked_key: "sk-ant-...xyz",
    set_by_user_name: "Alice Smith",
    status: "active",
  },
];

const mockResolved: ResolvedCredential[] = [
  { provider: "anthropic", source: "personal", masked_key: "sk-ant-...abc" },
  { provider: "openai", source: "team_default", masked_key: "sk-...def" },
  { provider: "gemini", source: "none" },
];

const mockOrgSettings: SingleResponse<Organization> = {
  data: {
    id: "org-1",
    name: "Test Org",
    settings: {
      autonomy_level: "auto_simple",
      execution_aggressiveness: 2,
      max_concurrent_runs: 5,
      default_agent_type: "claude_code",
    },
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
};

function setupHandlers({
  team = mockTeamDefaults,
  resolved = mockResolved,
  codexSubscriptions = [] as CodexSubscription[],
  claudeSubscriptions = [] as ClaudeCodeSubscription[],
  settings = mockOrgSettings,
}: {
  team?: UserCredentialSummary[];
  resolved?: ResolvedCredential[];
  codexSubscriptions?: CodexSubscription[];
  claudeSubscriptions?: ClaudeCodeSubscription[];
  settings?: SingleResponse<Organization>;
} = {}) {
  server.use(
    http.get("/api/v1/settings/credentials/team", () => {
      return HttpResponse.json({ data: team, meta: {} } satisfies ListResponse<UserCredentialSummary>);
    }),
    http.get("/api/v1/settings/credentials/resolved", () => {
      return HttpResponse.json({ data: resolved, meta: {} } satisfies ListResponse<ResolvedCredential>);
    }),
    http.get("/api/v1/settings", () => {
      return HttpResponse.json(settings);
    }),
    http.get("/api/v1/settings/codex-auth/status", () => {
      return HttpResponse.json({ data: { status: "none" } });
    }),
    http.get("/api/v1/settings/codex-auth/subscriptions", () => {
      return HttpResponse.json({ data: codexSubscriptions, meta: {} } satisfies ListResponse<CodexSubscription>);
    }),
    http.get("/api/v1/settings/claude-code-auth/subscriptions", () => {
      return HttpResponse.json({ data: claudeSubscriptions, meta: {} } satisfies ListResponse<ClaudeCodeSubscription>);
    }),
  );
}

describe("AgentPage", () => {
  beforeEach(() => {
    useAuthMock.mockReturnValue({
      user: { id: "user-1", name: "Alice Smith", email: "alice@example.com", role: "admin" },
      isLoading: false,
      isAuthenticated: true,
    });
    setupHandlers();
  });

  it("renders the page header and agent catalog", async () => {
    renderWithProviders(<AgentPage />);

    expect(screen.getByText("Coding agents")).toBeInTheDocument();
    expect(await screen.findByText("Available coding agents")).toBeInTheDocument();
    expect(screen.getByText("Selected agent")).toBeInTheDocument();
    expect(screen.getByText("Execution")).toBeInTheDocument();
    expect(screen.queryByText("OpenAI Codex (GPT-5 models)")).not.toBeInTheDocument();
    expect(screen.queryByText("Anthropic Claude (Opus, Sonnet, Haiku)")).not.toBeInTheDocument();
    expect(screen.queryByText("Google Gemini (Pro, Flash)")).not.toBeInTheDocument();
  });

  it("hides organization and execution sections for non-admins", async () => {
    useAuthMock.mockReturnValue({
      user: { id: "user-2", name: "Bob", email: "bob@example.com", role: "member" },
      isLoading: false,
      isAuthenticated: true,
    });

    renderWithProviders(<AgentPage />);

    await waitFor(() => {
      expect(screen.getByText("Coding agents")).toBeInTheDocument();
    });
    expect(screen.queryByText("Available coding agents")).not.toBeInTheDocument();
    expect(screen.queryByText("Execution")).not.toBeInTheDocument();
  });

  it("autosaves the default agent when a different catalog card is selected", async () => {
    let capturedBody: unknown;
    server.use(
      http.patch("/api/v1/settings", async ({ request }) => {
        capturedBody = await request.json();
        return HttpResponse.json(mockOrgSettings);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    await user.click(await screen.findByRole("radio", { name: /Codex/ }));

    await waitFor(() => {
      expect(capturedBody).toEqual({
        settings: { default_agent_type: "codex" },
      });
    });
  });

  it("shows an empty-state-first flow for Claude when no subscriptions are connected", async () => {
    setupHandlers({
      team: [],
      resolved: [],
    });

    renderWithProviders(<AgentPage />);

    expect(await screen.findByText("No Claude Code subscriptions connected yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add Claude subscription" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add API key fallback" })).toBeInTheDocument();
    expect(screen.getByText("Optional: add an API key too as a fallback source.")).toBeInTheDocument();
  });

  it("treats a team default as a configured API-key fallback", async () => {
    renderWithProviders(<AgentPage />);

    expect(await screen.findByText("Team default set")).toBeInTheDocument();
    expect(screen.getByText("API key fallback is configured via team default.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Manage API key & settings" })).toBeInTheDocument();
  });

  it("shows a credential summary and subscription table when Codex has subscriptions and an API key", async () => {
    setupHandlers({
      team: [],
      resolved: [],
      codexSubscriptions: [
        {
          id: "sub-1",
          label: "Team A",
          status: "active",
          account_type: "pro",
          last_used_at: "2026-04-21T20:00:00Z",
        },
        {
          id: "sub-2",
          label: "Team B",
          status: "invalid",
          account_type: "plus",
        },
      ],
      settings: {
        data: {
          ...mockOrgSettings.data,
          settings: {
            ...mockOrgSettings.data.settings,
            default_agent_type: "codex",
            agent_config: {
              codex: {
                OPENAI_API_KEY: "sk-test",
                OPENAI_MODEL: "gpt-5.3-codex",
              },
            },
          },
        },
      },
    });
    server.use(
      http.get("/api/v1/settings/codex-auth/status", () => {
        return HttpResponse.json({ data: { status: "completed" } });
      }),
    );

    renderWithProviders(<AgentPage />);

    expect(await screen.findByText("Default model:")).toBeInTheDocument();
    expect(screen.getByText("Team A")).toBeInTheDocument();
    expect(screen.getByText("Team B")).toBeInTheDocument();
    expect(screen.getByText(/API key fallback:/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Manage subscriptions" })).toBeInTheDocument();
  });

  it("opens the subscriptions management modal", async () => {
    setupHandlers({
      team: [],
      resolved: [],
      claudeSubscriptions: [
        {
          id: "claude-1",
          label: "Alice Smith",
          status: "active",
          account_type: "max",
        },
      ],
    });

    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    await user.click(await screen.findByRole("button", { name: "Manage subscriptions" }));

    expect(await screen.findByText("Manage Claude Code subscriptions")).toBeInTheDocument();
    expect(screen.getAllByText("Alice Smith").length).toBeGreaterThanOrEqual(1);
    expect(screen.getByRole("button", { name: "Add subscription" })).toBeInTheDocument();
  });

  it("resumes a pending Claude subscription with its existing label", async () => {
    let initiatedLabel = "";

    setupHandlers({
      team: [],
      resolved: [],
      claudeSubscriptions: [
        {
          id: "claude-1",
          label: "Alice Smith",
          status: "pending_auth",
          account_type: "max",
        },
      ],
    });

    server.use(
      http.post("/api/v1/settings/claude-code-auth/initiate", async ({ request }) => {
        const body = (await request.json()) as { label?: string };
        initiatedLabel = body.label ?? "";
        return HttpResponse.json({
          data: {
            authorize_url: "https://claude.ai/oauth/authorize",
            state: "state-123",
            label: "Alice Smith",
          },
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    await user.click(await screen.findByRole("button", { name: "Manage subscriptions" }));
    await user.click(screen.getByRole("button", { name: "Resume setup Alice Smith" }));

    await waitFor(() => {
      expect(initiatedLabel).toBe("Alice Smith");
    });
  });

  it("opens the API key and settings modal for the selected agent", async () => {
    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    await user.click(await screen.findByRole("button", { name: "Add API key fallback" }));

    expect(await screen.findByText("Claude Code API key & settings")).toBeInTheDocument();
    expect(screen.getByText("API Key")).toBeInTheDocument();
    expect(screen.getByText("Default model")).toBeInTheDocument();
  });

  it("shows API-key-only messaging for Gemini CLI", async () => {
    setupHandlers({
      team: [],
      resolved: [],
      settings: {
        data: {
          ...mockOrgSettings.data,
          settings: { ...mockOrgSettings.data.settings, default_agent_type: "gemini_cli" },
        },
      },
    });

    renderWithProviders(<AgentPage />);

    expect(await screen.findByText("This agent uses API-key credentials only.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Manage API key & settings" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Manage subscriptions" })).not.toBeInTheDocument();
  });

  it("keeps the execution controls available", async () => {
    const user = userEvent.setup();
    renderWithProviders(<AgentPage />);

    const input = await screen.findByLabelText("Max concurrent runs");
    await user.clear(input);
    await user.type(input, "8");

    expect(input).toHaveValue(8);
    expect(screen.getByText("Autonomy level")).toBeInTheDocument();
    expect(screen.getByText("Execution aggressiveness")).toBeInTheDocument();
  });
});
