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
  sessionComposerSlashCommandsMock: vi.fn().mockResolvedValue({
    groups: [
      {
        source: "builtin",
        label: "Codex commands",
        items: [
          {
            kind: "command",
            agent_type: "codex",
            name: "review",
            token: "/review",
            display: "/review",
            description: "Review pending changes",
            source: "builtin",
          },
        ],
      },
    ],
  }),
  uploadMock: vi.fn().mockResolvedValue({
    url: "https://example.com/uploaded-shot.png",
    file_name: "uploaded-shot.png",
    content_type: "image/png",
  }),
  resolvedCredsMock: vi.fn().mockResolvedValue({
    data: [
      { provider: "openai", source: "personal" },
      { provider: "anthropic", source: "personal" },
      { provider: "gemini", source: "personal" },
      { provider: "amp", source: "personal" },
      { provider: "pi", source: "personal" },
    ],
  }),
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
  searchParamGetMock: vi.fn<(key: string) => string | null>().mockImplementation(() => null),
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
      slashCommands: mocks.sessionComposerSlashCommandsMock,
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
    get: mocks.searchParamGetMock,
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
    mocks.searchParamGetMock.mockImplementation(() => null);
    window.sessionStorage.clear();
  });

  it("renders the session creation form", async () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    await waitFor(() => {
      expect(mocks.repositoriesListMock).toHaveBeenCalled();
    });

    expect(
      screen.getByText("Start a manual session with text, files, photos, dictation, or a screenshot anywhere here."),
    ).toBeInTheDocument();
    expect(screen.queryByText("Drop a screenshot anywhere here, or use +")).not.toBeInTheDocument();
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

  it("keeps the main message textarea at 16px on mobile", async () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    const textarea = await screen.findByRole("textbox", { name: "Manual session prompt" });
    expect(textarea).toHaveClass("text-base");
    expect(textarea).toHaveClass("sm:text-xs");
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

  it("clears the dropzone after nested drag-enter events once the drag leaves the hero", async () => {
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
    fireEvent.dragEnter(addButton, {
      dataTransfer: {
        files: [file],
        items: [{ kind: "file", type: "image/png", getAsFile: () => file }],
        types: ["Files"],
      },
    });

    expect(dropzone).toHaveAttribute("data-drag-active", "true");

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

  it("shows slash command suggestions when the user types a slash trigger", async () => {
    const user = userEvent.setup();
    renderWithProviders(<ManualSessionCreatePageContent />);

    const textarea = await screen.findByPlaceholderText("Tell the agent what to do...");
    await user.type(textarea, "/rev");

    await waitFor(() => {
      expect(mocks.sessionComposerSlashCommandsMock).toHaveBeenCalled();
    });
    expect(await screen.findByText("/review")).toBeInTheDocument();
    expect(screen.getByText("Review pending changes")).toBeInTheDocument();
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
          __v: 2,
          message: "Previously typed prompt",
          attachments: [],
          references: [],
          commands: [],
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
          __v: 2,
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
      await waitFor(() => {
        expect(mocks.settingsGetMock).toHaveBeenCalledTimes(1);
        expect(mocks.repositoriesListMock).toHaveBeenCalledTimes(1);
        expect(mocks.resolvedCredsMock).toHaveBeenCalledTimes(1);
        expect(mocks.codexAuthStatusMock).toHaveBeenCalledTimes(1);
        expect(mocks.authMeMock).toHaveBeenCalledTimes(1);
        expect(window.sessionStorage.getItem(DRAFT_STORAGE_KEY)).toBeNull();
      });
    });

    it("restores a hydrated reasoning override and uses it at submit time", async () => {
      window.sessionStorage.setItem(
        DRAFT_STORAGE_KEY,
        JSON.stringify({
          __v: 2,
          message: "tune this",
          attachments: [],
          references: [],
          commands: [],
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
          __v: 2,
          message: "still typing",
          attachments: [],
          references: [],
          commands: [],
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

    it("drops project commands when a repo query param conflicts with the saved draft", async () => {
      mocks.searchParamGetMock.mockImplementation((key: string) => (key === "repo" ? "repo-2" : null));
      mocks.repositoriesListMock.mockResolvedValueOnce({
        data: [
          {
            id: "repo-1",
            name: "repo-one",
            full_name: "org/repo-one",
            default_branch: "main",
            integration_id: "int-1",
          },
          {
            id: "repo-2",
            name: "repo-two",
            full_name: "org/repo-two",
            default_branch: "main",
            integration_id: "int-2",
          },
        ],
      });
      window.sessionStorage.setItem(
        DRAFT_STORAGE_KEY,
        JSON.stringify({
          __v: 2,
          message: "/repo-review\n\ncheck this repo",
          attachments: [],
          references: [],
          commands: [
            {
              kind: "command",
              agent_type: "codex",
              name: "repo-review",
              token: "/repo-review",
              display: "/repo-review",
              source: "project",
            },
            {
              kind: "command",
              agent_type: "codex",
              name: "diff",
              token: "/diff",
              display: "/diff",
              source: "builtin",
            },
          ],
          selectedModel: "",
          userSelectedRepoId: "repo-1",
          branchByRepoId: { "repo-1": "main", "repo-2": "main" },
          showImageInput: false,
          imageURL: "",
        }),
      );

      renderWithProviders(<ManualSessionCreatePageContent />);

      const textarea = await screen.findByPlaceholderText<HTMLTextAreaElement>("Tell the agent what to do...");
      await waitFor(() => {
        expect(textarea.value).toBe("check this repo");
      });
      expect(screen.queryByText("/repo-review")).not.toBeInTheDocument();
      expect(await screen.findByText("/diff")).toBeInTheDocument();

      await waitFor(() => {
        const raw = window.sessionStorage.getItem(DRAFT_STORAGE_KEY);
        expect(raw).not.toBeNull();
        const stored = JSON.parse(raw!);
        expect(stored.message).toBe("check this repo");
        expect(stored.commands).toEqual([
          expect.objectContaining({
            token: "/diff",
            source: "builtin",
          }),
        ]);
      });
    });
  });
});
