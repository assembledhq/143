import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
import { RepoContextSwitcher } from "./repo-context-switcher";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";

vi.mock("next/navigation", () => ({
  usePathname: () => "/sessions",
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}));

describe("RepoContextSwitcher", () => {
  it("renders nothing when org has only 1 repo", async () => {
    server.use(
      http.get("/api/v1/repositories/summary", () => {
        return HttpResponse.json({
          data: [
            {
              repository_id: "repo-1",
              full_name: "acme/api-server",
              active_session_count: 0,
              latest_session_status: null,
              active_project_count: 0,
            },
          ],
          meta: {},
        });
      })
    );

    const { container } = renderWithProviders(<RepoContextSwitcher />);

    // Wait for query to resolve then verify nothing is rendered
    await waitFor(() => {
      expect(container.querySelector('[data-testid="repo-context-switcher"]')).not.toBeInTheDocument();
    });
  });

  it("renders the context switcher when org has 2+ repos", async () => {
    server.use(
      http.get("/api/v1/repositories/summary", () => {
        return HttpResponse.json({
          data: [
            {
              repository_id: "repo-1",
              full_name: "acme/api-server",
              active_session_count: 3,
              latest_session_status: "running",
              active_project_count: 2,
            },
            {
              repository_id: "repo-2",
              full_name: "acme/web-app",
              active_session_count: 0,
              latest_session_status: null,
              active_project_count: 0,
            },
          ],
          meta: {},
        });
      })
    );

    renderWithProviders(<RepoContextSwitcher />);

    await waitFor(() => {
      expect(screen.getByTestId("repo-context-switcher")).toBeInTheDocument();
    });

    // Default label should be "All repositories"
    expect(screen.getByText("All repositories")).toBeInTheDocument();
  });

  it("shows 'All repositories' as default selected state", async () => {
    server.use(
      http.get("/api/v1/repositories/summary", () => {
        return HttpResponse.json({
          data: [
            { repository_id: "repo-1", full_name: "acme/api-server", active_session_count: 0, latest_session_status: null, active_project_count: 0 },
            { repository_id: "repo-2", full_name: "acme/web-app", active_session_count: 0, latest_session_status: null, active_project_count: 0 },
          ],
          meta: {},
        });
      })
    );

    renderWithProviders(<RepoContextSwitcher />);

    await waitFor(() => {
      expect(screen.getByText("All repositories")).toBeInTheDocument();
    });
  });

  it("renders nothing when org has zero repos", async () => {
    server.use(
      http.get("/api/v1/repositories/summary", () => {
        return HttpResponse.json({
          data: [],
          meta: {},
        });
      })
    );

    const { container } = renderWithProviders(<RepoContextSwitcher />);

    await waitFor(() => {
      expect(container.querySelector('[data-testid="repo-context-switcher"]')).not.toBeInTheDocument();
    });
  });
});
