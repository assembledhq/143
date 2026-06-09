import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, waitFor, userEvent } from "@/test/test-utils";
import RuntimeSettingsPage from "./page";

const {
  settingsGetMock,
  settingsUpdateMock,
  settingsNetworkStatusMock,
} = vi.hoisted(() => ({
  settingsGetMock: vi.fn(),
  settingsUpdateMock: vi.fn(),
  settingsNetworkStatusMock: vi.fn(),
}));

vi.mock("@/lib/api", () => ({
  api: {
    settings: {
      get: settingsGetMock,
      update: settingsUpdateMock,
      getNetworkStatus: settingsNetworkStatusMock,
    },
  },
}));

describe("RuntimeSettingsPage", () => {
  beforeEach(() => {
    settingsGetMock.mockReset();
    settingsUpdateMock.mockReset();
    settingsNetworkStatusMock.mockReset();
    settingsGetMock.mockResolvedValue({
      data: {
        id: "org-1",
        name: "Test Org",
        settings: {
          max_concurrent_runs: 5,
          max_session_duration_seconds: 1500,
          preview_max_previews_per_user: 7,
          coding_agent_tab_tools_enabled: false,
          sandbox_network: { static_egress_enabled: true },
        },
        created_at: "2026-05-01T12:00:00Z",
        updated_at: "2026-05-01T12:00:00Z",
      },
    });
    settingsUpdateMock.mockResolvedValue({
      data: {
        id: "org-1",
        name: "Test Org",
        settings: {},
        created_at: "2026-05-01T12:00:00Z",
        updated_at: "2026-05-06T15:30:00Z",
      },
    });
    settingsNetworkStatusMock.mockResolvedValue({
      data: {
        static_egress_available: true,
        static_egress_enabled: true,
        static_egress_public_ip: "203.0.113.10",
      },
    });
  });

  it("renders shared sandbox runtime sections with existing settings", async () => {
    renderWithProviders(<RuntimeSettingsPage />);

    expect(await screen.findByRole("heading", { name: "Runtime" })).toBeInTheDocument();
    expect(screen.getByText("Configure sandbox networking, capacity, and lifecycle defaults.")).toBeInTheDocument();
    expect(screen.getByText("Sandbox network")).toBeInTheDocument();
    expect(screen.getByText("Capacity limits")).toBeInTheDocument();
    expect(screen.getByText("Session runtime")).toBeInTheDocument();

    await waitFor(() => {
      expect(screen.getByLabelText("Use static egress IP for sessions and previews")).toBeChecked();
    });
    expect(screen.getAllByText("203.0.113.10").length).toBeGreaterThan(0);
    expect(screen.getByLabelText("Concurrent coding-agent runs")).toHaveValue(5);
    expect(screen.getByLabelText("Active previews per user")).toHaveValue(7);
    expect(screen.getByLabelText("Maximum session duration")).toHaveValue(25);
    expect(screen.getByLabelText("Sandbox tab tools")).not.toBeChecked();
  });

  it("saves runtime settings through the existing org settings API", async () => {
    settingsUpdateMock
      .mockResolvedValueOnce({
        data: {
          id: "org-1",
          name: "Test Org",
          settings: {
            max_concurrent_runs: 5,
            max_session_duration_seconds: 1500,
            preview_max_previews_per_user: 7,
            coding_agent_tab_tools_enabled: false,
            sandbox_network: { static_egress_enabled: false },
          },
          created_at: "2026-05-01T12:00:00Z",
          updated_at: "2026-05-06T15:30:00Z",
        },
      })
      .mockResolvedValueOnce({
        data: {
          id: "org-1",
          name: "Test Org",
          settings: {
            max_concurrent_runs: 5,
            max_session_duration_seconds: 1500,
            preview_max_previews_per_user: 7,
            coding_agent_tab_tools_enabled: true,
            sandbox_network: { static_egress_enabled: false },
          },
          created_at: "2026-05-01T12:00:00Z",
          updated_at: "2026-05-06T15:30:00Z",
        },
      });
    renderWithProviders(<RuntimeSettingsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByLabelText("Use static egress IP for sessions and previews"));
    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { sandbox_network: { static_egress_enabled: false } },
      });
    });

    await user.click(screen.getByLabelText("Sandbox tab tools"));
    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { coding_agent_tab_tools_enabled: true },
      });
    });
  });

  it("saves numeric runtime limits using the same clamped settings fields", async () => {
    renderWithProviders(<RuntimeSettingsPage />);

    const user = userEvent.setup();
    const concurrentRuns = await screen.findByLabelText("Concurrent coding-agent runs");
    await user.click(concurrentRuns);
    await user.keyboard("{Control>}a{/Control}8");
    await user.tab();

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { max_concurrent_runs: 8 },
      });
    });

    const sessionDuration = screen.getByLabelText("Maximum session duration");
    await user.click(sessionDuration);
    await user.keyboard("{Control>}a{/Control}30");
    await user.tab();

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { max_session_duration_seconds: 1800 },
      });
    });
  });

  it("does not show backend worker capability diagnostics", async () => {
    settingsNetworkStatusMock.mockResolvedValue({
      data: {
        static_egress_available: false,
        static_egress_enabled: true,
        static_egress_public_ip: "203.0.113.10",
        static_egress_unavailable_reason: "not all active session workers are static-egress-capable for the configured public IP",
      },
    });

    renderWithProviders(<RuntimeSettingsPage />);

    expect(await screen.findByText("Static egress is not currently available for new sandbox starts.")).toBeInTheDocument();
    expect(
      screen.queryByText("not all active session workers are static-egress-capable for the configured public IP"),
    ).not.toBeInTheDocument();
  });
});
