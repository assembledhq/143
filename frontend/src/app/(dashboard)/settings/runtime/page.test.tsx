import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, waitFor, userEvent } from "@/test/test-utils";
import RuntimeSettingsPage from "./page";

const {
  settingsGetMock,
  settingsUpdateMock,
  settingsNetworkStatusMock,
  settingsRuntimeStatusMock,
} = vi.hoisted(() => ({
  settingsGetMock: vi.fn(),
  settingsUpdateMock: vi.fn(),
  settingsNetworkStatusMock: vi.fn(),
  settingsRuntimeStatusMock: vi.fn(),
}));

vi.mock("@/lib/api", () => ({
  api: {
    settings: {
      get: settingsGetMock,
      update: settingsUpdateMock,
      getNetworkStatus: settingsNetworkStatusMock,
      getRuntimeStatus: settingsRuntimeStatusMock,
    },
  },
}));

describe("RuntimeSettingsPage", () => {
  beforeEach(() => {
    settingsGetMock.mockReset();
    settingsUpdateMock.mockReset();
    settingsNetworkStatusMock.mockReset();
    settingsRuntimeStatusMock.mockReset();
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
          sandbox_lifecycle: {
            completed_session_retention_minutes: 120,
            idle_preview_ttl_minutes: 300,
            preview_holds_sandbox: false,
          },
          sandbox_resources: {
            agent_default_tier: "standard",
            preview_default_tier: "small",
            allow_repo_resource_requests: false,
            preview_max_tier: "large",
            preview_max_cpu_millis: 1500,
            preview_max_memory_mib: 4096,
            preview_max_ephemeral_disk_mib: 6144,
          },
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
    settingsRuntimeStatusMock.mockResolvedValue({
      data: {
        static_egress: {
          available: true,
          enabled: true,
          public_ip: "203.0.113.10",
        },
        capacity: {
          state: "limited",
          active_agent_runs: 4,
          max_concurrent_agent_runs: 5,
          active_previews: 3,
          max_previews_per_user: 7,
        },
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
    expect(screen.getByText("Lifecycle defaults")).toBeInTheDocument();
    expect(screen.getByText("Resource defaults")).toBeInTheDocument();

    await waitFor(() => {
      expect(screen.getByLabelText("Use static egress IP for sessions and previews")).toBeChecked();
    });
    expect(screen.getAllByText("203.0.113.10").length).toBeGreaterThan(0);
    expect(screen.getByLabelText("Concurrent coding-agent runs")).toHaveValue(5);
    expect(screen.getByLabelText("Active previews per user")).toHaveValue(7);
    expect(screen.getByLabelText("Maximum session duration")).toHaveValue(25);
    expect(screen.getByLabelText("Sandbox tab tools")).not.toBeChecked();
    expect(screen.getByLabelText("Completed session retention")).toHaveValue(120);
    expect(screen.getByLabelText("Idle preview TTL")).toHaveValue(300);
    expect(screen.getByLabelText("Preview holds sandbox")).not.toBeChecked();
    expect(screen.getByRole("combobox", { name: "Agent default tier" })).toHaveTextContent("Standard");
    expect(screen.getByRole("combobox", { name: "Preview default tier" })).toHaveTextContent("Small");
    expect(screen.getByLabelText("Allow repo resource requests")).not.toBeChecked();
    expect(screen.getByRole("combobox", { name: "Preview max tier" })).toHaveTextContent("Large");
    expect(screen.getByLabelText("Preview CPU request max")).toHaveValue(1500);
    expect(screen.getByLabelText("Preview memory request max")).toHaveValue(4096);
    expect(screen.getByLabelText("Preview ephemeral disk request max")).toHaveValue(6144);
    expect(screen.getByText("4 / 5")).toBeInTheDocument();
    expect(screen.getByText("3 / 7")).toBeInTheDocument();
    expect(screen.getByText("Limited")).toBeInTheDocument();
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

    const previewLimit = screen.getByLabelText("Active previews per user");
    await user.click(previewLimit);
    await user.keyboard("{Control>}a{/Control}99");
    await user.tab();

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { preview_max_previews_per_user: 20 },
      });
    });
  });

  it("saves lifecycle defaults", async () => {
    settingsUpdateMock
      .mockResolvedValueOnce({
        data: {
          id: "org-1",
          name: "Test Org",
          settings: {
            sandbox_lifecycle: {
              completed_session_retention_minutes: 90,
              idle_preview_ttl_minutes: 300,
              preview_holds_sandbox: false,
            },
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
            sandbox_lifecycle: {
              completed_session_retention_minutes: 90,
              idle_preview_ttl_minutes: 300,
              preview_holds_sandbox: true,
            },
          },
          created_at: "2026-05-01T12:00:00Z",
          updated_at: "2026-05-06T15:30:00Z",
        },
      });
    renderWithProviders(<RuntimeSettingsPage />);

    const user = userEvent.setup();
    const retention = await screen.findByLabelText("Completed session retention");
    await user.click(retention);
    await user.keyboard("{Control>}a{/Control}90");
    await user.tab();

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { sandbox_lifecycle: { completed_session_retention_minutes: 90 } },
      });
    });

    await user.click(screen.getByLabelText("Preview holds sandbox"));
    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { sandbox_lifecycle: { preview_holds_sandbox: true } },
      });
    });
  });

  it("saves resource defaults", async () => {
    settingsUpdateMock
      .mockResolvedValueOnce({
        data: {
          id: "org-1",
          name: "Test Org",
          settings: {
            sandbox_resources: {
              agent_default_tier: "large",
              preview_default_tier: "small",
              allow_repo_resource_requests: false,
              preview_max_tier: "large",
              preview_max_cpu_millis: 1500,
              preview_max_memory_mib: 4096,
              preview_max_ephemeral_disk_mib: 6144,
            },
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
            sandbox_resources: {
              agent_default_tier: "large",
              preview_default_tier: "small",
              allow_repo_resource_requests: true,
              preview_max_tier: "large",
              preview_max_cpu_millis: 1500,
              preview_max_memory_mib: 4096,
              preview_max_ephemeral_disk_mib: 6144,
            },
          },
          created_at: "2026-05-01T12:00:00Z",
          updated_at: "2026-05-06T15:30:00Z",
        },
      });
    renderWithProviders(<RuntimeSettingsPage />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("combobox", { name: "Agent default tier" }));
    await user.click(await screen.findByRole("option", { name: "Large" }));

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { sandbox_resources: { agent_default_tier: "large" } },
      });
    });

    await user.click(screen.getByLabelText("Allow repo resource requests"));
    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { sandbox_resources: { allow_repo_resource_requests: true } },
      });
    });

    const memoryMax = screen.getByLabelText("Preview memory request max");
    await user.click(memoryMax);
    await user.keyboard("{Control>}a{/Control}99999");
    await user.tab();

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { sandbox_resources: { preview_max_memory_mib: 8192 } },
      });
    });
  });

  it("does not show unavailability message when static egress is intentionally disabled", async () => {
    settingsGetMock.mockResolvedValue({
      data: {
        id: "org-1",
        name: "Test Org",
        settings: { sandbox_network: { static_egress_enabled: false } },
        created_at: "2026-05-01T12:00:00Z",
        updated_at: "2026-05-01T12:00:00Z",
      },
    });
    settingsNetworkStatusMock.mockResolvedValue({
      data: {
        static_egress_available: false,
        static_egress_enabled: false,
        static_egress_public_ip: "203.0.113.10",
      },
    });

    renderWithProviders(<RuntimeSettingsPage />);

    await waitFor(() => {
      expect(screen.getByLabelText("Use static egress IP for sessions and previews")).not.toBeChecked();
    });
    expect(
      screen.queryByText("Static egress is not currently available for new sandbox starts."),
    ).not.toBeInTheDocument();
  });

  it("does not show backend worker capability diagnostics", async () => {
    settingsNetworkStatusMock.mockResolvedValue({
      data: {
        static_egress_available: false,
        static_egress_enabled: true,
        static_egress_public_ip: "203.0.113.10",
        static_egress_unavailable_reason: "worker capability mismatch",
      },
    });

    renderWithProviders(<RuntimeSettingsPage />);

    expect(await screen.findByText("Static egress is not currently available for new sandbox starts.")).toBeInTheDocument();
    expect(
      screen.queryByText("worker capability mismatch"),
    ).not.toBeInTheDocument();
  });
});
