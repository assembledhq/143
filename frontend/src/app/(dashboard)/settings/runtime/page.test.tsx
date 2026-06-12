import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  renderWithProviders,
  screen,
  waitFor,
  userEvent,
} from "@/test/test-utils";
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

  it("renders shared sandbox runtime policy sections with existing settings", async () => {
    renderWithProviders(<RuntimeSettingsPage />);

    expect(
      await screen.findByRole("heading", { name: "Runtime" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "Configure sandbox networking, capacity, and lifecycle defaults.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("Agent runs")).toBeInTheDocument();
    expect(await screen.findByText("5 concurrent")).toBeInTheDocument();
    expect(screen.getByText("Active previews")).toBeInTheDocument();
    expect(await screen.findByText("7 per user")).toBeInTheDocument();
    expect(screen.getByText("Session max")).toBeInTheDocument();
    expect(screen.getByText("25 minutes")).toBeInTheDocument();
    expect(screen.getByText("Preview idle TTL")).toBeInTheDocument();
    expect(screen.getByText("5 hours")).toBeInTheDocument();
    expect(screen.getByText("Sandbox network")).toBeInTheDocument();
    expect(screen.getByText("Capacity")).toBeInTheDocument();
    expect(screen.getByText("Sessions and cleanup")).toBeInTheDocument();
    expect(screen.getByText("Sandbox defaults")).toBeInTheDocument();
    expect(screen.getByText("Advanced resource limits")).toBeInTheDocument();
    expect(screen.queryByText("Usage limits")).not.toBeInTheDocument();
    expect(screen.queryByText("Sessions")).not.toBeInTheDocument();
    expect(screen.queryByText("Cleanup defaults")).not.toBeInTheDocument();
    expect(screen.queryByText("Resource defaults")).not.toBeInTheDocument();
    expect(
      screen.queryByText(
        "These settings apply to sandbox runtimes across coding-agent sessions and previews.",
      ),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("Runtime diagnostics")).not.toBeInTheDocument();
    expect(settingsRuntimeStatusMock).not.toHaveBeenCalled();

    await waitFor(() => {
      expect(screen.getByLabelText("Static egress IP")).toBeChecked();
    });
    expect(screen.getAllByText("203.0.113.10").length).toBeGreaterThan(0);
    expect(screen.getByLabelText("Concurrent agent runs")).toHaveValue(5);
    expect(screen.getByLabelText("Active previews per user")).toHaveValue(7);
    expect(screen.getByLabelText("Maximum session length")).toHaveValue(25);
    expect(screen.getByLabelText("Agent tab tools")).not.toBeChecked();
    expect(screen.getByLabelText("Keep completed sessions for")).toHaveValue(
      120,
    );
    expect(screen.getByLabelText("Idle preview timeout")).toHaveValue(300);
    expect(
      screen.getByLabelText("Keep sandbox while preview is active"),
    ).not.toBeChecked();
    expect(
      screen.getByRole("combobox", { name: "Agent sandbox size" }),
    ).toHaveTextContent("Standard");
    expect(
      screen.getByRole("combobox", { name: "Preview sandbox size" }),
    ).toHaveTextContent("Small");
    expect(
      screen.getByLabelText("Allow repository resource requests"),
    ).not.toBeChecked();
    expect(
      screen.queryByLabelText("Preview CPU limit"),
    ).not.toBeInTheDocument();
    expect(
      screen.getByText("Large max · 1.5 cores · 4 GiB"),
    ).toBeInTheDocument();
  });

  it("uses concise visible helper copy with question mark tooltips for caveats", async () => {
    renderWithProviders(<RuntimeSettingsPage />);

    expect(
      await screen.findByRole("button", { name: "About static egress IP" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "About concurrent agent runs" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "Use one stable public IP for new and resumed sandboxes.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "Limit simultaneous coding-agent turns across the organization.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(
        "These settings apply to sandbox runtimes across coding-agent sessions and previews.",
      ),
    ).not.toBeInTheDocument();

    const user = userEvent.setup();
    await user.hover(
      screen.getByRole("button", { name: "About static egress IP" }),
    );
    expect(
      await screen.findAllByText(
        "Use this when external services need to allowlist sandbox traffic.",
      ),
    ).not.toHaveLength(0);
  });

  it("keeps repository resource requests visible while exact resource caps stay advanced", async () => {
    renderWithProviders(<RuntimeSettingsPage />);

    await screen.findByLabelText("Allow repository resource requests");
    expect(
      screen.queryByLabelText("Preview disk limit"),
    ).not.toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(
      screen.getByRole("button", { name: "Expand advanced resource limits" }),
    );

    const diskLimit = await screen.findByLabelText("Preview disk limit");
    const repositoryRequests = screen.getByLabelText(
      "Allow repository resource requests",
    );
    const sandboxDefaults = screen.getByTestId("sandbox-defaults-section");
    const advancedLimits = screen.getByTestId(
      "advanced-resource-limits-section",
    );

    expect(
      repositoryRequests.compareDocumentPosition(diskLimit) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(sandboxDefaults).toContainElement(repositoryRequests);
    expect(advancedLimits).toContainElement(diskLimit);
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
    await user.click(await screen.findByLabelText("Static egress IP"));
    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { sandbox_network: { static_egress_enabled: false } },
      });
    });

    await user.click(screen.getByLabelText("Agent tab tools"));
    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { coding_agent_tab_tools_enabled: true },
      });
    });
  });

  it("saves numeric runtime limits using the same clamped settings fields", async () => {
    renderWithProviders(<RuntimeSettingsPage />);

    const user = userEvent.setup();
    const concurrentRuns = await screen.findByLabelText(
      "Concurrent agent runs",
    );
    await user.click(concurrentRuns);
    await user.keyboard("{Control>}a{/Control}8");
    await user.tab();

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { max_concurrent_runs: 8 },
      });
    });

    const sessionDuration = screen.getByLabelText("Maximum session length");
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

  it("uses the current setting value when stepping an empty numeric field", async () => {
    renderWithProviders(<RuntimeSettingsPage />);

    const user = userEvent.setup();
    const concurrentRuns = await screen.findByLabelText(
      "Concurrent agent runs",
    );
    await user.clear(concurrentRuns);
    await user.click(
      screen.getByRole("button", { name: "Increase Concurrent agent runs" }),
    );

    await waitFor(() => {
      expect(concurrentRuns).toHaveValue(6);
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { max_concurrent_runs: 6 },
      });
    });
  });

  it("saves preview CPU limits as millicores while showing cores", async () => {
    renderWithProviders(<RuntimeSettingsPage />);

    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", {
        name: "Expand advanced resource limits",
      }),
    );
    const cpuLimit = await screen.findByLabelText("Preview CPU limit");
    await waitFor(() => {
      expect(cpuLimit).toHaveValue(1.5);
    });

    await user.click(cpuLimit);
    await user.keyboard("{Control>}a{/Control}2.25");
    await user.tab();

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { sandbox_resources: { preview_max_cpu_millis: 2000 } },
      });
    });
    expect(cpuLimit).toHaveValue(2);
  });

  it("does not round and save an unchanged preview CPU value on blur", async () => {
    settingsGetMock.mockResolvedValue({
      data: {
        id: "org-1",
        name: "Test Org",
        settings: {
          sandbox_resources: {
            preview_max_cpu_millis: 333,
          },
        },
        created_at: "2026-05-01T12:00:00Z",
        updated_at: "2026-05-01T12:00:00Z",
      },
    });
    renderWithProviders(<RuntimeSettingsPage />);

    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", {
        name: "Expand advanced resource limits",
      }),
    );
    const cpuLimit = await screen.findByLabelText("Preview CPU limit");
    await waitFor(() => {
      expect(cpuLimit).toHaveValue(0.33);
    });
    settingsUpdateMock.mockClear();

    await user.click(cpuLimit);
    await user.tab();

    expect(settingsUpdateMock).not.toHaveBeenCalled();
    expect(cpuLimit).toHaveValue(0.33);
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
    const retention = await screen.findByLabelText(
      "Keep completed sessions for",
    );
    await user.click(retention);
    await user.keyboard("{Control>}a{/Control}90");
    await user.tab();

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: {
          sandbox_lifecycle: { completed_session_retention_minutes: 90 },
        },
      });
    });

    await user.click(
      screen.getByLabelText("Keep sandbox while preview is active"),
    );
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
    await user.click(
      await screen.findByRole("combobox", { name: "Agent sandbox size" }),
    );
    await user.click(await screen.findByRole("option", { name: "Large" }));

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { sandbox_resources: { agent_default_tier: "large" } },
      });
    });

    await user.click(
      screen.getByLabelText("Allow repository resource requests"),
    );
    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { sandbox_resources: { allow_repo_resource_requests: true } },
      });
    });

    await user.click(
      screen.getByRole("button", { name: "Expand advanced resource limits" }),
    );
    const memoryMax = screen.getByLabelText("Preview memory limit");
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
      expect(screen.getByLabelText("Static egress IP")).not.toBeChecked();
    });
    expect(
      screen.queryByText(
        "Static egress is not currently available for new sandbox starts.",
      ),
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

    expect(
      await screen.findByText(
        "Static egress is not currently available for new sandbox starts.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("worker capability mismatch"),
    ).not.toBeInTheDocument();
  });
});
