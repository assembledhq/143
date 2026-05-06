import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { http, HttpResponse } from "msw";
import AutomationDetailPage from "./page";

const pushMock = vi.fn();

vi.mock("next/link", () => ({
  default: ({ children, href, ...props }: React.ComponentProps<"a"> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

vi.mock("next/navigation", () => ({
  useParams: () => ({ id: "auto-1" }),
  useRouter: () => ({
    push: pushMock,
    replace: vi.fn(),
  }),
  useSearchParams: () => new URLSearchParams("tab=paused"),
}));

describe("AutomationDetailPage", () => {
  it("allows the timezone selector to wrap cleanly on mobile layouts", async () => {
    server.use(
      http.get("*/api/v1/automations/auto-1", () => HttpResponse.json({
        data: {
          id: "auto-1",
          org_id: "org-1",
          repository_id: "repo-1",
          name: "Weekly audit",
          goal: "Check release health",
          scope: "",
          interval_value: 1,
          interval_unit: "weeks",
          base_branch: "main",
          enabled: true,
          timezone: "UTC",
          last_run_at: null,
          next_run_at: null,
          priority: 50,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      })),
      http.get("*/api/v1/automations/auto-1/runs*", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/automations/auto-1/stats*", () => HttpResponse.json({
        data: {
          since: "2026-01-01T00:00:00Z",
          until: "2026-01-31T00:00:00Z",
          buckets: [],
          totals: {
            total: 0,
            completed: 0,
            completed_noop: 0,
            failed: 0,
            skipped: 0,
            running: 0,
            pending: 0,
            success_rate: 0,
            avg_duration_seconds: 0,
          },
        },
      })),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await userEvent.setup().click(screen.getByRole("tab", { name: "Settings" }));

    const timezoneButton = screen.getByTitle("UTC");
    const scheduleRow = timezoneButton.parentElement;

    expect(scheduleRow).toHaveClass("flex-wrap");
    expect(timezoneButton).toHaveClass("w-full", "sm:w-auto");
  });

  it("renders a back button to the automations list preserving query params", async () => {
    server.use(
      http.get("*/api/v1/automations/auto-1", () => HttpResponse.json({
        data: {
          id: "auto-1",
          org_id: "org-1",
          repository_id: "repo-1",
          name: "Weekly audit",
          goal: "Check release health",
          scope: "",
          interval_value: 1,
          interval_unit: "weeks",
          base_branch: "main",
          enabled: true,
          last_run_at: null,
          next_run_at: null,
          priority: 50,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      })),
      http.get("*/api/v1/automations/auto-1/runs*", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/automations/auto-1/stats*", () => HttpResponse.json({
        data: {
          since: "2026-01-01T00:00:00Z",
          until: "2026-01-31T00:00:00Z",
          buckets: [],
          totals: {
            total: 0,
            completed: 0,
            completed_noop: 0,
            failed: 0,
            skipped: 0,
            running: 0,
            pending: 0,
            success_rate: 0,
            avg_duration_seconds: 0,
          },
        },
      })),
    );

    renderWithProviders(<AutomationDetailPage />);

    const backLink = await screen.findByLabelText("Back to automations");
    expect(backLink).toHaveAttribute("href", "/automations?tab=paused");
  });

  it("saves the selected base branch from the branch picker", async () => {
    const user = userEvent.setup();
    let updateBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/automations/auto-1", () => HttpResponse.json({
        data: {
          id: "auto-1",
          org_id: "org-1",
          repository_id: "repo-1",
          name: "Weekly audit",
          goal: "Check release health",
          scope: "",
          interval_value: 1,
          interval_unit: "weeks",
          base_branch: "main",
          enabled: true,
          last_run_at: null,
          next_run_at: null,
          priority: 50,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      })),
      http.get("*/api/v1/automations/auto-1/runs*", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/automations/auto-1/stats*", () => HttpResponse.json({
        data: {
          since: "2026-01-01T00:00:00Z",
          until: "2026-01-31T00:00:00Z",
          buckets: [],
          totals: {
            total: 0,
            completed: 0,
            completed_noop: 0,
            failed: 0,
            skipped: 0,
            running: 0,
            pending: 0,
            success_rate: 0,
            avg_duration_seconds: 0,
          },
        },
      })),
      http.get("*/api/v1/repositories/repo-1/branches", () => HttpResponse.json({
        data: [
          { name: "main", protected: true },
          { name: "release/ops", protected: false },
        ],
        meta: {},
      })),
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBody = await request.json() as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("tab", { name: "Settings" }));
    await user.click(await screen.findByRole("button", { name: "Base branch" }));
    await user.type(await screen.findByPlaceholderText("Search branches..."), "ops");
    await user.click(await screen.findByText("release/ops"));
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(updateBody).toMatchObject({ base_branch: "release/ops" });
    });
  });

  it("saves the selected model override", async () => {
    const user = userEvent.setup();
    let updateBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/settings", () => HttpResponse.json({
        data: {
          settings: {
            default_agent_type: "codex",
          },
        },
      })),
      http.get("*/api/v1/settings/credentials/resolved", () => HttpResponse.json({
        data: [],
        meta: {},
      })),
      http.get("*/api/v1/settings/credentials/team", () => HttpResponse.json({
        data: [],
        meta: {},
      })),
      http.get("*/api/v1/settings/codex-auth/status", () => HttpResponse.json({
        data: null,
      })),
      http.get("*/api/v1/settings/coding-auths", () => HttpResponse.json({
        data: [
          {
            id: "auth-1",
            org_id: "org-1",
            agent: "claude_code",
            auth_type: "api_key",
            provider: "anthropic",
            label: "Claude Code API key",
            status: "healthy",
            is_default: true,
            priority: 1,
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
        ],
        meta: {},
      })),
      http.get("*/api/v1/coding-credentials*", () => HttpResponse.json({
        data: [],
        meta: {},
      })),
      http.get("*/api/v1/automations/auto-1", () => HttpResponse.json({
        data: {
          id: "auto-1",
          org_id: "org-1",
          repository_id: "repo-1",
          name: "Weekly audit",
          goal: "Check release health",
          scope: "",
          interval_value: 1,
          interval_unit: "weeks",
          base_branch: "main",
          enabled: true,
          timezone: "UTC",
          last_run_at: null,
          next_run_at: null,
          priority: 50,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      })),
      http.get("*/api/v1/automations/auto-1/runs*", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/automations/auto-1/stats*", () => HttpResponse.json({
        data: {
          since: "2026-01-01T00:00:00Z",
          until: "2026-01-31T00:00:00Z",
          buckets: [],
          totals: {
            total: 0,
            completed: 0,
            completed_noop: 0,
            failed: 0,
            skipped: 0,
            running: 0,
            pending: 0,
            success_rate: 0,
            avg_duration_seconds: 0,
          },
        },
      })),
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBody = await request.json() as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("tab", { name: "Settings" }));
    await user.click(screen.getByRole("combobox", { name: "Model" }));
    await user.click(await screen.findByText("claude-sonnet-4-6"));
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(updateBody).toMatchObject({ model: "claude-sonnet-4-6" });
    });
  });

  it("preserves a saved unavailable model when saving another field", async () => {
    const user = userEvent.setup();
    let updateBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/settings", () => HttpResponse.json({
        data: {
          settings: {
            default_agent_type: "codex",
          },
        },
      })),
      http.get("*/api/v1/settings/credentials/resolved", () => HttpResponse.json({
        data: [],
        meta: {},
      })),
      http.get("*/api/v1/settings/credentials/team", () => HttpResponse.json({
        data: [],
        meta: {},
      })),
      http.get("*/api/v1/settings/codex-auth/status", () => HttpResponse.json({
        data: null,
      })),
      http.get("*/api/v1/settings/coding-auths", () => HttpResponse.json({
        data: [],
        meta: {},
      })),
      http.get("*/api/v1/coding-credentials*", () => HttpResponse.json({
        data: [],
        meta: {},
      })),
      http.get("*/api/v1/automations/auto-1", () => HttpResponse.json({
        data: {
          id: "auto-1",
          org_id: "org-1",
          repository_id: "repo-1",
          name: "Weekly audit",
          goal: "Check release health",
          scope: "",
          agent_type: "claude_code",
          model_override: "claude-sonnet-4-6",
          interval_value: 1,
          interval_unit: "weeks",
          base_branch: "main",
          enabled: true,
          timezone: "UTC",
          last_run_at: null,
          next_run_at: null,
          priority: 50,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      })),
      http.get("*/api/v1/automations/auto-1/runs*", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/automations/auto-1/stats*", () => HttpResponse.json({
        data: {
          since: "2026-01-01T00:00:00Z",
          until: "2026-01-31T00:00:00Z",
          buckets: [],
          totals: {
            total: 0,
            completed: 0,
            completed_noop: 0,
            failed: 0,
            skipped: 0,
            running: 0,
            pending: 0,
            success_rate: 0,
            avg_duration_seconds: 0,
          },
        },
      })),
      http.get("*/api/v1/repositories/repo-1/branches", () => HttpResponse.json({
        data: [
          { name: "main", protected: true },
          { name: "release/ops", protected: false },
        ],
        meta: {},
      })),
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBody = await request.json() as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("tab", { name: "Settings" }));
    await user.click(await screen.findByRole("button", { name: "Base branch" }));
    await user.type(await screen.findByPlaceholderText("Search branches..."), "ops");
    await user.click(await screen.findByText("release/ops"));
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(updateBody).toMatchObject({
        base_branch: "release/ops",
        model: "claude-sonnet-4-6",
      });
    });
  });

});
