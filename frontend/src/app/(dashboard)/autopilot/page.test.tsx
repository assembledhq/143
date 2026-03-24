import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";
import AutopilotPage from "./page";

vi.mock("next/navigation", () => ({
  usePathname: () => "/autopilot",
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
}));

// Default handlers for a fully-onboarded org with PM data
function setupOnboardedHandlers() {
  server.use(
    http.get("/api/v1/integrations", () => {
      return HttpResponse.json({
        data: [
          { id: "int-1", provider: "github", status: "active", created_at: "2026-01-01T00:00:00Z" },
        ],
        meta: {},
      });
    }),
    http.get("/api/v1/settings", () => {
      return HttpResponse.json({
        data: {
          id: "org-1",
          settings: {
            default_agent_type: "codex",
            agent_config: { codex: { OPENAI_API_KEY: "sk-test" } },
          },
        },
      });
    }),
    http.get("/api/v1/settings/codex-auth/status", () => {
      return HttpResponse.json({ data: { status: "completed" } });
    }),
    http.get("/api/v1/settings/agent-defaults", () => {
      return HttpResponse.json({ data: {} });
    }),
    http.get("/api/v1/repositories", () => {
      return HttpResponse.json({
        data: [
          { id: "repo-1", full_name: "acme/api", settings: {} },
        ],
        meta: {},
      });
    }),
    http.get("/api/v1/pm/status", () => {
      return HttpResponse.json({
        data: {
          is_running: false,
          issues_reviewed: 14,
          success_rate: 75,
          success_count: 3,
          total_delegated: 4,
          last_run_at: "2026-03-20T10:00:00Z",
          last_run_status: "completed",
        },
      });
    }),
    http.get("/api/v1/pm/plans/latest", () => {
      return HttpResponse.json({
        data: {
          id: "plan-1",
          org_id: "org-1",
          status: "completed",
          analysis: "Found 14 open issues. 3 are high priority.",
          tasks: [],
          clusters: [],
          skipped: [],
          skipped_issues: [],
          created_at: "2026-03-20T10:00:00Z",
        },
      });
    }),
    http.get("/api/v1/pm/decisions", () => {
      return HttpResponse.json({
        data: [],
        summary: { total_delegated: 4, succeeded: 3, failed: 1, still_open: 0 },
        meta: {},
      });
    }),
    http.get("/api/v1/pm/plans", () => {
      return HttpResponse.json({ data: [], meta: {} });
    }),
    http.get("/api/v1/pm/documents", () => {
      return HttpResponse.json({ data: [], meta: {} });
    }),
    http.get("/api/v1/repositories/summary", () => {
      return HttpResponse.json({ data: [], meta: {} });
    }),
  );
}

function setupPreOnboardingHandlers({ noGitHub = false, noAgent = false } = {}) {
  server.use(
    http.get("/api/v1/integrations", () => {
      return HttpResponse.json({
        data: noGitHub
          ? []
          : [{ id: "int-1", provider: "github", status: "active", created_at: "2026-01-01T00:00:00Z" }],
        meta: {},
      });
    }),
    http.get("/api/v1/settings", () => {
      return HttpResponse.json({
        data: {
          id: "org-1",
          settings: noAgent
            ? { default_agent_type: "codex", agent_config: {} }
            : { default_agent_type: "codex", agent_config: { codex: { OPENAI_API_KEY: "sk-test" } } },
        },
      });
    }),
    http.get("/api/v1/settings/codex-auth/status", () => {
      return HttpResponse.json({ data: noAgent ? { status: "pending" } : { status: "completed" } });
    }),
    http.get("/api/v1/settings/agent-defaults", () => {
      return HttpResponse.json({ data: {} });
    }),
    http.get("/api/v1/repositories", () => {
      return HttpResponse.json({
        data: noGitHub ? [] : [{ id: "repo-1", full_name: "acme/api", settings: {} }],
        meta: {},
      });
    }),
    http.get("/api/v1/repositories/summary", () => {
      return HttpResponse.json({ data: [], meta: {} });
    }),
    http.get("/api/v1/pm/status", () => {
      return HttpResponse.json({ data: { is_running: false, issues_reviewed: 0, success_rate: 0, success_count: 0, total_delegated: 0 } });
    }),
    http.get("/api/v1/pm/plans/latest", () => {
      return HttpResponse.json({ error: { code: "NOT_FOUND", message: "no plans" } }, { status: 404 });
    }),
    http.get("/api/v1/pm/decisions", () => {
      return HttpResponse.json({ data: [], summary: { total_delegated: 0, succeeded: 0, failed: 0, still_open: 0 }, meta: {} });
    }),
    http.get("/api/v1/pm/plans", () => {
      return HttpResponse.json({ data: [], meta: {} });
    }),
  );
}

function setupPostOnboardingNoAnalysisHandlers() {
  server.use(
    http.get("/api/v1/integrations", () => {
      return HttpResponse.json({
        data: [{ id: "int-1", provider: "github", status: "active", created_at: "2026-01-01T00:00:00Z" }],
        meta: {},
      });
    }),
    http.get("/api/v1/settings", () => {
      return HttpResponse.json({
        data: { id: "org-1", settings: { default_agent_type: "codex", agent_config: { codex: { OPENAI_API_KEY: "sk-test" } } } },
      });
    }),
    http.get("/api/v1/settings/codex-auth/status", () => {
      return HttpResponse.json({ data: { status: "completed" } });
    }),
    http.get("/api/v1/settings/agent-defaults", () => {
      return HttpResponse.json({ data: {} });
    }),
    http.get("/api/v1/repositories", () => {
      return HttpResponse.json({
        data: [{ id: "repo-1", full_name: "acme/api", settings: {} }],
        meta: {},
      });
    }),
    http.get("/api/v1/repositories/summary", () => {
      return HttpResponse.json({ data: [], meta: {} });
    }),
    http.get("/api/v1/pm/status", () => {
      return HttpResponse.json({ data: { is_running: false, issues_reviewed: 0, success_rate: 0, success_count: 0, total_delegated: 0 } });
    }),
    http.get("/api/v1/pm/plans/latest", () => {
      return HttpResponse.json({ error: { code: "NOT_FOUND", message: "no plans" } }, { status: 404 });
    }),
    http.get("/api/v1/pm/decisions", () => {
      return HttpResponse.json({ data: [], summary: { total_delegated: 0, succeeded: 0, failed: 0, still_open: 0 }, meta: {} });
    }),
    http.get("/api/v1/pm/plans", () => {
      return HttpResponse.json({ data: [], meta: {} });
    }),
    http.get("/api/v1/pm/documents", () => {
      return HttpResponse.json({ data: [], meta: {} });
    }),
  );
}

describe("AutopilotPage", () => {
  describe("state detection", () => {
    it("shows pre-onboarding when GitHub is not connected", async () => {
      setupPreOnboardingHandlers({ noGitHub: true });
      renderWithProviders(<AutopilotPage />);

      await waitFor(() => {
        expect(screen.getByText("Help the PM agent get started by connecting your tools.")).toBeInTheDocument();
      });

      expect(screen.getByText("1. Connect a coding agent")).toBeInTheDocument();
      expect(screen.getByText("2. Connect integrations")).toBeInTheDocument();
    });

    it("shows pre-onboarding when agent is not connected", async () => {
      setupPreOnboardingHandlers({ noAgent: true });
      renderWithProviders(<AutopilotPage />);

      await waitFor(() => {
        expect(screen.getByText("Help the PM agent get started by connecting your tools.")).toBeInTheDocument();
      });

      expect(screen.getByText("Configure coding agent")).toBeInTheDocument();
    });

    it("shows post-onboarding empty state when onboarded but no analysis", async () => {
      setupPostOnboardingNoAnalysisHandlers();
      renderWithProviders(<AutopilotPage />);

      await waitFor(() => {
        expect(screen.getByText("Ready to analyze")).toBeInTheDocument();
      });

      expect(screen.getByText("Run First Analysis")).toBeInTheDocument();
    });

    it("shows full workspace when onboarded with PM data", async () => {
      setupOnboardedHandlers();
      renderWithProviders(<AutopilotPage />);

      await waitFor(() => {
        expect(screen.getByText("Autopilot")).toBeInTheDocument();
      });

      // Control strip should appear
      await waitFor(() => {
        expect(screen.getByText("Run now")).toBeInTheDocument();
      });

      // Decisions card should appear
      expect(screen.getByText("Recent decisions")).toBeInTheDocument();
    });
  });
});
