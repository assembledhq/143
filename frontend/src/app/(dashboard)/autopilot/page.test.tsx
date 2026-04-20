import { beforeEach, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
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

function mockLatestPlan(plan: PMPlan | null, status = 200) {
  server.use(
    http.get("/api/v1/pm/plans/latest", () => {
      if (!plan) {
        return HttpResponse.json(
          { error: { code: "NOT_FOUND", message: "No PM plan found" } },
          { status }
        );
      }
      return HttpResponse.json({ data: plan } satisfies SingleResponse<PMPlan>);
    })
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
    mockLatestPlan(null, 404);
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

  it("shows the first-analysis state when setup is complete but no plan exists", async () => {
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
    mockLatestPlan(null, 404);
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

    expect(await screen.findByText("Run first analysis")).toBeInTheDocument();
    expect(screen.getByText("Ready for your first analysis")).toBeInTheDocument();
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

    // Analysis headline (full analysis as title since it's < 80 chars with no sentence break)
    expect(await screen.findByText("3 payment failures appear linked by one auth middleware issue.")).toBeInTheDocument();
    // Config footer rows
    expect(screen.getByText("Impact 35 · Severity 25 · Recency 20 · Revenue 20")).toBeInTheDocument();
    expect(screen.getByText("1 attached")).toBeInTheDocument();
    // Evidence metrics
    expect(screen.getByText("84%")).toBeInTheDocument();
    expect(screen.getByText("14")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();
  });
});
