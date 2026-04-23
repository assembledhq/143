import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import AgentPage from "./page";

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    user: { id: "user-1", org_id: "org-1", role: "admin", email: "admin@example.com", name: "Admin", created_at: "", role_display: "admin" },
  }),
}));

function installHandlers() {
  server.use(
    http.get("/api/v1/settings/coding-auths", () =>
      HttpResponse.json({
        data: [
          {
            id: "auth-1",
            org_id: "org-1",
            priority: 1,
            agent: "codex",
            auth_type: "subscription",
            label: "Team seat A",
            scope: "organization",
            provider: "openai_chatgpt",
            status: "healthy",
            is_default: true,
            usage_note: "ChatGPT Plus",
            created_at: "2026-04-22T10:00:00Z",
            updated_at: "2026-04-22T10:00:00Z",
          },
        ],
        meta: {},
      }),
    ),
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
  it("renders the stack helper text and restored execution settings", async () => {
    installHandlers();

    renderWithProviders(<AgentPage />);

    expect(screen.getByText("Coding agents")).toBeInTheDocument();
    expect(await screen.findByText("Team seat A")).toBeInTheDocument();
    expect(screen.getByText("The stack runs from top to bottom. Move the auth you want to prefer higher in the list.")).toBeInTheDocument();
    expect(screen.getByLabelText("Max concurrent sessions")).toHaveValue(5);
    expect(screen.getByLabelText("Session max time (minutes)")).toHaveValue(25);
  });

  it("keeps the details sheet closed after dismissing it", async () => {
    const user = userEvent.setup();
    installHandlers();

    renderWithProviders(<AgentPage />);

    await user.click(await screen.findByRole("button", { name: "Team seat A" }));
    expect(screen.getByText("Usage note")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Close" }));

    await waitFor(() => {
      expect(screen.queryByText("Usage note")).not.toBeInTheDocument();
    });
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

  it("hides auth type selection for API-key-only providers and capitalizes Amp modes", async () => {
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
  });

  it("does not render the agent-specific access card", async () => {
    installHandlers();

    renderWithProviders(<AgentPage />);

    expect(screen.queryByText("Agent-specific access")).not.toBeInTheDocument();
  });
});
