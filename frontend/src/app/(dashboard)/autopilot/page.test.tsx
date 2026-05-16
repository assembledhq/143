import { beforeEach, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import AutopilotPage from "./page";
import type {
  Integration,
  ListResponse,
  Organization,
  PMDocument,
  PMPlan,
  PMStatus,
  Repository,
  SingleResponse,
  AutopilotQueueResponse,
} from "@/lib/types";

const mockReplace = vi.fn();

vi.mock("@/hooks/use-analyze", () => ({
  useAnalyze: () => ({
    isAnalyzing: false,
    isPending: false,
    analyzeError: null,
    handleAnalyze: vi.fn(),
    dismissError: vi.fn(),
  }),
}));

vi.mock("next/navigation", async () => {
  const actual = await vi.importActual<typeof import("next/navigation")>("next/navigation");
  return {
    ...actual,
    useRouter: () => ({
      push: vi.fn(),
      replace: mockReplace,
      back: vi.fn(),
    }),
  };
});

function mockSettings(settings: Organization["settings"]) {
  server.use(
    http.get("/api/v1/settings", () =>
      HttpResponse.json({
        data: {
          id: "org-1",
          name: "Test Org",
          settings,
          created_at: "2026-03-20T00:00:00Z",
          updated_at: "2026-03-20T00:00:00Z",
        },
      } satisfies SingleResponse<Organization>))
  );
}

function mockIntegrations(data: Integration[]) {
  server.use(
    http.get("/api/v1/integrations", () =>
      HttpResponse.json({ data, meta: {} } satisfies ListResponse<Integration>))
  );
}

function mockRepositories(data: Repository[]) {
  server.use(
    http.get("/api/v1/repositories", () =>
      HttpResponse.json({ data, meta: {} } satisfies ListResponse<Repository>))
  );
}

function mockStatus(data: PMStatus) {
  server.use(
    http.get("/api/v1/pm/status", () =>
      HttpResponse.json({ data } satisfies SingleResponse<PMStatus>))
  );
}

function mockLatestPlan(plan: PMPlan | null) {
  server.use(
    http.get("/api/v1/pm/plans/latest", () =>
      HttpResponse.json({ data: plan } satisfies SingleResponse<PMPlan | null>))
  );
}

function mockDocuments(docs: PMDocument[]) {
  server.use(
    http.get("/api/v1/pm/documents", () =>
      HttpResponse.json({ data: docs, meta: {} } satisfies ListResponse<PMDocument>))
  );
}

function mockAgentReadiness() {
  server.use(
    http.get("/api/v1/settings/codex-auth/status", () =>
      HttpResponse.json({ data: { status: "completed" } }))
  );
}

function buildPlan(overrides: Partial<PMPlan> = {}): PMPlan {
  return {
    id: "plan-1",
    org_id: "org-1",
    status: "completed",
    analysis: "3 payment failures appear linked by one auth middleware issue.",
    tasks: [],
    clusters: [],
    skipped_issues: [],
    issues_reviewed: 14,
    triggered_by: "manual",
    created_at: "2026-03-23T18:00:00Z",
    completed_at: "2026-03-23T18:02:00Z",
    ...overrides,
  };
}

describe("AutopilotPage", () => {
  beforeEach(() => {
    mockReplace.mockClear();
  });

  it("redirects to onboarding when prerequisites are incomplete", async () => {
    mockSettings({
      default_agent_type: "codex",
      product_context: {
        philosophy: "",
        direction: "",
        focus_areas: [],
        avoid_areas: [],
      },
    });
    mockStatus({
      is_running: false,
      issues_reviewed: 0,
      success_rate: 0,
      success_count: 0,
      total_delegated: 0,
    });
    mockLatestPlan(null);
    mockDocuments([]);
    mockIntegrations([]);
    mockRepositories([]);
    mockAgentReadiness();

    renderWithProviders(<AutopilotPage />);

    // Should redirect to onboarding, not render setup UI inline
    await waitFor(() => {
      expect(mockReplace).toHaveBeenCalledWith("/onboarding");
    });
  });

  it("shows the queue when setup is complete but no plan exists", async () => {
    mockSettings({
      default_agent_type: "codex",
      product_context: {
        philosophy: "Ship reliability first.",
        direction: "Payments hardening this quarter.",
        focus_areas: ["auth"],
        avoid_areas: ["redesigns"],
      },
    });
    mockStatus({
      is_running: false,
      issues_reviewed: 0,
      success_rate: 0,
      success_count: 0,
      total_delegated: 0,
    });
    mockLatestPlan(null);
    mockDocuments([]);
    mockIntegrations([
      {
        id: "github-1",
        org_id: "org-1",
        provider: "github",
        status: "active",
        created_at: "2026-03-20T00:00:00Z",
      },
    ]);
    mockRepositories([
      {
        id: "repo-1",
        org_id: "org-1",
        integration_id: "integration-1",
        github_id: 1,
        full_name: "acme/app",
        default_branch: "main",
        private: true,
        clone_url: "https://github.com/acme/app.git",
        installation_id: 1,
        status: "active",
        settings: {},
        created_at: "2026-03-20T00:00:00Z",
        updated_at: "2026-03-20T00:00:00Z",
      },
    ]);
    mockAgentReadiness();

    renderWithProviders(<AutopilotPage />);

    expect(await screen.findByText("Run analysis")).toBeInTheDocument();
    expect((await screen.findAllByText("TypeError: Cannot read properties of undefined")).length).toBeGreaterThan(0);
    expect(screen.getByText("Missing retry copy in payment flow")).toBeInTheDocument();
    expect(screen.getByText("Auto-runnable now")).toBeInTheDocument();
    // Direction shows in config footer
    expect(screen.getByText(/Payments hardening this quarter/)).toBeInTheDocument();
  });

  it("shows the analysis headline and config footer when a PM plan exists", async () => {
    mockSettings({
      autonomy_level: "auto_simple",
      default_agent_type: "codex",
      priority_weights: {
        customer_impact: 0.35,
        severity: 0.25,
        recency: 0.2,
        revenue_risk: 0.2,
      },
      product_context: {
        philosophy: "Ship reliability first.",
        direction: "Payments hardening this quarter.",
        focus_areas: ["auth", "incidents"],
        avoid_areas: ["redesigns"],
      },
    });
    mockStatus({
      is_running: false,
      last_run_at: "2026-03-23T18:02:00Z",
      last_run_status: "completed",
      issues_reviewed: 14,
      success_rate: 84,
      success_count: 8,
      total_delegated: 3,
      next_run_in: "in 2h",
    });
    mockLatestPlan(buildPlan());
    mockDocuments([
      {
        id: "doc-1",
        org_id: "org-1",
        title: "Roadmap",
        content: "content",
        doc_type: "roadmap",
        sort_order: 0,
        source_type: "manual",
        created_at: "2026-03-20T00:00:00Z",
        updated_at: "2026-03-21T00:00:00Z",
      },
    ]);
    mockIntegrations([
      {
        id: "github-1",
        org_id: "org-1",
        provider: "github",
        status: "active",
        created_at: "2026-03-20T00:00:00Z",
      },
    ]);
    mockRepositories([
      {
        id: "repo-1",
        org_id: "org-1",
        integration_id: "integration-1",
        github_id: 1,
        full_name: "acme/app",
        default_branch: "main",
        private: true,
        clone_url: "https://github.com/acme/app.git",
        installation_id: 1,
        status: "active",
        settings: {},
        created_at: "2026-03-20T00:00:00Z",
        updated_at: "2026-03-20T00:00:00Z",
      },
    ]);
    mockAgentReadiness();

    renderWithProviders(<AutopilotPage />);

    expect((await screen.findAllByText("TypeError: Cannot read properties of undefined")).length).toBeGreaterThan(0);
    expect(screen.getByText("Priority fit")).toBeInTheDocument();
    // Config footer rows
    expect(screen.getByText("Impact 35 · Severity 25 · Recency 20 · Revenue 20")).toBeInTheDocument();
    expect(screen.getByText("1 attached")).toBeInTheDocument();
    expect(screen.getByText(/2 issues ranked/)).toBeInTheDocument();
  });

  it("loads additional queue pages when more issues are available", async () => {
    mockSettings({
      default_agent_type: "codex",
      product_context: {
        philosophy: "Ship reliability first.",
        direction: "Payments hardening this quarter.",
        focus_areas: ["auth"],
        avoid_areas: [],
      },
    });
    mockStatus({
      is_running: false,
      issues_reviewed: 0,
      success_rate: 0,
      success_count: 0,
      total_delegated: 0,
    });
    mockLatestPlan(null);
    mockDocuments([]);
    mockIntegrations([
      {
        id: "github-1",
        org_id: "org-1",
        provider: "github",
        status: "active",
        created_at: "2026-03-20T00:00:00Z",
      },
    ]);
    mockRepositories([
      {
        id: "repo-1",
        org_id: "org-1",
        integration_id: "integration-1",
        github_id: 1,
        full_name: "acme/app",
        default_branch: "main",
        private: true,
        clone_url: "https://github.com/acme/app.git",
        installation_id: 1,
        status: "active",
        settings: {},
        created_at: "2026-03-20T00:00:00Z",
        updated_at: "2026-03-20T00:00:00Z",
      },
    ]);
    mockAgentReadiness();
    server.use(
      http.get("/api/v1/autopilot/queue", ({ request }) => {
        const cursor = new URL(request.url).searchParams.get("cursor");
        const baseRow: AutopilotQueueResponse["data"][number] = {
          id: "issue-page-1",
          rank: 1,
          source: { type: "linear", key: "VIR-100" },
          title: "First page issue",
          repo: { id: "repo-1", name: "acme/app" },
          issue_status: "triaged",
          customer_impact: { label: "High", count: 12 },
          implementation_ease: "High",
          low_hanging_fruit: {
            label: "High",
            reasons: ["straightforward implementation"],
            cluster_size: 1,
          },
          display_run_state: "not_started",
          available_action: "start_run",
        };
        if (cursor === "50") {
          return HttpResponse.json({
            data: [{ ...baseRow, id: "issue-page-2", rank: 51, source: { type: "sentry", key: "SENTRY-999" }, title: "Late issue after page boundary" }],
            meta: {
              summary: {
                autorunnable_count: 2,
                needs_review_count: 0,
                open_pr_count: 0,
                active_run_count: 0,
                ranked_issue_count: 51,
              },
            },
          } satisfies AutopilotQueueResponse);
        }
        return HttpResponse.json({
          data: [baseRow],
          meta: {
            next_cursor: "50",
            summary: {
              top_issue_id: "issue-page-1",
              autorunnable_count: 2,
              needs_review_count: 0,
              open_pr_count: 0,
              active_run_count: 0,
              ranked_issue_count: 51,
            },
          },
        } satisfies AutopilotQueueResponse);
      })
    );

    renderWithProviders(<AutopilotPage />);

    expect((await screen.findAllByText("First page issue")).length).toBeGreaterThan(0);
    await userEvent.click(screen.getByRole("button", { name: "Load more" }));

    expect(await screen.findByText("Late issue after page boundary")).toBeInTheDocument();
  });

  it("renders compact issue sources and consistent scoring badges", async () => {
    mockSettings({
      default_agent_type: "codex",
      product_context: {
        philosophy: "Ship reliability first.",
        direction: "Payments hardening this quarter.",
        focus_areas: ["auth"],
        avoid_areas: [],
      },
    });
    mockStatus({
      is_running: false,
      issues_reviewed: 0,
      success_rate: 0,
      success_count: 0,
      total_delegated: 0,
    });
    mockLatestPlan(null);
    mockDocuments([]);
    mockIntegrations([
      {
        id: "github-1",
        org_id: "org-1",
        provider: "github",
        status: "active",
        created_at: "2026-03-20T00:00:00Z",
      },
    ]);
    mockRepositories([
      {
        id: "repo-1",
        org_id: "org-1",
        integration_id: "integration-1",
        github_id: 1,
        full_name: "acme/app",
        default_branch: "main",
        private: true,
        clone_url: "https://github.com/acme/app.git",
        installation_id: 1,
        status: "active",
        settings: {},
        created_at: "2026-03-20T00:00:00Z",
        updated_at: "2026-03-20T00:00:00Z",
      },
    ]);
    mockAgentReadiness();
    server.use(
      http.get("/api/v1/autopilot/queue", () =>
        HttpResponse.json({
          data: [
            {
              id: "issue-linear",
              rank: 1,
              source: { type: "linear", key: "737e9a4c-8d01-4d77-b64f-77a4743e7b20" },
              title: "VIR-22: Create a branch instead of needing to push a PR",
              issue_status: "triaged",
              customer_impact: { label: "Low", count: 0 },
              implementation_ease: "Medium",
              low_hanging_fruit: {
                label: "High",
                reasons: ["eligible for automation"],
                cluster_size: 1,
              },
              display_run_state: "not_started",
              available_action: "blocked",
              action_disabled_reason: "Select a repository before starting a run.",
            },
            {
              id: "issue-manual",
              rank: 2,
              source: { type: "manual", key: "Manual-20260422232356-60685fba36584d41ad1cc22b32f9f11e" },
              title: "Internal product note",
              issue_status: "open",
              customer_impact: { label: "Medium", count: 3 },
              implementation_ease: "Low",
              low_hanging_fruit: {
                label: "Low",
                reasons: [],
                cluster_size: 1,
              },
              display_run_state: "not_started",
              available_action: "blocked",
              action_disabled_reason: "Select a repository before starting a run.",
            },
          ],
          meta: {
            summary: {
              top_issue_id: "issue-linear",
              autorunnable_count: 0,
              needs_review_count: 0,
              open_pr_count: 0,
              active_run_count: 0,
              ranked_issue_count: 2,
            },
          },
        } satisfies AutopilotQueueResponse))
    );

    renderWithProviders(<AutopilotPage />);

    expect(await screen.findByText("VIR-22")).toBeInTheDocument();
    expect(screen.getAllByText("Low").length).toBeGreaterThanOrEqual(2);
    expect(screen.getAllByText("Medium").length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText("Internal")).toBeInTheDocument();
    expect(screen.queryByText(/737e9a4c/)).not.toBeInTheDocument();
    expect(screen.queryByText(/60685fba/)).not.toBeInTheDocument();
  });
});
