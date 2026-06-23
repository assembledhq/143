import React from "react";
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { act } from "@testing-library/react";
import { fireEvent, renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { ManualSessionCreatePageContent } from "./manual-session-create-page-content";

const DRAFT_STORAGE_KEY = "143:new-session-draft";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

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
  routerPushMock: vi.fn(),
  routerReplaceMock: vi.fn(),
  addOptimisticSessionMock: vi.fn().mockReturnValue("optimistic-1"),
  removeOptimisticSessionMock: vi.fn(),
  markOptimisticResolvedMock: vi.fn(),
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
  // Default resolved stack: a healthy personal credential for every agent so
  // the model picker shows every group unless a test narrows it.
  codingCredentialsListMock: vi.fn().mockResolvedValue({
    data: [
      { agent: "codex", provider: "openai" },
      { agent: "claude_code", provider: "anthropic" },
      { agent: "opencode", provider: "opencode" },
      { agent: "amp", provider: "amp" },
      { agent: "pi", provider: "pi" },
    ].map((row, index) => ({
      id: `cc-${row.agent}`,
      org_id: "org-1",
      user_id: "user-1",
      scope: "personal",
      priority: index + 1,
      agent: row.agent,
      auth_type: "api_key",
      provider: row.provider,
      label: `${row.agent} API key`,
      status: "healthy",
      is_default: index === 0,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    })),
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
  integrationsListMock: vi.fn().mockResolvedValue({
    data: [
      {
        id: "integration-linear",
        provider: "linear",
        status: "active",
      },
    ],
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
    codingCredentials: {
      list: mocks.codingCredentialsListMock,
    },
    codexAuth: {
      status: mocks.codexAuthStatusMock,
    },
    auth: {
      me: mocks.authMeMock,
    },
    integrations: {
      list: mocks.integrationsListMock,
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
    push: mocks.routerPushMock,
    replace: mocks.routerReplaceMock,
    prefetch: vi.fn(),
  }),
  useSearchParams: () => ({
    get: mocks.searchParamGetMock,
  }),
}));

vi.mock("@/components/ui/dropdown-menu", () => ({
  DropdownMenu: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  DropdownMenuTrigger: ({ children }: { children: React.ReactNode; asChild?: boolean }) => <>{children}</>,
  DropdownMenuContent: ({ children }: { children: React.ReactNode }) => <div role="menu">{children}</div>,
  DropdownMenuItem: ({
    children,
    className,
    onClick,
  }: {
    children: React.ReactNode;
    className?: string;
    onClick?: () => void;
  }) => (
    <button type="button" role="menuitem" className={className} onClick={onClick}>
      {children}
    </button>
  ),
}));

vi.mock("@/components/no-repos-warning", () => ({
  NoReposWarning: () => <div data-testid="no-repos-warning" />,
}));

vi.mock("@/contexts/optimistic-sessions", () => ({
  useOptimisticSessions: () => ({
    addOptimisticSession: mocks.addOptimisticSessionMock,
    removeOptimisticSession: mocks.removeOptimisticSessionMock,
    markOptimisticResolved: mocks.markOptimisticResolvedMock,
  }),
  useOptimisticSessionsSafe: () => ({
    addOptimisticSession: mocks.addOptimisticSessionMock,
    removeOptimisticSession: mocks.removeOptimisticSessionMock,
    markOptimisticResolved: mocks.markOptimisticResolvedMock,
  }),
  OptimisticSessionsProvider: ({ children }: { children: React.ReactNode }) => children,
}));

function setMobileViewport(matches: boolean) {
  Object.defineProperty(window, "matchMedia", {
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: query === "(max-width: 767px)" ? matches : false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  });
}

describe("ManualSessionCreatePageContent", () => {
  beforeEach(() => {
    Object.values(mocks).forEach((value) => {
      if (typeof value === "function" && "mockClear" in value) {
        value.mockClear();
      }
    });
    mocks.searchParamGetMock.mockImplementation(() => null);
    window.sessionStorage.clear();
    setMobileViewport(false);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders the session creation form", async () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    await waitFor(() => {
      expect(mocks.repositoriesListMock).toHaveBeenCalled();
    });

    expect(
      screen.getByText("Start a manual session with text, files, photos, or a screenshot anywhere here."),
    ).toBeInTheDocument();
    expect(screen.getByTestId("manual-session-plane-canvas")).toHaveAttribute("aria-hidden", "true");
    expect(screen.queryByText("Drop a screenshot anywhere here, or use +")).not.toBeInTheDocument();
  });

  it("does not render dictation controls on desktop or mobile", async () => {
    const { unmount } = renderWithProviders(<ManualSessionCreatePageContent />);

    expect(screen.queryByRole("button", { name: "Dictate" })).not.toBeInTheDocument();

    unmount();
    setMobileViewport(true);
    renderWithProviders(<ManualSessionCreatePageContent />);

    expect(screen.queryByRole("button", { name: "Dictate" })).not.toBeInTheDocument();
  });

  it("shows repository selection", async () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    await waitFor(() => {
      expect(mocks.settingsGetMock).toHaveBeenCalled();
    });
  });

  it("keeps model controls visible on desktop when there are no repositories", async () => {
    mocks.repositoriesListMock.mockResolvedValueOnce({ data: [] });

    renderWithProviders(<ManualSessionCreatePageContent />);

    expect(await screen.findByTestId("no-repos-warning")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: /Model override/i })).toBeInTheDocument();
  });

  it("does not show repository setup warning before the repository fetch resolves", async () => {
    const repos = deferred<{ data: never[] }>();
    mocks.repositoriesListMock.mockImplementationOnce(() => repos.promise);

    renderWithProviders(<ManualSessionCreatePageContent />);

    expect(await screen.findByRole("textbox", { name: "Manual session prompt" })).toBeInTheDocument();
    expect(screen.queryByTestId("no-repos-warning")).not.toBeInTheDocument();

    repos.resolve({ data: [] });

    expect(await screen.findByTestId("no-repos-warning")).toBeInTheDocument();
  });

  it("does not show the agent configuration warning before setup queries resolve", async () => {
    const settings = deferred<Awaited<ReturnType<typeof mocks.settingsGetMock>>>();
    mocks.settingsGetMock.mockImplementationOnce(() => settings.promise);
    mocks.codingCredentialsListMock.mockResolvedValueOnce({ data: [] });
    mocks.codexAuthStatusMock.mockResolvedValueOnce({ data: { status: "none" } });

    renderWithProviders(<ManualSessionCreatePageContent />);

    expect(await screen.findByRole("textbox", { name: "Manual session prompt" })).toBeInTheDocument();
    expect(screen.queryByText(/isn't connected yet/)).not.toBeInTheDocument();

    settings.resolve({
      data: {
        name: "Test Org",
        settings: {
          default_agent_type: "codex",
          default_llm_model: "gpt-5.4-mini",
        },
      },
    });

    expect(await screen.findByText(/Codex isn't connected yet/)).toBeInTheDocument();
  });

  it("uses a mobile settings sheet instead of inline repo and model controls on small screens", async () => {
    const user = userEvent.setup();
    setMobileViewport(true);

    renderWithProviders(<ManualSessionCreatePageContent />);

    expect(await screen.findByRole("button", { name: "Session settings" })).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByText("test-repo")).toBeInTheDocument();
      expect(screen.getByText("main")).toBeInTheDocument();
      expect(screen.getByText("Default model")).toBeInTheDocument();
      expect(screen.getByText("Default reasoning")).toBeInTheDocument();
    });
    expect(screen.queryByRole("combobox", { name: /Model override/i })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Session settings" }));

    expect(await screen.findByRole("dialog", { name: "Session settings" })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: /Model override/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Target branch/ })).toBeInTheDocument();
  });

  it("submits the user's default coding-agent model when no per-session model is selected", async () => {
    const user = userEvent.setup();
    mocks.authMeMock.mockResolvedValueOnce({
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

    renderWithProviders(<ManualSessionCreatePageContent />);

    const prompt = await screen.findByRole("textbox", { name: "Manual session prompt" });
    fireEvent.change(prompt, { target: { value: "Use my default model" } });
    await user.click(screen.getByRole("button", { name: /start session/i }));

    await waitFor(() => {
      expect(mocks.createSessionMock).toHaveBeenCalledWith(expect.objectContaining({
        model: "claude-opus-4-7",
        agent_type: "claude_code",
      }));
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
    expect(textarea).toHaveClass("max-sm:text-base");
    expect(textarea).toHaveClass("text-xs");
    expect(textarea).not.toHaveClass("text-base");
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

  it("uploads an image pasted into the prompt and shows it in the attachment strip", async () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    const textarea = await screen.findByPlaceholderText("Tell the agent what to do...");
    const file = new File(["image-bytes"], "pasted-shot.png", { type: "image/png" });

    fireEvent.paste(textarea, {
      clipboardData: {
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

  it("allows starting a mobile session with only an uploaded image", async () => {
    const user = userEvent.setup();
    setMobileViewport(true);
    renderWithProviders(<ManualSessionCreatePageContent />);

    const textarea = await screen.findByPlaceholderText("Tell the agent what to do...");
    const file = new File(["image-bytes"], "mobile-shot.png", { type: "image/png" });

    fireEvent.paste(textarea, {
      clipboardData: {
        files: [file],
        items: [{ kind: "file", type: "image/png", getAsFile: () => file }],
        types: ["Files"],
      },
    });

    await waitFor(() => {
      expect(mocks.uploadMock).toHaveBeenCalledWith(file);
    });

    const startButton = await screen.findByRole("button", { name: "Start session" });
    expect(startButton).toBeEnabled();

    await user.click(startButton);

    await waitFor(() => {
      expect(mocks.createSessionMock).toHaveBeenCalledWith(
        expect.objectContaining({
          message: "",
          images: ["https://example.com/uploaded-shot.png"],
        }),
      );
    });
    expect(mocks.addOptimisticSessionMock).not.toHaveBeenCalled();
  });

  it("replaces the new-session route after create succeeds while navigation is pending", async () => {
    const user = userEvent.setup();
    const createSession = deferred<{ data: { id: string } }>();
    mocks.createSessionMock.mockImplementationOnce(() => createSession.promise);

    renderWithProviders(<ManualSessionCreatePageContent />);

    const textarea = await screen.findByRole("textbox", { name: "Manual session prompt" });
    await user.type(textarea, "Investigate slow checkout");

    const startButton = screen.getByRole("button", { name: "Start session" });
    await user.click(startButton);

    expect(startButton).toBeDisabled();

    await act(async () => {
      createSession.resolve({ data: { id: "new-sess" } });
      await createSession.promise;
    });

    await waitFor(() => {
      expect(mocks.routerReplaceMock).toHaveBeenCalledWith("/sessions/new-sess");
    });
    expect(mocks.routerPushMock).not.toHaveBeenCalled();
    expect(startButton).toBeDisabled();
    expect(startButton.querySelector(".animate-spin")).not.toBeNull();
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

  it("shows the shared add menu items in the new session composer", async () => {
    const user = userEvent.setup();
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(await screen.findByRole("button", { name: "Add files or photos" }));

    expect(await screen.findByRole("menuitem", { name: "Upload files or photos" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Add image URL" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Add linear issue" })).toBeInTheDocument();
  });

  it("hides the linear issue action when Linear is not configured", async () => {
    const user = userEvent.setup();
    mocks.integrationsListMock.mockResolvedValueOnce({ data: [] });
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(await screen.findByRole("button", { name: "Add files or photos" }));

    expect(screen.queryByRole("menuitem", { name: "Add linear issue" })).not.toBeInTheDocument();
  });

  it("hides the linear issue action until the integrations fetch confirms active Linear", async () => {
    const user = userEvent.setup();
    const integrations = deferred<Awaited<ReturnType<typeof mocks.integrationsListMock>>>();
    mocks.integrationsListMock.mockImplementationOnce(() => integrations.promise);
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(await screen.findByRole("button", { name: "Add files or photos" }));

    expect(screen.queryByRole("menuitem", { name: "Add linear issue" })).not.toBeInTheDocument();

    integrations.resolve({
      data: [
        {
          id: "integration-linear",
          provider: "linear",
          status: "active",
        },
      ],
    });

    expect(await screen.findByRole("menuitem", { name: "Add linear issue" })).toBeInTheDocument();
  });

  it("adds a linked Linear issue as a chip instead of moving it into the prompt text", async () => {
    const user = userEvent.setup();
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(await screen.findByRole("button", { name: "Add files or photos" }));
    await user.click(await screen.findByRole("menuitem", { name: "Add linear issue" }));

    const linearInput = await screen.findByRole("textbox", { name: "Linear issue id or URL" });
    await user.type(linearInput, "ACS-1234");
    await user.click(screen.getByRole("button", { name: "Add" }));

    expect(await screen.findByText("ACS-1234")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove ACS-1234" })).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Manual session prompt" })).toHaveValue("");
  });

  it("submits the linked Linear issue as a structured reference", async () => {
    const user = userEvent.setup();
    renderWithProviders(<ManualSessionCreatePageContent />);

    const textarea = await screen.findByRole("textbox", { name: "Manual session prompt" });
    await user.type(textarea, "Investigate the rollout issue");
    await user.click(screen.getByRole("button", { name: "Add files or photos" }));
    await user.click(await screen.findByRole("menuitem", { name: "Add linear issue" }));

    const linearInput = await screen.findByRole("textbox", { name: "Linear issue id or URL" });
    await user.type(linearInput, "ACS-1234");
    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => {
      expect(screen.queryByRole("textbox", { name: "Linear issue id or URL" })).not.toBeInTheDocument();
    });
    await user.click((await screen.findAllByRole("button", { name: "Start session" }))[0]);

    await waitFor(() => {
      expect(mocks.createSessionMock).toHaveBeenCalledWith(
        expect.objectContaining({
          message: "Investigate the rollout issue",
          references: [
            {
              kind: "app",
              id: "ACS-1234",
              display: "ACS-1234",
            },
          ],
        }),
      );
    });
  });

  it("allows starting a session with only a linked Linear issue", async () => {
    const user = userEvent.setup();
    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(await screen.findByRole("button", { name: "Add files or photos" }));
    await user.click(await screen.findByRole("menuitem", { name: "Add linear issue" }));

    const linearInput = await screen.findByRole("textbox", { name: "Linear issue id or URL" });
    await user.type(linearInput, "ACS-1234");
    await user.click(screen.getByRole("button", { name: "Add" }));

    const startButtons = await screen.findAllByRole("button", { name: "Start session" });
    expect(startButtons[0]).toBeEnabled();
    await user.click(startButtons[0]);

    await waitFor(() => {
      expect(mocks.createSessionMock).toHaveBeenCalledWith(
        expect.objectContaining({
          message: "",
          references: [
            {
              kind: "app",
              id: "ACS-1234",
              display: "ACS-1234",
            },
          ],
        }),
      );
    });
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

  it("shows an inline validation error when a pasted file exceeds the size limit", async () => {
    renderWithProviders(<ManualSessionCreatePageContent />);

    const oversizedFile = new File([new Uint8Array(10 * 1024 * 1024 + 1)], "too-large-paste.png", { type: "image/png" });
    const textarea = await screen.findByPlaceholderText("Tell the agent what to do...");

    fireEvent.paste(textarea, {
      clipboardData: {
        files: [oversizedFile],
        items: [{ kind: "file", type: "image/png", getAsFile: () => oversizedFile }],
        types: ["Files"],
      },
    });

    await waitFor(() => {
      expect(screen.getByText("File too large (max 10 MB): too-large-paste.png")).toBeInTheDocument();
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
    await user.click(screen.getByRole("option", { name: "openai/gpt-5.4-mini" }));

    const textarea = await screen.findByPlaceholderText("Tell the agent what to do...");
    await user.type(textarea, "Fix the login bug");
    await user.click((await screen.findAllByRole("button", { name: "Start session" }))[0]);

    await waitFor(() => {
      expect(mocks.createSessionMock).toHaveBeenCalled();
    });

    const requestBody = mocks.createSessionMock.mock.calls.at(-1)?.[0];
    expect(requestBody).toMatchObject({
      message: "Fix the login bug",
      model: "openai/gpt-5.4-mini",
      agent_type: "opencode",
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

  it("shows org coding-auth models even when the user has no personal credentials", async () => {
    const user = userEvent.setup();
    mocks.settingsGetMock.mockResolvedValueOnce({
      data: {
        name: "Test Org",
        settings: {
          default_agent_type: "codex",
        },
      },
    });
    mocks.codexAuthStatusMock.mockResolvedValueOnce({ data: { status: "pending" } });
    // The resolved stack falls through to the org fallback when the user has
    // no personal credentials.
    mocks.codingCredentialsListMock.mockResolvedValueOnce({
      data: [
        {
          id: "auth-1",
          org_id: "org-1",
          priority: 1,
          agent: "codex",
          auth_type: "api_key",
          label: "Org Codex",
          scope: "org",
          provider: "openai",
          status: "healthy",
          is_default: true,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      ],
    });

    renderWithProviders(<ManualSessionCreatePageContent />);

    await user.click(await screen.findByRole("combobox", { name: /Model/i }));

    expect(screen.getByRole("option", { name: "gpt-5.4" })).toBeInTheDocument();
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

    it("debounces prompt writes to sessionStorage while the user types", async () => {
      renderWithProviders(<ManualSessionCreatePageContent />);

      const textarea = await screen.findByPlaceholderText<HTMLTextAreaElement>(
        "Tell the agent what to do...",
      );

      vi.useFakeTimers();
      fireEvent.change(textarea, { target: { value: "draft in progress" } });

      expect(window.sessionStorage.getItem(DRAFT_STORAGE_KEY)).toBeNull();

      vi.advanceTimersByTime(399);
      expect(window.sessionStorage.getItem(DRAFT_STORAGE_KEY)).toBeNull();

      await vi.advanceTimersByTimeAsync(1);
      const stored = window.sessionStorage.getItem(DRAFT_STORAGE_KEY);
      expect(stored).not.toBeNull();
      expect(JSON.parse(stored!)).toMatchObject({
        __v: 2,
        message: "draft in progress",
      });
    });

    it("flushes the debounced draft immediately when the prompt blurs", async () => {
      renderWithProviders(<ManualSessionCreatePageContent />);

      const textarea = await screen.findByPlaceholderText<HTMLTextAreaElement>(
        "Tell the agent what to do...",
      );

      vi.useFakeTimers();
      fireEvent.change(textarea, { target: { value: "save on blur" } });

      expect(window.sessionStorage.getItem(DRAFT_STORAGE_KEY)).toBeNull();

      fireEvent.blur(textarea);

      const stored = window.sessionStorage.getItem(DRAFT_STORAGE_KEY);
      expect(stored).not.toBeNull();
      expect(JSON.parse(stored!)).toMatchObject({
        __v: 2,
        message: "save on blur",
      });
    });

    it("does not clear a restored model while the resolved credential stack is still loading", async () => {
      type CodingCredentialListResponse = {
        data: Array<{
          id: string;
          org_id: string;
          priority: number;
          agent: string;
          auth_type: string;
          label: string;
          scope: string;
          provider: string;
          status: string;
          is_default: boolean;
          created_at: string;
          updated_at: string;
        }>;
      };
      let resolveCodingCredentials: ((value: CodingCredentialListResponse) => void) | undefined;
      mocks.codexAuthStatusMock.mockResolvedValueOnce({ data: { status: "pending" } });
      mocks.codingCredentialsListMock.mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveCodingCredentials = resolve;
          }),
      );
      window.sessionStorage.setItem(
        DRAFT_STORAGE_KEY,
        JSON.stringify({
          __v: 2,
          message: "Restore my draft",
          attachments: [],
          references: [],
          commands: [],
          selectedModel: "gpt-5.4",
          userSelectedRepoId: null,
          branchByRepoId: {},
          showImageInput: false,
          imageURL: "",
        }),
      );

      renderWithProviders(<ManualSessionCreatePageContent />);

      const modelSelect = await screen.findByRole("combobox", { name: /Model/i });
      await waitFor(() => {
        expect(mocks.codingCredentialsListMock).toHaveBeenCalledTimes(1);
        expect(mocks.codexAuthStatusMock).toHaveBeenCalledTimes(1);
      });

      expect(resolveCodingCredentials).toBeDefined();
      resolveCodingCredentials!({
        data: [
          {
            id: "auth-1",
            org_id: "org-1",
            priority: 1,
            agent: "codex",
            auth_type: "api_key",
            label: "Org Codex",
            scope: "org",
            provider: "openai",
            status: "healthy",
            is_default: true,
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
        ],
      });

      await waitFor(() => {
        expect(modelSelect).toHaveTextContent("gpt-5.4");
      });
    });

    it("preserves detached Linear issue chips when a repo query param conflicts with the saved draft", async () => {
      mocks.searchParamGetMock.mockImplementation((key) => {
        if (key === "repo") return "repo-1";
        return null;
      });
      window.sessionStorage.setItem(
        DRAFT_STORAGE_KEY,
        JSON.stringify({
          __v: 2,
          message: "Previously typed prompt",
          attachments: [],
          references: [
            {
              kind: "app",
              id: "ACS-1234",
              display: "ACS-1234",
            },
          ],
          commands: [],
          selectedModel: "",
          userSelectedRepoId: "repo-2",
          branchByRepoId: {},
          showImageInput: false,
          imageURL: "",
        }),
      );

      renderWithProviders(<ManualSessionCreatePageContent />);

      expect(await screen.findByText("ACS-1234")).toBeInTheDocument();
      await waitFor(() => {
        expect(screen.getByRole("textbox", { name: "Manual session prompt" })).toHaveValue("Previously typed prompt");
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
        expect(mocks.codingCredentialsListMock).toHaveBeenCalledTimes(1);
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
