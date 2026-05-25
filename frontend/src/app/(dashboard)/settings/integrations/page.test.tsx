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
  githubConnectMock,
  currentUserMock,
  ApiErrorMock,
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
    githubConnectMock: vi.fn(),
    currentUserMock: {
      id: "user-1",
      email: "admin@example.com",
      name: "Admin User",
      role: "admin",
    },
    ApiErrorMock: MockApiError,
  };
});

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
    },
    repositories: {
      list: repositoriesListMock,
      disconnect: vi.fn(),
    },
    githubStatus: {
      connect: githubConnectMock,
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
    claimGitHubRepositoriesMock.mockRejectedValue(
      new ApiErrorMock("GITHUB_USER_AUTH_REQUIRED", "Connect your GitHub account before claiming repositories"),
    );
    githubConnectMock.mockClear();
    currentUserMock.role = "admin";
  });

  it("offers a GitHub user-auth connect action when repo claiming requires it", async () => {
    renderWithProviders(<IntegrationsPage />);

    const user = userEvent.setup();
    await screen.findByText("acme/api");
    await user.click(screen.getByRole("button", { name: "Claim" }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Connect GitHub account" })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Connect GitHub account" }));
    expect(githubConnectMock).toHaveBeenCalledTimes(1);
  });

  it("renders repository claiming controls inside the GitHub integration card", async () => {
    renderWithProviders(<IntegrationsPage />);

    await screen.findByText("acme/api");

    const githubCard = screen.getByText("GitHub").closest("[data-testid='integration-card']");
    expect(githubCard).not.toBeNull();
    expect(within(githubCard as HTMLElement).getByText("acme/api")).toBeInTheDocument();
    expect(screen.queryByText("GitHub repositories")).not.toBeInTheDocument();
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

    await screen.findByText("acme/api");

    expect(screen.queryByText("acme/web")).not.toBeInTheDocument();
    expect(screen.queryByText("Repository access")).not.toBeInTheDocument();
    expect(screen.queryByText(/Choose which repositories this 143 organization owns/)).not.toBeInTheDocument();
  });

  it("renders Linear agent routing settings in the Linear integration card", async () => {
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

    const linearCard = (await screen.findByText("Linear agent routing")).closest("[data-testid='integration-card']");
    expect(linearCard).not.toBeNull();
    expect(within(linearCard as HTMLElement).getByText("Linear agent routing")).toBeInTheDocument();
    expect(await within(linearCard as HTMLElement).findByText("Default repository")).toBeInTheDocument();
    expect(within(linearCard as HTMLElement).getByText("team-1")).toBeInTheDocument();
    expect(within(linearCard as HTMLElement).getByText("acme/web")).toBeInTheDocument();
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
});
