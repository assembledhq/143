import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";
import { DirectionSection } from "./direction-section";

vi.mock("next/navigation", () => ({
  usePathname: () => "/autopilot",
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
}));

function setupHandlers(overrides?: {
  settings?: Record<string, unknown>;
  repos?: Array<Record<string, unknown>>;
}) {
  const defaultSettings = {
    autonomy_level: "auto_simple",
    pm_schedule_hours: 4,
    pm_model: "claude-sonnet-4-5",
    priority_weights: {
      customer_impact: 0.35,
      severity: 0.25,
      recency: 0.2,
      revenue_risk: 0.2,
    },
    product_direction: "Focus on performance",
    product_context: {
      philosophy: "Move fast",
      direction: "Focus on performance",
      focus_areas: ["speed"],
      avoid_areas: ["legacy"],
    },
    agent_config: { codex: { OPENAI_API_KEY: "sk-test" } },
    default_agent_type: "codex",
  };

  const repos = overrides?.repos ?? [
    { id: "r1", full_name: "acme/api", settings: {} },
  ];

  server.use(
    http.get("/api/v1/settings", () =>
      HttpResponse.json({
        data: {
          id: "org-1",
          settings: overrides?.settings ?? defaultSettings,
        },
      })
    ),
    http.get("/api/v1/settings/agent-defaults", () =>
      HttpResponse.json({ data: {} })
    ),
    http.get("/api/v1/repositories", () =>
      HttpResponse.json({ data: repos, meta: {} })
    ),
    http.get("/api/v1/pm/documents", () =>
      HttpResponse.json({ data: [], meta: {} })
    ),
    http.get("/api/v1/repositories/summary", () =>
      HttpResponse.json({ data: [], meta: {} })
    ),
    http.patch("/api/v1/settings", () =>
      HttpResponse.json({
        data: { id: "org-1", settings: {} },
      })
    )
  );
}

describe("DirectionSection", () => {
  it("renders form fields after settings load", async () => {
    setupHandlers();
    renderWithProviders(<DirectionSection />);

    await waitFor(() => {
      expect(screen.getByLabelText("Schedule (hours)")).toBeInTheDocument();
    });

    expect(screen.getByLabelText("Philosophy")).toBeInTheDocument();
    expect(screen.getByLabelText("Current direction")).toBeInTheDocument();
    expect(screen.getByLabelText("Focus areas")).toBeInTheDocument();
    expect(screen.getByLabelText("Avoid areas")).toBeInTheDocument();
  });

  it("shows save button", async () => {
    setupHandlers();
    renderWithProviders(<DirectionSection />);

    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Save settings" })
      ).toBeInTheDocument();
    });
  });

  it("populates form from server data", async () => {
    setupHandlers();
    renderWithProviders(<DirectionSection />);

    await waitFor(() => {
      expect(screen.getByLabelText("Schedule (hours)")).toHaveValue(4);
    });

    await waitFor(() => {
      expect(screen.getByLabelText("Philosophy")).toHaveValue("Move fast");
    });

    await waitFor(() => {
      expect(screen.getByLabelText("Current direction")).toHaveValue(
        "Focus on performance"
      );
    });
  });

  it("shows focus area tags from server data", async () => {
    setupHandlers();
    renderWithProviders(<DirectionSection />);

    await waitFor(() => {
      expect(screen.getByText("speed")).toBeInTheDocument();
    });

    expect(screen.getByText("legacy")).toBeInTheDocument();
  });

  it("save button is enabled when weights are valid", async () => {
    setupHandlers();
    renderWithProviders(<DirectionSection />);

    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Save settings" })
      ).toBeEnabled();
    });
  });

  it('shows "Settings saved." after successful save', async () => {
    setupHandlers();
    const user = userEvent.setup();
    renderWithProviders(<DirectionSection />);

    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Save settings" })
      ).toBeEnabled();
    });

    await user.click(screen.getByRole("button", { name: "Save settings" }));

    await waitFor(() => {
      expect(screen.getByText("Settings saved.")).toBeInTheDocument();
    });
  });

  it("shows repos with custom PM settings when present", async () => {
    setupHandlers({
      repos: [
        {
          id: "r1",
          full_name: "acme/api",
          settings: { pm: { product_context: { philosophy: "Custom" } } },
        },
        { id: "r2", full_name: "acme/web", settings: {} },
      ],
    });
    renderWithProviders(<DirectionSection />);

    await waitFor(() => {
      expect(screen.getByText("acme/api")).toBeInTheDocument();
    });

    // acme/web has no custom PM settings, so it should NOT appear as a badge
    expect(screen.queryByText("acme/web")).not.toBeInTheDocument();
  });
});
