import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { fireEvent, renderWithProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import type { APIClient, APIToken } from "@/lib/types";
import APIKeysSettingsPage from "./page";

const repos = [
  repo("repo-1", "assembledhq/143"),
  repo("repo-2", "assembledhq/docs"),
];

const client: APIClient = {
  id: "api-client-1",
  org_id: "org-1",
  name: "CI automation",
  description: "Runs external API workflows from CI",
  status: "enabled",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function changeFieldValue(element: HTMLElement, value: string) {
  fireEvent.change(element, { target: { value } });
}

function setupAPIKeyPage(options?: { clients?: APIClient[]; tokens?: APIToken[] }) {
  server.use(
    http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
    http.get("*/api/v1/api-keys", () => HttpResponse.json({ data: options?.clients ?? [client], meta: {} })),
    http.get("*/api/v1/api-keys/:id/tokens", () => HttpResponse.json({ data: options?.tokens ?? [], meta: {} })),
  );
  renderWithProviders(<APIKeysSettingsPage />);
}

describe("APIKeysSettingsPage", () => {
  it("creates full access keys with expanded explicit scopes and shows one-time copy options", async () => {
    let savedBody: unknown;
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/api-keys", () => HttpResponse.json({ data: [], meta: {} })),
      http.post("*/api/v1/api-keys", async ({ request }) => {
        savedBody = await request.json();
        return HttpResponse.json({
          data: {
            client: { ...client, id: "api-client-new", name: "Cursor" },
            token: {
              id: "api-token-new",
              org_id: "org-1",
              api_client_id: "api-client-new",
              name: "production",
              token_prefix: "143_live_new",
              token: "143_live_new_secret",
              scopes: [
                "sessions:read",
                "sessions:create",
                "sessions:write",
                "sessions:cancel",
                "sessions:publish",
                "automations:read",
                "automations:create",
                "automations:write",
                "automations:run",
                "previews:read",
                "previews:create",
                "previews:stop",
              ],
              repository_ids: [],
              allowed_ip_cidrs: [],
              created_at: "2026-02-17T08:00:00Z",
            },
          },
        }, { status: 201 });
      }),
    );

    renderWithProviders(<APIKeysSettingsPage />);

    await userEvent.click(await screen.findByRole("button", { name: /create api key/i }));
    changeFieldValue(screen.getByLabelText("Integration name"), "Cursor");
    await userEvent.click(screen.getByRole("radio", { name: /full external api access/i }));
    await userEvent.click(screen.getByRole("button", { name: /^create$/i }));

    await waitFor(() => {
      expect(savedBody).toMatchObject({
        integration_name: "Cursor",
        scopes: expect.arrayContaining(["sessions:create", "automations:run", "previews:stop"]),
        repository_ids: [],
      });
    });
    expect(savedBody).not.toMatchObject({ scopes: expect.arrayContaining(["*:all"]) });

    expect(await screen.findByText("143_live_new_secret")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /copy raw token/i }));
    await userEvent.click(screen.getByRole("button", { name: /copy authorization header/i }));
    await userEvent.click(screen.getByRole("button", { name: /copy curl example/i }));
    expect(writeText).toHaveBeenCalledWith("143_live_new_secret");
    expect(writeText).toHaveBeenCalledWith("Authorization: Bearer 143_live_new_secret");
    expect(writeText).toHaveBeenCalledWith(expect.stringContaining("/api/v1/sessions"));
    expect(screen.getByRole("button", { name: /i have saved it/i })).toBeInTheDocument();
  });

  it("validates custom expiration, IP allowlists, and selected repository mode before submit", async () => {
    let submitCount = 0;
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/api-keys", () => HttpResponse.json({ data: [], meta: {} })),
      http.post("*/api/v1/api-keys", () => {
        submitCount += 1;
        return HttpResponse.json({ data: { client, token: {} } });
      }),
    );

    renderWithProviders(<APIKeysSettingsPage />);

    await userEvent.click(await screen.findByRole("button", { name: /create api key/i }));
    changeFieldValue(screen.getByLabelText("Integration name"), "Cursor");
    await userEvent.click(screen.getByRole("radio", { name: /^custom$/i }));
    changeFieldValue(screen.getByLabelText("Custom expiration"), "2020-01-01T00:00");
    await userEvent.click(screen.getByRole("button", { name: /^create$/i }));
    expect(await screen.findByText("Custom expiration must be in the future.")).toBeInTheDocument();

    changeFieldValue(screen.getByLabelText("Custom expiration"), "2030-01-01T00:00");
    await userEvent.click(screen.getByRole("radio", { name: /selected repositories/i }));
    await userEvent.click(screen.getByRole("button", { name: /^create$/i }));
    expect(await screen.findByText("Select at least one repository or switch to all repositories.")).toBeInTheDocument();

    await userEvent.click(screen.getByLabelText("Restrict by source IP"));
    changeFieldValue(screen.getByLabelText("Allowed IPs or CIDRs"), "not-an-ip");
    await userEvent.click(screen.getByText("assembledhq/143"));
    await userEvent.click(screen.getByRole("button", { name: /^create$/i }));
    expect(await screen.findByText("Enter valid IP addresses or CIDR ranges.")).toBeInTheDocument();
    expect(submitCount).toBe(0);
  });

  it("filters repositories in selected mode and submits selected repository IDs", async () => {
    let savedBody: { repository_ids?: string[] } | undefined;
    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({ data: repos, meta: {} })),
      http.get("*/api/v1/api-keys", () => HttpResponse.json({ data: [], meta: {} })),
      http.post("*/api/v1/api-keys", async ({ request }) => {
        savedBody = await request.json() as { repository_ids?: string[] };
        return HttpResponse.json({
          data: {
            client: { ...client, id: "api-client-new" },
            token: {
              id: "api-token-new",
              org_id: "org-1",
              api_client_id: "api-client-new",
              name: "production",
              token_prefix: "143_live_new",
              token: "143_live_new_secret",
              scopes: ["sessions:read", "sessions:create"],
              repository_ids: savedBody.repository_ids ?? [],
              allowed_ip_cidrs: [],
              created_at: "2026-02-17T08:00:00Z",
            },
          },
        }, { status: 201 });
      }),
    );

    renderWithProviders(<APIKeysSettingsPage />);

    await userEvent.click(await screen.findByRole("button", { name: /create api key/i }));
    changeFieldValue(screen.getByLabelText("Integration name"), "Cursor");
    await userEvent.click(screen.getByRole("radio", { name: /selected repositories/i }));
    changeFieldValue(screen.getByLabelText("Search repositories"), "docs");

    expect(screen.queryByText("assembledhq/143")).not.toBeInTheDocument();
    await userEvent.click(screen.getByText("assembledhq/docs"));
    await userEvent.click(screen.getByRole("button", { name: /^create$/i }));

    await waitFor(() => {
      expect(savedBody?.repository_ids).toEqual(["repo-2"]);
    });
  });

  it("renders token lifecycle metadata and confirms revocation", async () => {
    const tokens: APIToken[] = [
      token("active-token", "production", {}),
      token("expired-token", "old deploy", { expires_at: "2020-01-01T00:00:00Z" }),
      token("revoked-token", "leaked key", { revoked_at: "2026-02-01T00:00:00Z" }),
    ];
    let revokedTokenID = "";
    server.use(
      http.delete("*/api/v1/api-keys/:id/tokens/:tokenId", ({ params }) => {
        revokedTokenID = String(params.tokenId);
        return new HttpResponse(null, { status: 204 });
      }),
    );

    setupAPIKeyPage({ tokens });

    expect(await screen.findByText("production")).toBeInTheDocument();
    expect(screen.getByText("Active")).toBeInTheDocument();
    expect(screen.getByText("Expired Jan 1, 2020")).toBeInTheDocument();
    expect(screen.getByText("Revoked Feb 1, 2026")).toBeInTheDocument();
    expect(screen.getAllByText("203.0.113.10").length).toBeGreaterThan(0);
    expect(screen.getAllByText("curl/8.7.1").length).toBeGreaterThan(0);

    const activeRow = screen.getByText("production").closest("tr");
    expect(activeRow).not.toBeNull();
    await userEvent.click(within(activeRow as HTMLElement).getByRole("button", { name: /revoke production/i }));
    await userEvent.click(await screen.findByRole("button", { name: /^revoke token$/i }));

    await waitFor(() => {
      expect(revokedTokenID).toBe("active-token");
    });
  });
});

function repo(id: string, fullName: string) {
  return {
    id,
    org_id: "org-1",
    full_name: fullName,
    default_branch: "main",
    github_repo_id: 1,
    installation_id: "install-1",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

function token(id: string, name: string, overrides: Partial<APIToken>): APIToken {
  return {
    id,
    org_id: "org-1",
    api_client_id: "api-client-1",
    name,
    token_prefix: `143_live_${id}`,
    scopes: ["sessions:read", "sessions:create"],
    repository_ids: [],
    allowed_ip_cidrs: [],
    last_used_at: "2026-02-17T08:00:00Z",
    last_used_ip: "203.0.113.10",
    last_used_user_agent: "curl/8.7.1",
    created_at: "2026-01-02T00:00:00Z",
    ...overrides,
  };
}
