import React from "react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, renderWithProviders, screen, waitFor } from "@/test/test-utils";
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
  uploadMock: vi.fn().mockResolvedValue({
    url: "https://example.com/uploaded-shot.png",
    file_name: "uploaded-shot.png",
    content_type: "image/png",
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
});
