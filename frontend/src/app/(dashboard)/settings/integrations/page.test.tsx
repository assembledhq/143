import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, waitFor, within, userEvent } from "@/test/test-utils";
import IntegrationsPage from "./page";

const {
  integrationsListMock,
  repositoriesListMock,
  listGitHubRepositoriesMock,
  claimGitHubRepositoriesMock,
  linearAgentStatusMock,
  linearAgentMappingsMock,
  updateLinearAgentMock,
  upsertLinearAgentMappingMock,
  deleteLinearAgentMappingMock,
  getSlackHealthMock,
  getSlackSettingsMock,
  updateSlackSettingsMock,
  listSlackUserLinksMock,
  upsertSlackUserLinkMock,
  deleteSlackUserLinkMock,
  listSlackChannelsMock,
  updateSlackChannelsMock,
  updateSlackChannelSettingsMock,
  listPagerDutyMock,
  updatePagerDutyMock,
  testPagerDutyMock,
  listPagerDutyMappingsMock,
  upsertPagerDutyMappingMock,
  listPagerDutyIncidentsMock,
  startPagerDutyIncidentSessionMock,
  loginPagerDutyMock,
  teamListMembersMock,
  auditLogsListMock,
  githubConnectMock,
  githubStatusGetMock,
  githubDisconnectMock,
  currentUserMock,
  ApiErrorMock,
  routerReplaceMock,
  notifySuccessMock,
  navState,
} = vi.hoisted(() => {
  class MockApiError extends Error {
    constructor(public code: string, message: string, public details?: unknown) {
      super(message);
      this.name = "ApiError";
    }
  }

  return {
    integrationsListMock: vi.fn(),
    repositoriesListMock: vi.fn(),
    listGitHubRepositoriesMock: vi.fn(),
    claimGitHubRepositoriesMock: vi.fn(),
    linearAgentStatusMock: vi.fn(),
    linearAgentMappingsMock: vi.fn(),
    updateLinearAgentMock: vi.fn(),
    upsertLinearAgentMappingMock: vi.fn(),
    deleteLinearAgentMappingMock: vi.fn(),
    getSlackHealthMock: vi.fn(),
    getSlackSettingsMock: vi.fn(),
    updateSlackSettingsMock: vi.fn(),
    listSlackUserLinksMock: vi.fn(),
    upsertSlackUserLinkMock: vi.fn(),
    deleteSlackUserLinkMock: vi.fn(),
    listSlackChannelsMock: vi.fn(),
    updateSlackChannelsMock: vi.fn(),
    updateSlackChannelSettingsMock: vi.fn(),
    listPagerDutyMock: vi.fn(),
    updatePagerDutyMock: vi.fn(),
    testPagerDutyMock: vi.fn(),
    listPagerDutyMappingsMock: vi.fn(),
    upsertPagerDutyMappingMock: vi.fn(),
    listPagerDutyIncidentsMock: vi.fn(),
    startPagerDutyIncidentSessionMock: vi.fn(),
    loginPagerDutyMock: vi.fn(),
    teamListMembersMock: vi.fn(),
    auditLogsListMock: vi.fn(),
    githubConnectMock: vi.fn(),
    githubStatusGetMock: vi.fn(),
    githubDisconnectMock: vi.fn(),
    currentUserMock: {
      id: "user-1",
      email: "admin@example.com",
      name: "Admin User",
      role: "admin",
    },
    ApiErrorMock: MockApiError,
    routerReplaceMock: vi.fn(),
    notifySuccessMock: vi.fn(),
    navState: { params: {} as Record<string, string> },
  };
});

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: routerReplaceMock, push: vi.fn() }),
  useSearchParams: () => ({ get: (key: string) => navState.params[key] ?? null }),
}));

vi.mock("@/lib/notify", () => ({
  notify: {
    success: notifySuccessMock,
    info: vi.fn(),
    warning: vi.fn(),
    error: vi.fn(),
    dismiss: vi.fn(),
  },
}));

vi.mock("@/lib/api", () => ({
  ApiError: ApiErrorMock,
  api: {
    auth: {
      loginSentry: vi.fn(),
    },
    integrations: {
      list: integrationsListMock,
      loginGitHub: vi.fn(),
      loginLinear: vi.fn(),
      loginSlack: vi.fn(),
      connectNotion: vi.fn(),
      connectCircleCI: vi.fn(),
      disconnect: vi.fn(),
      listGitHubRepositories: listGitHubRepositoriesMock,
      claimGitHubRepositories: claimGitHubRepositoriesMock,
      getLinearAgentStatus: linearAgentStatusMock,
      listLinearAgentMappings: linearAgentMappingsMock,
      updateLinearAgentSettings: updateLinearAgentMock,
      upsertLinearAgentMapping: upsertLinearAgentMappingMock,
      deleteLinearAgentMapping: deleteLinearAgentMappingMock,
      getSlackHealth: getSlackHealthMock,
      getSlackSettings: getSlackSettingsMock,
      updateSlackSettings: updateSlackSettingsMock,
      listSlackUserLinks: listSlackUserLinksMock,
      upsertSlackUserLink: upsertSlackUserLinkMock,
      deleteSlackUserLink: deleteSlackUserLinkMock,
      listSlackChannels: listSlackChannelsMock,
      updateSlackChannels: updateSlackChannelsMock,
      updateSlackChannelSettings: updateSlackChannelSettingsMock,
      listPagerDuty: listPagerDutyMock,
      updatePagerDuty: updatePagerDutyMock,
      testPagerDuty: testPagerDutyMock,
      listPagerDutyMappings: listPagerDutyMappingsMock,
      upsertPagerDutyMapping: upsertPagerDutyMappingMock,
      listPagerDutyIncidents: listPagerDutyIncidentsMock,
      startPagerDutyIncidentSession: startPagerDutyIncidentSessionMock,
      loginPagerDuty: loginPagerDutyMock,
    },
    repositories: {
      list: repositoriesListMock,
      disconnect: vi.fn(),
    },
    githubStatus: {
      connect: githubConnectMock,
      get: githubStatusGetMock,
      disconnect: githubDisconnectMock,
    },
    team: {
      listMembers: teamListMembersMock,
    },
    auditLogs: {
      list: auditLogsListMock,
    },
  },
}));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    user: currentUserMock,
    isLoading: false,
  }),
}));

describe("IntegrationsPage", () => {
  beforeEach(() => {
    auditLogsListMock.mockResolvedValue({ data: [], meta: {} });
    integrationsListMock.mockResolvedValue({
      data: [
        {
          id: "integration-1",
          org_id: "org-1",
          provider: "github",
          status: "active",
          github_app_installed: true,
          github_installation_id: 12345,
          github_account_login: "acme",
          github_repo_selection_required: true,
          github_active_repo_count: 0,
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    repositoriesListMock.mockResolvedValue({ data: [], meta: {} });
    listGitHubRepositoriesMock.mockResolvedValue({
      data: [
        {
          github_id: 67890,
          full_name: "acme/api",
          default_branch: "main",
          private: true,
          clone_url: "https://github.com/acme/api.git",
          installation_id: 12345,
          status: "unclaimed",
          can_transfer: false,
        },
      ],
      meta: {},
    });
    linearAgentStatusMock.mockResolvedValue({
      data: {
        enabled: true,
        agent_scopes_granted: true,
        app_user_name: "143",
        has_linear_integration: true,
        default_repo_id: "repo-1",
        available_teams: [],
      },
    });
    linearAgentMappingsMock.mockResolvedValue({ data: [], meta: {} });
    updateLinearAgentMock.mockResolvedValue(undefined);
    upsertLinearAgentMappingMock.mockResolvedValue({ data: {}, meta: {} });
    deleteLinearAgentMappingMock.mockResolvedValue(undefined);
    getSlackHealthMock.mockResolvedValue({
      data: {
        installation: {
          id: "slack-install-1",
          org_id: "org-1",
          team_id: "T123",
          team_name: "Acme",
          bot_user_id: "U143",
          scope: ["chat:write"],
          status: "active",
          updated_at: "2026-01-01T00:00:00Z",
        },
        required_scopes: ["chat:write"],
        missing_scopes: [],
        auth_ok: true,
      },
    });
    getSlackSettingsMock.mockResolvedValue({
      data: {
        org_id: "org-1",
        slack_installation_id: "slack-install-1",
        default_repository_id: "repo-1",
        default_branch: "main",
        routing_mode: "auto",
        response_visibility: "thread",
        allowed_actions: ["session", "preview"],
        notification_preset: "balanced",
        active: true,
      },
    });
    updateSlackSettingsMock.mockResolvedValue({ data: {} });
    listSlackUserLinksMock.mockResolvedValue({ data: [], meta: {} });
    upsertSlackUserLinkMock.mockResolvedValue({ data: {}, meta: {} });
    deleteSlackUserLinkMock.mockResolvedValue(undefined);
    teamListMembersMock.mockResolvedValue({
      data: [
        {
          id: "user-1",
          org_id: "org-1",
          email: "admin@example.com",
          name: "Admin User",
          role: "admin",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    listSlackChannelsMock.mockResolvedValue({ data: [] });
    updateSlackChannelsMock.mockResolvedValue(undefined);
    updateSlackChannelSettingsMock.mockResolvedValue({ data: {} });
    listPagerDutyMock.mockResolvedValue({ data: [], meta: {} });
    updatePagerDutyMock.mockResolvedValue({ data: {}, meta: {} });
    testPagerDutyMock.mockResolvedValue({
      data: {
        integration: {
          id: "pd-1",
          org_id: "org-1",
          provider: "pagerduty",
          integration_id: "pagerduty-1",
          status: "active",
          writeback_enabled: true,
          auto_create_webhook: false,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
        credential_configured: true,
        auth_ok: true,
        webhook_secret_configured: true,
        recent_webhook_failures: 0,
        writeback_enabled: true,
        auto_create_webhook: false,
        last_health_check_at: "2026-01-02T00:00:00Z",
        last_synced_at: "2026-01-02T00:00:00Z",
        symptoms: [],
      },
    });
    listPagerDutyMappingsMock.mockResolvedValue({ data: [], meta: {} });
    upsertPagerDutyMappingMock.mockResolvedValue({ data: {}, meta: {} });
    listPagerDutyIncidentsMock.mockResolvedValue({ data: [], meta: {} });
    startPagerDutyIncidentSessionMock.mockResolvedValue({
      data: {
        id: "session-1",
        org_id: "org-1",
        repository_id: "repo-1",
        title: "PagerDuty incident session",
        status: "pending",
        created_at: "2026-01-01T00:00:00Z",
        updated_at: "2026-01-01T00:00:00Z",
      },
    });
    claimGitHubRepositoriesMock.mockRejectedValue(
      new ApiErrorMock("GITHUB_USER_AUTH_REQUIRED", "Connect your GitHub account before claiming repositories"),
    );
    githubConnectMock.mockClear();
    loginPagerDutyMock.mockClear();
    githubStatusGetMock.mockResolvedValue({
      connected: false,
      has_repo_scope: false,
      pr_authorship_mode: "user_preferred",
      pr_draft_default: false,
      account_requirement: "recommended",
      needs_reconnect: false,
    });
    githubDisconnectMock.mockResolvedValue(undefined);
    currentUserMock.role = "admin";
    routerReplaceMock.mockReset();
    notifySuccessMock.mockReset();
    navState.params = {};
  });

  it("starts PagerDuty OAuth from the connect card", async () => {
    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Connect PagerDuty" }));

    expect(loginPagerDutyMock).toHaveBeenCalledTimes(1);
    expect(screen.queryByLabelText("OAuth Access Token")).not.toBeInTheDocument();
  });

  it("offers a GitHub user-auth connect action when repo claiming requires it", async () => {
    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage GitHub" }));
    await screen.findByText("acme/api");
    await user.click(screen.getByRole("button", { name: "Claim" }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Connect GitHub account" })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Connect GitHub account" }));
    expect(githubConnectMock).toHaveBeenCalledTimes(1);
  });

  it("confirms a returning GitHub account connection and clears the one-shot param", async () => {
    navState.params = { github_pr: "connected" };
    renderWithProviders(<IntegrationsPage />);

    await waitFor(() => {
      expect(notifySuccessMock).toHaveBeenCalledWith(
        "GitHub account connected",
        expect.objectContaining({ description: expect.stringContaining("linked") }),
      );
    });
    expect(routerReplaceMock).toHaveBeenCalledWith("/settings/integrations");
  });

  it("does not fire a connection toast on a normal visit", async () => {
    renderWithProviders(<IntegrationsPage />);

    await screen.findByText("Integrations");
    expect(notifySuccessMock).not.toHaveBeenCalled();
    expect(routerReplaceMock).not.toHaveBeenCalled();
  });

  it("renders a first-class 'Your GitHub account' row with a connect action when not connected", async () => {
    renderWithProviders(<IntegrationsPage />);

    expect(await screen.findByText("Your GitHub account")).toBeInTheDocument();
    const connect = await screen.findByRole("button", { name: "Connect GitHub account" });
    const user = userEvent.setup();
    await user.click(connect);
    expect(githubConnectMock).toHaveBeenCalledTimes(1);
  });

  it("shows the connected GitHub login and a disconnect action when connected", async () => {
    githubStatusGetMock.mockResolvedValue({
      connected: true,
      has_repo_scope: true,
      github_login: "octocat",
      pr_authorship_mode: "user_preferred",
      pr_draft_default: false,
      account_requirement: "recommended",
    });
    renderWithProviders(<IntegrationsPage />);

    expect(await screen.findByText("Connected as @octocat")).toBeInTheDocument();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Disconnect GitHub account" }));
    // Confirm the destructive action in the dialog.
    await user.click(await screen.findByRole("button", { name: "Disconnect" }));
    expect(githubDisconnectMock).toHaveBeenCalledTimes(1);
  });

  it("marks the account optional and explains app authorship under app_only", async () => {
    githubStatusGetMock.mockResolvedValue({
      connected: false,
      has_repo_scope: false,
      pr_authorship_mode: "app_only",
      pr_draft_default: false,
      account_requirement: "optional",
    });
    renderWithProviders(<IntegrationsPage />);

    // Wait for the github-status query to resolve so the requirement-specific
    // copy (not the default "recommended" copy) has rendered.
    expect(await screen.findByText(/PRs are authored by the 143 app/)).toBeInTheDocument();
    // "Optional" also appears on other integration cards, so just assert presence.
    expect(screen.getAllByText("Optional").length).toBeGreaterThan(0);
  });

  it("prompts a reconnect when the account credential is no longer usable", async () => {
    githubStatusGetMock.mockResolvedValue({
      connected: false,
      has_repo_scope: false,
      github_login: "octocat",
      pr_authorship_mode: "user_required",
      pr_draft_default: false,
      account_requirement: "required",
      needs_reconnect: true,
    });
    renderWithProviders(<IntegrationsPage />);

    expect(await screen.findByRole("button", { name: "Reconnect GitHub account" })).toBeInTheDocument();
  });

  it("proactively prompts to connect the account before a repo transfer", async () => {
    listGitHubRepositoriesMock.mockResolvedValue({
      data: [
        {
          github_id: 222,
          full_name: "other/api",
          default_branch: "main",
          private: true,
          clone_url: "https://github.com/other/api.git",
          installation_id: 12345,
          status: "owned_by_other_org",
          can_transfer: true,
        },
      ],
      meta: {},
    });
    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage GitHub" }));
    await screen.findByText("other/api");
    expect(
      screen.getByText(/Transferring a repo owned by another organization needs your GitHub account/),
    ).toBeInTheDocument();
  });

  it("keeps GitHub repository claiming controls inside the manage sidesheet", async () => {
    renderWithProviders(<IntegrationsPage />);

    expect(await screen.findByText("No repositories connected")).toBeInTheDocument();
    expect(screen.queryByText("Available")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Claim" })).not.toBeInTheDocument();

    const githubCard = screen.getByText("GitHub App").closest("[data-testid='integration-card']");
    expect(githubCard).not.toBeNull();

    const user = userEvent.setup();
    await user.click(within(githubCard as HTMLElement).getByRole("button", { name: "Manage GitHub" }));

    expect(await screen.findByRole("heading", { name: "GitHub" })).toBeInTheDocument();
    expect(await screen.findByText("Repository access")).toBeInTheDocument();
    expect(screen.getByText("Members of acme can now join this workspace automatically. Manage auto-join in Team settings.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open Team settings" })).toHaveAttribute("href", "/settings/team");
    expect(await screen.findByText("acme/api")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Claim" })).toBeInTheDocument();
  });

  it("refreshes the card repository summary after claiming a GitHub repository", async () => {
    claimGitHubRepositoriesMock.mockResolvedValueOnce({ data: { claimed: 1 } });
    repositoriesListMock
      .mockResolvedValueOnce({ data: [], meta: {} })
      .mockResolvedValueOnce({
        data: [
          {
            id: "repo-1",
            full_name: "acme/api",
            status: "active",
          },
        ],
        meta: {},
      });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage GitHub" }));
    await user.click(await screen.findByRole("button", { name: "Claim" }));

    await waitFor(() => {
      const includeDisconnectedCalls = repositoriesListMock.mock.calls.filter(
        ([opts]) => opts?.includeDisconnected,
      );
      expect(includeDisconnectedCalls.length).toBeGreaterThanOrEqual(2);
    });
  });

  it("lays out GitHub repositories in a compact responsive grid", async () => {
    listGitHubRepositoriesMock.mockResolvedValue({
      data: [
        {
          github_id: 67890,
          full_name: "acme/api",
          default_branch: "main",
          private: true,
          clone_url: "https://github.com/acme/api.git",
          installation_id: 12345,
          status: "unclaimed",
          can_transfer: false,
        },
        {
          github_id: 67891,
          full_name: "acme/web",
          default_branch: "main",
          private: true,
          clone_url: "https://github.com/acme/web.git",
          installation_id: 12345,
          status: "owned_by_current_org",
          can_transfer: false,
        },
      ],
      meta: {},
    });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage GitHub" }));
    await screen.findByText("acme/api");

    const repoGrid = screen.getByTestId("github-repository-grid");
    expect(repoGrid).toHaveClass("grid-cols-[repeat(auto-fit,minmax(12rem,1fr))]");
    expect(within(repoGrid).getByText("acme/api")).toBeInTheDocument();
    expect(within(repoGrid).getByText("acme/web")).toBeInTheDocument();
  });

  it("renders GitHub repository cards without duplicate repo pills or section intro copy", async () => {
    repositoriesListMock.mockResolvedValue({
      data: [
        {
          id: "repo-1",
          full_name: "acme/web",
          status: "active",
        },
      ],
      meta: {},
    });

    renderWithProviders(<IntegrationsPage />);

    expect(await screen.findByText("acme/web")).toBeInTheDocument();

    expect(screen.queryByText("acme/api")).not.toBeInTheDocument();
    expect(screen.queryByText("Repository access")).not.toBeInTheDocument();
    expect(screen.queryByText(/Choose which repositories this 143 organization owns/)).not.toBeInTheDocument();
  });

  it("renders Linear agent routing settings in the Linear manage sidesheet", async () => {
    integrationsListMock.mockResolvedValueOnce({
      data: [
        {
          id: "integration-linear",
          org_id: "org-1",
          provider: "linear",
          status: "active",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    repositoriesListMock.mockResolvedValueOnce({
      data: [
        { id: "repo-1", org_id: "org-1", full_name: "acme/api", status: "active" },
        { id: "repo-2", org_id: "org-1", full_name: "acme/web", status: "active" },
      ],
      meta: {},
    });
    linearAgentMappingsMock.mockResolvedValueOnce({
      data: [
        {
          id: "mapping-1",
          org_id: "org-1",
          linear_team_id: "team-1",
          repository_id: "repo-2",
          priority: 0,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });

    renderWithProviders(<IntegrationsPage />);

    expect(await screen.findByText("Default repo: acme/api · 1 team override")).toBeInTheDocument();
    expect(screen.queryByText("Linear agent routing")).not.toBeInTheDocument();

    const user = userEvent.setup();
    const linearCard = screen.getByText("Linear").closest("[data-testid='integration-card']");
    expect(linearCard).not.toBeNull();
    await user.click(within(linearCard as HTMLElement).getByRole("button", { name: "Manage Linear" }));

    expect(await screen.findByText("Linear agent routing")).toBeInTheDocument();
    expect(await screen.findByText("Default repository")).toBeInTheDocument();
    expect(screen.getByText("team-1")).toBeInTheDocument();
    expect(screen.getByText("acme/web")).toBeInTheDocument();
  });

  it("updates the Linear agent default repository", async () => {
    integrationsListMock.mockResolvedValueOnce({
      data: [
        {
          id: "integration-linear",
          org_id: "org-1",
          provider: "linear",
          status: "active",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    repositoriesListMock.mockResolvedValueOnce({
      data: [
        { id: "repo-1", org_id: "org-1", full_name: "acme/api", status: "active" },
        { id: "repo-2", org_id: "org-1", full_name: "acme/web", status: "active" },
      ],
      meta: {},
    });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage Linear" }));
    await screen.findByText("Linear agent routing");
    await user.click(await screen.findByRole("combobox", { name: "Default repository" }));
    await user.click(await screen.findByRole("option", { name: "acme/web" }));

    await waitFor(() => {
      expect(updateLinearAgentMock).toHaveBeenCalledWith({ default_repo_id: "repo-2" });
    });
  });

  it("lets admins pick a Linear team by readable name and stores the team key", async () => {
    integrationsListMock.mockResolvedValueOnce({
      data: [
        {
          id: "integration-linear",
          org_id: "org-1",
          provider: "linear",
          status: "active",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    repositoriesListMock.mockResolvedValueOnce({
      data: [
        { id: "repo-143", org_id: "org-1", full_name: "assembledhq/143", status: "active" },
      ],
      meta: {},
    });
    linearAgentStatusMock.mockResolvedValueOnce({
      data: {
        enabled: true,
        agent_scopes_granted: true,
        app_user_name: "143",
        has_linear_integration: true,
        available_teams: [
          {
            org_id: "org-1",
            integration_id: "integration-linear",
            workspace_id: "workspace-1",
            team_id: "715c282d-55a7-48d8-9d7d-d7f6fe4ebd7f",
            team_key: "VIR",
            team_name: "Virtuous Cycle",
            refreshed_at: "2026-01-01T00:00:00Z",
          },
        ],
      },
    });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage Linear" }));
    await screen.findByText("Linear agent routing");
    await user.click(await screen.findByRole("combobox", { name: "Linear team" }));
    await user.click(await screen.findByRole("option", { name: "Virtuous Cycle (VIR)" }));
    await user.click(await screen.findByRole("combobox", { name: "Override repository" }));
    await user.click(await screen.findByRole("option", { name: "assembledhq/143" }));
    await user.click(screen.getByRole("button", { name: "Add" }));

    await waitFor(() => {
      expect(upsertLinearAgentMappingMock).toHaveBeenCalledWith({
        linear_team_id: "VIR",
        linear_project_id: undefined,
        repository_id: "repo-143",
      });
    });
  });

  it("shows stale Linear mappings for disconnected repositories while selectors only offer active repos", async () => {
    integrationsListMock.mockResolvedValueOnce({
      data: [
        {
          id: "integration-linear",
          org_id: "org-1",
          provider: "linear",
          status: "active",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    repositoriesListMock.mockResolvedValueOnce({
      data: [
        { id: "repo-143", org_id: "org-1", full_name: "assembledhq/143", status: "disconnected" },
        { id: "repo-api", org_id: "org-1", full_name: "assembledhq/api", status: "active" },
      ],
      meta: {},
    });
    linearAgentStatusMock.mockResolvedValueOnce({
      data: {
        enabled: true,
        agent_scopes_granted: true,
        app_user_name: "143",
        has_linear_integration: true,
        available_teams: [
          {
            org_id: "org-1",
            integration_id: "integration-linear",
            workspace_id: "workspace-1",
            team_id: "715c282d-55a7-48d8-9d7d-d7f6fe4ebd7f",
            team_key: "VIR",
            team_name: "Virtuous Cycle",
            refreshed_at: "2026-01-01T00:00:00Z",
          },
        ],
      },
    });
    linearAgentMappingsMock
      .mockResolvedValueOnce({
        data: [
          {
            id: "mapping-stale",
            org_id: "org-1",
            linear_team_id: "VIR",
            repository_id: "repo-143",
            priority: 0,
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
        ],
        meta: {},
      })
      .mockResolvedValueOnce({ data: [], meta: {} });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage Linear" }));
    await screen.findByText("Linear agent routing");
    expect(screen.getByText("assembledhq/143")).toBeInTheDocument();
    expect(screen.getByText("Disconnected")).toBeInTheDocument();
    expect(screen.getByText(/This mapping will block new Linear agent sessions until it is removed or changed to an active repository/)).toBeInTheDocument();

    await user.click(await screen.findByRole("combobox", { name: "Override repository" }));
    expect(await screen.findByRole("option", { name: "assembledhq/api" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "assembledhq/143" })).not.toBeInTheDocument();

    await user.keyboard("{Escape}");
    const staleRepoText = screen.getByText("assembledhq/143");
    const staleMappingRow = staleRepoText.closest(".rounded-md");
    expect(staleMappingRow).not.toBeNull();
    const mappingFetchesBeforeDelete = linearAgentMappingsMock.mock.calls.length;
    await user.click(within(staleMappingRow as HTMLElement).getByRole("button", { name: "Remove mapping" }));
    await waitFor(() => {
      expect(deleteLinearAgentMappingMock).toHaveBeenCalledWith("mapping-stale");
    });
    await waitFor(() => {
      expect(linearAgentMappingsMock.mock.calls.length).toBeGreaterThan(mappingFetchesBeforeDelete);
    });
  });

  it("shows a stale Linear default repository and lets admins clear it", async () => {
    integrationsListMock.mockResolvedValueOnce({
      data: [
        {
          id: "integration-linear",
          org_id: "org-1",
          provider: "linear",
          status: "active",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    repositoriesListMock.mockResolvedValueOnce({
      data: [
        { id: "repo-143", org_id: "org-1", full_name: "assembledhq/143", status: "disconnected" },
        { id: "repo-api", org_id: "org-1", full_name: "assembledhq/api", status: "active" },
      ],
      meta: {},
    });
    linearAgentStatusMock.mockResolvedValueOnce({
      data: {
        enabled: true,
        agent_scopes_granted: true,
        app_user_name: "143",
        has_linear_integration: true,
        default_repo_id: "repo-143",
        available_teams: [],
      },
    });
    linearAgentMappingsMock.mockResolvedValueOnce({ data: [], meta: {} });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage Linear" }));
    await screen.findByText("Linear agent routing");

    expect(screen.getByText("assembledhq/143")).toBeInTheDocument();
    expect(screen.getByText("Disconnected")).toBeInTheDocument();
    expect(screen.getByText(/This default repository will block new Linear agent sessions until it is cleared or changed to an active repository/)).toBeInTheDocument();

    await user.click(await screen.findByRole("combobox", { name: "Default repository" }));
    expect(await screen.findByRole("option", { name: "assembledhq/api" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "assembledhq/143" })).not.toBeInTheDocument();

    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "Clear default repository" }));
    await waitFor(() => {
      expect(updateLinearAgentMock).toHaveBeenCalledWith({ default_repo_id: null });
    });
  });

  it("manages PagerDuty health, settings, and recent incidents", async () => {
    integrationsListMock.mockResolvedValueOnce({
      data: [],
      meta: {},
    });
    repositoriesListMock.mockResolvedValueOnce({
      data: [
        { id: "repo-1", org_id: "org-1", full_name: "acme/api", status: "active" },
        { id: "repo-2", org_id: "org-1", full_name: "acme/web", status: "active" },
      ],
      meta: {},
    });
    listPagerDutyMock.mockResolvedValue({
      data: [
        {
          id: "pd-1",
          org_id: "org-1",
          provider: "pagerduty",
          integration_id: "pagerduty-1",
          account_subdomain: "acme",
          status: "degraded",
          default_repository_id: "repo-1",
          writeback_enabled: true,
          auto_create_webhook: false,
          last_error: "OAuth token expired",
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    listPagerDutyIncidentsMock.mockResolvedValue({
      data: [
        {
          id: "mirror-1",
          org_id: "org-1",
          pagerduty_integration_id: "pd-1",
          incident_id: "PINC123",
          incident_number: 42,
          html_url: "https://acme.pagerduty.com/incidents/PINC123",
          title: "Checkout outage",
          status: "triggered",
          urgency: "high",
          priority_name: "P1",
          service_id: "P123",
          service_name: "Checkout",
          escalation_policy_name: "Primary On-call",
          assigned_user_ids: [],
          team_ids: ["TEAM1", "TEAM2"],
          latest_note: "Database failover in progress",
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage PagerDuty" }));
    const sheet = await screen.findByRole("dialog", { name: "PagerDuty" });

    expect(await within(sheet).findByText("OAuth token expired")).toBeInTheDocument();
    expect(await within(sheet).findByText("Checkout outage")).toBeInTheDocument();
    expect(within(sheet).getByText("#42")).toBeInTheDocument();
    expect(within(sheet).getByText("P1")).toBeInTheDocument();
    expect(within(sheet).getByText("Teams: TEAM1, TEAM2")).toBeInTheDocument();
    expect(within(sheet).getByText("Escalation: Primary On-call")).toBeInTheDocument();
    expect(within(sheet).getByText("Latest note: Database failover in progress")).toBeInTheDocument();
    expect(within(sheet).getByRole("link", { name: "Open incident in PagerDuty" })).toHaveAttribute("href", "https://acme.pagerduty.com/incidents/PINC123");
    expect(within(sheet).getByText("Repository: acme/api")).toBeInTheDocument();
    expect(within(sheet).getByLabelText("PagerDuty automation workflow run URL")).toHaveValue("/api/v1/automations/{automation_id}/run");
    await user.click(within(sheet).getByRole("button", { name: "Reauthorize PagerDuty" }));
    expect(loginPagerDutyMock).toHaveBeenCalledTimes(1);

    await user.click(within(sheet).getByRole("button", { name: "Test PagerDuty connection" }));
    await within(sheet).findByText("Connection healthy");

    await user.click(within(sheet).getByRole("switch", { name: "PagerDuty writeback" }));
    await waitFor(() => {
      expect(updatePagerDutyMock).toHaveBeenCalledWith({ writeback_enabled: false }, "pd-1");
    });

    await user.click(within(sheet).getByRole("combobox", { name: "Default PagerDuty repository" }));
    await user.click(await screen.findByRole("option", { name: "acme/web" }));
    await waitFor(() => {
      expect(updatePagerDutyMock).toHaveBeenCalledWith({ default_repository_id: "repo-2" }, "pd-1");
    });

    await user.click(within(sheet).getByRole("button", { name: "Start session for Checkout outage" }));
    await waitFor(() => {
      expect(startPagerDutyIncidentSessionMock).toHaveBeenCalledWith("PINC123", {
        pagerduty_integration_id: "pd-1",
      });
    });
  });

  it("filters monitored Slack channels in the Slack manage sidesheet", async () => {
    integrationsListMock.mockResolvedValueOnce({
      data: [
        {
          id: "integration-slack",
          org_id: "org-1",
          provider: "slack",
          status: "active",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    listSlackChannelsMock.mockResolvedValue({
      data: [
        { id: "chan-1", name: "engineering", selected: true },
        { id: "chan-2", name: "customer-success", selected: false },
        { id: "chan-3", name: "random", selected: false },
      ],
    });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage Slack" }));
    const sheet = await screen.findByRole("dialog", { name: "Slack" });
    await within(sheet).findByRole("option", { name: "Monitor #engineering" });

    await user.type(within(sheet).getByPlaceholderText("Search channels..."), "customer");

    expect(within(sheet).getByRole("option", { name: "Monitor #customer-success" })).toBeInTheDocument();
    expect(within(sheet).queryByRole("option", { name: "Monitor #engineering" })).not.toBeInTheDocument();
    expect(within(sheet).queryByRole("option", { name: "Monitor #random" })).not.toBeInTheDocument();

    await user.click(within(sheet).getByRole("option", { name: "Monitor #customer-success" }));

    await waitFor(() => {
      expect(updateSlackChannelsMock).toHaveBeenCalledWith(["chan-1", "chan-2"]);
    });
  });

  it("shows Slackbot defaults before channel rows in the Slack manage sidesheet", async () => {
    integrationsListMock.mockResolvedValueOnce({
      data: [
        {
          id: "integration-slack",
          org_id: "org-1",
          provider: "slack",
          status: "active",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    repositoriesListMock.mockResolvedValueOnce({
      data: [
        {
          id: "repo-1",
          org_id: "org-1",
          integration_id: "github-1",
          github_id: 123,
          full_name: "acme/api",
          default_branch: "main",
          private: true,
          clone_url: "https://github.com/acme/api.git",
          installation_id: 12345,
          status: "active",
          settings: {},
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    listSlackChannelsMock.mockResolvedValue({
      data: [
        {
          id: "chan-1",
          name: "engineering",
          selected: true,
          effective_settings: {
            slack_channel_id: "chan-1",
            default_repository_id: "repo-1",
            default_branch: "main",
            routing_mode: "auto",
            response_visibility: "thread",
            allowed_actions: ["session", "preview"],
            notification_preset: "balanced",
            has_channel_override: false,
          },
        },
      ],
    });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage Slack" }));
    const sheet = await screen.findByRole("dialog", { name: "Slack" });

    expect(await within(sheet).findByText("Slackbot defaults")).toBeInTheDocument();
    expect(within(sheet).getByText("PM/context monitoring")).toBeInTheDocument();
    expect(within(sheet).getByText("Interactive bot channel overrides")).toBeInTheDocument();
    expect(within(sheet).getByText("User linking")).toBeInTheDocument();
    expect(within(sheet).getByLabelText("Slack default repository")).toHaveTextContent("acme/api");
    expect(within(sheet).getByText("Auto · Balanced")).toBeInTheDocument();

    await user.click(within(sheet).getByLabelText("Slack routing mode"));
    await user.click(await screen.findByRole("option", { name: "Start work" }));

    await waitFor(() => {
      expect(updateSlackSettingsMock).toHaveBeenCalledWith({ routing_mode: "start_work" });
    });

    await user.click(within(sheet).getByLabelText("Notifications for #engineering"));
    await user.click(await screen.findByRole("option", { name: "Quiet" }));

    await waitFor(() => {
      expect(updateSlackChannelSettingsMock).toHaveBeenCalledWith("chan-1", {
        slack_channel_name: "engineering",
        channel_type: "channel",
        notification_preset: "quiet",
      });
    });
  });

  it("lets admins manage Slack user links in the Slack manage sidesheet", async () => {
    integrationsListMock.mockResolvedValueOnce({
      data: [
        {
          id: "integration-slack",
          org_id: "org-1",
          provider: "slack",
          status: "active",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    listSlackUserLinksMock.mockResolvedValue({
      data: [
        {
          id: "link-1",
          org_id: "org-1",
          slack_installation_id: "slack-install-1",
          slack_team_id: "T123",
          slack_user_id: "U123",
          slack_email: "admin@example.com",
          slack_display_name: "Admin Slack",
          user_id: "user-1",
          source: "admin_linked",
          active: true,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage Slack" }));
    const sheet = await screen.findByRole("dialog", { name: "Slack" });

    expect(await within(sheet).findByText("Admin Slack")).toBeInTheDocument();
    await user.click(within(sheet).getByLabelText("143 user"));
    await user.click(await screen.findByRole("option", { name: "Admin User" }));
    await user.type(within(sheet).getByLabelText("Slack user ID"), "U999");
    await user.type(within(sheet).getByLabelText("Slack email"), "new@example.com");
    await user.type(within(sheet).getByLabelText("Slack display name"), "New Slack");
    await user.click(within(sheet).getByRole("button", { name: "Add link" }));

    await waitFor(() => {
      expect(upsertSlackUserLinkMock).toHaveBeenCalledWith({
        user_id: "user-1",
        slack_user_id: "U999",
        slack_email: "new@example.com",
        slack_display_name: "New Slack",
      });
    });

    await user.click(within(sheet).getByRole("button", { name: "Delete Slack link for Admin Slack" }));
    await waitFor(() => {
      expect(deleteSlackUserLinkMock).toHaveBeenCalledWith("link-1");
    });
  });

  it("lets admins edit custom Slack notification event subscriptions", async () => {
    integrationsListMock.mockResolvedValueOnce({
      data: [
        {
          id: "integration-slack",
          org_id: "org-1",
          provider: "slack",
          status: "active",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    getSlackSettingsMock.mockResolvedValueOnce({
      data: {
        org_id: "org-1",
        slack_installation_id: "slack-install-1",
        routing_mode: "auto",
        response_visibility: "thread",
        allowed_actions: ["session", "preview"],
        notification_preset: "custom",
        notification_subscriptions: { events: ["session.completed"] },
        active: true,
      },
    });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage Slack" }));
    const sheet = await screen.findByRole("dialog", { name: "Slack" });

    await user.click(await within(sheet).findByLabelText("Human input requested"));

    await waitFor(() => {
      expect(updateSlackSettingsMock).toHaveBeenCalledWith({
        notification_preset: "custom",
        notification_subscriptions: { events: ["human_input.requested"], automations: [], slack_user_ids: [] },
      });
    });
  });

  it("keeps advanced Slack notification controls behind the custom preset", async () => {
    integrationsListMock.mockResolvedValueOnce({
      data: [
        {
          id: "integration-slack",
          org_id: "org-1",
          provider: "slack",
          status: "active",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage Slack" }));
    const sheet = await screen.findByRole("dialog", { name: "Slack" });

    expect(within(sheet).queryByText("Custom notification events")).not.toBeInTheDocument();
    expect(within(sheet).queryByLabelText("Automation IDs")).not.toBeInTheDocument();
    expect(within(sheet).queryByLabelText("DM Slack user IDs")).not.toBeInTheDocument();

    await user.click(within(sheet).getByLabelText("Slack notification preset"));
    await user.click(await screen.findByRole("option", { name: "Custom" }));

    await waitFor(() => {
      expect(updateSlackSettingsMock).toHaveBeenCalledWith({ notification_preset: "custom" });
    });
  });

  it("surfaces Slack health symptoms in the Slack manage sidesheet", async () => {
    integrationsListMock.mockResolvedValueOnce({
      data: [
        {
          id: "integration-slack",
          org_id: "org-1",
          provider: "slack",
          status: "active",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    getSlackHealthMock.mockResolvedValueOnce({
      data: {
        installation: {
          id: "slack-install-1",
          org_id: "org-1",
          team_id: "T123",
          team_name: "Acme",
          bot_user_id: "U143",
          scope: ["chat:write"],
          status: "active",
          updated_at: "2026-01-01T00:00:00Z",
        },
        required_scopes: ["chat:write"],
        missing_scopes: [],
        auth_ok: false,
        symptoms: ["no_events_observed_check_event_subscriptions_and_signing_secret"],
      },
    });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage Slack" }));
    const sheet = await screen.findByRole("dialog", { name: "Slack" });

    expect(await within(sheet).findByText("No Slack events observed. Check event subscriptions and signing secret.")).toBeInTheDocument();
  });

  it("deselects a monitored Slack channel in the Slack manage sidesheet", async () => {
    integrationsListMock.mockResolvedValueOnce({
      data: [
        {
          id: "integration-slack",
          org_id: "org-1",
          provider: "slack",
          status: "active",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    listSlackChannelsMock.mockResolvedValue({
      data: [
        { id: "chan-1", name: "engineering", selected: true },
        { id: "chan-2", name: "customer-success", selected: false },
      ],
    });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage Slack" }));
    const sheet = await screen.findByRole("dialog", { name: "Slack" });
    await within(sheet).findByRole("option", { name: "Monitor #engineering" });

    await user.click(within(sheet).getByRole("option", { name: "Monitor #engineering" }));

    await waitFor(() => {
      expect(updateSlackChannelsMock).toHaveBeenCalledWith([]);
    });
  });

  it("shows empty state when Slack channel search matches nothing", async () => {
    integrationsListMock.mockResolvedValueOnce({
      data: [
        {
          id: "integration-slack",
          org_id: "org-1",
          provider: "slack",
          status: "active",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });
    listSlackChannelsMock.mockResolvedValue({
      data: [
        { id: "chan-1", name: "engineering", selected: true },
        { id: "chan-2", name: "customer-success", selected: false },
      ],
    });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Manage Slack" }));
    const sheet = await screen.findByRole("dialog", { name: "Slack" });
    await within(sheet).findByRole("option", { name: "Monitor #engineering" });

    await user.type(within(sheet).getByPlaceholderText("Search channels..."), "zzznomatch");

    expect(within(sheet).queryByRole("option", { name: "Monitor #engineering" })).not.toBeInTheDocument();
    expect(within(sheet).queryByRole("option", { name: "Monitor #customer-success" })).not.toBeInTheDocument();
    expect(within(sheet).getByText("No channels found.")).toBeInTheDocument();
  });

  it("shows Notion workspace and CircleCI project metadata on connected cards", async () => {
    integrationsListMock.mockResolvedValueOnce({
      data: [
        {
          id: "integration-notion",
          org_id: "org-1",
          provider: "notion",
          status: "active",
          notion_workspace_name: "Acme HQ",
          created_at: "2026-01-01T00:00:00Z",
        },
        {
          id: "integration-circleci",
          org_id: "org-1",
          provider: "circleci",
          status: "active",
          circleci_project_slug: "gh/acme/api",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
      meta: {},
    });

    renderWithProviders(<IntegrationsPage />);

    expect(await screen.findByText("Workspace: Acme HQ")).toBeInTheDocument();
    expect(await screen.findByText("Project: gh/acme/api")).toBeInTheDocument();
  });

  it("helps users find the CircleCI project slug while connecting", async () => {
    integrationsListMock.mockResolvedValueOnce({ data: [], meta: {} });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Connect CircleCI" }));

    const dialog = await screen.findByRole("alertdialog", { name: "Connect CircleCI" });
    expect(within(dialog).getByRole("link", { name: "Open CircleCI projects" })).toHaveAttribute(
      "href",
      "https://app.circleci.com/projects",
    );
    expect(within(dialog).getByText("In CircleCI, open Projects, find your repository, then open Project Settings. Copy the slug from the settings overview.")).toBeInTheDocument();

    await user.hover(within(dialog).getByRole("button", { name: "Where to find the CircleCI project slug" }));

    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "Use the API project slug from CircleCI. OAuth projects usually look like gh/org/repo; GitHub App projects can use a circleci/... slug.",
    );
  });

  it("helps users find Mezmo service keys while connecting", async () => {
    integrationsListMock.mockResolvedValueOnce({ data: [], meta: {} });

    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Connect Mezmo" }));

    const dialog = await screen.findByRole("alertdialog", { name: "Connect Mezmo" });
    expect(dialog).toHaveTextContent("Open Mezmo, select the right organization, then go to Settings > Organization > API Keys. Create a service key there so agents can query production logs.");
    expect(within(dialog).getByRole("link", { name: "Open Mezmo" })).toHaveAttribute(
      "href",
      "https://app.mezmo.com/",
    );

    expect(within(dialog).queryByLabelText("Dataset (optional)")).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Where to find the Mezmo dataset" })).not.toBeInTheDocument();
  });
});
