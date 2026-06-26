import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import AutomationsPage from "./page";

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

    expect(await screen.findByRole("heading", { name: "Template library" })).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Search templates...")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Start from blank/i })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /^New$/i })).toHaveAttribute("href", "/automations/new");
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

    expect(await screen.findByRole("heading", { name: "Template library" })).toBeInTheDocument();
    expect(screen.getByText("Find flaky tests")).toBeInTheDocument();
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

    // A query with no matches shows the empty message
    await user.clear(input);
    await user.type(input, "zzznomatch");
    expect(screen.getByText("No templates match your search.")).toBeInTheDocument();
    expect(screen.queryByText("Find flaky tests")).not.toBeInTheDocument();

    // Clearing restores the full list
    await user.clear(input);
    expect(screen.getByText("Find flaky tests")).toBeInTheDocument();
    expect(screen.getByText("Security sweep")).toBeInTheDocument();
  });

  it("renders automation cards with mobile-friendly stacked details", async () => {
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

    const title = await screen.findByText("Weekly release hardening sweep for mobile checkout reliability");
    expect(title).toHaveClass("break-words", "leading-5");
    expect(screen.getByRole("heading", { name: "Template library" })).toBeInTheDocument();
    expect(screen.getByText("Find flaky tests")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Use Find flaky tests/i })).toHaveAttribute(
      "href",
      "/automations/new?template=flaky-tests",
    );
    expect(screen.getByLabelText("Automation icon for Weekly release hardening sweep for mobile checkout reliability")).toHaveTextContent("🧪");

    const schedule = screen.getByText(/Every 2 weeks at/);
    expect(schedule).toHaveClass("block", "break-words", "leading-5", "sm:max-w-[18rem]", "sm:text-right");

    const detailRow = screen.getByText(/Last run:/).parentElement;
    expect(detailRow).not.toBeNull();
    expect(detailRow).toHaveClass("flex", "flex-col", "gap-1", "sm:flex-row", "sm:flex-wrap");

    const menuButton = screen.getByRole("button", {
      name: "More options for Weekly release hardening sweep for mobile checkout reliability",
    });
    expect(menuButton).toHaveClass("self-start", "shrink-0");
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

    await screen.findByText("Weekly sweep");
    expect(screen.queryByRole("link", { name: /new/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /more options for weekly sweep/i })).not.toBeInTheDocument();
  });
});
