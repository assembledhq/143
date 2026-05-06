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

  it("opens the PM model dropdown with groups for every agent the org has configured (incl. Amp modes and Pi)", async () => {
    server.use(
      http.get("/api/v1/settings", () => HttpResponse.json({
        data: {
          id: "org-1",
          name: "Org",
          settings: {
            pm_model: "gpt-5.4",
            default_agent_type: "codex",
            agent_config: {
              codex: { OPENAI_API_KEY: "sk-***" },
              claude_code: { ANTHROPIC_API_KEY: "sk-ant-***" },
              gemini_cli: { GEMINI_API_KEY: "AIza-***" },
              amp: { AMP_API_KEY: "amp_***" },
              pi: { PI_API_KEY: "pi_***" },
            },
          },
          created_at: "2026-03-20T00:00:00Z",
          updated_at: "2026-03-20T00:00:00Z",
        },
      })),
      http.get("/api/v1/repositories", () => HttpResponse.json({ data: [], meta: {} })),
    );

    const user = userEvent.setup();
    renderWithProviders(<AutopilotSettingsPage />);

    await user.click(await screen.findByLabelText("PM model"));

    // Group labels — Amp's row is relabeled "Amp modes" so the mode names
    // (smart/deep/...) read correctly next to model IDs from other agents.
    expect(await screen.findByText("Codex")).toBeInTheDocument();
    expect(screen.getByText("Claude Code")).toBeInTheDocument();
    expect(screen.getByText("Gemini CLI")).toBeInTheDocument();
    expect(screen.getByText("Amp modes")).toBeInTheDocument();
    expect(screen.getByText("Pi")).toBeInTheDocument();

    // Spot-check a row from each kind: Codex model, Amp mode, Pi provider/model.
    expect(screen.getByRole("option", { name: "claude-opus-4-7" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "smart" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "anthropic/claude-opus-4-7" })).toBeInTheDocument();
  });

  it("includes unified org subscription credentials in PM model availability", async () => {
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
      http.get("/api/v1/repositories", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("/api/v1/settings/coding-auths", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("/api/v1/coding-credentials", ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get("scope") !== "org") {
          return HttpResponse.json({ data: [], meta: {} });
        }
        return HttpResponse.json({
          data: [{
            id: "cred-1",
            org_id: "org-1",
            scope: "org",
            priority: 1,
            agent: "claude_code",
            auth_type: "subscription",
            provider: "anthropic_subscription",
            label: "Claude subscription",
            status: "healthy",
            is_default: true,
            created_at: "2026-03-20T00:00:00Z",
            updated_at: "2026-03-20T00:00:00Z",
          }],
          meta: {},
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<AutopilotSettingsPage />);

    await user.click(await screen.findByLabelText("PM model"));

    expect(await screen.findByText("Claude Code")).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "claude-sonnet-4-5" })).toBeInTheDocument();
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
    expect(screen.getByLabelText("Suggest").parentElement).not.toHaveClass("rounded-lg", "border", "p-3");
    expect(screen.getByLabelText("Conservative").parentElement).not.toHaveClass("rounded-lg", "border", "p-3");
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
