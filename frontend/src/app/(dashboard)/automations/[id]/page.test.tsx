import { beforeEach, describe, it, expect, vi } from "vitest";
import { fireEvent, renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { http, HttpResponse } from "msw";
import AutomationDetailPage from "./page";
import { AUTOMATION_GOAL_MAX_LENGTH } from "@/lib/automation-validation";

const pushMock = vi.fn();
const currentUserRole = vi.hoisted(() => ({ value: "member" }));

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

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    user: { role: currentUserRole.value },
    isLoading: false,
  }),
}));

vi.mock("./automation-stats-card", () => ({
  AutomationStatsCard: () => <div data-testid="automation-stats-card" />,
}));

describe("AutomationDetailPage", () => {
  beforeEach(() => {
    currentUserRole.value = "member";
    pushMock.mockReset();
  });

  it("matches the schedule controls and labels to the app input sizing", async () => {
    server.use(
      http.get("*/api/v1/automations/auto-1", () => HttpResponse.json({
        data: {
          id: "auto-1",
          org_id: "org-1",
          repository_id: "repo-1",
          name: "Weekly audit",
          goal: "Check release health",
          scope: "",
          icon_type: "emoji",
          icon_value: "🧪",
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
    expect(screen.getByRole("button", { name: "Change automation emoji" })).toHaveTextContent("🧪");

    await userEvent.setup().click(screen.getByRole("tab", { name: "Settings" }));

    const timezoneButton = screen.getByTitle("UTC");
    const scheduleRow = timezoneButton.parentElement;
    const runEveryText = screen.getByText("Run every");
    const atText = screen.getByText("At");
    const intervalUnitTrigger = screen.getByLabelText("Interval unit");
    const hourTrigger = screen.getByLabelText("Run at hour");
    const minuteTrigger = screen.getByLabelText("Run at minute");

    expect(scheduleRow).toHaveClass("flex-wrap");
    expect(timezoneButton).toHaveClass("w-full", "sm:w-auto");
    expect(intervalUnitTrigger).toHaveClass("h-9", "text-base", "sm:text-xs");
    expect(hourTrigger).toHaveClass("h-9", "text-base", "sm:text-xs");
    expect(minuteTrigger).toHaveClass("h-9", "text-base", "sm:text-xs");
    expect(timezoneButton).toHaveClass("h-9", "text-base", "sm:text-xs");
    expect(runEveryText).toHaveClass("text-xs", "font-medium", "leading-none", "text-muted-foreground");
    expect(atText).toHaveClass("text-xs", "font-medium", "leading-none", "text-muted-foreground");
    expect(screen.queryByText(/Run time is in/i)).not.toBeInTheDocument();
  });

  it("allows the interval value to be cleared while editing", async () => {
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
          interval_unit: "hours",
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
    const intervalInput = screen.getByLabelText("Interval value");
    await user.clear(intervalInput);

    expect(intervalInput).toHaveValue(null);
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();

    await user.type(intervalInput, "2");
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(updateBody).toMatchObject({ interval_value: 2 });
    });
  });

  it("hides member-only automation actions from builders", async () => {
    currentUserRole.value = "builder";
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

    expect(screen.queryByRole("button", { name: "Pause" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Run now" })).not.toBeInTheDocument();

    await userEvent.setup().click(screen.getByRole("tab", { name: "Settings" }));
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
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
      expect(updateBody).toMatchObject({ base_branch: "release/ops", identity_scope: "org" });
    });
  });

  it("saves the selected personal automation identity scope", async () => {
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
          identity_scope: "org",
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
    await user.click(screen.getByRole("combobox", { name: "Run as" }));
    await user.click(await screen.findByText("Personal automation"));
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(updateBody).toMatchObject({ identity_scope: "personal" });
    });
  });

  it("saves the selected automation emoji", async () => {
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
          icon_type: "emoji",
          icon_value: "🧪",
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
    await user.click(screen.getByRole("button", { name: "Automation emoji" }));
    await user.click(await screen.findByRole("option", { name: /Rocket/ }));
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(updateBody).toMatchObject({ icon_type: "emoji", icon_value: "🚀" });
    });
  });

  it("keeps the settings emoji selector small on the same row as the name field", async () => {
    server.use(
      http.get("*/api/v1/automations/auto-1", () => HttpResponse.json({
        data: {
          id: "auto-1",
          org_id: "org-1",
          repository_id: "repo-1",
          name: "Weekly audit",
          goal: "Check release health",
          scope: "",
          icon_type: "emoji",
          icon_value: "🧪",
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

    await screen.findByText("Weekly audit");
    await userEvent.click(screen.getByRole("tab", { name: "Settings" }));

    const identityRow = screen.getByTestId("automation-settings-identity-row");
    expect(identityRow).toHaveClass("grid-cols-[4.75rem_minmax(0,1fr)]");
    expect(screen.getByRole("button", { name: "Automation emoji" })).toHaveClass("h-9", "w-16");
    expect(screen.getByLabelText("Name")).toHaveValue("Weekly audit");
  });

  it("updates the automation emoji from the header picker without changing tabs", async () => {
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
          icon_type: "emoji",
          icon_value: "🧪",
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
        return HttpResponse.json({ data: { id: "auto-1", icon_type: "emoji", icon_value: "🚀" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Change automation emoji" }));
    await user.click(await screen.findByRole("option", { name: /Rocket/ }));

    expect(screen.getByRole("tab", { name: "Runs" })).toHaveAttribute("data-state", "active");
    await waitFor(() => {
      expect(updateBody).toMatchObject({ icon_type: "emoji", icon_value: "🚀" });
    });
  });

  it("inserts selected @ mentions into the edit goal field", async () => {
    const user = userEvent.setup();

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
      http.get("*/api/v1/session-composer/files", ({ request }) => {
        const url = new URL(request.url);
        if (!url.searchParams.get("q")) {
          return HttpResponse.json({ data: [], meta: {} });
        }

        return HttpResponse.json({
          data: [
            {
              kind: "directory",
              token: "@internal/services",
              path: "internal/services",
              display: "internal/services",
            },
          ],
          meta: {},
        });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("tab", { name: "Settings" }));

    const goalInput = screen.getByLabelText("Goal");
    await user.clear(goalInput);
    await user.type(goalInput, "Inspect @serv");
    await user.click(await screen.findByRole("button", { name: "internal/services" }));

    expect(goalInput).toHaveValue("Inspect @internal/services ");
  });

  it("inserts selected slash commands into the edit goal field", async () => {
    const user = userEvent.setup();

    server.use(
      http.get("*/api/v1/settings", () => HttpResponse.json({
        data: {
          settings: {
            default_agent_type: "codex",
          },
        },
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
      http.get("*/api/v1/session-composer/slash-commands", () => HttpResponse.json({
        groups: [
          {
            source: "builtin",
            label: "Codex commands",
            items: [
              {
                kind: "command",
                agent_type: "codex",
                name: "review",
                token: "/review",
                display: "/review",
                description: "Review pending changes",
                source: "builtin",
              },
            ],
          },
        ],
      })),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("tab", { name: "Settings" }));

    const goalInput = screen.getByLabelText("Goal");
    await user.clear(goalInput);
    await user.type(goalInput, "/rev");
    await user.click(await screen.findByRole("button", { name: /\/review/i }));

    expect(goalInput).toHaveValue("/review ");
  });

  it("shows goal length validation and blocks saving when the goal exceeds the backend limit", async () => {
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

    fireEvent.change(screen.getByLabelText("Goal"), {
      target: { value: "x".repeat(AUTOMATION_GOAL_MAX_LENGTH + 1) },
    });

    expect(
      screen.getByText(`Goal must be at most ${AUTOMATION_GOAL_MAX_LENGTH.toLocaleString("en-US")} characters.`),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        `${(AUTOMATION_GOAL_MAX_LENGTH + 1).toLocaleString("en-US")} / ${AUTOMATION_GOAL_MAX_LENGTH.toLocaleString("en-US")}`,
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
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

  it("saves the selected reasoning override", async () => {
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
        data: [{ provider: "openai", source: "org" }],
        meta: {},
      })),
      http.get("*/api/v1/settings/credentials/team", () => HttpResponse.json({
        data: [],
        meta: {},
      })),
      http.get("*/api/v1/settings/codex-auth/status", () => HttpResponse.json({
        data: { status: "completed" },
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
    await user.click(screen.getByRole("combobox", { name: "Reasoning" }));
    await user.click(await screen.findByText("High"));
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(updateBody).toMatchObject({ reasoning_effort: "high" });
    });
  });
});
