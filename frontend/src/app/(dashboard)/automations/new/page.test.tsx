import { describe, expect, it, vi, beforeEach } from "vitest";
import { http, HttpResponse } from "msw";
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

const pushMock = vi.fn();
const replaceMock = vi.fn();
const searchParams = new URLSearchParams("template=security-sweep");
const currentUserRole = vi.hoisted(() => ({ value: "member" }));

vi.mock("next/navigation", () => ({
  useRouter: () => ({
    push: pushMock,
    replace: replaceMock,
  }),
  useSearchParams: () => searchParams,
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
    currentUserRole.value = "member";
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
    const atText = screen.getByText("At");

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
    expect(screen.getByLabelText("When a PR is opened")).not.toBeChecked();
    expect(
      screen.getByLabelText("When there is new PR feedback"),
    ).not.toBeChecked();
    expect(screen.queryByText("Also trigger on")).not.toBeInTheDocument();
    expect(screen.queryByText("Pull requests")).not.toBeInTheDocument();
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

    await user.clear(await screen.findByLabelText("Name"));
    await user.type(screen.getByLabelText("Name"), "PR feedback responder");
    await user.clear(screen.getByLabelText("Goal"));
    await user.type(
      screen.getByLabelText("Goal"),
      "Respond to new PR feedback.",
    );
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
