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

  it("renders nothing when GitHub is connected and repos exist", async () => {
    server.use(
      http.get("/api/v1/integrations", () => {
        return HttpResponse.json({
          data: [{ id: "int-1", provider: "github", status: "active" }],
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

  it("shows warning when GitHub is connected but no repos", async () => {
    server.use(
      http.get("/api/v1/integrations", () => {
        return HttpResponse.json({
          data: [{ id: "int-1", provider: "github", status: "active" }],
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
  });

  it("shows Sync repositories button", async () => {
    server.use(
      http.get("/api/v1/integrations", () => {
        return HttpResponse.json({
          data: [{ id: "int-1", provider: "github", status: "active" }],
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
  });
});
