import React from "react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { ManualSessionCreatePageContent } from "./manual-session-create-page-content";

const mocks = vi.hoisted(() => ({
  settingsGetMock: vi.fn().mockResolvedValue({
    data: {
      name: "Test Org",
      settings: {
        default_agent_type: "codex",
        default_llm_model: "gpt-5.4-mini",
      },
    },
  }),
  repositoriesListMock: vi.fn().mockResolvedValue({
    data: [
      {
        id: "repo-1",
        name: "test-repo",
        full_name: "org/test-repo",
        default_branch: "main",
        integration_id: "int-1",
      },
    ],
  }),
  branchesMock: vi.fn().mockResolvedValue({
    data: [{ name: "main", protected: true }],
  }),
  llmModelsMock: vi.fn().mockResolvedValue({
    data: { openai: ["gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano"], anthropic: ["claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5"] },
  }),
  createSessionMock: vi.fn().mockResolvedValue({
    data: { id: "new-sess" },
  }),
  sessionComposerFilesMock: vi.fn().mockResolvedValue({ data: [] }),
  resolvedCredsMock: vi.fn().mockResolvedValue({ data: [] }),
  codexAuthStatusMock: vi.fn().mockResolvedValue({ data: { status: "completed" } }),
  authMeMock: vi.fn().mockResolvedValue({
    data: {
      id: "user-1",
      org_id: "org-1",
      email: "alice@example.com",
      name: "Alice Smith",
      role: "admin",
      settings: {},
      created_at: "2026-01-01T00:00:00Z",
    },
  }),
}));

vi.mock("@/lib/api", () => ({
  api: {
    settings: {
      get: mocks.settingsGetMock,
      getLLMModels: mocks.llmModelsMock,
    },
    repositories: {
      list: mocks.repositoriesListMock,
      branches: mocks.branchesMock,
    },
    sessionComposer: {
      files: mocks.sessionComposerFilesMock,
    },
    userCredentials: {
      listResolved: mocks.resolvedCredsMock,
    },
    codexAuth: {
      status: mocks.codexAuthStatusMock,
    },
    auth: {
      me: mocks.authMeMock,
    },
    sessions: {
      createManual: mocks.createSessionMock,
    },
  },
}));

vi.mock("@/lib/errors", () => ({
  captureError: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
    prefetch: vi.fn(),
  }),
  useSearchParams: () => ({
    get: () => null,
  }),
}));

vi.mock("@/components/no-repos-warning", () => ({
  NoReposWarning: () => <div data-testid="no-repos-warning" />,
}));

vi.mock("@/contexts/optimistic-sessions", () => ({
  useOptimisticSessions: () => ({
    addOptimisticSession: vi.fn(),
    removeOptimisticSession: vi.fn(),
    markOptimisticResolved: vi.fn(),
  }),
  OptimisticSessionsProvider: ({ children }: { children: React.ReactNode }) => children,
}));

describe("ManualSessionCreatePageContent", () => {
  beforeEach(() => {
    Object.values(mocks).forEach((m) => m.mockClear());
  });

  it("renders the session creation form", async () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    await waitFor(() => {
      expect(mocks.repositoriesListMock).toHaveBeenCalled();
    });
  });

  it("shows repository selection", async () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    await waitFor(() => {
      expect(mocks.settingsGetMock).toHaveBeenCalled();
    });
  });

  it("renders the message input area", async () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    await waitFor(() => {
      // Should have a textarea for the message
      const textareas = screen.getAllByRole("textbox");
      expect(textareas.length).toBeGreaterThanOrEqual(1);
    });
  });

  it("autofocuses the main message textarea", async () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    const textarea = await screen.findByPlaceholderText("Tell the agent what to do...");
    await waitFor(() => {
      expect(textarea).toHaveFocus();
    });
  });

  it("submits the saved default reasoning effort with a new session", async () => {
    const user = userEvent.setup();
    mocks.authMeMock.mockResolvedValueOnce({
      data: {
        id: "user-1",
        org_id: "org-1",
        email: "alice@example.com",
        name: "Alice Smith",
        role: "admin",
        settings: {
          coding_agent_reasoning_defaults: {
            codex: "xhigh",
          },
        },
        created_at: "2026-01-01T00:00:00Z",
      },
    });
    renderWithProviders(<ManualSessionCreatePageContent />);

    const textarea = await screen.findByPlaceholderText("Tell the agent what to do...");
    await user.type(textarea, "Fix the login bug");
    await user.click((await screen.findAllByRole("button", { name: "Start session" }))[0]);

    await waitFor(() => {
        expect(mocks.createSessionMock).toHaveBeenCalledWith(
        expect.objectContaining({
          message: "Fix the login bug",
          reasoning_effort: "xhigh",
        }),
      );
    });
  });

  it("does not submit a hidden default reasoning effort for unsupported agents", async () => {
    const user = userEvent.setup();
    mocks.authMeMock.mockResolvedValueOnce({
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
    });
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(await screen.findByRole("combobox", { name: /Model/i }));
    await user.click(screen.getByRole("option", { name: "gemini-2.5-pro" }));

    const textarea = await screen.findByPlaceholderText("Tell the agent what to do...");
    await user.type(textarea, "Fix the login bug");
    await user.click((await screen.findAllByRole("button", { name: "Start session" }))[0]);

    await waitFor(() => {
      expect(mocks.createSessionMock).toHaveBeenCalled();
    });

    const requestBody = mocks.createSessionMock.mock.calls.at(-1)?.[0];
    expect(requestBody).toMatchObject({
      message: "Fix the login bug",
      model: "gemini-2.5-pro",
      agent_type: "gemini_cli",
    });
    expect(requestBody).not.toHaveProperty("reasoning_effort");
  });

  it("uses the Claude Code-specific default reasoning effort", async () => {
    const user = userEvent.setup();
    mocks.authMeMock.mockResolvedValueOnce({
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
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(await screen.findByRole("combobox", { name: /Model/i }));
    await user.click(screen.getByRole("option", { name: "claude-sonnet-4-6" }));

    const textarea = await screen.findByPlaceholderText("Tell the agent what to do...");
    await user.type(textarea, "Fix the login bug");
    await user.click((await screen.findAllByRole("button", { name: "Start session" }))[0]);

    await waitFor(() => {
      expect(mocks.createSessionMock).toHaveBeenCalledWith(
        expect.objectContaining({
          message: "Fix the login bug",
          model: "claude-sonnet-4-6",
          agent_type: "claude_code",
          reasoning_effort: "max",
        }),
      );
    });
  });
});
