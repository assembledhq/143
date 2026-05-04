import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import userEvent from "@testing-library/user-event";
import { renderWithProviders, screen, waitFor, within } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { RepoPMSettingsEditor } from "./repo-pm-settings";
import type { Repository } from "@/lib/types";

function repo(overrides: Partial<Repository> = {}): Repository {
  return {
    id: "repo-1",
    org_id: "org-1",
    integration_id: "integration-1",
    github_id: 123,
    full_name: "acme/app",
    default_branch: "main",
    private: true,
    clone_url: "https://github.com/acme/app.git",
    installation_id: 456,
    status: "active",
    settings: {},
    created_at: "2026-03-20T00:00:00Z",
    updated_at: "2026-03-20T00:00:00Z",
    ...overrides,
  };
}

describe("RepoPMSettingsEditor", () => {
  it("checks org-scoped coding credentials for PM availability", async () => {
    let statusRequested = false;
    let credentialsURL = "";

    server.use(
      http.get("/api/v1/settings", () => HttpResponse.json({
        data: {
          id: "org-1",
          name: "Org",
          settings: {
            default_agent_type: "codex",
            agent_config: {},
          },
          created_at: "2026-03-20T00:00:00Z",
          updated_at: "2026-03-20T00:00:00Z",
        },
      })),
      http.get("/api/v1/settings/codex-auth/status", () => {
        statusRequested = true;
        return HttpResponse.json({ data: { status: "none" } });
      }),
      http.get("/api/v1/coding-credentials", ({ request }) => {
        credentialsURL = request.url;
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    renderWithProviders(<RepoPMSettingsEditor repository={repo()} />);

    await waitFor(() => {
      expect(credentialsURL).toContain("/api/v1/coding-credentials");
    });
    expect(credentialsURL).toContain("scope=org");
    expect(statusRequested).toBe(false);
  });

  it("does not block PM model providers when org Codex status polling is forbidden", async () => {
    const user = userEvent.setup();

    server.use(
      http.get("/api/v1/settings", () => HttpResponse.json({
        data: {
          id: "org-1",
          name: "Org",
          settings: {
            default_agent_type: "codex",
            agent_config: {},
          },
          created_at: "2026-03-20T00:00:00Z",
          updated_at: "2026-03-20T00:00:00Z",
        },
      })),
      http.get("/api/v1/settings/codex-auth/status", () =>
        HttpResponse.json({ error: { code: "FORBIDDEN", message: "admin role required for scope=org operations" } }, { status: 403 }),
      ),
      http.patch("/api/v1/repositories/:id", async ({ request }) => {
        const body = await request.json() as Partial<Repository>;
        return HttpResponse.json({ data: repo(body) });
      }),
      http.get("/api/v1/coding-credentials", ({ request }) => {
        const scope = new URL(request.url).searchParams.get("scope");
        if (scope !== "org") {
          return HttpResponse.json({ data: [], meta: {} });
        }
        return HttpResponse.json({
          data: [{
            id: "cred-1",
            org_id: "org-1",
            scope: "org",
            priority: 1,
            agent: "codex",
            auth_type: "subscription",
            provider: "openai_subscription",
            label: "Team Codex",
            status: "healthy",
            is_default: true,
            created_at: "2026-03-20T00:00:00Z",
            updated_at: "2026-03-20T00:00:00Z",
          }],
          meta: {},
        });
      }),
    );

    renderWithProviders(<RepoPMSettingsEditor repository={repo({
      settings: {
        pm: {
          pm_schedule_hours: 24,
          pm_model: "gpt-5.4",
          product_context: {
            philosophy: "",
            direction: "",
            focus_areas: [],
            avoid_areas: [],
          },
        },
      },
    })} />);

    const trigger = await screen.findByRole("combobox", { name: "PM Model" });
    await user.click(trigger);

    const listbox = await screen.findByRole("listbox");
    expect(within(listbox).queryByText("Loading providers…")).not.toBeInTheDocument();
    expect(within(listbox).getByText("Codex")).toBeInTheDocument();
  });
});
