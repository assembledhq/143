import { describe, it, expect, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { NoReposWarning } from "./no-repos-warning";

vi.mock("next/navigation", () => ({
  usePathname: () => "/autopilot",
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
}));

describe("NoReposWarning", () => {
  it("renders nothing when GitHub is not connected", async () => {
    server.use(
      http.get("/api/v1/integrations", () => {
        return HttpResponse.json({ data: [], meta: {} });
      })
    );

    const { container } = renderWithProviders(<NoReposWarning />);

    // Wait for queries to settle
    await waitFor(() => {
      expect(container.textContent).toBe("");
    });
  });

  it("renders nothing while the integrations query is still pending", async () => {
    let releaseIntegrations: () => void = () => {};
    const integrationsGate = new Promise<void>((resolve) => {
      releaseIntegrations = resolve;
    });

    server.use(
      http.get("/api/v1/integrations", async () => {
        await integrationsGate;
        // No GitHub connected: once resolved this renders the warning, so the
        // absence of the warning before release proves the loading guard.
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get("/api/v1/repositories", () => {
        return HttpResponse.json({ data: [], meta: {} });
      })
    );

    const { container } = renderWithProviders(
      <NoReposWarning showDisconnectedState />
    );

    // While integrations is pending the guard returns null — nothing renders.
    expect(container).toBeEmptyDOMElement();
    expect(screen.queryByText(/github setup required/i)).not.toBeInTheDocument();

    // Once the query resolves, the warning appears.
    releaseIntegrations();
    await waitFor(() => {
      expect(screen.getByText(/github setup required/i)).toBeInTheDocument();
    });
  });

  it("renders nothing when GitHub is connected and repos exist", async () => {
    server.use(
      http.get("/api/v1/integrations", () => {
        return HttpResponse.json({
          data: [{ id: "int-1", provider: "github", status: "active", github_app_installed: true }],
          meta: {},
        });
      }),
      http.get("/api/v1/repositories", () => {
        return HttpResponse.json({
          data: [{ id: "repo-1", full_name: "org/repo" }],
          meta: {},
        });
      }),
      http.post("/api/v1/integrations/github/sync", () => {
        return HttpResponse.json({ data: { repos_synced: 0, errors: 0 } });
      })
    );

    renderWithProviders(<NoReposWarning />);

    await waitFor(
      () => {
        // It should either be empty or show a sync result
        const warning = screen.queryByText(/no repositories are synced/i);
        expect(warning).not.toBeInTheDocument();
      },
      { timeout: 3000 }
    );
  });

  it("directs GitHub App users to repository selection when no repos are claimed", async () => {
    server.use(
      http.get("/api/v1/integrations", () => {
        return HttpResponse.json({
          data: [{
            id: "int-1",
            provider: "github",
            status: "active",
            github_app_installed: true,
            github_repo_selection_required: true,
          }],
          meta: {},
        });
      }),
      http.get("/api/v1/repositories", () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.post("/api/v1/integrations/github/sync", () => {
        return HttpResponse.json({ data: { repos_synced: 0, repos_seen: 2, errors: 0 } });
      })
    );

    renderWithProviders(<NoReposWarning />);

    await waitFor(() => {
      expect(
        screen.getByText(/no repositories are claimed yet/i)
      ).toBeInTheDocument();
    });
    expect(screen.getByRole("link", { name: /choose repositories/i })).toHaveAttribute("href", "/settings/integrations?select_repos=1");
  });

  it("shows Sync repositories button for legacy GitHub integrations without an app install", async () => {
    server.use(
      http.get("/api/v1/integrations", () => {
        return HttpResponse.json({
          data: [{ id: "int-1", provider: "github", status: "active", github_app_installed: false }],
          meta: {},
        });
      }),
      http.get("/api/v1/repositories", () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.post("/api/v1/integrations/github/sync", () => {
        return HttpResponse.json({ data: { repos_synced: 0, errors: 0 } });
      })
    );

    renderWithProviders(<NoReposWarning />);

    await waitFor(() => {
      expect(
        screen.getByText(/no repositories are synced/i)
      ).toBeInTheDocument();
    });
    expect(screen.getByRole("button", { name: /sync repositories/i })).toBeInTheDocument();
  });

  it("keeps neutral copy when github integration exists without an app installation", async () => {
    server.use(
      http.get("/api/v1/integrations", () => {
        return HttpResponse.json({
          data: [{ id: "int-1", provider: "github", status: "active", github_app_installed: false }],
          meta: {},
        });
      }),
      http.get("/api/v1/repositories", () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.post("/api/v1/integrations/github/sync", () => {
        return HttpResponse.json({ data: { repos_synced: 0, errors: 0 } });
      }),
    );

    renderWithProviders(<NoReposWarning />);

    await waitFor(() => {
      expect(
        screen.getByText(/github is connected but no repositories are synced/i)
      ).toBeInTheDocument();
    });
  });

  it("renders as a labeled setup row with asRow", async () => {
    server.use(
      http.get("/api/v1/integrations", () => {
        return HttpResponse.json({
          data: [{
            id: "int-1",
            provider: "github",
            status: "active",
            github_app_installed: true,
            github_repo_selection_required: true,
          }],
          meta: {},
        });
      }),
      http.get("/api/v1/repositories", () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.post("/api/v1/integrations/github/sync", () => {
        return HttpResponse.json({ data: { repos_synced: 0, repos_seen: 2, errors: 0 } });
      })
    );

    renderWithProviders(<NoReposWarning asRow />);

    await waitFor(() => {
      expect(screen.getByText("Repository")).toBeInTheDocument();
    });
    expect(screen.getByText(/choose repositories in integrations/i)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /choose repositories/i })).toHaveAttribute(
      "href",
      "/settings/integrations?select_repos=1",
    );
  });
});
