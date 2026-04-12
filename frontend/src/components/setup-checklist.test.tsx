import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
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
  agentDefaultsMock: vi.fn().mockResolvedValue({ data: {} }),
  codexAuthMock: vi.fn().mockResolvedValue({ data: { status: "pending" } }),
  integrationsListMock: vi.fn().mockResolvedValue({ data: [] }),
  repositoriesListMock: vi.fn().mockResolvedValue({ data: [] }),
}));

vi.mock("@/lib/api", () => ({
  api: {
    settings: {
      get: mocks.settingsGetMock,
      getAgentDefaults: mocks.agentDefaultsMock,
      update: vi.fn().mockResolvedValue({}),
    },
    codexAuth: {
      status: mocks.codexAuthMock,
      start: vi.fn().mockResolvedValue({ data: {} }),
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

vi.mock("@/components/agent-settings-editor", () => ({
  AgentSettingsEditor: () => <div data-testid="agent-settings-editor" />,
}));

vi.mock("@/components/codex-device-code-modal", () => ({
  CodexDeviceCodeModal: () => null,
}));

vi.mock("@/components/no-repos-warning", () => ({
  NoReposWarning: () => <div data-testid="no-repos-warning" />,
}));

vi.mock("@/components/integration-connection-cards", () => ({
  SourceControlIntegrationCard: () => <div data-testid="source-control-card" />,
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

  it("renders the GitHub connection step", async () => {
    renderWithProviders(<SetupChecklist />);

    await waitFor(() => {
      expect(screen.getByText(/GitHub/i)).toBeInTheDocument();
    });
  });

  it("shows agent options", async () => {
    renderWithProviders(<SetupChecklist />);

    await waitFor(() => {
      expect(screen.getByText("Codex")).toBeInTheDocument();
    });
  });
});
