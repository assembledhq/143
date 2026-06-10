import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import AccountPage from "./page";

// Helper that returns a stable handler set for the unified coding-credentials
// endpoints. Each test overrides only the calls it cares about.
function emptyCodingCredentialsHandlers() {
  return [
    http.get("/api/v1/coding-credentials", ({ request }) => {
      // Echo scope back as `meta` so failing tests are easier to diagnose.
      const url = new URL(request.url);
      return HttpResponse.json({ data: [], meta: { scope: url.searchParams.get("scope") } });
    }),
  ];
}

describe("Account settings page", () => {
  it("renders the personal stack alongside the org fallback", async () => {
    server.use(
      http.get("/api/v1/coding-credentials", ({ request }) => {
        const scope = new URL(request.url).searchParams.get("scope");
        if (scope === "personal") {
          return HttpResponse.json({
            data: [
              {
                id: "p1",
                org_id: "org-1",
                user_id: "user-1",
                scope: "personal",
                priority: 1,
                agent: "claude_code",
                auth_type: "api_key",
                provider: "anthropic",
                label: "Claude Code API key",
                status: "healthy",
                is_default: true,
                usage_note: "sk-ant...5678",
                created_at: "2026-01-01T00:00:00Z",
                updated_at: "2026-01-01T00:00:00Z",
              },
              {
                id: "p2",
                org_id: "org-1",
                user_id: "user-1",
                scope: "personal",
                priority: 2,
                agent: "codex",
                auth_type: "api_key",
                provider: "openai",
                label: "Codex API key",
                status: "healthy",
                is_default: false,
                usage_note: "sk-open...1234",
                created_at: "2026-01-01T00:00:00Z",
                updated_at: "2026-01-01T00:00:00Z",
              },
            ],
            meta: {},
          });
        }
        if (scope === "org") {
          return HttpResponse.json({
            data: [
              {
                id: "o1",
                org_id: "org-1",
                scope: "org",
                priority: 1,
                agent: "codex",
                auth_type: "subscription",
                provider: "openai_subscription",
                label: "Team seat A",
                status: "healthy",
                is_default: true,
                created_at: "2026-01-01T00:00:00Z",
                updated_at: "2026-01-01T00:00:00Z",
              },
            ],
            meta: {},
          });
        }
        // resolved
        return HttpResponse.json({
          data: [
            { id: "p1", scope: "personal", agent: "claude_code", auth_type: "api_key", provider: "anthropic", label: "p1", status: "healthy", is_default: true, priority: 1, org_id: "org-1", created_at: "x", updated_at: "x" },
            { id: "p2", scope: "personal", agent: "codex", auth_type: "api_key", provider: "openai", label: "p2", status: "healthy", is_default: false, priority: 2, org_id: "org-1", created_at: "x", updated_at: "x" },
            { id: "o1", scope: "org", agent: "codex", auth_type: "subscription", provider: "openai_subscription", label: "o1", status: "healthy", is_default: true, priority: 1, org_id: "org-1", created_at: "x", updated_at: "x" },
          ],
          meta: {},
        });
      }),
    );

    renderWithProviders(<AccountPage />);

    expect(screen.getByText("My settings")).toBeInTheDocument();
    expect(await screen.findByText("My coding agents")).toBeInTheDocument();
    // Both the user-set label and the auto-generated usage note render so
    // multiple rows of the same agent/auth-type can still be told apart.
    expect((await screen.findAllByText("Claude Code API key")).length).toBeGreaterThan(0);
    expect((await screen.findAllByText("sk-ant...5678")).length).toBeGreaterThan(0);
    expect((await screen.findAllByText("Codex API key")).length).toBeGreaterThan(0);
    expect((await screen.findAllByText("sk-open...1234")).length).toBeGreaterThan(0);
    expect(await screen.findByText("Org fallback")).toBeInTheDocument();
    expect((await screen.findAllByText("Team seat A")).length).toBeGreaterThan(0);
  });

  it("renders the org fallback section even when the personal stack is empty", async () => {
    server.use(
      http.get("/api/v1/coding-credentials", ({ request }) => {
        const scope = new URL(request.url).searchParams.get("scope");
        if (scope === "personal") {
          return HttpResponse.json({ data: [], meta: {} });
        }
        if (scope === "org") {
          return HttpResponse.json({
            data: [
              {
                id: "o1",
                org_id: "org-1",
                scope: "org",
                priority: 1,
                agent: "claude_code",
                auth_type: "api_key",
                provider: "anthropic",
                label: "Org Claude key",
                status: "healthy",
                is_default: true,
                usage_note: "sk-ant...team",
                created_at: "2026-01-01T00:00:00Z",
                updated_at: "2026-01-01T00:00:00Z",
              },
            ],
            meta: {},
          });
        }
        // resolved: org-only
        return HttpResponse.json({
          data: [
            { id: "o1", scope: "org", agent: "claude_code", auth_type: "api_key", provider: "anthropic", label: "o1", status: "healthy", is_default: true, priority: 1, org_id: "org-1", created_at: "x", updated_at: "x" },
          ],
          meta: {},
        });
      }),
    );

    renderWithProviders(<AccountPage />);

    // Personal stack should show a clear empty state with its local action.
    expect(await screen.findByText("No personal auths yet")).toBeInTheDocument();
    expect(screen.getByText(/Add a personal auth to use your own subscription/)).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Add auth" })).toHaveLength(2);
    // Org fallback should still render with both the label and the masked key.
    expect((await screen.findAllByText("Org Claude key")).length).toBeGreaterThan(0);
    expect((await screen.findAllByText("sk-ant...team")).length).toBeGreaterThan(0);
  });

  it("renders an org fallback empty state when no org auths exist", async () => {
    server.use(...emptyCodingCredentialsHandlers());

    renderWithProviders(<AccountPage />);

    expect(await screen.findByText("No org fallback yet")).toBeInTheDocument();
    expect(screen.getByText(/Ask an admin to add an org-level fallback/)).toBeInTheDocument();
  });

  it("uses the shared provider-card modal with Gemini, Amp, and Pi support", async () => {
    const user = userEvent.setup();
    server.use(...emptyCodingCredentialsHandlers());

    renderWithProviders(<AccountPage />);

    await user.click(screen.getAllByRole("button", { name: "Add auth" })[0]);

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

  // This test drives the full add-auth flow through the Radix dialog (open →
  // pick agent → pick auth type → fill label + api key → submit), the
  // heaviest interaction sequence in the file. It runs in ~1.5s on an idle
  // machine but scales with CPU contention: under the threads pool several
  // files share the 2-core CI runner, and the default 5s budget is tight for
  // this many sequential Radix interactions when they collide. The 12s budget
  // matches the comparably-heavy flow in page-pr-creation.test.tsx. (Trimming
  // user-event's per-keystroke delay / pointer-events check was measured and
  // made no difference — the cost is dialog rendering and async waits, not
  // typing — so the budget is the fix.)
  it("posts new personal auths against the unified API with scope=personal", { timeout: 12_000 }, async () => {
    const user = userEvent.setup();
    let createBody: Record<string, unknown> | null = null;
    server.use(
      ...emptyCodingCredentialsHandlers(),
      http.post("/api/v1/coding-credentials", async ({ request }) => {
        createBody = await request.json() as Record<string, unknown>;
        return HttpResponse.json({
          id: "p1",
          org_id: "org-1",
          user_id: "user-1",
          scope: "personal",
          priority: 1,
          agent: "claude_code",
          auth_type: "api_key",
          provider: "anthropic",
          label: "Claude Code API key",
          status: "healthy",
          is_default: true,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        });
      }),
    );

    renderWithProviders(<AccountPage />);

    await user.click(screen.getAllByRole("button", { name: "Add auth" })[0]);
    await user.click(await screen.findByLabelText("Claude Code"));
    await user.click(screen.getByRole("radio", { name: /API key/i }));
    await user.type(screen.getByLabelText("Label"), "Personal Claude backup");
    // Use the input id directly — the visible label text is shared with the
    // help-tooltip button so getByLabelText("API key") would be ambiguous.
    await user.type(screen.getByPlaceholderText("sk-ant-..."), "sk-ant-test123");
    await user.click(screen.getByRole("button", { name: "Save auth" }));

    await waitFor(() => {
      expect(createBody).toEqual({
        scope: "personal",
        agent: "claude_code",
        auth_type: "api_key",
        label: "Personal Claude backup",
        api_key: "sk-ant-test123",
      });
    });
  });

  it("stores a default coding-agent reasoning preference", async () => {
    const user = userEvent.setup();
    let requestBody: Record<string, unknown> | null = null;
    server.use(
      ...emptyCodingCredentialsHandlers(),
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

  it("stores a default coding-agent model preference", async () => {
    const user = userEvent.setup();
    let requestBody: Record<string, unknown> | null = null;
    server.use(
      ...emptyCodingCredentialsHandlers(),
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
              coding_agent_model_default: "claude-opus-4-7",
            },
            created_at: "2026-01-01T00:00:00Z",
          },
        });
      }),
    );

    renderWithProviders(<AccountPage />);

    await user.click(await screen.findByRole("combobox", { name: "Default coding-agent model" }));
    await user.click(screen.getByRole("option", { name: "claude-opus-4-7" }));

    expect(requestBody).toEqual({
      coding_agent_model_default: "claude-opus-4-7",
    });
  });

  it("serializes reasoning-default saves so older responses cannot overwrite newer ones", async () => {
    const user = userEvent.setup();
    const requestBodies: Array<Record<string, unknown>> = [];
    let resolveFirstRequest: (() => void) | undefined;

    server.use(
      ...emptyCodingCredentialsHandlers(),
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
        // Merge-patch endpoint: the queued save carries only the agent that
        // changed while the first request was in flight.
        {
          coding_agent_reasoning_defaults: {
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
      ...emptyCodingCredentialsHandlers(),
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
        // Merge-patch endpoint: the queued save carries only the agent that
        // changed while the first request was in flight.
        {
          coding_agent_reasoning_defaults: {
            claude_code: "max",
          },
        },
      ]);
    });

    await waitFor(() => {
      expect(screen.getByRole("combobox", { name: "Codex default coding-agent reasoning" })).toHaveTextContent("High");
      expect(screen.getByRole("combobox", { name: "Claude Code default coding-agent reasoning" })).toHaveTextContent("Default");
    });
  });
});
