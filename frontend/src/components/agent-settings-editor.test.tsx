import { describe, it, expect, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { AgentSettingsEditor } from "./agent-settings-editor";

vi.mock("next/navigation", () => ({
  usePathname: () => "/settings",
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
}));

describe("AgentSettingsEditor", () => {
  it("renders title and description", async () => {
    renderWithProviders(
      <AgentSettingsEditor
        title="Agent settings"
        description="Configure your coding agent"
      />
    );

    expect(screen.getByText("Agent settings")).toBeInTheDocument();
    expect(screen.getByText("Configure your coding agent")).toBeInTheDocument();
  });

  it("renders agent type radio buttons", async () => {
    renderWithProviders(
      <AgentSettingsEditor
        title="Agent settings"
        description="Configure your coding agent"
      />
    );

    expect(screen.getByText("Codex")).toBeInTheDocument();
    expect(screen.getByText("Claude Code")).toBeInTheDocument();
    expect(screen.getByText("Gemini CLI")).toBeInTheDocument();
  });

  it("shows Claude Code API fields when claude_code is the default agent", async () => {
    renderWithProviders(
      <AgentSettingsEditor
        title="Agent settings"
        description="Configure"
        initialAgentType="claude_code"
      />
    );

    await waitFor(() => {
      expect(screen.getByLabelText("API Key")).toBeInTheDocument();
    });
    expect(screen.getByLabelText("Default model")).toBeInTheDocument();
  });

  it("shows Codex credential method selector when codex is selected", async () => {
    renderWithProviders(
      <AgentSettingsEditor
        title="Agent settings"
        description="Configure"
        initialAgentType="codex"
      />
    );

    await waitFor(() => {
      expect(screen.getByText("Credential method")).toBeInTheDocument();
    });
    expect(screen.getAllByText("Sign in with ChatGPT").length).toBeGreaterThan(0);
    expect(screen.getByText("Use API key")).toBeInTheDocument();
  });

  it("shows API key fields when Codex api_key method is selected", async () => {
    const user = userEvent.setup();
    renderWithProviders(
      <AgentSettingsEditor
        title="Agent settings"
        description="Configure"
        initialAgentType="codex"
      />
    );

    await waitFor(() => {
      expect(screen.getByText("Use API key")).toBeInTheDocument();
    });

    // Click the "Use API key" radio
    await user.click(screen.getByLabelText("Use API key"));

    await waitFor(() => {
      expect(screen.getByLabelText("API Key")).toBeInTheDocument();
    });
  });

  it("hides API fields when Codex chatgpt method is selected", async () => {
    const user = userEvent.setup();
    renderWithProviders(
      <AgentSettingsEditor
        title="Agent settings"
        description="Configure"
        initialAgentType="codex"
      />
    );

    await waitFor(() => {
      expect(screen.getByText("Credential method")).toBeInTheDocument();
    });

    // Click Sign in with ChatGPT
    await user.click(screen.getByLabelText("Sign in with ChatGPT"));

    await waitFor(() => {
      expect(
        screen.getByText("API key fields are hidden while ChatGPT sign-in is selected.")
      ).toBeInTheDocument();
    });
  });

  it("shows save button and cancel when onClose is provided", () => {
    const onClose = vi.fn();
    renderWithProviders(
      <AgentSettingsEditor
        title="Agent settings"
        description="Configure"
        onClose={onClose}
      />
    );

    expect(screen.getByRole("button", { name: "Save changes" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeInTheDocument();
  });

  it("calls onClose when Cancel is clicked", async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(
      <AgentSettingsEditor
        title="Agent settings"
        description="Configure"
        onClose={onClose}
      />
    );

    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("calls save mutation when Save changes is clicked", async () => {
    const user = userEvent.setup();
    let savedPayload: unknown = null;
    server.use(
      http.patch("/api/v1/settings", async ({ request }) => {
        savedPayload = await request.json();
        return HttpResponse.json({
          data: { id: "org-1", name: "Test Org", settings: {} },
        });
      })
    );

    renderWithProviders(
      <AgentSettingsEditor
        title="Agent settings"
        description="Configure"
        initialAgentType="claude_code"
      />
    );

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Save changes" })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(savedPayload).toBeTruthy();
    });
  });

  it("shows success message after save", async () => {
    const user = userEvent.setup();
    renderWithProviders(
      <AgentSettingsEditor
        title="Agent settings"
        description="Configure"
        initialAgentType="claude_code"
      />
    );

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Save changes" })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(screen.getByText("Saved.")).toBeInTheDocument();
    });
  });

  it("shows error message when save fails", async () => {
    const user = userEvent.setup();
    server.use(
      http.patch("/api/v1/settings", () => {
        return HttpResponse.json(
          { error: { code: "INTERNAL", message: "fail" } },
          { status: 500 }
        );
      })
    );

    renderWithProviders(
      <AgentSettingsEditor
        title="Agent settings"
        description="Configure"
        initialAgentType="claude_code"
      />
    );

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Save changes" })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(screen.getByText("Save failed.")).toBeInTheDocument();
    });
  });

  it("shows Gemini CLI fields when gemini_cli is selected", async () => {
    renderWithProviders(
      <AgentSettingsEditor
        title="Agent settings"
        description="Configure"
        initialAgentType="gemini_cli"
      />
    );

    await waitFor(() => {
      expect(screen.getByLabelText("API Key")).toBeInTheDocument();
    });
    expect(screen.getByLabelText("Default model")).toBeInTheDocument();
  });

  it("renders in setup mode without advanced settings", () => {
    renderWithProviders(
      <AgentSettingsEditor
        title="Quick setup"
        description="Set up now"
        initialAgentType="claude_code"
        setupMode
      />
    );

    expect(screen.getByText("Quick setup")).toBeInTheDocument();
    // Advanced settings button should not be visible in setup mode
    expect(screen.queryByText("Show advanced settings")).not.toBeInTheDocument();
  });

  it("shows server default badge when org value matches server default", async () => {
    server.use(
      http.get("/api/v1/settings/agent-defaults", () => {
        return HttpResponse.json({
          data: {
            claude_code: { ANTHROPIC_API_KEY: "sk-server-default" },
          },
        });
      })
    );

    renderWithProviders(
      <AgentSettingsEditor
        title="Agent settings"
        description="Configure"
        initialAgentType="claude_code"
      />
    );

    await waitFor(() => {
      expect(screen.getByText("server default")).toBeInTheDocument();
    });
  });

  it("shows Sign in with ChatGPT button when codex chatgpt method and not connected", async () => {
    server.use(
      http.get("/api/v1/settings/codex-auth/status", () => {
        return HttpResponse.json({ data: { status: "none" } });
      })
    );

    renderWithProviders(
      <AgentSettingsEditor
        title="Agent settings"
        description="Configure"
        initialAgentType="codex"
      />
    );

    await waitFor(() => {
      expect(screen.getAllByText("Sign in with ChatGPT").length).toBeGreaterThan(0);
    });
  });

  it("shows Connected badge when codex auth status is completed", async () => {
    server.use(
      http.get("/api/v1/settings/codex-auth/status", () => {
        return HttpResponse.json({ data: { status: "completed" } });
      })
    );

    renderWithProviders(
      <AgentSettingsEditor
        title="Agent settings"
        description="Configure"
        initialAgentType="codex"
      />
    );

    await waitFor(() => {
      expect(screen.getByText("Connected")).toBeInTheDocument();
    });
    expect(screen.getByRole("button", { name: "Disconnect" })).toBeInTheDocument();
  });
});
