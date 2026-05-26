import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { fireEvent, renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
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
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<RepositoryDetailContent id="repo-1" />);

    await waitFor(() => {
      expect(document.title).toBe("143 | acme/web");
    });
  });

  it("lets admins create a preview secret bundle from the repository page", async () => {
    let savedBody: unknown;
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
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
      http.post("*/api/v1/repositories/repo-1/preview-secret-bundles", async ({ request }) => {
        savedBody = await request.json();
        return HttpResponse.json({
          data: {
            id: "bundle-1",
            repository_id: "repo-1",
            name: "assembled-dev",
            source_type: "managed",
            exposure_policy: "preview_runtime",
            outputs: [{ type: "env", env: ["DATABASE_URL"] }],
            created_by_user_id: "user-1",
            created_at: "2026-01-01T00:00:00Z",
          },
        });
      }),
    );

    renderWithProviders(<RepositoryDetailContent id="repo-1" />);

    await userEvent.type(await screen.findByLabelText("Bundle name"), "assembled-dev");
    fireEvent.change(screen.getByLabelText("Secret values"), {
      target: { value: '{"DATABASE_URL":"postgres://dev"}' },
    });
    fireEvent.change(screen.getByLabelText("Outputs"), {
      target: { value: '[{"type":"env","values":{"DATABASE_URL":"secret:DATABASE_URL"}}]' },
    });
    await userEvent.click(screen.getByRole("button", { name: /save bundle/i }));

    await waitFor(() => {
      expect(savedBody).toEqual({
        name: "assembled-dev",
        source: {
          type: "managed",
          values: { DATABASE_URL: "postgres://dev" },
        },
        outputs: [{ type: "env", values: { DATABASE_URL: "secret:DATABASE_URL" } }],
        exposure_policy: "preview_runtime",
      });
    });
  });

  it("lets admins test a preview secret bundle without exposing values", async () => {
    let testedBundleID = "";
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
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({
        data: [{
          id: "bundle-1",
          repository_id: "repo-1",
          name: "assembled-dev",
          source_type: "managed",
          exposure_policy: "preview_runtime",
          outputs: [{ type: "env", env: ["DATABASE_URL"] }],
          created_by_user_id: "user-1",
          created_at: "2026-01-01T00:00:00Z",
        }],
        meta: {},
      })),
      http.post("*/api/v1/preview-secret-bundles/:bundleId/test", ({ params }) => {
        testedBundleID = String(params.bundleId);
        return HttpResponse.json({
          data: {
            status: "ready",
            bundle: {
              id: "bundle-1",
              repository_id: "repo-1",
              name: "assembled-dev",
              source_type: "managed",
              exposure_policy: "preview_runtime",
              outputs: [{ type: "env", env: ["DATABASE_URL"] }],
              created_by_user_id: "user-1",
              created_at: "2026-01-01T00:00:00Z",
            },
          },
        });
      }),
    );

    renderWithProviders(<RepositoryDetailContent id="repo-1" />);

    await userEvent.click(await screen.findByRole("button", { name: /test assembled-dev/i }));

    await waitFor(() => {
      expect(testedBundleID).toBe("bundle-1");
    });
    expect(screen.queryByText("postgres://user:pass@db/app")).not.toBeInTheDocument();
  });
});
