import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import AutomationsPage from "./page";
import { formatDateTime } from "@/lib/utils";

const currentUserRole = vi.hoisted(() => ({ value: "member" }));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    user: { role: currentUserRole.value },
    isLoading: false,
  }),
}));

describe("AutomationsPage", () => {
  it("renders a selectable template gallery when no automations exist", async () => {
    currentUserRole.value = "member";
    server.use(
      http.get("*/api/v1/automations", () => HttpResponse.json({
        data: [],
        meta: {},
      })),
    );

    renderWithProviders(<AutomationsPage />);

    expect(await screen.findByPlaceholderText("Search templates...")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Template library" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Start from blank/i })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /^New automation$/i })).toHaveAttribute("href", "/automations/new");
    expect(screen.getByRole("tab", { name: "Popular" })).toBeInTheDocument();
    expect(screen.getByText("Find flaky tests")).toBeInTheDocument();
    expect(screen.getByText("Security sweep")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Use Find flaky tests/i })).toHaveAttribute(
      "href",
      "/automations/new?template=flaky-tests",
    );
  });

  it("shows empty-state templates without create actions for builders", async () => {
    currentUserRole.value = "builder";
    server.use(
      http.get("*/api/v1/automations", () => HttpResponse.json({
        data: [],
        meta: {},
      })),
    );

    renderWithProviders(<AutomationsPage />);

    expect(await screen.findByText("Find flaky tests")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Template library" })).toBeInTheDocument();
    expect(screen.getByText("Security sweep")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Search templates...")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Start from blank/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Use Find flaky tests/i })).not.toBeInTheDocument();
  });

  it("filters templates by search query and shows empty state when none match", async () => {
    currentUserRole.value = "member";
    server.use(
      http.get("*/api/v1/automations", () => HttpResponse.json({
        data: [],
        meta: {},
      })),
    );

    const user = userEvent.setup();
    renderWithProviders(<AutomationsPage />);

    const input = await screen.findByPlaceholderText("Search templates...");

    // Typing a query that matches only one featured template hides others
    await user.type(input, "flaky");
    expect(screen.getByText("Find flaky tests")).toBeInTheDocument();
    expect(screen.queryByText("Security sweep")).not.toBeInTheDocument();

    // A query with no matches shows the shared empty state.
    await user.clear(input);
    await user.type(input, "zzznomatch");
    expect(screen.getByText("No templates found")).toBeInTheDocument();
    expect(screen.queryByText("Find flaky tests")).not.toBeInTheDocument();

    // Clearing restores the full list
    await user.clear(input);
    expect(screen.getByText("Find flaky tests")).toBeInTheDocument();
    expect(screen.getByText("Security sweep")).toBeInTheDocument();
  });

  it("renders automations as a responsive management list", async () => {
    currentUserRole.value = "member";
    server.use(
      http.get("*/api/v1/automations", () => HttpResponse.json({
        data: [
          {
            id: "auto-enabled",
            org_id: "org-1",
            repository_id: "repo-1",
            name: "Weekly release hardening sweep for mobile checkout reliability",
            goal: "Keep the mobile checkout stable",
            scope: "",
            icon_type: "emoji",
            icon_value: "🧪",
            execution_mode: "async",
            max_concurrent: 1,
            base_branch: "main",
            schedule_type: "interval",
            interval_value: 2,
            interval_unit: "weeks",
            interval_run_at: "09:15",
            timezone: "America/Los_Angeles",
            next_run_at: "2026-05-01T09:15:00Z",
            last_run_at: "2026-04-29T09:15:00Z",
            enabled: true,
            priority: 50,
            created_at: "2026-04-01T00:00:00Z",
            updated_at: "2026-04-01T00:00:00Z",
          },
          {
            id: "auto-paused",
            org_id: "org-1",
            repository_id: "repo-1",
            name: "Dependency cleanup",
            goal: "Clean stale dependencies",
            scope: "",
            execution_mode: "async",
            max_concurrent: 1,
            base_branch: "main",
            schedule_type: "cron",
            cron_expression: "0 9 * * 1",
            timezone: "UTC",
            enabled: false,
            paused_at: "2026-04-29T12:00:00Z",
            priority: 40,
            created_at: "2026-04-01T00:00:00Z",
            updated_at: "2026-04-01T00:00:00Z",
          },
        ],
        meta: {},
      })),
    );

    renderWithProviders(<AutomationsPage />);

    expect(await screen.findByRole("table", { name: "Automations" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Automation" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Status" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Schedule" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Next run" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Last run" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "All 2" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Enabled 1" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Paused 1" })).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Search automations...")).toBeInTheDocument();

    const titles = screen.getAllByText("Weekly release hardening sweep for mobile checkout reliability");
    expect(titles.length).toBeGreaterThan(0);
    expect(titles[0]).toHaveClass("text-sm", "font-medium");
    expect(screen.getByRole("heading", { name: "Template library" })).toBeInTheDocument();
    expect(screen.getByText("Find flaky tests")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Use Find flaky tests/i })).toHaveAttribute(
      "href",
      "/automations/new?template=flaky-tests",
    );
    expect(screen.getAllByLabelText("Automation icon for Weekly release hardening sweep for mobile checkout reliability")[0]).toHaveTextContent("🧪");
    expect(screen.getAllByText("Enabled").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Paused").length).toBeGreaterThan(0);
    // The schedule label already carries the timezone, so it must not be
    // duplicated as a standalone line.
    expect(
      screen.getAllByText(/Every 2 weeks at .*\(America\/Los_Angeles\)/).length,
    ).toBeGreaterThan(0);
    expect(screen.queryByText("America/Los_Angeles")).not.toBeInTheDocument();

    const menuButtons = screen.getAllByRole("button", {
      name: "More options for Weekly release hardening sweep for mobile checkout reliability",
    });
    expect(menuButtons.length).toBeGreaterThan(0);
    expect(menuButtons[0]).toHaveClass("shrink-0");
    const mobileMenuButton = menuButtons.find((button) =>
      button.closest('[data-slot="resource-row"]'),
    );
    expect(mobileMenuButton).toBeTruthy();
    expect(mobileMenuButton?.closest('[data-slot="resource-row-actions"]')).not.toHaveClass(
      "w-full",
      "ml-7",
    );

    // Computed via the same helper so the assertion is independent of the host
    // timezone (the fixture's next_run_at is a UTC instant).
    expect(screen.getByText(formatDateTime("2026-05-01T09:15:00Z"))).toBeInTheDocument();
    expect(screen.queryByText(/9:15:00/)).not.toBeInTheDocument();
  });

  it("filters automations by status and search query", async () => {
    currentUserRole.value = "member";
    server.use(
      http.get("*/api/v1/automations", () => HttpResponse.json({
        data: [
          {
            id: "auto-enabled",
            org_id: "org-1",
            repository_id: "repo-1",
            name: "Design consistency",
            goal: "Review product surfaces for design drift",
            scope: "",
            icon_type: "emoji",
            icon_value: "🎨",
            execution_mode: "async",
            max_concurrent: 1,
            base_branch: "main",
            schedule_type: "interval",
            interval_value: 1,
            interval_unit: "days",
            timezone: "America/Los_Angeles",
            enabled: true,
            priority: 50,
            created_at: "2026-04-01T00:00:00Z",
            updated_at: "2026-04-01T00:00:00Z",
          },
          {
            id: "auto-paused",
            org_id: "org-1",
            repository_id: "repo-1",
            name: "Dependency cleanup",
            goal: "Clean stale dependencies",
            scope: "",
            icon_type: "emoji",
            icon_value: "🧹",
            execution_mode: "async",
            max_concurrent: 1,
            base_branch: "main",
            schedule_type: "interval",
            interval_value: 1,
            interval_unit: "weeks",
            timezone: "UTC",
            enabled: false,
            priority: 40,
            created_at: "2026-04-01T00:00:00Z",
            updated_at: "2026-04-01T00:00:00Z",
          },
        ],
        meta: {},
      })),
    );

    const user = userEvent.setup();
    renderWithProviders(<AutomationsPage />);

    expect(await screen.findAllByText("Design consistency")).toHaveLength(2);
    expect(screen.getAllByText("Dependency cleanup")).toHaveLength(2);

    await user.click(screen.getByRole("tab", { name: "Paused 1" }));
    expect(screen.queryByText("Design consistency")).not.toBeInTheDocument();
    expect(screen.getAllByText("Dependency cleanup")).toHaveLength(2);

    await user.click(screen.getByRole("tab", { name: "All 2" }));
    await user.type(screen.getByPlaceholderText("Search automations..."), "design");
    expect(screen.getAllByText("Design consistency")).toHaveLength(2);
    expect(screen.queryByText("Dependency cleanup")).not.toBeInTheDocument();

    await user.clear(screen.getByPlaceholderText("Search automations..."));
    await user.type(screen.getByPlaceholderText("Search automations..."), "nomatch");
    expect(screen.getByText("No automations match your search.")).toBeInTheDocument();
  });

  it("hides member-only create and mutation controls from builders", async () => {
    currentUserRole.value = "builder";
    server.use(
      http.get("*/api/v1/automations", () => HttpResponse.json({
        data: [
          {
            id: "auto-enabled",
            org_id: "org-1",
            repository_id: "repo-1",
            name: "Weekly sweep",
            goal: "Keep things healthy",
            scope: "",
            execution_mode: "async",
            max_concurrent: 1,
            base_branch: "main",
            schedule_type: "interval",
            interval_value: 1,
            interval_unit: "weeks",
            enabled: true,
            priority: 50,
            created_at: "2026-04-01T00:00:00Z",
            updated_at: "2026-04-01T00:00:00Z",
          },
        ],
        meta: {},
      })),
    );

    renderWithProviders(<AutomationsPage />);

    expect(await screen.findAllByText("Weekly sweep")).toHaveLength(2);
    expect(screen.queryByRole("link", { name: /new/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /more options for weekly sweep/i })).not.toBeInTheDocument();
  });

  it("renders the automation workspace skeleton while automations load", () => {
    server.use(
      http.get("*/api/v1/automations", async () => new Promise<never>(() => {})),
    );

    renderWithProviders(<AutomationsPage />);

    expect(screen.getByLabelText("Loading automations")).toHaveAttribute("aria-busy", "true");
    expect(screen.getByRole("heading", { name: "Your automations" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Template library" })).toBeInTheDocument();
    expect(screen.queryByText("Loading automations...")).not.toBeInTheDocument();
  });
});
