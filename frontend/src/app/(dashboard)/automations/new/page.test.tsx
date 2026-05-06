import { describe, expect, it, vi, beforeEach } from "vitest";
import { http, HttpResponse } from "msw";
import { fireEvent, renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import NewAutomationPage from "./page";
import { AUTOMATION_GOAL_MAX_LENGTH } from "@/lib/automation-validation";

const pushMock = vi.fn();
const searchParams = new URLSearchParams("template=security-sweep");

vi.mock("next/navigation", () => ({
  useRouter: () => ({
    push: pushMock,
    replace: vi.fn(),
  }),
  useSearchParams: () => searchParams,
}));

describe("NewAutomationPage", () => {
  beforeEach(() => {
    pushMock.mockReset();
  });

  it("allows the timezone selector to wrap cleanly on mobile layouts", async () => {
    const expectedTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

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
    expect(runEveryText).toHaveClass("text-sm", "font-medium", "leading-none", "text-muted-foreground");
    expect(atText).toHaveClass("text-sm", "font-medium", "leading-none", "text-muted-foreground");
    expect(screen.queryByText(/Run time is in/i)).not.toBeInTheDocument();
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

    expect(screen.getByRole("link", { name: /Browse all templates/i })).toHaveAttribute(
      "href",
      "/automations/templates",
    );
    expect(
      (screen.getByLabelText("Goal") as HTMLTextAreaElement).value,
    ).toContain(
      "Review the repository for concrete, actionable security risk",
    );
  });

  it("submits the selected base branch from the branch picker", async () => {
    const user = userEvent.setup();
    let requestBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/settings", () => HttpResponse.json({
        data: {
          id: "org-1",
          name: "Test Org",
          settings: { default_agent_type: "codex" },
        },
      })),
      http.get("*/api/v1/settings/codex-auth/status", () => HttpResponse.json({
        data: { status: "completed" },
      })),
      http.get("*/api/v1/settings/credentials/resolved", () => HttpResponse.json({
        data: [
          { provider: "openai", source: "org" },
        ],
        meta: {},
      })),
      http.get("*/api/v1/settings/credentials/team", () => HttpResponse.json({
        data: [],
        meta: {},
      })),
      http.get("*/api/v1/settings/coding-auths", () => HttpResponse.json({
        data: [],
        meta: {},
      })),
      http.get("*/api/v1/coding-credentials*", () => HttpResponse.json({
        data: [],
        meta: {},
      })),
      http.get("*/api/v1/repositories", () => HttpResponse.json({
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
      })),
      http.get("*/api/v1/repositories/:id/branches", () => HttpResponse.json({
        data: [
          { name: "main", protected: true },
          { name: "release/weekly", protected: false },
        ],
        meta: {},
      })),
      http.post("*/api/v1/automations", async ({ request }) => {
        requestBody = await request.json() as Record<string, unknown>;
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
    await user.type(screen.getByLabelText("Goal"), "Check the release branch every week");

    await user.click(screen.getByText("Advanced options"));
    await user.click(await screen.findByRole("button", { name: "Base branch" }));
    await user.type(screen.getByPlaceholderText("Search branches..."), "weekly");
    await user.click(await screen.findByText("release/weekly"));

    await user.click(screen.getByRole("button", { name: "Create automation" }));

    await waitFor(() => {
      expect(requestBody).toMatchObject({
        repository_id: "repo-1",
        base_branch: "release/weekly",
      });
    });
  }, 20000);

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
      screen.getByText(`Goal must be at most ${AUTOMATION_GOAL_MAX_LENGTH.toLocaleString("en-US")} characters.`),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        `${(AUTOMATION_GOAL_MAX_LENGTH + 1).toLocaleString("en-US")} / ${AUTOMATION_GOAL_MAX_LENGTH.toLocaleString("en-US")}`,
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create automation" })).toBeDisabled();
  });

});
