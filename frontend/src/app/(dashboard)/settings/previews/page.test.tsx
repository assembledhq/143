import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { fireEvent, renderWithProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import PreviewSettingsPage from "./page";

const repos = [
  repo("repo-1", "assembledhq/143"),
  repo("repo-2", "assembledhq/docs"),
];

describe("PreviewSettingsPage", () => {
  it("renders the renamed Preview settings surface and loads bundles for the selected repository", async () => {
    const bundleRequests: string[] = [];
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/:id/preview-secret-bundles", ({ params }) => {
        bundleRequests.push(String(params.id));
        return HttpResponse.json({
          data: params.id === "repo-1" ? [bundle("bundle-1", "repo-1", "assembled-dev")] : [],
          meta: {},
        });
      }),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<PreviewSettingsPage />);

    expect(await screen.findByRole("heading", { level: 1, name: "Preview" })).toBeInTheDocument();
    expect((await screen.findAllByText("assembled-dev"))[0]).toBeInTheDocument();
    expect(screen.getAllByText("env DATABASE_URL")[0]).toBeInTheDocument();
    expect(bundleRequests).toContain("repo-1");
  });

  it("refetches repo-scoped bundles when the repository selection changes", async () => {
    const bundleRequests: string[] = [];
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/:id/preview-secret-bundles", ({ params }) => {
        bundleRequests.push(String(params.id));
        return HttpResponse.json({
          data: params.id === "repo-2" ? [bundle("bundle-2", "repo-2", "docs-preview")] : [],
          meta: {},
        });
      }),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click(await screen.findByRole("combobox", { name: /repository/i }));
    await userEvent.click(await screen.findByRole("option", { name: "assembledhq/docs" }));

    expect((await screen.findAllByText("docs-preview"))[0]).toBeInTheDocument();
    expect(bundleRequests).toEqual(expect.arrayContaining(["repo-1", "repo-2"]));
  });

  it("creates, tests, and deletes preview secret bundles through repository-scoped APIs", async () => {
    let savedBody: unknown;
    let testedBundleID = "";
    let deletedPath = "";
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({
        data: [bundle("bundle-1", "repo-1", "assembled-dev")],
        meta: {},
      })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
      http.post("*/api/v1/repositories/repo-1/preview-secret-bundles", async ({ request }) => {
        savedBody = await request.json();
        return HttpResponse.json({ data: bundle("bundle-2", "repo-1", "staging") });
      }),
      http.post("*/api/v1/preview-secret-bundles/:bundleId/test", ({ params }) => {
        testedBundleID = String(params.bundleId);
        return HttpResponse.json({ data: { status: "ready", bundle: bundle("bundle-1", "repo-1", "assembled-dev") } });
      }),
      http.delete("*/api/v1/repositories/repo-1/preview-secret-bundles/:name", ({ request }) => {
        deletedPath = new URL(request.url).pathname;
        return new HttpResponse(null, { status: 204 });
      }),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    await userEvent.type(screen.getByLabelText("Bundle name"), "staging");
    await userEvent.type(screen.getByLabelText("Secret key"), "API_TOKEN");
    await userEvent.type(screen.getByLabelText("Secret value"), "super-secret");
    await userEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => {
      expect(savedBody).toEqual({
        name: "staging",
        source: { type: "managed", values: { API_TOKEN: "super-secret" } },
        outputs: [{ type: "env", values: { API_TOKEN: "secret:API_TOKEN" } }],
        exposure_policy: "preview_runtime",
      });
    });

    await userEvent.click((await screen.findAllByRole("button", { name: /test assembled-dev/i }))[0]);
    await waitFor(() => {
      expect(testedBundleID).toBe("bundle-1");
    });

    await userEvent.click(screen.getAllByRole("button", { name: /delete assembled-dev/i })[0]);
    await userEvent.click(await screen.findByRole("button", { name: "Delete bundle" }));
    await waitFor(() => {
      expect(deletedPath).toBe("/api/v1/repositories/repo-1/preview-secret-bundles/assembled-dev");
    });
    expect(screen.queryByText("super-secret")).not.toBeInTheDocument();
  }, 10000);

  it("does not expose file-only preview secrets as environment variables", async () => {
    let savedBody: unknown;
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
      http.post("*/api/v1/repositories/repo-1/preview-secret-bundles", async ({ request }) => {
        savedBody = await request.json();
        return HttpResponse.json({ data: bundle("bundle-2", "repo-1", "file-only") });
      }),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    await userEvent.type(screen.getByLabelText("Bundle name"), "file-only");
    await userEvent.type(screen.getByLabelText("Secret key"), "API_TOKEN");
    await userEvent.type(screen.getByLabelText("Secret value"), "super-secret");
    await userEvent.click(screen.getByLabelText("Expose as env"));
    fireEvent.change(screen.getByLabelText("File outputs JSON"), {
      target: {
        value: '[{"type":"file","path":"development.conf.json","format":"json","content":{"token":"secret:API_TOKEN"}}]',
      },
    });
    await userEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => {
      expect(savedBody).toEqual({
        name: "file-only",
        source: { type: "managed", values: { API_TOKEN: "super-secret" } },
        outputs: [{
          type: "file",
          path: "development.conf.json",
          format: "json",
          content: { token: "secret:API_TOKEN" },
        }],
        exposure_policy: "preview_runtime",
      });
    });
  }, 10000);

  it("disables Save when a bundle has a filled value but no env or file outputs configured", async () => {
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    await userEvent.type(screen.getByLabelText("Bundle name"), "no-outputs");
    await userEvent.type(screen.getByLabelText("Secret key"), "API_TOKEN");
    await userEvent.type(screen.getByLabelText("Secret value"), "super-secret");
    // Uncheck the only env output row so outputs will be empty
    await userEvent.click(screen.getByLabelText("Expose as env"));

    // Save is disabled — the button gives immediate feedback via the title tooltip
    // rather than allowing submission and showing a post-submit error.
    expect(screen.getByRole("button", { name: /save/i })).toBeDisabled();
    expect(screen.getByRole("button", { name: /save/i })).toHaveAttribute(
      "title",
      "At least one env or file output is required",
    );
  }, 10000);

  it("edits a preview secret bundle via the patch endpoint", async () => {
    let patchedBody: unknown;
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({
        data: [bundle("bundle-1", "repo-1", "assembled-dev")],
        meta: {},
      })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
      http.patch("*/api/v1/preview-secret-bundles/:bundleId", async ({ request }) => {
        patchedBody = await request.json();
        return HttpResponse.json({ data: bundle("bundle-1", "repo-1", "assembled-dev") });
      }),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click((await screen.findAllByRole("button", { name: /edit assembled-dev/i }))[0]);
    // In edit mode the key is pre-filled from the existing bundle outputs; only the value needs re-entry.
    await userEvent.type(screen.getByLabelText("Secret value"), "new-secret");
    await userEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => {
      expect(patchedBody).toMatchObject({
        name: "assembled-dev",
        source: { type: "managed", values: { DATABASE_URL: "new-secret" } },
        outputs: [{ type: "env", values: { DATABASE_URL: "secret:DATABASE_URL" } }],
        exposure_policy: "preview_runtime",
      });
    });
  }, 10000);

  it("keeps the id-based Test bundle action available inside the edit dialog", async () => {
    let testedBundleID = "";
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({
        data: [bundle("bundle-1", "repo-1", "assembled-dev")],
        meta: {},
      })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
      http.post("*/api/v1/preview-secret-bundles/:bundleId/test", ({ params }) => {
        testedBundleID = String(params.bundleId);
        return HttpResponse.json({ data: { status: "ready", bundle: bundle("bundle-1", "repo-1", "assembled-dev") } });
      }),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click((await screen.findAllByRole("button", { name: /edit assembled-dev/i }))[0]);
    await userEvent.click(await screen.findByRole("button", { name: "Test bundle" }));

    await waitFor(() => {
      expect(testedBundleID).toBe("bundle-1");
    });
  });

  it("creates and revokes preview API tokens in the secondary section", async () => {
    let createdBody: unknown;
    let revokedPath = "";
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({
        data: [{
          id: "token-1",
          org_id: "org-1",
          name: "CI previews",
          scopes: ["previews:create", "previews:read"],
          repository_ids: [],
          created_by_user_id: "user-1",
          created_at: "2026-05-20T00:00:00Z",
        }],
        meta: {},
      })),
      http.post("*/api/v1/previews/api-tokens", async ({ request }) => {
        createdBody = await request.json();
        return HttpResponse.json({
          data: {
            id: "token-2",
            org_id: "org-1",
            name: "Docs preview",
            scopes: ["previews:read"],
            repository_ids: ["repo-2"],
            created_by_user_id: "user-1",
            created_at: "2026-05-27T00:00:00Z",
            token: "pt_live_once",
          },
        });
      }),
      http.delete("*/api/v1/previews/api-tokens/:id", ({ request }) => {
        revokedPath = new URL(request.url).pathname;
        return HttpResponse.json({ data: { status: "revoked" } });
      }),
    );

    renderWithProviders(<PreviewSettingsPage />);

    const apiSection = await screen.findByRole("region", { name: "Preview API" });
    expect((await within(apiSection).findAllByText("CI previews"))[0]).toBeInTheDocument();
    const repositoryAccess = within(apiSection).getAllByText("All repositories")[0];
    expect(repositoryAccess.closest('[data-slot="badge"]')).not.toBeNull();

    await userEvent.click(within(apiSection).getByRole("button", { name: /create token/i }));
    await userEvent.type(screen.getByLabelText("Name"), "Docs preview");
    await userEvent.click(screen.getByLabelText("previews:create"));
    await userEvent.click(screen.getByLabelText("previews:stop"));
    await userEvent.click(screen.getByLabelText("assembledhq/docs"));
    await userEvent.click(screen.getAllByRole("button", { name: "Create token" }).at(-1)!);

    await waitFor(() => {
      expect(createdBody).toEqual({
        name: "Docs preview",
        scopes: ["previews:read"],
        repository_ids: ["repo-2"],
      });
    });
    expect(await screen.findByText("pt_live_once")).toBeInTheDocument();
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    });
    await userEvent.click(screen.getByRole("button", { name: "Copy token" }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith("pt_live_once");
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));

    await userEvent.click(within(apiSection).getAllByRole("button", { name: /revoke ci previews/i })[0]);
    await waitFor(() => {
      expect(revokedPath).toBe("/api/v1/previews/api-tokens/token-1");
    });
  }, 10000);
});

function repo(id: string, fullName: string) {
  return {
    id,
    org_id: "org-1",
    integration_id: "int-1",
    github_id: id === "repo-1" ? 1 : 2,
    full_name: fullName,
    default_branch: "main",
    private: false,
    clone_url: `https://github.com/${fullName}.git`,
    installation_id: 10,
    status: "active",
    settings: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

function bundle(id: string, repositoryId: string, name: string) {
  return {
    id,
    repository_id: repositoryId,
    name,
    source_type: "managed",
    exposure_policy: "preview_runtime",
    outputs: [{ type: "env", env: ["DATABASE_URL"] }],
    created_by_user_id: "user-1",
    created_at: "2026-05-27T00:00:00Z",
  };
}
