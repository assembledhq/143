import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import type { CodingCredentialSummary } from "@/lib/types";
import AgentPage, { reorderRows } from "./page";

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    user: { id: "user-1", org_id: "org-1", role: "admin", email: "admin@example.com", name: "Admin", created_at: "", role_display: "admin" },
  }),
}));

function installHandlers() {
  server.use(
    http.get("/api/v1/coding-credentials", ({ request }) => {
      const scope = new URL(request.url).searchParams.get("scope");
      if (scope !== "org") {
        return HttpResponse.json({ data: [], meta: { scope } });
      }
      return HttpResponse.json({
        data: [
          {
            id: "auth-1",
            org_id: "org-1",
            priority: 1,
            agent: "codex",
            auth_type: "subscription",
            label: "Team seat A",
            scope: "org",
            provider: "openai_subscription",
            status: "healthy",
            is_default: true,
            usage_note: "ChatGPT Plus",
            created_at: "2026-04-22T10:00:00Z",
            updated_at: "2026-04-22T10:00:00Z",
          },
        ],
        meta: {},
      });
    }),
    http.get("/api/v1/settings", () =>
      HttpResponse.json({
        data: {
          id: "org-1",
          name: "Acme",
          settings: {
            default_agent_type: "codex",
            max_concurrent_runs: 5,
            max_session_duration_seconds: 1500,
            agent_config: {},
          },
          created_at: "2026-04-22T10:00:00Z",
          updated_at: "2026-04-22T10:00:00Z",
        },
      }),
    ),
  );
}

describe("Agent settings page", () => {
  it("renders the stack helper text without shared runtime controls", async () => {
    installHandlers();

    renderWithProviders(<AgentPage />);

    expect(screen.getByText("Coding agents")).toBeInTheDocument();
    expect((await screen.findAllByText("Team seat A")).length).toBeGreaterThan(0);
    expect(screen.getByText("The stack runs from top to bottom. Move the auth you want to prefer higher in the list.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Runtime settings" })).toHaveAttribute("href", "/settings/runtime");
    expect(screen.queryByLabelText("Max concurrent sessions")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Session max time (minutes)")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Agent tab tools")).not.toBeInTheDocument();
  });

  it("keeps the details sheet closed after dismissing it", async () => {
    const user = userEvent.setup();
    installHandlers();

    renderWithProviders(<AgentPage />);

    await user.click((await screen.findAllByRole("button", { name: "Team seat A" }))[0]);
    expect(screen.getByText("Usage note")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Close" }));

    await waitFor(() => {
      expect(screen.queryByText("Usage note")).not.toBeInTheDocument();
    });
  });

  it("capitalizes status and usage note in the details sheet", async () => {
    const user = userEvent.setup();
    installHandlers();
    server.use(
      http.get("/api/v1/coding-credentials", ({ request }) => {
        const scope = new URL(request.url).searchParams.get("scope");
        if (scope !== "org") {
          return HttpResponse.json({ data: [], meta: { scope } });
        }
        return HttpResponse.json({
          data: [
            {
              id: "auth-1",
              org_id: "org-1",
              priority: 1,
              agent: "codex",
              auth_type: "subscription",
              label: "Team seat A",
              scope: "org",
              provider: "openai_subscription",
              status: "needs_reauth",
              is_default: true,
              usage_note: "chatgpt plus",
              created_at: "2026-04-22T10:00:00Z",
              updated_at: "2026-04-22T10:00:00Z",
            },
          ],
          meta: {},
        });
      }),
    );

    renderWithProviders(<AgentPage />);

    await user.click((await screen.findAllByRole("button", { name: "Team seat A" }))[0]);

    expect(screen.getByText("Needs Reauth")).toBeInTheDocument();
    expect(screen.getByText("Chatgpt Plus")).toBeInTheDocument();
  });

  it("shows expanded provider choices in the add auth modal", async () => {
    const user = userEvent.setup();
    installHandlers();

    renderWithProviders(<AgentPage />);

    await user.click(screen.getByRole("button", { name: "Add auth" }));

    expect(await screen.findByText("Gemini CLI")).toBeInTheDocument();
    expect(screen.getAllByText("Amp").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Pi").length).toBeGreaterThan(0);
    expect(screen.queryByText(/Leave blank and we'll generate a sensible name/)).not.toBeInTheDocument();
    expect(screen.queryByText(/OpenAI Codex with ChatGPT subscription/)).not.toBeInTheDocument();
    expect(screen.getByPlaceholderText("Optional — defaults to “Codex subscription”")).toBeInTheDocument();
  });

  it("hides auth type selection for API-key-only providers and shows Amp/Pi defaults", async () => {
    const user = userEvent.setup();
    installHandlers();

    renderWithProviders(<AgentPage />);

    await user.click(screen.getByRole("button", { name: "Add auth" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(screen.getByLabelText("Gemini CLI"));

    expect(within(dialog).queryByText("Auth type")).not.toBeInTheDocument();

    await user.click(screen.getByLabelText("Amp"));

    expect(within(dialog).queryByText("Auth type")).not.toBeInTheDocument();
    await user.click(screen.getByRole("combobox", { name: "Default mode" }));
    expect(await screen.findByRole("option", { name: "Smart" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Deep" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Large" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Rush" })).toBeInTheDocument();
    await user.keyboard("{Escape}");

	    await user.click(screen.getByLabelText("Pi"));
	    expect(within(dialog).queryByText("Auth type")).not.toBeInTheDocument();
	    expect(within(dialog).getByLabelText("Default model")).toBeInTheDocument();
	    expect(within(dialog).getByPlaceholderText("pi_...")).toBeInTheDocument();
	    expect(within(dialog).getByRole("button", { name: "Save auth" })).toBeDisabled();

	    await user.click(screen.getByLabelText("OpenCode"));
	    expect(within(dialog).queryByText("Auth type")).not.toBeInTheDocument();
	    expect(within(dialog).getByLabelText("OpenCode provider")).toBeInTheDocument();
	    expect(within(dialog).getByLabelText("Default model")).toBeInTheDocument();
	    expect(within(dialog).getByLabelText("Custom model override")).toBeInTheDocument();
	    expect(within(dialog).getByPlaceholderText("OpenCode or provider key")).toBeInTheDocument();
	  });

	  it("creates OpenCode auth with an explicit backing provider", async () => {
	    const user = userEvent.setup();
	    let capturedBody: Record<string, unknown> | null = null;

	    installHandlers();
	    server.use(
	      http.post("/api/v1/coding-credentials", async ({ request }) => {
	        capturedBody = await request.json() as Record<string, unknown>;
	        return HttpResponse.json({
	          id: "auth-opencode",
	          org_id: "org-1",
	          priority: 2,
	          agent: "opencode",
	          auth_type: "api_key",
	          label: "OpenCode API key",
	          scope: "org",
	          provider: "opencode",
	          status: "healthy",
	          is_default: false,
	          created_at: "2026-04-22T10:00:00Z",
	          updated_at: "2026-04-22T10:00:00Z",
	        });
	      }),
	    );

	    renderWithProviders(<AgentPage />);

	    await user.click(screen.getByRole("button", { name: "Add auth" }));
	    const dialog = await screen.findByRole("dialog");
	    await user.click(screen.getByLabelText("OpenCode"));
	    await user.click(within(dialog).getByRole("combobox", { name: "OpenCode provider" }));
	    await user.click(await screen.findByRole("option", { name: "OpenCode via OpenRouter" }));
	    await user.clear(within(dialog).getByLabelText("Custom model override"));
	    await user.type(within(dialog).getByLabelText("Custom model override"), "xai/grok-code-fast");
	    await user.type(within(dialog).getByPlaceholderText("OpenCode or provider key"), "sk-opencode-openrouter");
	    await user.click(within(dialog).getByRole("button", { name: "Save auth" }));

	    await waitFor(() => {
	      expect(capturedBody).toMatchObject({
	        agent: "opencode",
	        auth_type: "api_key",
	        api_key: "sk-opencode-openrouter",
	        api_type: "openrouter",
	        agent_defaults: {
	          OPENCODE_MODEL: "openai/gpt-5.4-mini",
	          OPENCODE_MODEL_CUSTOM: "xai/grok-code-fast",
	        },
	      });
	    });
	  });

	  it("creates Amp auth and defaults in a single coding-auth request", async () => {
    const user = userEvent.setup();
    let capturedBody: Record<string, unknown> | null = null;
    let settingsPatched = false;

    installHandlers();
    server.use(
      // The unified create endpoint returns the new row unwrapped.
      http.post("/api/v1/coding-credentials", async ({ request }) => {
        capturedBody = await request.json() as Record<string, unknown>;
        return HttpResponse.json({
          id: "auth-2",
          org_id: "org-1",
          priority: 2,
          agent: "amp",
          auth_type: "api_key",
          label: "Amp API key",
          scope: "org",
          provider: "amp",
          status: "healthy",
          is_default: false,
          created_at: "2026-04-22T10:00:00Z",
          updated_at: "2026-04-22T10:00:00Z",
        });
      }),
      http.patch("/api/v1/settings", () => {
        settingsPatched = true;
        return HttpResponse.json({
          data: {
            id: "org-1",
            name: "Acme",
            settings: {
              default_agent_type: "codex",
              max_concurrent_runs: 5,
              max_session_duration_seconds: 1500,
              agent_config: {},
            },
            created_at: "2026-04-22T10:00:00Z",
            updated_at: "2026-04-22T10:00:00Z",
          },
        });
      }),
    );

    renderWithProviders(<AgentPage />);

    await user.click(screen.getByRole("button", { name: "Add auth" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(screen.getByLabelText("Amp"));
    await user.type(within(dialog).getByPlaceholderText("amp_..."), "amp_123");
    await user.click(within(dialog).getByRole("combobox", { name: "Default mode" }));
    await user.click(await screen.findByRole("option", { name: "Deep" }));
    await user.click(within(dialog).getByRole("button", { name: "Save auth" }));

    await waitFor(() => {
      expect(capturedBody).not.toBeNull();
    });
    expect(capturedBody).toMatchObject({
      scope: "org",
      agent: "amp",
      auth_type: "api_key",
      api_key: "amp_123",
      agent_defaults: {
        AMP_MODE: "deep",
      },
    });
    expect(settingsPatched).toBe(false);
  });

  it("uses concise auth type helper text", async () => {
    const user = userEvent.setup();
    installHandlers();

    renderWithProviders(<AgentPage />);

    await user.click(screen.getByRole("button", { name: "Add auth" }));

    expect(await screen.findByText("Use this when a seat is already provisioned and you want managed sign-in.")).toBeInTheDocument();
    expect(screen.getByText("Use this for service accounts, rotation, and pay-as-you-go billing.")).toBeInTheDocument();
  });

  it("defaults to subscription when the modal opens for subscription-capable providers", async () => {
    const user = userEvent.setup();
    installHandlers();

    renderWithProviders(<AgentPage />);

    await user.click(screen.getByRole("button", { name: "Add auth" }));
    const dialog = await screen.findByRole("dialog");

    expect(await within(dialog).findByText("Auth type")).toBeInTheDocument();
    expect(within(dialog).getByRole("radio", { name: /Subscription/ })).toBeChecked();

    await user.click(within(dialog).getByRole("radio", { name: /API key/ }));
    expect(within(dialog).getByRole("radio", { name: /API key/ })).toBeChecked();

    await user.click(screen.getByLabelText("Amp"));
    await user.click(screen.getByLabelText("Claude Code"));
    expect(await within(dialog).findByText("Auth type")).toBeInTheDocument();
    expect(within(dialog).getByRole("radio", { name: /API key/ })).toBeChecked();
  });

  it("shows provider-specific API key help links", async () => {
    const user = userEvent.setup();
    installHandlers();

    renderWithProviders(<AgentPage />);

    await user.click(screen.getByRole("button", { name: "Add auth" }));
    const dialog = await screen.findByRole("dialog");

    await user.click(within(dialog).getByRole("radio", { name: /API key/ }));
    await user.hover(within(dialog).getByRole("button", { name: "Where to get a Codex API key" }));
    const codexLinks = await screen.findAllByRole("link", { name: "OpenAI API key management" });
    expect(codexLinks[0]).toHaveAttribute("href", "https://platform.openai.com/api-keys");

    await user.click(screen.getByLabelText("Claude Code"));
    await user.click(within(dialog).getByRole("radio", { name: /API key/ }));
    await user.hover(within(dialog).getByRole("button", { name: "Where to get a Claude Code API key" }));
    const claudeLinks = await screen.findAllByRole("link", { name: "Claude API key management" });
    expect(claudeLinks[0]).toHaveAttribute("href", "https://platform.claude.com/settings/keys");

    await user.click(screen.getByLabelText("Gemini CLI"));
    await user.hover(within(dialog).getByRole("button", { name: "Where to get a Gemini CLI API key" }));
    const geminiLinks = await screen.findAllByRole("link", { name: "Google AI Studio API keys" });
    expect(geminiLinks[0]).toHaveAttribute("href", "https://aistudio.google.com/apikey");

    await user.click(screen.getByLabelText("Amp"));
    await user.hover(within(dialog).getByRole("button", { name: "Where to get a Amp API key" }));
    const ampLinks = await screen.findAllByRole("link", { name: "Amp settings" });
    expect(ampLinks[0]).toHaveAttribute("href", "https://ampcode.com/settings");

    await user.click(screen.getByLabelText("Pi"));
    await user.hover(within(dialog).getByRole("button", { name: "Where to get a Pi API key" }));
    const piLinks = await screen.findAllByRole("link", { name: "Pi dashboard" });
    expect(piLinks[0]).toHaveAttribute("href", "https://pi.dev/");
  }, 10000);

  it("does not render the agent-specific access card", async () => {
    installHandlers();

    renderWithProviders(<AgentPage />);

    expect(screen.queryByText("Agent-specific access")).not.toBeInTheDocument();
  });

  it("shows a shared empty state when the org fallback stack has no auths", async () => {
    installHandlers();
    server.use(
      http.get("/api/v1/coding-credentials", () =>
        HttpResponse.json({
          data: [],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<AgentPage />);

    expect(await screen.findByText("No org coding auths yet")).toBeInTheDocument();
    expect(screen.getByText(/Add an org-level auth so coding-agent sessions/)).toBeInTheDocument();
  });
});

describe("reorderRows", () => {
  function makeRows(ids: string[]): CodingCredentialSummary[] {
    return ids.map((id, index) => ({
      id,
      org_id: "org-1",
      priority: index + 1,
      agent: "codex" as const,
      auth_type: "subscription" as const,
      label: id,
      scope: "org" as const,
      provider: "openai_subscription",
      status: "healthy" as const,
      is_default: index === 0,
      created_at: "2026-04-22T10:00:00Z",
      updated_at: "2026-04-22T10:00:00Z",
    }));
  }

  function ids(rows: CodingCredentialSummary[]) {
    return rows.map((row) => row.id);
  }

  it("returns the same array when source and target match", () => {
    const rows = makeRows(["a", "b", "c"]);
    expect(reorderRows(rows, "b", "b", "before")).toBe(rows);
  });

  it("drops 'before' an earlier target by inserting source above it", () => {
    const rows = makeRows(["a", "b", "c", "d"]);
    expect(ids(reorderRows(rows, "d", "b", "before"))).toEqual(["a", "d", "b", "c"]);
  });

  it("drops 'after' an earlier target by inserting source below it", () => {
    const rows = makeRows(["a", "b", "c", "d"]);
    expect(ids(reorderRows(rows, "d", "b", "after"))).toEqual(["a", "b", "d", "c"]);
  });

  it("drops 'before' a later target by inserting source above it", () => {
    const rows = makeRows(["a", "b", "c", "d"]);
    expect(ids(reorderRows(rows, "a", "c", "before"))).toEqual(["b", "a", "c", "d"]);
  });

  it("drops 'after' a later target by inserting source below it", () => {
    const rows = makeRows(["a", "b", "c", "d"]);
    expect(ids(reorderRows(rows, "a", "c", "after"))).toEqual(["b", "c", "a", "d"]);
  });

  it("returns the same array when the move would not change positions", () => {
    const rows = makeRows(["a", "b", "c"]);
    // Dropping 'b' "after" 'a' leaves the array in the same order.
    expect(reorderRows(rows, "b", "a", "after")).toBe(rows);
    // Dropping 'b' "before" 'c' likewise.
    expect(reorderRows(rows, "b", "c", "before")).toBe(rows);
  });

  it("returns the same array when source or target is missing", () => {
    const rows = makeRows(["a", "b"]);
    expect(reorderRows(rows, "missing", "a", "before")).toBe(rows);
    expect(reorderRows(rows, "a", "missing", "before")).toBe(rows);
  });
});
