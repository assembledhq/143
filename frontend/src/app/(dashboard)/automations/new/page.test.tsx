import { describe, expect, it, vi, beforeEach } from "vitest";
import { http, HttpResponse } from "msw";
import { QueryClient } from "@tanstack/react-query";
import {
  fireEvent,
  renderWithProviders,
  screen,
  userEvent,
  waitFor,
  within,
} from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import NewAutomationPage from "./page";
import { AUTOMATION_GOAL_MAX_LENGTH } from "@/lib/automation-validation";
import { queryKeys } from "@/lib/query-keys";
import type { Automation, ListResponse, SingleResponse } from "@/lib/types";

const DRAFT_STORAGE_KEY = "143:new-automation-draft";
const pushMock = vi.fn();
const replaceMock = vi.fn();
const searchParamsState = vi.hoisted(() => ({ value: "template=security-sweep" }));
const currentUserRole = vi.hoisted(() => ({ value: "member" }));

vi.mock("next/navigation", () => ({
  useRouter: () => ({
    push: pushMock,
    replace: replaceMock,
  }),
  useSearchParams: () => new URLSearchParams(searchParamsState.value),
}));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    user: { role: currentUserRole.value },
    isLoading: false,
  }),
}));

describe("NewAutomationPage", () => {
  beforeEach(() => {
    pushMock.mockReset();
    replaceMock.mockReset();
    searchParamsState.value = "template=security-sweep";
    currentUserRole.value = "member";
    window.sessionStorage.clear();
  });

  it("allows the timezone selector to wrap cleanly on mobile layouts", async () => {
    const expectedTimezone =
      Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<NewAutomationPage />);

    const timezoneButton = await screen.findByTitle(expectedTimezone);
    const scheduleRow = timezoneButton.parentElement;
    const runEveryText = screen.getByText("Run every");
    const atText = screen.getByText("at");

    expect(scheduleRow).toHaveClass("flex-wrap");
    expect(timezoneButton).toHaveClass("w-full", "sm:w-auto");
    expect(runEveryText).toHaveClass(
      "text-sm",
      "font-medium",
      "leading-none",
      "text-muted-foreground",
    );
    expect(atText).toHaveClass(
      "text-sm",
      "font-medium",
      "leading-none",
      "text-muted-foreground",
    );
    expect(screen.queryByText(/Run time is in/i)).not.toBeInTheDocument();
  });

  it("shows weekly schedule day context and keeps schedule controls consistently sized", async () => {
    const user = userEvent.setup();

    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<NewAutomationPage />);

    await user.click(await screen.findByRole("combobox", { name: "Interval unit" }));
    await user.click(screen.getByRole("option", { name: "weeks" }));

    expect(screen.getByText("at")).toBeInTheDocument();
    expect(screen.getByText(/first run anchors on/i)).toBeInTheDocument();
    expect(screen.getByText(/then repeats every \d+ weeks?/i)).toBeInTheDocument();
    expect(screen.queryByText("At")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Interval value")).toHaveClass("h-8");
    expect(screen.getByRole("combobox", { name: "Interval unit" })).toHaveClass("h-8");
    expect(screen.getByRole("combobox", { name: "Run at hour" })).toHaveClass("h-8");
    expect(screen.getByRole("combobox", { name: "Run at minute" })).toHaveClass("h-8");
  });

  it("keeps timezone in the primary schedule controls and moves execution defaults into advanced settings", async () => {
    const user = userEvent.setup();
    const expectedTimezone =
      Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

    server.use(
      http.get("*/api/v1/settings", () =>
        HttpResponse.json({
          data: {
            id: "org-1",
            name: "Test Org",
            settings: { default_agent_type: "codex" },
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<NewAutomationPage />);

    expect(await screen.findByTitle(expectedTimezone)).toBeInTheDocument();
    expect(
      screen.queryByRole("combobox", { name: "Run as" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("combobox", { name: "Model" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("combobox", { name: "Reasoning" }),
    ).not.toBeInTheDocument();

    await user.click(screen.getByText("Advanced options"));

    const sheet = screen.getByRole("dialog", { name: "Advanced settings" });
    expect(
      within(sheet).getByRole("combobox", { name: "Run as" }),
    ).toBeInTheDocument();
    expect(
      within(sheet).getByRole("combobox", { name: "Model" }),
    ).toBeInTheDocument();
    expect(
      within(sheet).getByRole("combobox", { name: "Reasoning" }),
    ).toBeInTheDocument();
    expect(within(sheet).queryByText("Timezone")).not.toBeInTheDocument();
  });

  it("presents schedule and PR events as compact trigger controls", async () => {
    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<NewAutomationPage />);

    expect(await screen.findByText("Triggers")).toBeInTheDocument();
    const triggerGroup = screen.getByRole("group", { name: "Automation triggers" });
    expect(triggerGroup).toBeInTheDocument();
    expect(triggerGroup).toHaveClass("rounded-lg", "bg-muted/25");
    expect(triggerGroup).not.toHaveClass("border-border", "bg-background");
    expect(screen.getByText("Pull request events")).toBeInTheDocument();
    expect(screen.getByLabelText("On a schedule")).toBeChecked();
    expect(screen.getByLabelText("When checks finish")).not.toBeChecked();
    expect(screen.getByLabelText("When a PR is opened")).not.toBeChecked();
    expect(
      screen.getByLabelText("When there is new PR feedback"),
    ).not.toBeChecked();
    expect(screen.getByLabelText("When a PR is merged")).not.toBeChecked();
    expect(screen.queryByText("Also trigger on")).not.toBeInTheDocument();
    expect(screen.queryByText("Pull requests")).not.toBeInTheDocument();
    expect(screen.getByText("Triggers").parentElement).toHaveClass("flex-wrap");
    expect(screen.getByText("on a schedule")).toHaveClass("block");
  });

  it("explains why the create button is disabled even when schedule triggering is selected", async () => {
    const user = userEvent.setup();
    searchParamsState.value = "";

    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<NewAutomationPage />);

    const createButton = await screen.findByRole("button", {
      name: "Create automation",
    });
    expect(screen.getByLabelText("On a schedule")).toBeChecked();
    expect(createButton).toBeDisabled();

    const tooltipWrapper = createButton.parentElement;
    expect(tooltipWrapper).not.toBeNull();
    await user.hover(tooltipWrapper!);

    const tooltip = await screen.findByRole("tooltip");
    expect(tooltip).toHaveTextContent(
      "Add an automation name and goal to create this automation.",
    );
  });

  it("submits an event-only PR feedback automation without schedule fields", async () => {
    const user = userEvent.setup();
    let requestBody: Record<string, unknown> | undefined;

    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
      http.post("/api/v1/automations", async ({ request }) => {
        requestBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json(
          {
            data: {
              id: "automation-1",
              org_id: "org-1",
              repository_id: "repo-1",
              name: requestBody.name,
              goal: requestBody.goal,
              icon_type: "emoji",
              icon_value: "⚙️",
              execution_mode: "sequential",
              max_concurrent: 1,
              base_branch: "main",
              identity_scope: "org",
              pre_pr_review_loops: 1,
              schedule_type: requestBody.schedule_type,
              github_event_triggers: [],
              timezone: "UTC",
              enabled: true,
              priority: 50,
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          },
          { status: 201 },
        );
      }),
    );

    renderWithProviders(<NewAutomationPage />);

    fireEvent.change(await screen.findByLabelText("Name"), {
      target: { value: "PR feedback responder" },
    });
    fireEvent.change(screen.getByLabelText("Goal"), {
      target: { value: "Respond to new PR feedback." },
    });
    await user.click(screen.getByLabelText("On a schedule"));
    await user.click(screen.getByLabelText("When there is new PR feedback"));
    await user.click(screen.getByRole("button", { name: "Create automation" }));

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith("/automations/automation-1");
    });
    expect(requestBody).toMatchObject({
      schedule_type: "none",
      triggers: ["github.pr.feedback"],
    });
    expect(requestBody).not.toHaveProperty("interval_value");
    expect(requestBody).not.toHaveProperty("interval_unit");
    expect(requestBody).not.toHaveProperty("interval_run_at");
  });

  it("updates the automations list and detail caches after creating an automation", async () => {
    const user = userEvent.setup();
    const queryClient = new QueryClient({
      defaultOptions: {
        queries: {
          retry: false,
        },
      },
    });
    const existingAutomation: Automation = {
      id: "automation-existing",
      org_id: "org-1",
      repository_id: "repo-1",
      name: "Existing automation",
      goal: "Keep existing recurring work visible.",
      icon_type: "emoji",
      icon_value: "⚙️",
      execution_mode: "sequential",
      max_concurrent: 1,
      base_branch: "main",
      identity_scope: "org",
      pre_pr_review_loops: 1,
      schedule_type: "interval",
      interval_value: 1,
      interval_unit: "days",
      interval_run_at: "09:00",
      timezone: "UTC",
      enabled: true,
      priority: 50,
      github_event_triggers: [],
      created_at: "2026-03-04T12:00:00Z",
      updated_at: "2026-03-04T12:00:00Z",
    };
    const createdAutomation: Automation = {
      ...existingAutomation,
      id: "automation-1",
      name: "PR feedback responder",
      goal: "Respond to new PR feedback.",
      schedule_type: "none",
      interval_value: undefined,
      interval_unit: undefined,
      interval_run_at: undefined,
      created_at: "2026-03-05T12:00:00Z",
      updated_at: "2026-03-05T12:00:00Z",
    };
    const createdResponse: SingleResponse<Automation> = {
      data: createdAutomation,
    };

    queryClient.setQueryData<ListResponse<Automation>>(
      queryKeys.automations.all,
      {
        data: [existingAutomation],
        meta: {},
      },
    );

    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
      http.post("/api/v1/automations", () =>
        HttpResponse.json(createdResponse, { status: 201 }),
      ),
    );

    renderWithProviders(<NewAutomationPage />, { queryClient });

    fireEvent.change(await screen.findByLabelText("Name"), {
      target: { value: "PR feedback responder" },
    });
    fireEvent.change(screen.getByLabelText("Goal"), {
      target: { value: "Respond to new PR feedback." },
    });
    await user.click(screen.getByLabelText("On a schedule"));
    await user.click(screen.getByLabelText("When there is new PR feedback"));
    await user.click(screen.getByRole("button", { name: "Create automation" }));

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith("/automations/automation-1");
    });
    expect(
      queryClient.getQueryData<ListResponse<Automation>>(
        queryKeys.automations.all,
      )?.data,
    ).toEqual([createdAutomation, existingAutomation]);
    expect(
      queryClient.getQueryData<SingleResponse<Automation>>(
        queryKeys.automations.detail("automation-1"),
      ),
    ).toEqual(createdResponse);
  });

  it("submits an event-only PagerDuty incident automation", async () => {
    const user = userEvent.setup();
    let requestBody: Record<string, unknown> | undefined;

    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
      http.get("/api/v1/integrations/pagerduty", () =>
        HttpResponse.json({
          data: [
            {
              id: "pd-1",
              org_id: "org-1",
              integration_id: "int-pd",
              status: "active",
              account_subdomain: "acme",
              default_repository_id: "repo-1",
              writeback_enabled: true,
              connected_at: "2026-06-01T00:00:00Z",
              created_at: "2026-06-01T00:00:00Z",
              updated_at: "2026-06-01T00:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
      http.post("/api/v1/automations", async ({ request }) => {
        requestBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json(
          {
            data: {
              id: "automation-1",
              org_id: "org-1",
              repository_id: "repo-1",
              name: requestBody.name,
              goal: requestBody.goal,
              icon_type: "emoji",
              icon_value: "⚙️",
              execution_mode: "sequential",
              max_concurrent: 1,
              base_branch: "main",
              identity_scope: "org",
              pre_pr_review_loops: 1,
              schedule_type: requestBody.schedule_type,
              github_event_triggers: [],
              timezone: "UTC",
              enabled: true,
              priority: 50,
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          },
          { status: 201 },
        );
      }),
    );

    renderWithProviders(<NewAutomationPage />);

    fireEvent.change(await screen.findByLabelText("Name"), {
      target: { value: "PagerDuty responder" },
    });
    fireEvent.change(screen.getByLabelText("Goal"), {
      target: { value: "Investigate triggered PagerDuty incidents." },
    });
    await user.click(screen.getByLabelText("On a schedule"));
    await user.click(screen.getByLabelText("PagerDuty incidents"));
    await user.click(screen.getByLabelText("PagerDuty annotated events"));
    fireEvent.change(screen.getByLabelText("PagerDuty service IDs"), {
      target: { value: "P123, P456" },
    });
    fireEvent.change(screen.getByLabelText("PagerDuty team IDs"), {
      target: { value: "TEAM1" },
    });
    fireEvent.change(screen.getByLabelText("PagerDuty statuses"), {
      target: { value: "triggered, acknowledged" },
    });
    fireEvent.change(screen.getByLabelText("PagerDuty priority names"), {
      target: { value: "P1, Sev 1" },
    });
    fireEvent.change(screen.getByLabelText("PagerDuty title contains"), {
      target: { value: "checkout" },
    });
    fireEvent.change(screen.getByLabelText("PagerDuty cooldown minutes"), {
      target: { value: "30" },
    });
    await user.click(screen.getByRole("button", { name: "Create automation" }));

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith("/automations/automation-1");
    });
    expect(requestBody).toMatchObject({
      schedule_type: "none",
      event_triggers: [
        {
          provider: "pagerduty",
          event_types: ["incident.triggered", "incident.annotated"],
          filter: {
            service_ids: ["P123", "P456"],
            team_ids: ["TEAM1"],
            statuses: ["triggered", "acknowledged"],
            urgencies: ["high"],
            priority_names: ["P1", "Sev 1"],
            title_contains: "checkout",
            cooldown_minutes: 30,
          },
          repository_id: "repo-1",
          enabled: true,
        },
      ],
    });
    expect(requestBody).not.toHaveProperty("interval_value");
    expect(requestBody).not.toHaveProperty("interval_unit");
    expect(requestBody).not.toHaveProperty("interval_run_at");
  });

  it("prefills the form from the selected template and links to the full library", async () => {
    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<NewAutomationPage />);

    await waitFor(() => {
      expect(screen.getByDisplayValue("Security sweep")).toBeInTheDocument();
    });

    expect(
      screen.getByRole("link", { name: /Browse all templates/i }),
    ).toHaveAttribute("href", "/automations/templates");
    expect(
      (screen.getByLabelText("Goal") as HTMLTextAreaElement).value,
    ).toContain("Review the repository for concrete, actionable security risk");
  });

  it("resets event trigger choices when applying a template", async () => {
    searchParamsState.value = "";
    const user = userEvent.setup();
    let requestBody: Record<string, unknown> | undefined;

    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
      http.post("/api/v1/automations", async ({ request }) => {
        requestBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json(
          {
            data: {
              id: "automation-1",
              org_id: "org-1",
              repository_id: "repo-1",
              name: requestBody.name,
              goal: requestBody.goal,
              icon_type: "emoji",
              icon_value: "⚙️",
              execution_mode: "sequential",
              max_concurrent: 1,
              base_branch: "main",
              identity_scope: "org",
              pre_pr_review_loops: 1,
              schedule_type: requestBody.schedule_type,
              github_event_triggers: [],
              timezone: "UTC",
              enabled: true,
              priority: 50,
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          },
          { status: 201 },
        );
      }),
    );

    renderWithProviders(<NewAutomationPage />);

    await screen.findByLabelText("Name");
    await user.click(screen.getByLabelText("On a schedule"));
    await user.click(screen.getByLabelText("When a PR is updated"));
    expect(screen.getByLabelText("On a schedule")).not.toBeChecked();
    expect(screen.getByLabelText("When a PR is updated")).toBeChecked();

    await user.click(screen.getByRole("button", { name: "Templates" }));
    await user.click(await screen.findByText("Security sweep"));

    expect(screen.getByDisplayValue("Security sweep")).toBeInTheDocument();
    expect(screen.getByLabelText("On a schedule")).toBeChecked();
    expect(screen.getByLabelText("When a PR is updated")).not.toBeChecked();

    await user.click(screen.getByRole("button", { name: "Create automation" }));

    await waitFor(() => {
      expect(requestBody).toMatchObject({
        schedule_type: "interval",
        triggers: [],
      });
    });
    expect(requestBody).toMatchObject({
      interval_value: 7,
      interval_unit: "days",
      interval_run_at: "09:00",
    });
    expect(requestBody).not.toHaveProperty("event_triggers");
  });

  it("restores the latest in-progress automation draft when no template is selected", async () => {
    searchParamsState.value = "";
    window.sessionStorage.setItem(
      DRAFT_STORAGE_KEY,
      JSON.stringify({
        __v: 1,
        name: "Saved automation",
        goal: "Continue the automation I was writing.",
        iconValue: "✨",
        selectedRepoId: "repo-2",
        intervalValue: 3,
        intervalUnit: "weeks",
        intervalRunHour: "14",
        intervalRunMinute: "45",
        timezone: "UTC",
        scheduleEnabled: true,
        productTriggers: ["github.pr.feedback"],
        identityScope: "personal",
        prePRReviewLoops: 2,
        priority: 25,
      }),
    );
    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
            {
              id: "repo-2",
              org_id: "org-1",
              integration_id: "int-1",
              github_id: 2,
              full_name: "acme/worker",
              default_branch: "develop",
              private: false,
              clone_url: "https://github.com/acme/worker.git",
              installation_id: 10,
              status: "active",
              settings: {},
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<NewAutomationPage />);

    expect(await screen.findByDisplayValue("Saved automation")).toBeInTheDocument();
    expect(screen.getByLabelText("Goal")).toHaveValue("Continue the automation I was writing.");
    await waitFor(() => {
      expect(screen.getByRole("combobox", { name: "Repository" })).toHaveTextContent("acme/worker");
    });
    expect(screen.getByLabelText("Interval value")).toHaveValue(3);
    expect(screen.getByRole("combobox", { name: "Interval unit" })).toHaveTextContent("weeks");
    expect(screen.getByLabelText("When there is new PR feedback")).toBeChecked();
  });

  it("saves draft changes before navigating to the template library", async () => {
    searchParamsState.value = "";
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<NewAutomationPage />);

    await user.type(await screen.findByLabelText("Name"), "Draft before browsing");
    await user.type(screen.getByLabelText("Goal"), "Do not lose this automation.");
    await user.click(screen.getByRole("link", { name: /Browse all templates/i }));

    const stored = window.sessionStorage.getItem(DRAFT_STORAGE_KEY);
    expect(stored).not.toBeNull();
    expect(JSON.parse(stored!)).toMatchObject({
      __v: 1,
      name: "Draft before browsing",
      goal: "Do not lose this automation.",
    });
  });

  it("debounces automation draft writes while typing", async () => {
    searchParamsState.value = "";
    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<NewAutomationPage />);

    const nameInput = await screen.findByLabelText("Name");
    vi.useFakeTimers();
    try {
      fireEvent.change(nameInput, { target: { value: "D" } });
      fireEvent.change(nameInput, { target: { value: "Draft before browsing" } });

      expect(window.sessionStorage.getItem(DRAFT_STORAGE_KEY)).toBeNull();

      vi.advanceTimersByTime(399);
      expect(window.sessionStorage.getItem(DRAFT_STORAGE_KEY)).toBeNull();

      await vi.advanceTimersByTimeAsync(1);
      const stored = window.sessionStorage.getItem(DRAFT_STORAGE_KEY);
      expect(stored).not.toBeNull();
      expect(JSON.parse(stored!)).toMatchObject({
        __v: 1,
        name: "Draft before browsing",
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("clears the stored automation draft after successful creation", async () => {
    searchParamsState.value = "";
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
      http.post("/api/v1/automations", async ({ request }) => {
        const requestBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json(
          {
            data: {
              id: "automation-1",
              org_id: "org-1",
              repository_id: "repo-1",
              name: requestBody.name,
              goal: requestBody.goal,
              icon_type: "emoji",
              icon_value: "⚙️",
              execution_mode: "sequential",
              max_concurrent: 1,
              base_branch: "main",
              identity_scope: "org",
              pre_pr_review_loops: 1,
              schedule_type: "interval",
              github_event_triggers: [],
              timezone: "UTC",
              enabled: true,
              priority: 50,
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          },
          { status: 201 },
        );
      }),
    );

    renderWithProviders(<NewAutomationPage />);

    await user.type(await screen.findByLabelText("Name"), "Draft to create");
    await user.type(screen.getByLabelText("Goal"), "Create this automation.");
    await waitFor(() => {
      expect(window.sessionStorage.getItem(DRAFT_STORAGE_KEY)).not.toBeNull();
    });

    await user.click(screen.getByRole("button", { name: "Create automation" }));

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith("/automations/automation-1");
    });
    expect(window.sessionStorage.getItem(DRAFT_STORAGE_KEY)).toBeNull();
  });

  it("does not restore a stored draft when a template is selected via URL param", async () => {
    // searchParamsState.value is already "template=security-sweep" (default from beforeEach)
    window.sessionStorage.setItem(
      DRAFT_STORAGE_KEY,
      JSON.stringify({
        __v: 1,
        name: "My saved draft",
        goal: "This should not appear.",
      }),
    );

    renderWithProviders(<NewAutomationPage />);

    // The template name should be pre-filled, not the draft name
    expect(await screen.findByDisplayValue("Security sweep")).toBeInTheDocument();
    expect(screen.queryByDisplayValue("My saved draft")).not.toBeInTheDocument();
  });

  it("renders the composer as a lightweight document-style form", async () => {
    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<NewAutomationPage />);

    await screen.findByDisplayValue("Security sweep");

    const composer = screen.getByTestId("automation-composer");
    const identityRow = screen.getByTestId("automation-identity-row");
    const emojiTrigger = screen.getByRole("button", { name: "Automation emoji" });
    const nameInput = screen.getByLabelText("Name");

    expect(composer).toHaveClass("rounded-xl", "bg-card");
    expect(identityRow).toHaveClass("flex", "items-start");
    expect(identityRow).not.toHaveClass("grid-cols-[4.75rem_minmax(0,1fr)]", "border-b");
    expect(emojiTrigger).toHaveClass("size-10", "border-transparent", "bg-transparent", "shadow-none");
    expect(emojiTrigger.querySelector("svg")).toBeNull();
    expect(nameInput).toHaveClass("text-2xl", "font-semibold");
    expect(screen.getByPlaceholderText("Untitled automation")).toBe(nameInput);
    expect(screen.getByLabelText("Name")).toBeInTheDocument();
  });

  it("aligns the new automation header with the standard page container", async () => {
    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<NewAutomationPage />);

    await screen.findByDisplayValue("Security sweep");

    const heading = screen.getByRole("heading", { name: "New automation" });
    const pageContainer = heading.closest('[data-slot="page-container"]');
    const composerWrapper = screen.getByTestId("automation-composer").parentElement;

    expect(pageContainer).toHaveAttribute(
      "data-size",
      "default",
    );
    expect(composerWrapper).not.toHaveClass(
      "mx-auto",
    );
  });

  it("inserts selected @ mentions into the automation goal", async () => {
    const user = userEvent.setup();

    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
      http.get("/api/v1/session-composer/files", ({ request }) => {
        const url = new URL(request.url);
        if (!url.searchParams.get("q")) {
          return HttpResponse.json({ data: [], meta: {} });
        }

        return HttpResponse.json({
          data: [
            {
              kind: "file",
              token: "@internal/services/automations.go",
              path: "internal/services/automations.go",
              display: "internal/services/automations.go",
            },
          ],
          meta: {},
        });
      }),
    );

    renderWithProviders(<NewAutomationPage />);

    const goalInput = await screen.findByLabelText("Goal");
    await user.clear(goalInput);
    await user.type(goalInput, "Review @auto");
    await user.click(
      await screen.findByRole("button", {
        name: "internal/services/automations.go",
      }),
    );

    expect(goalInput).toHaveValue("Review @internal/services/automations.go ");
  });

  it("inserts selected slash commands into the automation goal", async () => {
    const user = userEvent.setup();

    server.use(
      http.get("*/api/v1/settings", () =>
        HttpResponse.json({
          data: {
            id: "org-1",
            name: "Test Org",
            settings: { default_agent_type: "codex" },
          },
        }),
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
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

    renderWithProviders(<NewAutomationPage />);

    const goalInput = await screen.findByLabelText("Goal");
    await user.clear(goalInput);
    await user.type(goalInput, "/rev");
    await user.click(await screen.findByRole("button", { name: /\/review/i }));

    expect(goalInput).toHaveValue("/review ");
  });

  it("redirects builders away from the new automation form", async () => {
    currentUserRole.value = "builder";

    renderWithProviders(<NewAutomationPage />);

    await waitFor(() => {
      expect(replaceMock).toHaveBeenCalledWith("/automations");
    });
  });

  it("submits the selected base branch from the branch picker", async () => {
    const user = userEvent.setup();
    let requestBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/settings", () =>
        HttpResponse.json({
          data: {
            id: "org-1",
            name: "Test Org",
            settings: { default_agent_type: "codex" },
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
      http.get("*/api/v1/repositories/:id/branches", () =>
        HttpResponse.json({
          data: [
            { name: "main", protected: true },
            { name: "release/weekly", protected: false },
          ],
          meta: {},
        }),
      ),
      http.post("*/api/v1/automations", async ({ request }) => {
        requestBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<NewAutomationPage />);

    await waitFor(() => {
      expect(screen.getByDisplayValue("Security sweep")).toBeInTheDocument();
    });
    await user.clear(screen.getByLabelText("Name"));
    await user.type(screen.getByLabelText("Name"), "Weekly audit");
    await user.clear(screen.getByLabelText("Goal"));
    await user.type(
      screen.getByLabelText("Goal"),
      "Check the release branch every week",
    );

    await user.click(screen.getByText("Advanced options"));
    await user.click(
      await screen.findByRole("button", { name: "Base branch" }),
    );
    await user.type(
      screen.getByPlaceholderText("Search branches..."),
      "weekly",
    );
    await user.click(await screen.findByText("release/weekly"));

    await user.click(screen.getByRole("button", { name: "Create automation" }));

    await waitFor(() => {
      expect(requestBody).toMatchObject({
        repository_id: "repo-1",
        base_branch: "release/weekly",
        identity_scope: "org",
      });
    });
  }, 20000);

  it("submits selected pull request triggers as product triggers", async () => {
    const user = userEvent.setup();
    let requestBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/settings", () =>
        HttpResponse.json({
          data: {
            id: "org-1",
            name: "Test Org",
            settings: { default_agent_type: "codex" },
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
          data: [],
          meta: {},
        }),
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
      http.post("*/api/v1/automations", async ({ request }) => {
        requestBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<NewAutomationPage />);

    await waitFor(() => {
      expect(screen.getByDisplayValue("Security sweep")).toBeInTheDocument();
    });
    await user.click(
      screen.getByRole("checkbox", { name: "When a PR is opened" }),
    );
    await user.click(
      screen.getByRole("checkbox", { name: "When there is new PR feedback" }),
    );
    await user.click(screen.getByRole("button", { name: "Create automation" }));

    await waitFor(() => {
      expect(requestBody).toMatchObject({
        triggers: ["github.pr.opened", "github.pr.feedback"],
      });
    });
  });

  it("submits pre-PR review disabled when the default agent does not support native review", async () => {
    const user = userEvent.setup();
    let requestBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/settings", () =>
        HttpResponse.json({
          data: {
            id: "org-1",
            name: "Test Org",
            settings: { default_agent_type: "custom" },
          },
        }),
      ),
      http.get("*/api/v1/coding-credentials*", () =>
        HttpResponse.json({
          data: [],
          meta: {},
        }),
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
      http.post("*/api/v1/automations", async ({ request }) => {
        requestBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<NewAutomationPage />);

    await waitFor(() => {
      expect(screen.getByDisplayValue("Security sweep")).toBeInTheDocument();
    });
    await user.click(screen.getByRole("button", { name: "Create automation" }));

    await waitFor(() => {
      expect(requestBody).toMatchObject({ pre_pr_review_loops: 0 });
    });
  });

  it("submits a personal automation identity scope when selected", async () => {
    const user = userEvent.setup();
    let requestBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/settings", () =>
        HttpResponse.json({
          data: {
            id: "org-1",
            name: "Test Org",
            settings: { default_agent_type: "codex" },
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
      http.post("*/api/v1/automations", async ({ request }) => {
        requestBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<NewAutomationPage />);

    await waitFor(() => {
      expect(screen.getByDisplayValue("Security sweep")).toBeInTheDocument();
    });
    await user.clear(screen.getByLabelText("Name"));
    await user.type(screen.getByLabelText("Name"), "Personal sweep");
    await user.clear(screen.getByLabelText("Goal"));
    await user.type(screen.getByLabelText("Goal"), "Use my own credentials");
    await user.click(screen.getByText("Advanced options"));
    await user.click(screen.getByRole("combobox", { name: "Run as" }));
    await user.click(await screen.findByText("Personal"));
    await user.click(screen.getByRole("button", { name: "Create automation" }));

    await waitFor(() => {
      expect(requestBody).toMatchObject({ identity_scope: "personal" });
    });
  }, 20000);

  it("submits an explicit reasoning override for supported automation agents", async () => {
    const user = userEvent.setup();
    let requestBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/settings", () =>
        HttpResponse.json({
          data: {
            id: "org-1",
            name: "Test Org",
            settings: { default_agent_type: "codex" },
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
      http.post("*/api/v1/automations", async ({ request }) => {
        requestBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "auto-1" } });
      }),
    );

    renderWithProviders(<NewAutomationPage />);

    await waitFor(() => {
      expect(screen.getByDisplayValue("Security sweep")).toBeInTheDocument();
    });

    await user.click(screen.getByText("Advanced options"));
    await user.click(screen.getByRole("combobox", { name: "Reasoning" }));
    await user.click(await screen.findByText("Extra High"));
    await user.click(screen.getByRole("button", { name: "Create automation" }));

    await waitFor(() => {
      expect(requestBody).toMatchObject({ reasoning_effort: "xhigh" });
    });
  });

  it("shows goal length validation and blocks submit when the goal exceeds the backend limit", async () => {
    const user = userEvent.setup();

    server.use(
      http.get("/api/v1/repositories", () =>
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
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<NewAutomationPage />);

    await waitFor(() => {
      expect(screen.getByDisplayValue("Security sweep")).toBeInTheDocument();
    });

    await user.clear(screen.getByLabelText("Name"));
    await user.type(screen.getByLabelText("Name"), "Weekly audit");

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
    expect(
      screen.getByRole("button", { name: "Create automation" }),
    ).toBeDisabled();
  });
});
