import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { SetupChecklist } from "./setup-checklist";

const mocks = vi.hoisted(() => ({
  settingsGetMock: vi.fn().mockResolvedValue({
    data: {
      name: "Test Org",
      settings: {
        default_agent_type: "codex",
        agent_config: {},
      },
    },
  }),
  codexAuthMock: vi.fn().mockResolvedValue({ data: { status: "pending" } }),
  integrationsListMock: vi.fn().mockResolvedValue({ data: [] }),
  repositoriesListMock: vi.fn().mockResolvedValue({ data: [] }),
  codingCredentialsListMock: vi.fn().mockResolvedValue({ data: [] }),
  codexModalMock: vi.fn((props: unknown) => {
    void props;
    return <div data-testid="codex-device-code-modal" />;
  }),
  sourceControlCardMock: vi.fn((props: unknown) => {
    void props;
    return <div data-testid="source-control-card" />;
  }),
}));

vi.mock("@/lib/api", () => ({
  api: {
    settings: {
      get: mocks.settingsGetMock,
      update: vi.fn().mockResolvedValue({}),
    },
    codexAuth: {
      status: mocks.codexAuthMock,
      start: vi.fn().mockResolvedValue({ data: {} }),
    },
    codingCredentials: {
      list: mocks.codingCredentialsListMock,
    },
    integrations: {
      list: mocks.integrationsListMock,
    },
    repositories: {
      list: mocks.repositoriesListMock,
    },
  },
}));

vi.mock("@/hooks/use-disconnect-integration", () => ({
  useDisconnectIntegration: () => ({
    disconnect: vi.fn(),
    isPending: false,
  }),
}));

vi.mock("@/hooks/use-github-repo-sync", () => ({
  useGitHubRepoSync: () => ({
    syncRepos: vi.fn(),
    isSyncing: false,
  }),
}));

vi.mock("@/components/codex-device-code-modal", () => ({
  CodexDeviceCodeModal: (props: unknown) => mocks.codexModalMock(props),
}));

vi.mock("@/components/no-repos-warning", () => ({
  NoReposWarning: () => <div data-testid="no-repos-warning" />,
}));

vi.mock("@/components/integration-connection-cards", () => ({
  SourceControlIntegrationCard: (props: unknown) => mocks.sourceControlCardMock(props),
  AdditionalIntegrationCards: () => <div data-testid="additional-cards" />,
}));

describe("SetupChecklist", () => {
  beforeEach(() => {
    Object.values(mocks).forEach((m) => m.mockClear());
  });

  it("renders setup steps", async () => {
    renderWithProviders(<SetupChecklist />);

    await waitFor(() => {
      expect(mocks.settingsGetMock).toHaveBeenCalled();
    });

    // Should show the coding agent step
    expect(screen.getByText(/coding agent/i)).toBeInTheDocument();
  });

  it("renders the integrations connection step", async () => {
    renderWithProviders(<SetupChecklist />);

    await waitFor(() => {
      expect(screen.getByText(/Connect integrations/i)).toBeInTheDocument();
    });
  });

  it("shows agent options", async () => {
    renderWithProviders(<SetupChecklist />);

    await waitFor(() => {
      expect(screen.getByText("Codex")).toBeInTheDocument();
    });
  });

  it("checks and opens Codex auth in personal scope", async () => {
    const user = userEvent.setup();

    renderWithProviders(<SetupChecklist />);

    await waitFor(() => {
      expect(mocks.codexAuthMock).toHaveBeenCalledWith(undefined, "personal");
    });

    await user.click(await screen.findByRole("button", { name: "Sign in with ChatGPT" }));

    expect(mocks.codexModalMock).toHaveBeenLastCalledWith(
      expect.objectContaining({ scope: "personal" }),
    );
  });

  it("treats oauth-only github integrations as connected", async () => {
    mocks.integrationsListMock.mockResolvedValueOnce({
      data: [{ id: "int-1", provider: "github", status: "active", github_app_installed: false }],
    });

    renderWithProviders(<SetupChecklist />);

    await waitFor(() => {
      expect(mocks.sourceControlCardMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ githubConnected: true })
      );
    });
  });
});
