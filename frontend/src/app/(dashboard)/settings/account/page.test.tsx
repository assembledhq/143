import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import AccountPage from "./page";

describe("Account settings page", () => {
  it("renders configured personal auths without inventing a fallback order", async () => {
    server.use(
      http.get("/api/v1/settings/credentials/personal", () =>
        HttpResponse.json({
          data: [
            {
              provider: "anthropic",
              configured: true,
              is_team_default: false,
              masked_key: "sk-ant...5678",
              status: "active",
            },
            {
              provider: "openai",
              configured: true,
              is_team_default: false,
              masked_key: "sk-open...1234",
              status: "active",
            },
          ],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<AccountPage />);

    expect(screen.getByText("My settings")).toBeInTheDocument();
    expect(await screen.findByText("Configured personal auths")).toBeInTheDocument();
    expect(await screen.findByText("sk-ant...5678")).toBeInTheDocument();
    expect(await screen.findByText("sk-open...1234")).toBeInTheDocument();
    expect(screen.queryByText("Default auth")).not.toBeInTheDocument();
    expect(screen.queryByText("Backups in fallback order")).not.toBeInTheDocument();
    expect(screen.queryByText(/Effective resolution:/)).not.toBeInTheDocument();
  });

  it("uses the shared provider-card modal with Gemini, Amp, and Pi support", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/settings/credentials/personal", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
    );

    renderWithProviders(<AccountPage />);

    await user.click(screen.getByRole("button", { name: "Add auth" }));

    expect(await screen.findByText("Codex")).toBeInTheDocument();
    expect(screen.getAllByText("Claude Code").length).toBeGreaterThan(0);
    expect(screen.getByText("Gemini CLI")).toBeInTheDocument();
    expect(screen.getAllByText("Amp").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Pi").length).toBeGreaterThan(0);

    await user.click(screen.getByLabelText("Gemini CLI"));
    expect(screen.getByPlaceholderText("AIza...")).toBeInTheDocument();

    await user.click(screen.getByLabelText("Amp"));
    expect(screen.getByPlaceholderText("amp_...")).toBeInTheDocument();

    await user.click(screen.getByLabelText("Pi"));
    expect(screen.getByPlaceholderText("pi_...")).toBeInTheDocument();
  });

  it("stores a default coding-agent reasoning preference", async () => {
    const user = userEvent.setup();
    let requestBody: Record<string, unknown> | null = null;
    server.use(
      http.get("/api/v1/settings/credentials/personal", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.patch("/api/v1/auth/me/settings", async ({ request }) => {
        requestBody = await request.json() as Record<string, unknown>;
        return HttpResponse.json({
          data: {
            id: "user-1",
            org_id: "org-1",
            email: "alice@example.com",
            name: "Alice Smith",
            role: "admin",
            settings: {
              coding_agent_reasoning_defaults: {
                claude_code: "max",
              },
            },
            created_at: "2026-01-01T00:00:00Z",
          },
        });
      }),
    );

    renderWithProviders(<AccountPage />);

    await user.click(await screen.findByRole("combobox", { name: "Claude Code default coding-agent reasoning" }));
    await user.click(screen.getByRole("option", { name: "Max" }));

    expect(requestBody).toEqual({
      coding_agent_reasoning_defaults: {
        claude_code: "max",
      },
    });
  });

  it("serializes reasoning-default saves so older responses cannot overwrite newer ones", async () => {
    const user = userEvent.setup();
    const requestBodies: Array<Record<string, unknown>> = [];
    let resolveFirstRequest: (() => void) | undefined;

    server.use(
      http.get("/api/v1/settings/credentials/personal", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.patch("/api/v1/auth/me/settings", async ({ request }) => {
        const body = await request.json() as Record<string, unknown>;
        requestBodies.push(body);

        if (requestBodies.length === 1) {
          return await new Promise<Response>((resolve) => {
            resolveFirstRequest = () => resolve(HttpResponse.json({
              data: {
                id: "user-1",
                org_id: "org-1",
                email: "alice@example.com",
                name: "Alice Smith",
                role: "admin",
                settings: {
                  coding_agent_reasoning_defaults: {
                    codex: "high",
                  },
                },
                created_at: "2026-01-01T00:00:00Z",
              },
            }));
          });
        }

        return HttpResponse.json({
          data: {
            id: "user-1",
            org_id: "org-1",
            email: "alice@example.com",
            name: "Alice Smith",
            role: "admin",
            settings: {
              coding_agent_reasoning_defaults: {
                codex: "high",
                claude_code: "max",
              },
            },
            created_at: "2026-01-01T00:00:00Z",
          },
        });
      }),
    );

    renderWithProviders(<AccountPage />);

    await user.click(await screen.findByRole("combobox", { name: "Codex default coding-agent reasoning" }));
    await user.click(screen.getByRole("option", { name: "High" }));

    await user.click(await screen.findByRole("combobox", { name: "Claude Code default coding-agent reasoning" }));
    await user.click(screen.getByRole("option", { name: "Max" }));

    expect(requestBodies).toEqual([
      {
        coding_agent_reasoning_defaults: {
          codex: "high",
        },
      },
    ]);

    expect(resolveFirstRequest).toBeDefined();
    resolveFirstRequest!();

    await waitFor(() => {
      expect(requestBodies).toEqual([
        {
          coding_agent_reasoning_defaults: {
            codex: "high",
          },
        },
        {
          coding_agent_reasoning_defaults: {
            codex: "high",
            claude_code: "max",
          },
        },
      ]);
    });
  });

  it("keeps the first successful save in the UI when a queued retry fails", async () => {
    const user = userEvent.setup();
    const requestBodies: Array<Record<string, unknown>> = [];
    let resolveFirstRequest: (() => void) | undefined;

    server.use(
      http.get("/api/v1/settings/credentials/personal", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
      http.patch("/api/v1/auth/me/settings", async ({ request }) => {
        const body = await request.json() as Record<string, unknown>;
        requestBodies.push(body);

        if (requestBodies.length === 1) {
          return await new Promise<Response>((resolve) => {
            resolveFirstRequest = () => resolve(HttpResponse.json({
              data: {
                id: "user-1",
                org_id: "org-1",
                email: "alice@example.com",
                name: "Alice Smith",
                role: "admin",
                settings: {
                  coding_agent_reasoning_defaults: {
                    codex: "high",
                  },
                },
                created_at: "2026-01-01T00:00:00Z",
              },
            }));
          });
        }

        return HttpResponse.json(
          { error: { code: "SAVE_FAILED", message: "boom" } },
          { status: 500 },
        );
      }),
    );

    renderWithProviders(<AccountPage />);

    await user.click(await screen.findByRole("combobox", { name: "Codex default coding-agent reasoning" }));
    await user.click(screen.getByRole("option", { name: "High" }));

    await user.click(await screen.findByRole("combobox", { name: "Claude Code default coding-agent reasoning" }));
    await user.click(screen.getByRole("option", { name: "Max" }));

    expect(resolveFirstRequest).toBeDefined();
    resolveFirstRequest!();

    await waitFor(() => {
      expect(requestBodies).toEqual([
        {
          coding_agent_reasoning_defaults: {
            codex: "high",
          },
        },
        {
          coding_agent_reasoning_defaults: {
            codex: "high",
            claude_code: "max",
          },
        },
      ]);
    });

    await waitFor(() => {
      expect(screen.getByRole("combobox", { name: "Codex default coding-agent reasoning" })).toHaveTextContent("High");
      expect(screen.getByRole("combobox", { name: "Claude Code default coding-agent reasoning" })).toHaveTextContent("Product default");
    });
  });
});
