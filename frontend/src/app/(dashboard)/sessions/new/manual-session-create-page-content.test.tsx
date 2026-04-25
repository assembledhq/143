import React from "react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { ManualSessionCreatePageContent } from "./manual-session-create-page-content";

const DRAFT_STORAGE_KEY = "143:new-session-draft";

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
  uploadMock: vi.fn().mockResolvedValue({
    url: "https://example.com/uploaded-shot.png",
    file_name: "uploaded-shot.png",
    content_type: "image/png",
  }),
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
    uploads: {
      upload: mocks.uploadMock,
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
    window.sessionStorage.clear();
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

  it("activates the full hero and composer region as a drop target", async () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    const dropzone = await screen.findByTestId("manual-session-dropzone");
    expect(dropzone).not.toHaveAttribute("data-drag-active", "true");

    const file = new File(["image-bytes"], "design-shot.png", { type: "image/png" });
    fireEvent.dragEnter(dropzone, {
      dataTransfer: {
        files: [file],
        items: [{ kind: "file", type: "image/png", getAsFile: () => file }],
        types: ["Files"],
      },
    });

    expect(dropzone).toHaveAttribute("data-drag-active", "true");
    expect(screen.getAllByText(/Drop image[s]? to attach/).length).toBeGreaterThan(0);

    fireEvent.dragLeave(dropzone, {
      dataTransfer: {
        files: [file],
        items: [{ kind: "file", type: "image/png", getAsFile: () => file }],
        types: ["Files"],
      },
    });

    await waitFor(() => {
      expect(dropzone).toHaveAttribute("data-drag-active", "false");
    });
  });

  it("keeps the dropzone active when the drag moves across internal controls", async () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    const dropzone = await screen.findByTestId("manual-session-dropzone");
    const addButton = screen.getByRole("button", { name: "Add files or photos" });
    const file = new File(["image-bytes"], "design-shot.png", { type: "image/png" });

    fireEvent.dragEnter(dropzone, {
      dataTransfer: {
        files: [file],
        items: [{ kind: "file", type: "image/png", getAsFile: () => file }],
        types: ["Files"],
      },
    });

    expect(dropzone).toHaveAttribute("data-drag-active", "true");

    fireEvent.dragLeave(addButton, {
      relatedTarget: addButton,
      dataTransfer: {
        files: [file],
        items: [{ kind: "file", type: "image/png", getAsFile: () => file }],
        types: ["Files"],
      },
    });

    expect(dropzone).toHaveAttribute("data-drag-active", "true");
  });

  it("uploads an image dropped onto the hero area and shows it in the attachment strip", async () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    const dropzone = await screen.findByTestId("manual-session-dropzone");
    const file = new File(["image-bytes"], "hero-shot.png", { type: "image/png" });

    fireEvent.drop(dropzone, {
      dataTransfer: {
        files: [file],
        items: [{ kind: "file", type: "image/png", getAsFile: () => file }],
        types: ["Files"],
      },
    });

    await waitFor(() => {
      expect(mocks.uploadMock).toHaveBeenCalledWith(file);
    });
    expect(await screen.findByRole("button", { name: "Preview uploaded-shot.png" })).toBeInTheDocument();
  });

  it("returns focus to the prompt after a dropped image finishes uploading", async () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    const dropzone = await screen.findByTestId("manual-session-dropzone");
    const textarea = screen.getByPlaceholderText("Tell the agent what to do...");
    const file = new File(["image-bytes"], "focus-shot.png", { type: "image/png" });

    fireEvent.drop(dropzone, {
      dataTransfer: {
        files: [file],
        items: [{ kind: "file", type: "image/png", getAsFile: () => file }],
        types: ["Files"],
      },
    });

    await waitFor(() => {
      expect(mocks.uploadMock).toHaveBeenCalledWith(file);
    });
    await waitFor(() => {
      expect(textarea).toHaveFocus();
    });
  });

  it("shows an inline validation error when a dropped file exceeds the size limit", async () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    const oversizedFile = new File([new Uint8Array(10 * 1024 * 1024 + 1)], "too-large.png", { type: "image/png" });
    const dropzone = await screen.findByTestId("manual-session-dropzone");

    fireEvent.drop(dropzone, {
      dataTransfer: {
        files: [oversizedFile],
        items: [{ kind: "file", type: "image/png", getAsFile: () => oversizedFile }],
        types: ["Files"],
      },
    });

    await waitFor(() => {
      expect(screen.getByText("File too large (max 10 MB): too-large.png")).toBeInTheDocument();
    });
    expect(mocks.uploadMock).not.toHaveBeenCalled();
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

  describe("draft persistence", () => {
    it("restores a stored prompt on mount", async () => {
      window.sessionStorage.setItem(
        DRAFT_STORAGE_KEY,
        JSON.stringify({
          __v: 1,
          message: "Previously typed prompt",
          attachments: [],
          references: [],
          selectedModel: "",
          userSelectedRepoId: null,
          branchByRepoId: {},
          showImageInput: false,
          imageURL: "",
        }),
      );

      renderWithProviders(<ManualSessionCreatePageContent />);

      const textarea = await screen.findByPlaceholderText<HTMLTextAreaElement>(
        "Tell the agent what to do...",
      );
      await waitFor(() => {
        expect(textarea.value).toBe("Previously typed prompt");
      });
    });

    it("writes the prompt to sessionStorage as the user types", async () => {
      renderWithProviders(<ManualSessionCreatePageContent />);

      const textarea = await screen.findByPlaceholderText<HTMLTextAreaElement>(
        "Tell the agent what to do...",
      );
      fireEvent.change(textarea, { target: { value: "draft in progress" } });

      await waitFor(() => {
        const stored = window.sessionStorage.getItem(DRAFT_STORAGE_KEY);
        expect(stored).not.toBeNull();
        expect(JSON.parse(stored!)).toMatchObject({
          __v: 1,
          message: "draft in progress",
        });
      });
    });

    it("clears the stored draft on successful submit", async () => {
      renderWithProviders(<ManualSessionCreatePageContent />);

      const textarea = await screen.findByPlaceholderText<HTMLTextAreaElement>(
        "Tell the agent what to do...",
      );
      fireEvent.change(textarea, { target: { value: "ship it" } });

      await waitFor(() => {
        expect(window.sessionStorage.getItem(DRAFT_STORAGE_KEY)).not.toBeNull();
      });

      fireEvent.keyDown(textarea, { key: "Enter" });

      await waitFor(() => {
        expect(mocks.createSessionMock).toHaveBeenCalled();
      });
      await waitFor(() => {
        expect(window.sessionStorage.getItem(DRAFT_STORAGE_KEY)).toBeNull();
      });
    });

    it("does not persist an empty draft", async () => {
      renderWithProviders(<ManualSessionCreatePageContent />);

      await screen.findByPlaceholderText("Tell the agent what to do...");
      // Give the persist effect a chance to run.
      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(window.sessionStorage.getItem(DRAFT_STORAGE_KEY)).toBeNull();
    });

    it("restores a hydrated reasoning override and uses it at submit time", async () => {
      window.sessionStorage.setItem(
        DRAFT_STORAGE_KEY,
        JSON.stringify({
          __v: 1,
          message: "tune this",
          attachments: [],
          references: [],
          selectedModel: "",
          reasoningOverride: "high",
          userSelectedRepoId: null,
          branchByRepoId: {},
          showImageInput: false,
          imageURL: "",
        }),
      );

      const user = userEvent.setup();
      renderWithProviders(<ManualSessionCreatePageContent />);

      const textarea = await screen.findByPlaceholderText<HTMLTextAreaElement>(
        "Tell the agent what to do...",
      );
      await waitFor(() => {
        expect(textarea.value).toBe("tune this");
      });

      await user.click((await screen.findAllByRole("button", { name: "Start session" }))[0]);

      await waitFor(() => {
        expect(mocks.createSessionMock).toHaveBeenCalledWith(
          expect.objectContaining({
            message: "tune this",
            reasoning_effort: "high",
          }),
        );
      });
    });

    it("clears a hydrated repo id that no longer exists once repos load", async () => {
      window.sessionStorage.setItem(
        DRAFT_STORAGE_KEY,
        JSON.stringify({
          __v: 1,
          message: "still typing",
          attachments: [],
          references: [],
          selectedModel: "",
          // Not present in repositoriesListMock, which only returns repo-1.
          userSelectedRepoId: "repo-deleted",
          branchByRepoId: { "repo-deleted": "feature-branch" },
          showImageInput: false,
          imageURL: "",
        }),
      );

      renderWithProviders(<ManualSessionCreatePageContent />);

      await screen.findByPlaceholderText("Tell the agent what to do...");
      await waitFor(() => {
        expect(mocks.repositoriesListMock).toHaveBeenCalled();
      });

      // The draft should be re-saved with the stale repo id cleared so it
      // doesn't haunt future mounts. Message content survives.
      await waitFor(() => {
        const raw = window.sessionStorage.getItem(DRAFT_STORAGE_KEY);
        expect(raw).not.toBeNull();
        const stored = JSON.parse(raw!);
        expect(stored.userSelectedRepoId).toBeNull();
        expect(stored.message).toBe("still typing");
      });
    });
  });
});
