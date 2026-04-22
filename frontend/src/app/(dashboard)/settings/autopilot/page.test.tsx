import { beforeEach, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import AutopilotSettingsPage from "./page";

const { useAuthMock } = vi.hoisted(() => ({
  useAuthMock: vi.fn(),
}));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: useAuthMock,
}));

describe("AutopilotSettingsPage", () => {
  beforeEach(() => {
    useAuthMock.mockReturnValue({
      user: { id: "user-1", name: "Admin User", email: "admin@example.com", role: "admin" },
      isLoading: false,
      isAuthenticated: true,
    });
  });

  it("renders Autopilot settings without workspace steering fields", async () => {
    server.use(
      http.get("/api/v1/settings", () => HttpResponse.json({
        data: {
          id: "org-1",
          name: "Org",
          settings: {
            pm_model: "claude-sonnet-4-5",
            default_agent_type: "codex",
            agent_config: {},
          },
          created_at: "2026-03-20T00:00:00Z",
          updated_at: "2026-03-20T00:00:00Z",
        },
      })),
      http.get("/api/v1/repositories", () => HttpResponse.json({ data: [], meta: {} }))
    );

    renderWithProviders(<AutopilotSettingsPage />);

    expect(await screen.findByText("Autopilot")).toBeInTheDocument();
    expect(await screen.findByLabelText("Schedule (hours)")).toHaveValue(24);
    expect(screen.getByLabelText("PM model")).toBeInTheDocument();
    expect(screen.queryByText("Reference documents")).not.toBeInTheDocument();
    expect(screen.queryByText("Priority weights")).not.toBeInTheDocument();
  });

  it("autosaves the PM cadence when the value changes and the input blurs", async () => {
    let capturedBody: unknown;
    server.use(
      http.get("/api/v1/settings", () => HttpResponse.json({
        data: {
          id: "org-1",
          name: "Org",
          settings: {
            pm_schedule_hours: 4,
            pm_model: "claude-sonnet-4-5",
            default_agent_type: "codex",
            agent_config: {},
          },
          created_at: "2026-03-20T00:00:00Z",
          updated_at: "2026-03-20T00:00:00Z",
        },
      })),
      http.get("/api/v1/repositories", () => HttpResponse.json({ data: [], meta: {} })),
      http.patch("/api/v1/settings", async ({ request }) => {
        capturedBody = await request.json();
        return HttpResponse.json({ data: { id: "org-1", name: "Org", settings: {} } });
      })
    );

    const user = userEvent.setup();
    renderWithProviders(<AutopilotSettingsPage />);

    const scheduleInput = await screen.findByLabelText("Schedule (hours)");
    await user.clear(scheduleInput);
    await user.type(scheduleInput, "6");
    await user.tab();

    await waitFor(() => {
      expect(capturedBody).toEqual({
        settings: { pm_schedule_hours: 6 },
      });
    });
  });

  it("renders execution settings with autonomy, aggressiveness, and concurrency controls", async () => {
    server.use(
      http.get("/api/v1/settings", () => HttpResponse.json({
        data: {
          id: "org-1",
          name: "Org",
          settings: {
            pm_schedule_hours: 4,
            pm_model: "claude-sonnet-4-5",
            autonomy_level: "auto_simple",
            execution_aggressiveness: 3,
            max_concurrent_runs: 5,
            default_agent_type: "codex",
            agent_config: {},
          },
          created_at: "2026-03-20T00:00:00Z",
          updated_at: "2026-03-20T00:00:00Z",
        },
      })),
      http.get("/api/v1/repositories", () => HttpResponse.json({ data: [], meta: {} }))
    );

    renderWithProviders(<AutopilotSettingsPage />);

    expect(await screen.findByText("Execution")).toBeInTheDocument();
    expect(screen.getByLabelText("Act on low-risk")).toBeChecked();
    await waitFor(() => {
      expect(screen.getByLabelText("Aggressive")).toBeChecked();
    });
    await waitFor(() => {
      expect(screen.getByLabelText("Max concurrent runs")).toHaveValue(5);
    });
  });

  it("autosaves a changed autonomy level immediately", async () => {
    let capturedBody: unknown;
    server.use(
      http.get("/api/v1/settings", () => HttpResponse.json({
        data: {
          id: "org-1",
          name: "Org",
          settings: {
            pm_schedule_hours: 4,
            pm_model: "claude-sonnet-4-5",
            autonomy_level: "auto_simple",
            max_concurrent_runs: 3,
            default_agent_type: "codex",
            agent_config: {},
          },
          created_at: "2026-03-20T00:00:00Z",
          updated_at: "2026-03-20T00:00:00Z",
        },
      })),
      http.get("/api/v1/repositories", () => HttpResponse.json({ data: [], meta: {} })),
      http.patch("/api/v1/settings", async ({ request }) => {
        capturedBody = await request.json();
        return HttpResponse.json({ data: { id: "org-1", name: "Org", settings: {} } });
      })
    );

    const user = userEvent.setup();
    renderWithProviders(<AutopilotSettingsPage />);

    await screen.findByText("Execution");
    await user.click(screen.getByLabelText("Operate broadly"));

    await waitFor(() => {
      expect(capturedBody).toEqual({
        settings: { autonomy_level: "auto_all" },
      });
    });
  });

  it("autosaves execution aggressiveness immediately", async () => {
    let capturedBody: unknown;
    server.use(
      http.get("/api/v1/settings", () => HttpResponse.json({
        data: {
          id: "org-1",
          name: "Org",
          settings: {
            pm_schedule_hours: 4,
            pm_model: "claude-sonnet-4-5",
            autonomy_level: "auto_simple",
            execution_aggressiveness: 2,
            max_concurrent_runs: 3,
            default_agent_type: "codex",
            agent_config: {},
          },
          created_at: "2026-03-20T00:00:00Z",
          updated_at: "2026-03-20T00:00:00Z",
        },
      })),
      http.get("/api/v1/repositories", () => HttpResponse.json({ data: [], meta: {} })),
      http.patch("/api/v1/settings", async ({ request }) => {
        capturedBody = await request.json();
        return HttpResponse.json({ data: { id: "org-1", name: "Org", settings: {} } });
      })
    );

    const user = userEvent.setup();
    renderWithProviders(<AutopilotSettingsPage />);

    await screen.findByText("Execution");
    await user.click(screen.getByLabelText("Maximum"));

    await waitFor(() => {
      expect(capturedBody).toEqual({
        settings: { execution_aggressiveness: 4 },
      });
    });
  });

  it("shows an admin-only message for non-admin users", async () => {
    useAuthMock.mockReturnValue({
      user: { id: "user-2", name: "Member User", email: "member@example.com", role: "member" },
      isLoading: false,
      isAuthenticated: true,
    });

    renderWithProviders(<AutopilotSettingsPage />);

    expect(await screen.findByText("Autopilot")).toBeInTheDocument();
    expect(screen.getByText("Only admins can manage Autopilot settings.")).toBeInTheDocument();
    expect(screen.queryByText("PM configuration")).not.toBeInTheDocument();
    expect(screen.queryByText("Execution aggressiveness")).not.toBeInTheDocument();
  });
});
