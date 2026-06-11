import { beforeEach, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { act } from "@testing-library/react";
import { fireEvent, renderWithProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import PreviewSettingsPage from "./page";

const repos = [
  repo("repo-1", "assembledhq/143"),
  repo("repo-2", "assembledhq/docs"),
];

function changeFieldValue(element: HTMLElement, value: string) {
  fireEvent.change(element, { target: { value } });
}

async function renderPreviewSettingsTab(tabName: "Secrets" | "API tokens") {
  renderWithProviders(<PreviewSettingsPage />);
  await userEvent.click(await screen.findByRole("tab", { name: tabName }));
}

describe("PreviewSettingsPage", () => {
  beforeEach(() => {
    server.use(
      http.get("*/api/v1/previews/policies", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/settings", () => HttpResponse.json({
        data: {
          id: "org-1",
          name: "Assembled",
          settings: { preview_auto_pool_max_active: 4 },
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      })),
      http.patch("*/api/v1/settings", async ({ request }) => {
        const body = await request.json() as { settings?: Record<string, unknown> };
        return HttpResponse.json({
          data: {
            id: "org-1",
            name: "Assembled",
            settings: { preview_auto_pool_max_active: body.settings?.preview_auto_pool_max_active ?? 4 },
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
        });
      }),
    );
  });

  it("renders auto-preview policies first and autosaves mode and pool changes", async () => {
    let savedPolicy: unknown;
    let savedSettings: unknown;
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/:id/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/policies", () => HttpResponse.json({
        data: [
          {
            repository_id: "repo-1",
            repository_full_name: "assembledhq/143",
            auto_mode: "off",
            open_pr_count: 12,
            updated_at: null,
          },
          {
            repository_id: "repo-2",
            repository_full_name: "assembledhq/docs",
            auto_mode: "warm",
            open_pr_count: 3,
            updated_at: "2026-06-08T12:00:00Z",
          },
        ],
        meta: {},
      })),
      http.put("*/api/v1/repositories/repo-1/preview-policy", async ({ request }) => {
        savedPolicy = await request.json();
        return HttpResponse.json({ data: { id: "policy-1", auto_mode: "warm" } });
      }),
      http.patch("*/api/v1/settings", async ({ request }) => {
        savedSettings = await request.json();
        return HttpResponse.json({
          data: {
            id: "org-1",
            name: "Assembled",
            settings: { preview_auto_pool_max_active: 8 },
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
        });
      }),
    );

    renderWithProviders(<PreviewSettingsPage />);

    expect(await screen.findByRole("tab", { name: "Auto-preview" })).toHaveAttribute("aria-selected", "true");
    expect(await screen.findByText("assembledhq/143")).toBeInTheDocument();
    expect(screen.getByText("12")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: /use warm auto-preview for assembledhq\/143/i }));
    await waitFor(() => {
      expect(savedPolicy).toEqual({ auto_mode: "warm" });
    });

    const poolInput = screen.getByLabelText("Concurrent auto-previews");
    changeFieldValue(poolInput, "8");
    await waitFor(() => {
      expect(savedSettings).toEqual({ settings: { preview_auto_pool_max_active: 8 } });
    });
  });

  it("keeps auto-preview rows usable in the mobile stacked layout", async () => {
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/:id/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/policies", () => HttpResponse.json({
        data: [
          {
            repository_id: "repo-1",
            repository_full_name: "assembledhq/143",
            auto_mode: "warm",
            open_pr_count: 5,
            updated_at: "2026-06-08T12:00:00Z",
          },
        ],
        meta: {},
      })),
    );

    renderWithProviders(<PreviewSettingsPage />);

    const row = (await screen.findByText("assembledhq/143")).closest("tr");
    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getByText("Open PRs")).toBeInTheDocument();
    expect(within(row as HTMLElement).getByText("Updated")).toBeInTheDocument();
  });

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

    await renderPreviewSettingsTab("Secrets");

    expect(await screen.findByRole("heading", { level: 1, name: "Preview" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Runtime settings" })).toHaveAttribute("href", "/settings/runtime");
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

    await renderPreviewSettingsTab("Secrets");

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

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    changeFieldValue(screen.getByLabelText("Bundle name"), "staging");
    changeFieldValue(screen.getByLabelText("Secret name"), "API_TOKEN");
    changeFieldValue(screen.getByLabelText("Secret value"), "super-secret");
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

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    await userEvent.click(screen.getByRole("combobox", { name: "Bundle repository" }));
    await userEvent.click(await screen.findByRole("option", { name: "assembledhq/docs" }));
    changeFieldValue(screen.getByLabelText("Bundle name"), "docs-preview");
    changeFieldValue(screen.getByLabelText("Secret name"), "API_TOKEN");
    changeFieldValue(screen.getByLabelText("Secret value"), "super-secret");
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

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));

    expect(screen.getByRole("tablist", { name: "Bundle output editor" })).toBeInTheDocument();
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

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    changeFieldValue(screen.getByLabelText("Bundle name"), "file-only");
    await userEvent.click(screen.getByRole("tab", { name: "Secret file" }));
    changeFieldValue(screen.getByLabelText("Secret file path"), "development.conf.json");
    changeFieldValue(screen.getByLabelText("Secret file contents"), '{"token":"super-secret"}');
    await userEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => {
      expect(savedBody).toEqual({
        name: "file-only",
        source: { type: "managed", values: { SECRET_FILE_CONTENT: '{"token":"super-secret"}' } },
        outputs: [{
          type: "file",
          path: "development.conf.json",
          format: "raw",
          value: "secret:SECRET_FILE_CONTENT",
        }],
        exposure_policy: "preview_runtime",
      });
    });
  }, 10000);

  it("saves multiple environment variables and one secret file in the same bundle", async () => {
    let savedBody: unknown;
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
      http.post("*/api/v1/repositories/repo-1/preview-secret-bundles", async ({ request }) => {
        savedBody = await request.json();
        return HttpResponse.json({ data: bundle("bundle-2", "repo-1", "mixed") });
      }),
    );

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    changeFieldValue(screen.getByLabelText("Bundle name"), "mixed");
    changeFieldValue(screen.getByLabelText("Secret name"), "DATABASE_URL");
    changeFieldValue(screen.getByLabelText("Secret value"), "postgres://dev");
    await userEvent.click(screen.getByRole("button", { name: "Add value" }));
    changeFieldValue(screen.getByLabelText("Secret name 2"), "GOOGLE_DRIVE_REFRESH_TOKEN");
    changeFieldValue(screen.getByLabelText("Secret value 2"), "refresh-token");
    await userEvent.click(screen.getByRole("tab", { name: "Secret file" }));
    changeFieldValue(screen.getByLabelText("Secret file path"), "development.conf");
    changeFieldValue(screen.getByLabelText("Secret file contents"), '{"api":"secret"}');
    await userEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => {
      expect(savedBody).toEqual({
        name: "mixed",
        source: {
          type: "managed",
          values: {
            DATABASE_URL: "postgres://dev",
            GOOGLE_DRIVE_REFRESH_TOKEN: "refresh-token",
            SECRET_FILE_CONTENT: '{"api":"secret"}',
          },
        },
        outputs: [
          {
            type: "env",
            values: {
              DATABASE_URL: "secret:DATABASE_URL",
              GOOGLE_DRIVE_REFRESH_TOKEN: "secret:GOOGLE_DRIVE_REFRESH_TOKEN",
            },
          },
          {
            type: "file",
            path: "development.conf",
            format: "raw",
            value: "secret:SECRET_FILE_CONTENT",
          },
        ],
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

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    changeFieldValue(screen.getByLabelText("Bundle name"), "no-outputs");
    await userEvent.click(screen.getByRole("tab", { name: "Secret file" }));
    changeFieldValue(screen.getByLabelText("Secret file path"), "development.conf.json");

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

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    changeFieldValue(screen.getByLabelText("Bundle name"), "my-bundle");

    expect(screen.getByRole("tab", { name: "Environment variables" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("button", { name: /save/i })).toBeDisabled();

    await userEvent.hover(screen.getByText("Save").closest("span")!);
    expect(await screen.findByRole("tooltip")).toHaveTextContent("Add at least one environment variable or secret file");
  });

  it("allows editing a bundle that has both env and file outputs", async () => {
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

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click((await screen.findAllByRole("button", { name: /edit dual-delivery/i }))[0]);

    expect(screen.getByText(/Add one or more environment variables and optionally one generated file/i)).toBeInTheDocument();
    expect(screen.queryByText(/the other will be removed on save/i)).not.toBeInTheDocument();
  });

  it("keeps the file-preservation hint available when editing a file bundle", async () => {
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

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click((await screen.findAllByRole("button", { name: /edit file-bundle/i }))[0]);

    expect(screen.getByText(/leave the file contents blank/i)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("tab", { name: "Environment variables" }));

    expect(screen.getByText(/leave the file contents blank/i)).toBeInTheDocument();
  });

  it("rejects invalid JSON in secret file contents when format is JSON", async () => {
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    changeFieldValue(screen.getByLabelText("Bundle name"), "bad-json");
    await userEvent.click(screen.getByRole("tab", { name: "Secret file" }));
    changeFieldValue(screen.getByLabelText("Secret file path"), "config.json");
    await chooseSecretFileType("JSON");
    changeFieldValue(screen.getByLabelText("Secret file contents"), "not valid json{{");
    await userEvent.click(screen.getByRole("button", { name: /save/i }));

    expect(await screen.findByText(/must be valid JSON/i)).toBeInTheDocument();
  });

  it("debounces JSON validation while editing secret file contents", async () => {
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () => HttpResponse.json({ data: [], meta: {} })),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click(await screen.findByRole("button", { name: /new bundle/i }));
    changeFieldValue(screen.getByLabelText("Bundle name"), "json-file");
    await userEvent.click(screen.getByRole("tab", { name: "Secret file" }));
    changeFieldValue(screen.getByLabelText("Secret file path"), "config.json");
    await chooseSecretFileType("JSON");

    vi.useFakeTimers();
    try {
      changeFieldValue(screen.getByLabelText("Secret file contents"), "not valid json{{");
      await act(async () => {
        await vi.advanceTimersByTimeAsync(400);
      });
      expect(screen.getByText(/must be valid JSON/i)).toBeInTheDocument();

      changeFieldValue(screen.getByLabelText("Secret file contents"), '{"token":"ok"}');
      await act(async () => {
        await vi.advanceTimersByTimeAsync(400);
      });
      expect(screen.queryByText(/must be valid JSON/i)).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
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

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click((await screen.findAllByRole("button", { name: /edit assembled-dev/i }))[0]);
    // In edit mode the key is pre-filled from the existing bundle outputs; only the value needs re-entry.
    changeFieldValue(screen.getByLabelText("Secret value"), "new-secret");
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

  it("preserves leading mask characters when replacing an existing environment secret", async () => {
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

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click((await screen.findAllByRole("button", { name: /edit assembled-dev/i }))[0]);
    await userEvent.click(screen.getByLabelText("Secret value"));
    changeFieldValue(screen.getByLabelText("Secret value"), "********TOKEN");
    await userEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => {
      expect(patchedBody).toMatchObject({
        source: { type: "managed", values: { DATABASE_URL: "********TOKEN" } },
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

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click((await screen.findAllByRole("button", { name: /edit file-bundle/i }))[0]);
    changeFieldValue(screen.getByLabelText("Secret file path"), "development.conf.json");
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

  it("preserves leading mask lines when replacing existing secret file contents", async () => {
    let patchedBody: unknown;
    const fileBundle = {
      id: "bundle-file",
      repository_id: "repo-1",
      name: "file-bundle",
      source_type: "managed",
      exposure_policy: "preview_runtime",
      outputs: [{ type: "file", path: "config.json", format: "raw" }],
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

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click((await screen.findAllByRole("button", { name: /edit file-bundle/i }))[0]);
    await userEvent.click(screen.getByLabelText("Secret file contents"));
    changeFieldValue(screen.getByLabelText("Secret file contents"), "********\n********\n********\nactual-content");
    await userEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => {
      expect(patchedBody).toMatchObject({
        source: { type: "managed", values: { SECRET_FILE_CONTENT: "********\n********\n********\nactual-content" } },
      });
    });
  }, 10000);

  it("reveals existing secret file contents on explicit request", async () => {
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
      http.post("*/api/v1/preview-secret-bundles/bundle-file/reveal", () =>
        HttpResponse.json({
          data: {
            bundle: fileBundle,
            source: { type: "managed", values: { SECRET_FILE_CONTENT: '{"token":"super-secret"}' } },
            outputs: [{ type: "file", path: "config.json", format: "json", value: "secret:SECRET_FILE_CONTENT" }],
          },
        }),
      ),
    );

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click((await screen.findAllByRole("button", { name: /edit file-bundle/i }))[0]);

    expect(screen.getByLabelText("Secret file contents")).toHaveValue("********\n********\n********");
    expect(screen.getByLabelText("Secret file contents")).toHaveClass("[-webkit-text-security:disc]");

    expect(screen.queryByRole("button", { name: "Reveal contents" })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Reveal secret file contents" }));

    await waitFor(() => {
      expect(screen.getByLabelText("Secret file contents")).toHaveValue('{"token":"super-secret"}');
    });
  }, 10000);

  it("reveals existing environment variable contents on explicit request", async () => {
    const envBundle = bundle("bundle-env", "repo-1", "env-bundle");

    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () =>
        HttpResponse.json({ data: [envBundle], meta: {} }),
      ),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
      http.post("*/api/v1/preview-secret-bundles/bundle-env/reveal", () =>
        HttpResponse.json({
          data: {
            bundle: envBundle,
            source: { type: "managed", values: { DATABASE_URL: "postgres://secret-url" } },
            outputs: [{ type: "env", values: { DATABASE_URL: "secret:DATABASE_URL" } }],
          },
        }),
      ),
    );

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click((await screen.findAllByRole("button", { name: /edit env-bundle/i }))[0]);

    expect(screen.getByLabelText("Secret value")).toHaveValue("********");
    expect(screen.getByLabelText("Secret value")).toHaveAttribute("type", "password");

    await userEvent.click(screen.getByRole("button", { name: "Reveal secret value DATABASE_URL" }));

    await waitFor(() => {
      expect(screen.getByLabelText("Secret value")).toHaveValue("postgres://secret-url");
    });
  }, 10000);

  it("shows an error when the environment variable reveal API call fails", async () => {
    const envBundle = bundle("bundle-env-err", "repo-1", "env-bundle-err");

    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/repositories/repo-1/preview-secret-bundles", () =>
        HttpResponse.json({ data: [envBundle], meta: {} }),
      ),
      http.get("*/api/v1/previews/api-tokens", () => HttpResponse.json({ data: [], meta: {} })),
      http.post("*/api/v1/preview-secret-bundles/bundle-env-err/reveal", () =>
        HttpResponse.json({ error: { code: "FORBIDDEN", message: "Access denied" } }, { status: 403 }),
      ),
    );

    await renderPreviewSettingsTab("Secrets");

    await userEvent.click((await screen.findAllByRole("button", { name: /edit env-bundle-err/i }))[0]);

    await userEvent.click(screen.getByRole("button", { name: "Reveal secret value DATABASE_URL" }));

    await waitFor(() => {
      expect(screen.getByText("Access denied")).toBeInTheDocument();
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

    await renderPreviewSettingsTab("Secrets");

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

    await renderPreviewSettingsTab("API tokens");

    const apiSection = await screen.findByRole("region", { name: "Preview API tokens" });
    expect((await within(apiSection).findAllByText("CI previews"))[0]).toBeInTheDocument();
    const repositoryAccess = within(apiSection).getAllByText("All repositories")[0];
    expect(repositoryAccess.closest('[data-slot="badge"]')).not.toBeNull();

    await userEvent.click(within(apiSection).getByRole("button", { name: /create token/i }));
    changeFieldValue(screen.getByLabelText("Name"), "Docs preview");
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

async function chooseSecretFileType(optionName: string) {
  await userEvent.click(screen.getByRole("combobox", { name: "Secret file type" }));
  await userEvent.click(await screen.findByRole("option", { name: optionName }));
}
