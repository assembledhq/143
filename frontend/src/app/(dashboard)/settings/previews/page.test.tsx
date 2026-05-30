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

    await userEvent.click(await screen.findByRole("combobox", { name: /filter by repository/i }));
    await userEvent.click(await screen.findByRole("option", { name: "assembledhq/docs" }));

    expect((await screen.findAllByText("docs-preview"))[0]).toBeInTheDocument();
    expect(bundleRequests).toEqual(expect.arrayContaining(["repo-1", "repo-2"]));
  });

  it("creates and deletes preview secret bundles through repository-scoped APIs", async () => {
    let savedBody: unknown;
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
      http.delete("*/api/v1/repositories/repo-1/preview-secret-bundles/:name", ({ request }) => {
        deletedPath = new URL(request.url).pathname;
        return new HttpResponse(null, { status: 204 });
      }),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    await userEvent.type(screen.getByLabelText("Bundle name"), "staging");
    await userEvent.type(screen.getByLabelText("Secret name"), "API_TOKEN");
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

    expect(screen.queryByRole("button", { name: /test assembled-dev/i })).not.toBeInTheDocument();

    await userEvent.click(screen.getAllByRole("button", { name: /delete assembled-dev/i })[0]);
    await userEvent.click(await screen.findByRole("button", { name: "Delete bundle" }));
    await waitFor(() => {
      expect(deletedPath).toBe("/api/v1/repositories/repo-1/preview-secret-bundles/assembled-dev");
    });
    expect(screen.queryByText("super-secret")).not.toBeInTheDocument();
  }, 10000);

  it("lets users choose the bundle repository inside the create dialog", async () => {
    let savedPath = "";
    let savedBody: unknown;
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/:id/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
      http.post("*/api/v1/repositories/repo-2/preview-secret-bundles", async ({ request }) => {
        savedPath = new URL(request.url).pathname;
        savedBody = await request.json();
        return HttpResponse.json({ data: bundle("bundle-2", "repo-2", "docs-preview") });
      }),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    await userEvent.click(screen.getByRole("combobox", { name: "Bundle repository" }));
    await userEvent.click(await screen.findByRole("option", { name: "assembledhq/docs" }));
    await userEvent.type(screen.getByLabelText("Bundle name"), "docs-preview");
    await userEvent.type(screen.getByLabelText("Secret name"), "API_TOKEN");
    await userEvent.type(screen.getByLabelText("Secret value"), "super-secret");
    await userEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => {
      expect(savedPath).toBe("/api/v1/repositories/repo-2/preview-secret-bundles");
    });
    expect(savedBody).toMatchObject({ name: "docs-preview" });
  }, 10000);

  it("uses delivery tabs to choose environment variables or a pasted secret file", async () => {
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));

    expect(screen.getByRole("tablist", { name: "Delivery method" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Environment variables" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: "Secret file" })).toHaveAttribute("aria-selected", "false");
    expect(screen.getByText("Stored secrets")).toBeInTheDocument();
    expect(screen.getByText(/Each secret name becomes an environment variable/i)).toBeInTheDocument();
    expect(screen.queryByLabelText("Secret file contents")).not.toBeInTheDocument();
    expect(screen.queryByText(/Send as env var/i)).not.toBeInTheDocument();
    expect(screen.getByLabelText("Stored secrets help")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("tab", { name: "Secret file" }));

    expect(screen.getByRole("tab", { name: "Secret file" })).toHaveAttribute("aria-selected", "true");
    expect(screen.queryByText("Stored secrets")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Secret file path")).toBeInTheDocument();
    expect(screen.getByLabelText("Secret file type")).toBeInTheDocument();
    expect(screen.getByLabelText("Secret file contents")).toBeInTheDocument();
    expect(screen.getByText(/Paste the exact file that the preview app expects/i)).toBeInTheDocument();
  });

  it("stores a pasted secret file without exposing it as environment variables", async () => {
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
    await userEvent.click(screen.getByRole("tab", { name: "Secret file" }));
    await userEvent.type(screen.getByLabelText("Secret file path"), "development.conf.json");
    await userEvent.click(screen.getByRole("combobox", { name: "Secret file type" }));
    await userEvent.click(await screen.findByRole("option", { name: "JSON" }));
    fireEvent.change(screen.getByLabelText("Secret file contents"), { target: { value: '{"token":"super-secret"}' } });
    await userEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => {
      expect(savedBody).toEqual({
        name: "file-only",
        source: { type: "managed", values: { SECRET_FILE_CONTENT: '{"token":"super-secret"}' } },
        outputs: [{
          type: "file",
          path: "development.conf.json",
          format: "json",
          value: "secret:SECRET_FILE_CONTENT",
        }],
        exposure_policy: "preview_runtime",
      });
    });
  }, 10000);

  it("uses the shared tooltip for disabled Save guidance", async () => {
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    await userEvent.type(screen.getByLabelText("Bundle name"), "no-outputs");
    await userEvent.click(screen.getByRole("tab", { name: "Secret file" }));
    await userEvent.type(screen.getByLabelText("Secret file path"), "development.conf.json");

    // Save is disabled until the selected secret-file delivery method is configured.
    expect(screen.getByRole("button", { name: /save/i })).toBeDisabled();
    expect(screen.getByRole("button", { name: /save/i })).not.toHaveAttribute("title");

    await userEvent.hover(screen.getByText("Save").closest("span")!);
    expect(await screen.findByRole("tooltip")).toHaveTextContent("Paste the secret file contents before saving");
  }, 10000);

  it("shows tooltip guidance when Save is disabled in env mode with no filled values", async () => {
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    await userEvent.type(screen.getByLabelText("Bundle name"), "my-bundle");

    expect(screen.getByRole("tab", { name: "Environment variables" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("button", { name: /save/i })).toBeDisabled();

    await userEvent.hover(screen.getByText("Save").closest("span")!);
    expect(await screen.findByRole("tooltip")).toHaveTextContent("Add at least one secret name and value");
  });

  it("warns when editing a legacy bundle that has both env and file outputs", async () => {
    const dualBundle = {
      id: "bundle-dual",
      repository_id: "repo-1",
      name: "dual-delivery",
      source_type: "managed",
      exposure_policy: "preview_runtime",
      outputs: [
        { type: "env", env: ["API_TOKEN"] },
        { type: "file", path: "config.json", format: "json" },
      ],
      created_by_user_id: "user-1",
      created_at: "2026-05-27T00:00:00Z",
    };

    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () =>
        HttpResponse.json({ data: [dualBundle], meta: {} }),
      ),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click((await screen.findAllByRole("button", { name: /edit dual-delivery/i }))[0]);

    expect(screen.getByText(/uses both env vars and a secret file/i)).toBeInTheDocument();
  });

  it("hides the re-enter file hint when switching to env delivery mode on a file bundle", async () => {
    const fileBundle = {
      id: "bundle-file",
      repository_id: "repo-1",
      name: "file-bundle",
      source_type: "managed",
      exposure_policy: "preview_runtime",
      outputs: [{ type: "file", path: "config.json", format: "json" }],
      created_by_user_id: "user-1",
      created_at: "2026-05-27T00:00:00Z",
    };

    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () =>
        HttpResponse.json({ data: [fileBundle], meta: {} }),
      ),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click((await screen.findAllByRole("button", { name: /edit file-bundle/i }))[0]);

    expect(screen.getByText(/leave the file contents blank/i)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("tab", { name: "Environment variables" }));

    expect(screen.queryByText(/leave the file contents blank/i)).not.toBeInTheDocument();
  });

  it("rejects invalid JSON in secret file contents when format is JSON", async () => {
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    await userEvent.type(screen.getByLabelText("Bundle name"), "bad-json");
    await userEvent.click(screen.getByRole("tab", { name: "Secret file" }));
    await userEvent.type(screen.getByLabelText("Secret file path"), "config.json");
    await userEvent.click(screen.getByRole("combobox", { name: "Secret file type" }));
    await userEvent.click(await screen.findByRole("option", { name: "JSON" }));
    fireEvent.change(screen.getByLabelText("Secret file contents"), { target: { value: "not valid json{{" } });
    await userEvent.click(screen.getByRole("button", { name: /save/i }));

    expect(await screen.findByText(/must be valid JSON/i)).toBeInTheDocument();
  });

  it("debounces JSON validation while editing secret file contents", async () => {
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    await userEvent.type(screen.getByLabelText("Bundle name"), "json-file");
    await userEvent.click(screen.getByRole("tab", { name: "Secret file" }));
    await userEvent.type(screen.getByLabelText("Secret file path"), "config.json");
    await userEvent.click(screen.getByRole("combobox", { name: "Secret file type" }));
    await userEvent.click(await screen.findByRole("option", { name: "JSON" }));

    fireEvent.change(screen.getByLabelText("Secret file contents"), { target: { value: "not valid json{{" } });

    await waitFor(() => {
      expect(screen.getByText(/must be valid JSON/i)).toBeInTheDocument();
    });

    fireEvent.change(screen.getByLabelText("Secret file contents"), { target: { value: '{"token":"ok"}' } });

    await waitFor(() => {
      expect(screen.queryByText(/must be valid JSON/i)).not.toBeInTheDocument();
    });
  });

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

  it("preserves existing secret file contents when editing file bundle metadata", async () => {
    let patchedBody: unknown;
    const fileBundle = {
      id: "bundle-file",
      repository_id: "repo-1",
      name: "file-bundle",
      source_type: "managed",
      exposure_policy: "preview_runtime",
      outputs: [{ type: "file", path: "config.json", format: "json" }],
      created_by_user_id: "user-1",
      created_at: "2026-05-27T00:00:00Z",
    };

    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () =>
        HttpResponse.json({ data: [fileBundle], meta: {} }),
      ),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
      http.patch("*/api/v1/preview-secret-bundles/:bundleId", async ({ request }) => {
        patchedBody = await request.json();
        return HttpResponse.json({ data: fileBundle });
      }),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click((await screen.findAllByRole("button", { name: /edit file-bundle/i }))[0]);
    fireEvent.change(screen.getByLabelText("Secret file path"), { target: { value: "development.conf.json" } });
    await userEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => {
      expect(patchedBody).toEqual({
        name: "file-bundle",
        outputs: [{
          type: "file",
          path: "development.conf.json",
          format: "json",
          value: "secret:SECRET_FILE_CONTENT",
        }],
        exposure_policy: "preview_runtime",
      });
    });
  }, 10000);

  it("does not show a Test bundle action inside the edit dialog", async () => {
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({
        data: [bundle("bundle-1", "repo-1", "assembled-dev")],
        meta: {},
      })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<PreviewSettingsPage />);

    await userEvent.click((await screen.findAllByRole("button", { name: /edit assembled-dev/i }))[0]);

    expect(screen.queryByRole("button", { name: "Test bundle" })).not.toBeInTheDocument();
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
