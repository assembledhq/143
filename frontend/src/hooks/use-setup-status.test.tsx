import { describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
import { useSetupStatus } from "./use-setup-status";

const mocks = vi.hoisted(() => ({
  settingsGet: vi.fn().mockResolvedValue({
    data: { settings: { default_agent_type: "codex" } },
  }),
  codexStatus: vi.fn().mockResolvedValue({
    data: { status: "completed", account_type: "plus" },
  }),
  listResolved: vi.fn().mockResolvedValue({ data: [] }),
  codingCredentialsList: vi.fn().mockResolvedValue({ data: [] }),
  integrationsList: vi.fn().mockResolvedValue({
    data: [{ id: "int-1", provider: "github", status: "active" }],
  }),
  repositoriesList: vi.fn().mockResolvedValue({
    data: [{ id: "repo-1", name: "repo" }],
  }),
}));

vi.mock("@/lib/api", () => ({
  api: {
    settings: { get: mocks.settingsGet },
    codexAuth: { status: mocks.codexStatus },
    userCredentials: { listResolved: mocks.listResolved },
    codingCredentials: { list: mocks.codingCredentialsList },
    integrations: { list: mocks.integrationsList },
    repositories: { list: mocks.repositoriesList },
  },
}));

function SetupStatusProbe() {
  const status = useSetupStatus();
  return (
    <div>
      <div>{status.isLoading ? "loading" : "loaded"}</div>
      <div>{status.isSetupComplete ? "complete" : "incomplete"}</div>
    </div>
  );
}

describe("useSetupStatus", () => {
  it("checks Codex auth status in personal scope", async () => {
    renderWithProviders(<SetupStatusProbe />);

    await waitFor(() => {
      expect(mocks.codexStatus).toHaveBeenCalledWith(undefined, "personal");
    });
    expect(await screen.findByText("complete")).toBeInTheDocument();
  });

  it("counts personal Claude subscriptions from unified credentials as setup-complete agent auth", async () => {
    mocks.settingsGet.mockResolvedValueOnce({
      data: { settings: { default_agent_type: "claude_code" } },
    });
    mocks.codexStatus.mockResolvedValueOnce({ data: { status: "none" } });
    mocks.codingCredentialsList.mockResolvedValueOnce({
      data: [{
        id: "cc-claude",
        org_id: "org-1",
        user_id: "user-1",
        scope: "personal",
        priority: 1,
        agent: "claude_code",
        auth_type: "subscription",
        provider: "anthropic_subscription",
        label: "Personal Claude",
        status: "healthy",
        is_default: true,
        created_at: "2026-03-20T00:00:00Z",
        updated_at: "2026-03-20T00:00:00Z",
      }],
    });

    renderWithProviders(<SetupStatusProbe />);

    expect(await screen.findByText("complete")).toBeInTheDocument();
  });
});
