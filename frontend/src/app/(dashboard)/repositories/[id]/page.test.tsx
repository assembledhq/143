import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { RepositoryDetailContent } from "./page";

describe("RepositoryDetailPage", () => {
  it("updates the browser tab title with the repository name", async () => {
    server.use(
      http.get("*/api/v1/repositories/repo-1", () => HttpResponse.json({
        data: {
          id: "repo-1",
          org_id: "org-1",
          integration_id: "int-1",
          github_id: 1,
          full_name: "acme/web",
          default_branch: "main",
          private: false,
          clone_url: "https://github.com/acme/web.git",
          installation_id: 10,
          status: "active",
          settings: {},
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      })),
    );

    renderWithProviders(<RepositoryDetailContent id="repo-1" />);

    await waitFor(() => {
      expect(document.title).toBe("143 | acme/web");
    });
  });
});
