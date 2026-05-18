import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, waitFor, within, userEvent } from "@/test/test-utils";
import IntegrationsPage from "./page";

const {
  integrationsListMock,
  repositoriesListMock,
  listGitHubRepositoriesMock,
  claimGitHubRepositoriesMock,
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
});
