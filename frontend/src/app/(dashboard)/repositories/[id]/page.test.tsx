import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
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

  it("links to preview branch actions without rendering preview secret bundle management", async () => {
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

    expect(await screen.findByRole("link", { name: /preview branch/i })).toHaveAttribute("href", "/previews/new?repo=repo-1");
    expect(screen.queryByText("Preview secrets")).not.toBeInTheDocument();
    expect(screen.queryByText("Create or update")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Secret values")).not.toBeInTheDocument();
  });

  it("explains draft-first handoff and patches only the repository handoff key", async () => {
    let patchBody: unknown;
    const repository = {
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
      settings: { preview: { enabled: true }, pr_handoff_mode: "pre_publish" },
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
    server.use(
      http.get("*/api/v1/repositories/repo-1", () => HttpResponse.json({ data: repository })),
      http.patch("*/api/v1/repositories/repo-1", async ({ request }) => {
        patchBody = await request.json();
        return HttpResponse.json({
          data: { ...repository, settings: { ...repository.settings, pr_handoff_mode: "draft_first" } },
        });
      }),
    );

    renderWithProviders(<RepositoryDetailContent id="repo-1" />);

    expect(await screen.findByText(/143 marks it ready only after review passes/i)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("radio", { name: /Create a draft first/i }));

    await waitFor(() => {
      expect(patchBody).toEqual({ settings: { pr_handoff_mode: "draft_first" } });
    });
  });
});
