import { beforeEach, describe, it, expect, vi } from "vitest";
import {
  fireEvent,
  renderWithProviders,
  screen,
  userEvent,
  waitFor,
} from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { http, HttpResponse } from "msw";
import AutomationDetailPage from "./page";
import { AUTOMATION_GOAL_MAX_LENGTH } from "@/lib/automation-validation";

const pushMock = vi.fn();
const currentUserRole = vi.hoisted(() => ({ value: "member" }));

vi.mock("next/link", () => ({
  default: ({
    children,
    href,
    ...props
  }: React.ComponentProps<"a"> & { href: string }) => (
    <a href={href} {...props}>
      {children}
    </a>
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

const selectEmojiOption = async (name: string) => {
  const listbox = await screen.findByRole("listbox");
  const option = listbox.querySelector<HTMLElement>(
    `[role="option"][aria-label="${name}"]`,
  );
  expect(option).not.toBeNull();
  fireEvent.click(option as HTMLElement);
};

describe("AutomationDetailPage", () => {
  beforeEach(() => {
    currentUserRole.value = "member";
    pushMock.mockReset();
    server.use(
      http.get("*/api/v1/repositories/repo-1", () =>
        HttpResponse.json({
          data: {
            id: "repo-1",
            org_id: "org-1",
            integration_id: "int-1",
            github_id: 1,
            full_name: "acme/repo",
            default_branch: "main",
            private: false,
            clone_url: "https://github.com/acme/repo.git",
            installation_id: 10,
            status: "active",
            settings: {},
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
        }),
      ),
    );
  });

  it("matches the schedule controls and labels to the app input sizing", async () => {
    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });
    const headerEmoji = screen.getByRole("button", {
      name: "Change automation emoji",
    });
    expect(headerEmoji).toHaveTextContent("🧪");
    expect(headerEmoji).toHaveClass("h-auto", "p-0", "align-baseline");
    expect(headerEmoji).not.toHaveClass("size-9");

    await userEvent.setup().click(screen.getByRole("button", { name: "Edit" }));

    const timezoneButton = screen.getByTitle("UTC");
    const scheduleRow = timezoneButton.parentElement;
    const runEveryText = screen.getByText("Run every");
    const atText = screen.getByText("At");
    const intervalUnitTrigger = screen.getByLabelText("Interval unit");
    const hourTrigger = screen.getByLabelText("Run at hour");
    const minuteTrigger = screen.getByLabelText("Run at minute");

    expect(scheduleRow).not.toHaveClass("flex-wrap");
    expect(timezoneButton).toHaveClass("w-[12.5rem]", "max-w-full");
    expect(intervalUnitTrigger).toHaveClass(
      "h-9",
      "type-dense",
      "max-sm:text-base",
    );
    expect(hourTrigger).toHaveClass("h-9", "type-dense", "max-sm:text-base");
    expect(minuteTrigger).toHaveClass("h-9", "type-dense", "max-sm:text-base");
    expect(timezoneButton).toHaveClass("h-9", "type-dense", "max-sm:text-base");
    expect(intervalUnitTrigger).not.toHaveClass("text-base");
    expect(timezoneButton).not.toHaveClass("text-base");
    expect(runEveryText).toHaveClass(
      "text-xs",
      "font-medium",
      "leading-none",
      "text-muted-foreground",
    );
    expect(atText).toHaveClass(
      "text-xs",
      "font-medium",
      "leading-none",
      "text-muted-foreground",
    );
    expect(screen.queryByText(/Run time is in/i)).not.toBeInTheDocument();
  });

  it("keeps advanced automation controls collapsed by default", async () => {
    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await userEvent.setup().click(screen.getByRole("button", { name: "Edit" }));

    expect(screen.getByLabelText("Goal")).toHaveAttribute("rows", "9");
    expect(
      screen.queryByRole("combobox", { name: "Model" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Base branch" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Review passes")).not.toBeInTheDocument();

    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "Advanced settings" }));

    expect(screen.getByRole("combobox", { name: "Model" })).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Base branch" }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Review passes")).toBeInTheDocument();
  });

  it("updates the browser tab title with the automation name", async () => {
    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
          data: {
            id: "auto-1",
            org_id: "org-1",
            repository_id: "repo-1",
            name: "Weekly release audit",
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(document.title).toBe("143 | Weekly release audit");
    });
  });

  it("allows a blank interval while editing and restores it on blur", async () => {
    const user = userEvent.setup();
    let updateBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Edit" }));
    const intervalInput = screen.getByLabelText("Interval value");
    await user.clear(intervalInput);

    expect(intervalInput).toHaveValue(null);
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();

    await user.tab();

    expect(intervalInput).toHaveValue(1);
    expect(screen.getByRole("button", { name: "Save changes" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(updateBody).toMatchObject({ interval_value: 1 });
    });
  });

  it("shows readable metadata and run actions in the details rail", async () => {
    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
          data: {
            id: "auto-1",
            org_id: "org-1",
            repository_id: "repo-1",
            name: "Weekly audit",
            goal: "## Goal\nCheck release health",
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
            identity_scope: "org",
            priority: 50,
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get("limit") === "5") {
          return HttpResponse.json({
            data: [
              {
                id: "run-1",
                automation_id: "auto-1",
                triggered_at: "2026-01-02T00:00:00Z",
                triggered_by: "manual",
                goal_snapshot: "Check release health",
                status: "completed_noop",
                created_at: "2026-01-02T00:00:00Z",
                updated_at: "2026-01-02T00:00:00Z",
              },
            ],
            meta: {},
          });
        }
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
    );

    renderWithProviders(<AutomationDetailPage />);

    expect(await screen.findByLabelText("Goal")).toHaveValue(
      "## Goal\nCheck release health",
    );
    expect(screen.queryByRole("tab")).not.toBeInTheDocument();
    expect(await screen.findByText("acme/repo")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run now" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Pause" })).toBeInTheDocument();

    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "Details" }));
    expect(
      screen.getByRole("dialog", { name: "Automation details" }),
    ).toBeInTheDocument();
    expect(screen.getAllByText("acme/repo").length).toBeGreaterThan(1);
  });

  it("renders the goal as markdown for viewers who cannot edit", async () => {
    currentUserRole.value = "viewer";
    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
          data: {
            id: "auto-1",
            org_id: "org-1",
            repository_id: "repo-1",
            name: "Weekly audit",
            goal: "## Goal\nCheck release health",
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
    );

    renderWithProviders(<AutomationDetailPage />);

    await screen.findByText("Weekly audit");

    // Viewers get rendered markdown, not the inline editor or raw source.
    expect(screen.queryByLabelText("Goal")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Automation title")).not.toBeInTheDocument();
    expect(screen.getByText("Check release health")).toBeInTheDocument();
    expect(screen.queryByText(/##/)).not.toBeInTheDocument();
    // The `## Goal` heading is rendered as a real heading in addition to the
    // section title, confirming markdown rendering rather than raw text.
    expect(
      screen.getAllByRole("heading", { name: "Goal", level: 2 }).length,
    ).toBeGreaterThan(1);
  });

  it("keeps run history in the main column instead of duplicating previous runs in the rail", async () => {
    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({
          data: [
            {
              id: "run-1",
              automation_id: "auto-1",
              triggered_at: "2026-01-02T00:00:00Z",
              triggered_by: "schedule",
              goal_snapshot: "Check release health",
              status: "completed",
              result_summary: "Checked release health",
              completed_at: "2026-01-02T00:00:30Z",
              created_at: "2026-01-02T00:00:00Z",
              updated_at: "2026-01-02T00:00:30Z",
              session: {
                id: "sess-1",
                title: "Checked release health",
                status: "completed",
                failure_retry_advised: false,
                pr_creation_state: "idle",
              },
            },
          ],
          meta: {},
        }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
          data: {
            since: "2026-01-01T00:00:00Z",
            until: "2026-01-31T00:00:00Z",
            buckets: [],
            totals: {
              total: 1,
              completed: 1,
              completed_noop: 0,
              failed: 0,
              skipped: 0,
              running: 0,
              pending: 0,
              success_rate: 1,
              avg_duration_seconds: 30,
            },
          },
        }),
      ),
    );

    renderWithProviders(<AutomationDetailPage />);

    expect(
      await screen.findByRole("heading", { name: "Run history" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: "Previous runs" }),
    ).not.toBeInTheDocument();
  });

  it("hides member-only automation actions from builders", async () => {
    currentUserRole.value = "builder";
    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    expect(
      screen.queryByRole("button", { name: "Pause" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Run now" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Edit" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("tab")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Save changes" }),
    ).not.toBeInTheDocument();
  });

  it("renders a back button to the automations list preserving query params", async () => {
    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
    );

    renderWithProviders(<AutomationDetailPage />);

    const backLink = await screen.findByLabelText("Back to automations");
    expect(backLink).toHaveAttribute("href", "/automations?tab=paused");
  });

  it("saves the selected base branch from the branch picker", async () => {
    const user = userEvent.setup();
    let updateBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/repositories/repo-1/branches", () =>
        HttpResponse.json({
          data: [
            { name: "main", protected: true },
            { name: "release/ops", protected: false },
          ],
          meta: {},
        }),
      ),
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Advanced settings" }));
    await user.click(
      await screen.findByRole("button", { name: "Base branch" }),
    );
    await user.type(
      await screen.findByPlaceholderText("Search branches..."),
      "ops",
    );
    await user.click(await screen.findByText("release/ops"));
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(updateBody).toMatchObject({
        base_branch: "release/ops",
        identity_scope: "org",
      });
    });
  });

  it("saves the selected personal automation identity scope", async () => {
    const user = userEvent.setup();
    let updateBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("combobox", { name: "Run as" }));
    await user.click(await screen.findByText("Personal automation"));
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(updateBody).toMatchObject({ identity_scope: "personal" });
    });
  });

  it("saves the selected automation emoji inline", async () => {
    const user = userEvent.setup();
    let updateBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(
      screen.getByRole("button", { name: "Change automation emoji" }),
    );
    await selectEmojiOption("Rocket");

    await waitFor(() => {
      expect(updateBody).toMatchObject({
        icon_type: "emoji",
        icon_value: "🚀",
      });
    });
  });

  it("edits the title inline and keeps title and goal out of settings", async () => {
    const user = userEvent.setup();
    let updateBody: Record<string, unknown> | null = null;
    let updatedAt = "2026-01-01T00:00:00Z";
    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
            updated_at: updatedAt,
          },
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBody = (await request.json()) as Record<string, unknown>;
        updatedAt = "2026-01-02T00:00:00Z";
        return HttpResponse.json({
          data: {
            id: "auto-1",
            name: "Release audit",
            goal: "Check release health",
          },
        });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await screen.findByText("Weekly audit");
    await user.click(screen.getByRole("button", { name: "Edit" }));
    const scope = screen.getByLabelText(/Scope/);
    await user.type(scope, "backend services");

    const title = screen.getByLabelText("Automation title");
    fireEvent.change(title, { target: { value: "Release audit" } });
    fireEvent.blur(title);

    await waitFor(() => {
      expect(updateBody).toEqual({ name: "Release audit" });
    });

    await waitFor(() => {
      expect(screen.getByLabelText(/Scope/)).toHaveValue("backend services");
    });
    expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Goal")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Automation emoji" }),
    ).not.toBeInTheDocument();
  });

  it("reverts a cleared title on blur instead of saving or leaving it blank", async () => {
    const user = userEvent.setup();
    let patched = false;
    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
      http.patch("*/api/v1/automations/auto-1", async () => {
        patched = true;
        return HttpResponse.json({ data: {} });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    const title = await screen.findByLabelText("Automation title");
    await user.clear(title);
    await user.tab();

    // Empty is never persisted, and the field snaps back to the saved name
    // rather than being left blank.
    await waitFor(() => {
      expect(screen.getByLabelText("Automation title")).toHaveValue(
        "Weekly audit",
      );
    });
    expect(patched).toBe(false);
  });

  it(
    "updates the automation emoji from the header picker without changing tabs",
    { timeout: 12_000 },
    async () => {
      const user = userEvent.setup();
      let updateBody: Record<string, unknown> | null = null;

      server.use(
        http.get("*/api/v1/automations/auto-1", () =>
          HttpResponse.json({
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
          }),
        ),
        http.get("*/api/v1/automations/auto-1/runs*", () =>
          HttpResponse.json({ data: [], meta: {} }),
        ),
        http.get("*/api/v1/automations/auto-1/stats*", () =>
          HttpResponse.json({
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
          }),
        ),
        http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
          updateBody = (await request.json()) as Record<string, unknown>;
          return HttpResponse.json({
            data: { id: "auto-1", icon_type: "emoji", icon_value: "🚀" },
          });
        }),
      );

      renderWithProviders(<AutomationDetailPage />);

      await waitFor(() => {
        expect(screen.getByText("Weekly audit")).toBeInTheDocument();
      });

      await user.click(
        screen.getByRole("button", { name: "Change automation emoji" }),
      );
      await selectEmojiOption("Rocket");

      expect(screen.queryByRole("tab")).not.toBeInTheDocument();
      await waitFor(() => {
        expect(updateBody).toMatchObject({
          icon_type: "emoji",
          icon_value: "🚀",
        });
      });
    },
  );

  it("inserts selected @ mentions into the edit goal field", async () => {
    const user = userEvent.setup();

    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
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
      http.patch("*/api/v1/automations/auto-1", async () => {
        await new Promise((resolve) => setTimeout(resolve, 200));
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Edit" }));

    const goalInput = screen.getByLabelText("Goal");
    await user.clear(goalInput);
    await user.type(goalInput, "Inspect @serv");
    await user.click(
      await screen.findByRole("button", { name: "internal/services" }),
    );

    expect(goalInput).toHaveValue("Inspect @internal/services ");
    expect(await screen.findByText("Saving…")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Improve goal" }),
    ).toBeEnabled();
  });

  it("inserts selected slash commands into the edit goal field", async () => {
    const user = userEvent.setup();

    server.use(
      http.get("*/api/v1/settings", () =>
        HttpResponse.json({
          data: {
            settings: {
              default_agent_type: "codex",
            },
          },
        }),
      ),
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/session-composer/slash-commands", () =>
        HttpResponse.json({
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
        }),
      ),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Edit" }));

    const goalInput = screen.getByLabelText("Goal");
    await user.clear(goalInput);
    await user.type(goalInput, "/rev");
    await user.click(await screen.findByRole("button", { name: /\/review/i }));

    expect(goalInput).toHaveValue("/review ");
  });

  it("shows goal length validation and blocks saving when the goal exceeds the backend limit", async () => {
    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await userEvent.setup().click(screen.getByRole("button", { name: "Edit" }));

    fireEvent.change(screen.getByLabelText("Goal"), {
      target: { value: "x".repeat(AUTOMATION_GOAL_MAX_LENGTH + 1) },
    });

    expect(
      screen.getByText(
        `Goal must be at most ${AUTOMATION_GOAL_MAX_LENGTH.toLocaleString("en-US")} characters.`,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        `${(AUTOMATION_GOAL_MAX_LENGTH + 1).toLocaleString("en-US")} / ${AUTOMATION_GOAL_MAX_LENGTH.toLocaleString("en-US")}`,
      ),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Goal")).toHaveAttribute(
      "aria-invalid",
      "true",
    );
  });

  it("saves the selected model override", async () => {
    const user = userEvent.setup();
    let updateBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/settings", () =>
        HttpResponse.json({
          data: {
            settings: {
              default_agent_type: "codex",
            },
          },
        }),
      ),
      http.get("*/api/v1/settings/codex-auth/status", () =>
        HttpResponse.json({
          data: null,
        }),
      ),
      http.get("*/api/v1/coding-credentials*", ({ request }) => {
        const scope = new URL(request.url).searchParams.get("scope");
        if (scope !== "org") {
          return HttpResponse.json({ data: [], meta: { scope } });
        }
        return HttpResponse.json({
          data: [
            {
              id: "auth-1",
              org_id: "org-1",
              scope: "org",
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
        });
      }),
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Advanced settings" }));
    await user.click(screen.getByRole("combobox", { name: "Model" }));
    await user.click(await screen.findByText("claude-sonnet-4-6"));
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(updateBody).toMatchObject({ model: "claude-sonnet-4-6" });
    });
  });

  it("saves product triggers from automation settings", async () => {
    const user = userEvent.setup();
    let updateBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/settings", () =>
        HttpResponse.json({
          data: { settings: { default_agent_type: "codex" } },
        }),
      ),
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
          data: {
            id: "auto-1",
            org_id: "org-1",
            repository_id: "repo-1",
            name: "Weekly audit",
            goal: "Check release health",
            scope: "",
            icon_type: "emoji",
            icon_value: "🧪",
            schedule_type: "interval",
            interval_value: 1,
            interval_unit: "weeks",
            interval_run_at: "09:00",
            base_branch: "main",
            identity_scope: "org",
            pre_pr_review_loops: 1,
            github_event_triggers: [],
            github_event_filters: {},
            enabled: true,
            timezone: "UTC",
            last_run_at: null,
            next_run_at: null,
            priority: 50,
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("checkbox", { name: "On a schedule" }));
    await user.click(
      screen.getByRole("checkbox", { name: "When a PR is merged" }),
    );
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(updateBody).toMatchObject({
        schedule_type: "none",
        triggers: ["github.pr.merged"],
      });
    });
    expect(updateBody).not.toHaveProperty("interval_value");
    expect(updateBody).not.toHaveProperty("interval_unit");
    expect(updateBody).not.toHaveProperty("interval_run_at");
  });

  it("preserves a saved unavailable model when saving another field", async () => {
    const user = userEvent.setup();
    let updateBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/settings", () =>
        HttpResponse.json({
          data: {
            settings: {
              default_agent_type: "codex",
            },
          },
        }),
      ),
      http.get("*/api/v1/settings/codex-auth/status", () =>
        HttpResponse.json({
          data: null,
        }),
      ),
      http.get("*/api/v1/coding-credentials*", () =>
        HttpResponse.json({
          data: [],
          meta: {},
        }),
      ),
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/repositories/repo-1/branches", () =>
        HttpResponse.json({
          data: [
            { name: "main", protected: true },
            { name: "release/ops", protected: false },
          ],
          meta: {},
        }),
      ),
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Advanced settings" }));
    await user.click(
      await screen.findByRole("button", { name: "Base branch" }),
    );
    await user.type(
      await screen.findByPlaceholderText("Search branches..."),
      "ops",
    );
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
      http.get("*/api/v1/settings", () =>
        HttpResponse.json({
          data: {
            settings: {
              default_agent_type: "codex",
            },
          },
        }),
      ),
      http.get("*/api/v1/settings/codex-auth/status", () =>
        HttpResponse.json({
          data: { status: "completed" },
        }),
      ),
      http.get("*/api/v1/coding-credentials*", () =>
        HttpResponse.json({
          data: [
            {
              id: "auth-1",
              org_id: "org-1",
              scope: "org",
              agent: "codex",
              auth_type: "api_key",
              provider: "openai",
              label: "Org Codex API key",
              status: "healthy",
              is_default: true,
              priority: 1,
              created_at: "2026-01-01T00:00:00Z",
              updated_at: "2026-01-01T00:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({
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
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1/stats*", () =>
        HttpResponse.json({
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
        }),
      ),
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Advanced settings" }));
    await user.click(screen.getByRole("combobox", { name: "Reasoning" }));
    await user.click(await screen.findByText("High"));
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(updateBody).toMatchObject({ reasoning_effort: "high" });
    });
  });
});
