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
const toast = vi.hoisted(() => ({
  success: vi.fn(),
  info: vi.fn(),
  warning: vi.fn(),
  error: vi.fn(),
}));

vi.mock("@/lib/notify", () => ({ notify: toast }));

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

// An unhandled request would REJECT, and the editor deliberately treats a
// transport failure as "valid" so a dead preview can't wedge unrelated fields.
// Tests that need the schedule to stay unsettled have to say so explicitly.
const neverResolves = () => new Promise<never>(() => {});

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
    toast.error.mockReset();
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

  it("renders the automation detail skeleton while the automation loads", () => {
    server.use(
      http.get("*/api/v1/automations/auto-1", async () => new Promise<never>(() => {})),
    );

    renderWithProviders(<AutomationDetailPage />);

    expect(screen.getByLabelText("Loading automation")).toHaveAttribute("aria-busy", "true");
    expect(screen.getByRole("link", { name: "Back to automations" })).toBeInTheDocument();
    expect(screen.queryByText("Loading...")).not.toBeInTheDocument();
    expect(screen.getByTestId("automation-detail-header-skeleton-copy")).toHaveClass("min-w-0", "flex-1");
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

    // The schedule is a compound control, so it lives in a popover hung off the
    // Triggers property rather than a separate edit mode.
    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "Triggers" }));

    expect(
      screen.queryByRole("dialog", { name: "Automation settings" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Edit" }),
    ).not.toBeInTheDocument();

    const timezoneButton = screen.getByTitle("UTC");
    const scheduleRow = timezoneButton.parentElement;
    const everyText = screen.getByText("Every");
    const intervalUnitTrigger = screen.getByLabelText("Interval unit");

    expect(scheduleRow).toHaveClass("flex-wrap");
    expect(everyText).toHaveClass("text-muted-foreground");
    expect(intervalUnitTrigger).toHaveClass("h-9");
    expect(timezoneButton).toHaveClass("h-9", "type-dense", "max-sm:text-base");
    expect(intervalUnitTrigger).not.toHaveClass("text-base");
    expect(timezoneButton).not.toHaveClass("text-base");
    expect(screen.queryByText(/Run time is in/i)).not.toBeInTheDocument();
  });

  it("keeps everyday properties inline and only rare ones behind Advanced", async () => {
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

    // No edit mode to enter: the goal and the day-to-day properties are already
    // live controls on first paint.
    expect(screen.getByLabelText("Goal")).toHaveAttribute("rows", "9");
    expect(screen.getByRole("combobox", { name: "Model" })).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Base branch" }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Scope")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Triggers" }),
    ).toBeInTheDocument();

    // Only the rarely-touched knobs stay folded away.
    expect(screen.queryByLabelText("Review passes")).not.toBeInTheDocument();

    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "Advanced" }));

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

    await user.click(screen.getByRole("button", { name: "Triggers" }));
    const intervalInput = screen.getByLabelText("Interval value");
    await user.clear(intervalInput);

    expect(intervalInput).toHaveValue(null);

    await user.tab();

    expect(intervalInput).toHaveValue(1);

    // Blur restored the stored value, so the draft never reaches a state that
    // differs from what was loaded — and an autosave that fires per-field must
    // not treat a transient empty box as a schedule change, or PATCH would
    // recompute next_run_at for nothing.
    await waitFor(() => expect(screen.getByTitle("UTC")).toBeInTheDocument());
    expect(updateBody).toBeNull();
  });

  it("sends the interval when the value actually changes", async () => {
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

    await user.click(screen.getByRole("button", { name: "Triggers" }));
    const intervalInput = screen.getByLabelText("Interval value");
    await user.clear(intervalInput);
    await user.type(intervalInput, "6");
    expect(intervalInput).toHaveValue(6);

    // The preview is debounced, and the schedule commits itself only once the
    // draft settles into a server-previewed state — no Save button involved.
    await screen.findByText(/Next run:/);

    await waitFor(() => {
      expect(updateBody).toMatchObject({
        schedule_type: "interval",
        interval_value: 6,
        interval_unit: "hours",
        interval_run_at: "",
        timezone: "UTC",
      });
    });
  });

  it("clears a stored run time when the cadence drops below a day", async () => {
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
            schedule_type: "interval",
            interval_value: 3,
            interval_unit: "days",
            interval_run_at: "09:00",
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

    await user.click(screen.getByRole("button", { name: "Triggers" }));
    await user.click(screen.getByRole("combobox", { name: "Interval unit" }));
    await user.click(await screen.findByRole("option", { name: "hours" }));

    // A sub-day cadence has no wall clock, so the editor stops showing one.
    expect(
      screen.queryByRole("combobox", { name: "Run time" }),
    ).not.toBeInTheDocument();

    // PATCH treats an absent interval_run_at as "unchanged", so an explicit ""
    // is the only thing that actually unanchors the stored schedule.
    await waitFor(() => {
      expect(updateBody).toMatchObject({
        schedule_type: "interval",
        interval_unit: "hours",
        interval_run_at: "",
      });
    });
  });

  it("sends nothing at all when the trigger popover is opened but untouched", async () => {
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
            schedule_type: "cron",
            // Sunday-as-7 and an unsorted day list both round-trip to a
            // different-but-equivalent expression, so an untouched schedule
            // must not be regenerated from the parsed draft. PATCH also
            // recomputes next_run_at whenever any schedule field is present,
            // so the safe thing is to send none of them.
            cron_expression: "0 9 * * 4,7",
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

    await user.click(screen.getByRole("button", { name: "Triggers" }));
    // Wait for the editor's debounced preview to settle, which is the point at
    // which a genuine edit would commit.
    await screen.findByText(/Next run:|Could not preview/);

    // Sunday-as-7 and an unsorted day list both round-trip to a
    // different-but-equivalent cron expression, so simply looking at the
    // schedule must not regenerate it. Timezone alone is enough to make PATCH
    // recompute next_run_at, which would push the next run out by a full
    // interval every time someone opened the popover to read the cadence.
    expect(updateBody).toBeNull();
  });

  it("does not disturb an interval automation's next run when saving other fields", async () => {
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
            schedule_type: "interval",
            interval_value: 3,
            interval_unit: "days",
            interval_run_at: "09:00",
            base_branch: "main",
            enabled: true,
            timezone: "UTC",
            last_run_at: null,
            next_run_at: "2026-01-02T09:00:00Z",
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

    const scope = screen.getByLabelText("Scope");
    fireEvent.change(scope, { target: { value: "src/" } });
    fireEvent.blur(scope);

    await waitFor(() => expect(updateBody).not.toBeNull());
    // Per-field autosave makes this structural rather than a guard someone has
    // to remember: editing the scope can only ever send the scope, so PATCH has
    // no schedule field to recompute next_run_at from.
    expect(updateBody).toEqual({ scope: "src/" });
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
      .click(screen.getByRole("button", { name: "Properties" }));
    expect(
      screen.getByRole("dialog", { name: "Automation properties" }),
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
      await screen.findByRole("heading", { name: "Execution history" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: "Latest execution" }),
    ).toBeInTheDocument();
    expect(screen.getByText(/Operational status only/)).toBeInTheDocument();
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

    // Inline editing must not become an accidental permission grant: the same
    // rows render as plain text for someone who cannot manage the automation.
    expect(
      screen.queryByRole("combobox", { name: "Repository" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("combobox", { name: "Run as" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Scope")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Triggers" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Base branch" }),
    ).not.toBeInTheDocument();
  });

  it("reports autosave progress and rolls rejected property and trigger edits back", async () => {
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
            identity_scope: "org",
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
      http.patch("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({ error: "nope" }, { status: 500 }),
      ),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    const runAs = screen.getByRole("combobox", { name: "Run as" });
    expect(runAs).toHaveTextContent("Organization");

    await user.click(runAs);
    await user.click(await screen.findByRole("option", { name: "Personal" }));

    // Without a Save button the indicator is the only signal that a click was
    // persisted, so a failed write has to say so and put the value back.
    expect(await screen.findByText("Couldn't save")).toBeInTheDocument();
    await waitFor(() =>
      expect(
        screen.getByRole("combobox", { name: "Run as" }),
      ).toHaveTextContent("Organization"),
    );

    await user.click(screen.getByRole("button", { name: "Triggers" }));
    const intervalInput = screen.getByLabelText("Interval value");
    await user.clear(intervalInput);
    await user.type(intervalInput, "6");
    await screen.findByText(/Next run:/);

    // Trigger controls keep a local draft while their popover is open. A
    // rejected schedule must restore that draft, not merely the query cache.
    await waitFor(() => expect(intervalInput).toHaveValue(1));

    const mergedTrigger = screen.getByRole("checkbox", {
      name: "When a PR is merged",
    });
    await user.click(mergedTrigger);
    await waitFor(() => expect(mergedTrigger).not.toBeChecked());
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

    await user.click(
      await screen.findByRole("button", { name: "Base branch" }),
    );
    await user.type(
      await screen.findByPlaceholderText("Search branches..."),
      "ops",
    );
    await user.click(await screen.findByText("release/ops"));

    // Picking the branch is the save. Nothing else rides along with it.
    await waitFor(() => {
      expect(updateBody).toEqual({ base_branch: "release/ops" });
    });
  });

  it("updates the repository and resets the base branch to its default", async () => {
    const user = userEvent.setup();
    let updateBody: Record<string, unknown> | null = null;
    // Autosaved rows render whatever the server last returned, so the mock has
    // to actually apply the patch — otherwise the refetch that follows every
    // save would replay the pre-edit fixture and mask a real regression.
    const stored: Record<string, unknown> = {
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
    };

    server.use(
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({ data: { ...stored } }),
      ),
      http.get("*/api/v1/repositories", () =>
        HttpResponse.json({
          data: [
            {
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
            {
              id: "repo-2",
              org_id: "org-1",
              integration_id: "int-1",
              github_id: 2,
              full_name: "acme/worker",
              default_branch: "trunk",
              private: false,
              clone_url: "https://github.com/acme/worker.git",
              installation_id: 10,
              status: "active",
              settings: {},
              created_at: "2026-01-01T00:00:00Z",
              updated_at: "2026-01-01T00:00:00Z",
            },
          ],
          meta: {},
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
        Object.assign(stored, updateBody);
        return HttpResponse.json({ data: { ...stored } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("combobox", { name: "Repository" }));
    await user.click(
      await screen.findByRole("option", { name: "acme/worker" }),
    );

    // The branch reset has to ride along in the same patch: a repository saved
    // on its own would momentarily point at a branch the new repo may not have.
    await waitFor(() => {
      expect(updateBody).toEqual({
        repository_id: "repo-2",
        base_branch: "trunk",
      });
    });
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Base branch" }),
      ).toHaveTextContent("trunk"),
    );
  });

  it("saves identity scope and publish policy as independent edits", async () => {
    const user = userEvent.setup();
    const updateBodies: Record<string, unknown>[] = [];

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
        updateBodies.push((await request.json()) as Record<string, unknown>);
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("combobox", { name: "Run as" }));
    await user.click(await screen.findByRole("option", { name: "Personal" }));

    await waitFor(() =>
      expect(updateBodies).toContainEqual({ identity_scope: "personal" }),
    );

    await user.click(
      screen.getByRole("combobox", { name: "After a successful run" }),
    );
    await user.click(
      await screen.findByRole("option", { name: "Do not publish" }),
    );

    // Two separate selects, two separate patches — neither carries the other's
    // field, so a stale render of one can never overwrite the other.
    await waitFor(() =>
      expect(updateBodies).toContainEqual({ publish_policy: "none" }),
    );
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

  it("edits the title inline without disturbing an in-progress scope edit", async () => {
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
    const scope = screen.getByLabelText("Scope");
    await user.type(scope, "backend services");

    const title = screen.getByLabelText("Automation title");
    fireEvent.change(title, { target: { value: "Release audit" } });
    fireEvent.blur(title);

    await waitFor(() => {
      expect(updateBody).toEqual({ name: "Release audit" });
    });

    // The title's save (and the refetch it triggers) must not stomp text the
    // user is still typing in a different field.
    await waitFor(() => {
      expect(screen.getByLabelText("Scope")).toHaveValue("backend services");
    });
    expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Goal")).toBeInTheDocument();
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

    const goalInput = screen.getByLabelText("Goal");
    await user.clear(goalInput);
    await user.type(goalInput, "Inspect @serv");
    await user.click(
      await screen.findByRole("button", { name: "internal/services" }),
    );

    expect(goalInput).toHaveValue("Inspect @internal/services ");
    expect(await screen.findByText("Saving…")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Improve goal" })).toBeEnabled();
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

    await user.click(screen.getByRole("combobox", { name: "Model" }));
    await user.click(await screen.findByText("claude-sonnet-4-6"));

    await waitFor(() => {
      expect(updateBody).toEqual({ model: "claude-sonnet-4-6" });
    });
  });

  it("holds back a schedule removal until another trigger replaces it", async () => {
    const user = userEvent.setup();
    const updateBodies: Record<string, unknown>[] = [];

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
        updateBodies.push((await request.json()) as Record<string, unknown>);
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Triggers" }));
    await user.click(screen.getByRole("button", { name: "Remove schedule" }));

    // Removing the only trigger would leave an automation that can never fire,
    // so it is refused rather than saved — with an inline reason.
    expect(screen.getByText(/Select at least one trigger/)).toBeInTheDocument();
    expect(updateBodies).toHaveLength(0);

    await user.click(
      screen.getByRole("checkbox", { name: "When a PR is merged" }),
    );

    // Once a PR trigger restores the invariant, the held-back schedule removal
    // lands too.
    await waitFor(() =>
      expect(updateBodies).toContainEqual(
        expect.objectContaining({ triggers: ["github.pr.merged"] }),
      ),
    );
    await waitFor(() =>
      expect(updateBodies).toContainEqual(
        expect.objectContaining({ schedule_type: "none" }),
      ),
    );
    for (const body of updateBodies) {
      expect(body).not.toHaveProperty("interval_value");
      expect(body).not.toHaveProperty("interval_unit");
      expect(body).not.toHaveProperty("interval_run_at");
    }
  });

  it("never re-sends an unrelated saved model when another field is edited", async () => {
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

    await user.click(
      await screen.findByRole("button", { name: "Base branch" }),
    );
    await user.type(
      await screen.findByPlaceholderText("Search branches..."),
      "ops",
    );
    await user.click(await screen.findByText("release/ops"));

    // The saved model is unavailable to this org, so a batch save had to echo
    // it back to avoid clearing it. Per-field patches never mention it, which
    // removes the failure mode instead of working around it.
    await waitFor(() => {
      expect(updateBody).toEqual({ base_branch: "release/ops" });
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

    await user.click(screen.getByRole("combobox", { name: "Reasoning" }));
    await user.click(await screen.findByText("High"));

    await waitFor(() => {
      expect(updateBody).toEqual({ reasoning_effort: "high" });
    });
  });

  it("clears an unsupported reasoning override in the same patch as the model switch", async () => {
    const user = userEvent.setup();
    let updateBody: Record<string, unknown> | null = null;
    const stored: Record<string, unknown> = {
      id: "auto-1",
      org_id: "org-1",
      repository_id: "repo-1",
      name: "Weekly audit",
      goal: "Check release health",
      scope: "",
      // No agent_type: the effective agent comes from the model, which is
      // exactly the shape the API infers from too.
      model_override: "gpt-5.4",
      reasoning_effort: "high",
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
    };

    server.use(
      // Amp is the org default, so its modes are offered without a credential.
      // Amp has no reasoning levels — picking one has to clear the override.
      http.get("*/api/v1/settings", () =>
        HttpResponse.json({
          data: { settings: { default_agent_type: "amp" } },
        }),
      ),
      http.get("*/api/v1/settings/codex-auth/status", () =>
        HttpResponse.json({ data: null }),
      ),
      http.get("*/api/v1/coding-credentials*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.get("*/api/v1/automations/auto-1", () =>
        HttpResponse.json({ data: { ...stored } }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      // Stateful, so the refetch that follows the save can't replay the
      // pre-edit fixture and hide whether the reset actually landed.
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBody = (await request.json()) as Record<string, unknown>;
        const { model, reasoning_effort } = updateBody as {
          model?: string;
          reasoning_effort?: string;
        };
        if (model !== undefined) stored.model_override = model;
        if (reasoning_effort !== undefined) {
          stored.reasoning_effort = reasoning_effort;
        }
        return HttpResponse.json({ data: { ...stored } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });
    // Codex today, so the reasoning row is on screen and the override is live.
    expect(
      screen.getByRole("combobox", { name: "Reasoning" }),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("combobox", { name: "Model" }));
    await user.click(await screen.findByRole("option", { name: "smart" }));

    // The API re-validates the STORED reasoning override against the new
    // model's agent, so a lone `model` patch would come back 400 and the row
    // the user would have to clear is the one that disappears with the switch.
    await waitFor(() => {
      expect(updateBody).toEqual({ model: "smart", reasoning_effort: "" });
    });
    await waitFor(() =>
      expect(
        screen.queryByRole("combobox", { name: "Reasoning" }),
      ).not.toBeInTheDocument(),
    );
  });

  it("leaves a still-supported reasoning override alone when the model changes", async () => {
    const user = userEvent.setup();
    let updateBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/settings", () =>
        HttpResponse.json({
          data: { settings: { default_agent_type: "claude_code" } },
        }),
      ),
      http.get("*/api/v1/settings/codex-auth/status", () =>
        HttpResponse.json({ data: null }),
      ),
      http.get("*/api/v1/coding-credentials*", () =>
        HttpResponse.json({ data: [], meta: {} }),
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
            model_override: "gpt-5.4",
            reasoning_effort: "high",
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
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);

    await waitFor(() => {
      expect(screen.getByText("Weekly audit")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("combobox", { name: "Model" }));
    await user.click(await screen.findByRole("option", { name: "claude-sonnet-4-6" }));

    // Claude Code has reasoning levels, so the reset must NOT ride along —
    // clearing an override the user never touched would be its own bug.
    await waitFor(() => {
      expect(updateBody).toEqual({ model: "claude-sonnet-4-6" });
    });
  });

  it("commits a pending trigger-filter edit when the popover closes", async () => {
    const user = userEvent.setup();
    const updateBodies: Record<string, unknown>[] = [];

    server.use(
      // Pin the preview as never-settling. An unmocked preview FAILS, and the
      // editor treats a transport failure as valid — which lets the settled
      // path emit a byte-identical patch and satisfy this test without the
      // unmount flush ever running.
      http.post("*/api/v1/automations/schedule-preview", () => neverResolves()),
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
            github_event_triggers: ["github.pr.merged"],
            github_event_filters: { authors: [] },
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
        }),
      ),
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBodies.push((await request.json()) as Record<string, unknown>);
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);
    await screen.findByText("Weekly audit");

    await user.click(screen.getByRole("button", { name: "Triggers" }));
    await user.click(screen.getByRole("button", { name: /Trigger filters/ }));
    fireEvent.change(await screen.findByLabelText("Authors"), {
      target: { value: "octocat" },
    });

    // Closing the popover removes the focused input from the DOM, which does
    // NOT dispatch focusout — so onBlur never runs and the debounced edit has
    // to be flushed on unmount or it is lost with no error and no indicator.
    await user.keyboard("{Escape}");

    await waitFor(() =>
      expect(updateBodies).toContainEqual({
        github_event_filters: { authors: ["octocat"] },
      }),
    );
  });

  it("commits a pending schedule edit when the popover closes", async () => {
    const user = userEvent.setup();
    const updateBodies: Record<string, unknown>[] = [];

    server.use(
      // Never-settling, so `valid` stays false for the whole test and the only
      // thing that can produce a patch is the unmount flush under test.
      http.post("*/api/v1/automations/schedule-preview", () => neverResolves()),
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
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBodies.push((await request.json()) as Record<string, unknown>);
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);
    await screen.findByText("Weekly audit");

    await user.click(screen.getByRole("button", { name: "Triggers" }));
    const intervalInput = await screen.findByLabelText("Interval value");
    await user.clear(intervalInput);
    await user.type(intervalInput, "6");

    // The schedule has no blur to fall back on: it normally commits only after
    // the editor's 300ms debounce AND a server preview round-trip. Closing
    // inside that window must still persist the draft, on the client-side
    // verdict — the API validates it again on write.
    await user.keyboard("{Escape}");

    await waitFor(() =>
      expect(updateBodies).toContainEqual(
        expect.objectContaining({
          schedule_type: "interval",
          interval_value: 6,
        }),
      ),
    );
  });

  it("does not commit a client-invalid schedule when the popover closes", async () => {
    const user = userEvent.setup();
    const updateBodies: Record<string, unknown>[] = [];

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
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBodies.push((await request.json()) as Record<string, unknown>);
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);
    await screen.findByText("Weekly audit");

    await user.click(screen.getByRole("button", { name: "Triggers" }));
    // 999 is past the API's 1-365 bound, so validateScheduleDraft rejects the
    // draft locally. Flushing on unmount must not turn that into a certain 400.
    const intervalInput = await screen.findByLabelText("Interval value");
    await user.clear(intervalInput);
    await user.type(intervalInput, "999");
    await user.keyboard("{Escape}");

    await new Promise((resolve) => setTimeout(resolve, 600));
    expect(updateBodies).toEqual([]);
  });

  it("does not commit a schedule the preview already refused when the popover closes", async () => {
    const user = userEvent.setup();
    const updateBodies: Record<string, unknown>[] = [];

    server.use(
      // The API has already told us, on screen, that this draft is unusable.
      http.post("*/api/v1/automations/schedule-preview", () =>
        HttpResponse.json(
          {
            error: {
              code: "INVALID_SCHEDULE",
              message: "No future occurrence.",
            },
          },
          { status: 400 },
        ),
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
      http.patch("*/api/v1/automations/auto-1", async ({ request }) => {
        updateBodies.push((await request.json()) as Record<string, unknown>);
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<AutomationDetailPage />);
    await screen.findByText("Weekly audit");

    await user.click(screen.getByRole("button", { name: "Triggers" }));
    const intervalInput = await screen.findByLabelText("Interval value");
    await user.clear(intervalInput);
    await user.type(intervalInput, "6");

    // Wait until the refusal for THIS draft is on screen — that is the state
    // the user walks away from.
    await screen.findByText("No future occurrence.");
    await user.keyboard("{Escape}");

    // The write cannot succeed, so sending it would buy nothing but a failed
    // request and an error toast for a schedule the user watched get rejected.
    await new Promise((resolve) => setTimeout(resolve, 600));
    expect(updateBodies).toEqual([]);
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("surfaces the API's reason when an inline property save is rejected", async () => {
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
            identity_scope: "org",
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
      http.patch("*/api/v1/automations/auto-1", () =>
        HttpResponse.json(
          {
            error: {
              code: "INVALID_IDENTITY_SCOPE",
              message: "identity_scope=personal requires automation.created_by",
            },
          },
          { status: 400 },
        ),
      ),
    );

    renderWithProviders(<AutomationDetailPage />);
    await screen.findByText("Weekly audit");

    await user.click(screen.getByRole("combobox", { name: "Run as" }));
    await user.click(await screen.findByRole("option", { name: "Personal" }));

    // With no Save button, this toast is the only thing that can tell the user
    // WHICH row the server refused and why. A fixed string throws that away.
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "Couldn\u2019t save automation: identity_scope=personal requires automation.created_by",
      ),
    );
  });
});
