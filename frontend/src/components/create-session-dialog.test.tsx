import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { CreateSessionDialog } from "./create-session-dialog";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";

const DRAFT_STORAGE_KEY = "143:new-session-draft";

vi.mock("next/navigation", () => ({
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
  useSearchParams: () => new URLSearchParams(),
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

function setupManualSessionHandler() {
  server.use(
    http.post("/api/v1/sessions/manual", () => {
      return HttpResponse.json({
        data: {
          id: "session-new-123",
          status: "pending",
          agent_type: "codex",
          created_at: "2026-04-09T00:00:00Z",
        },
      });
    }),
  );
}

function setupRepoHandlers() {
  server.use(
    http.get("/api/v1/repositories", () => {
      return HttpResponse.json({
        data: [
          {
            id: "repo-1",
            org_id: "org-1",
            full_name: "acme/api-server",
            default_branch: "main",
            github_id: 1,
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          {
            id: "repo-2",
            org_id: "org-1",
            full_name: "acme/web-app",
            default_branch: "main",
            github_id: 2,
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
        ],
        meta: {},
      });
    }),
    http.get("/api/v1/repositories/:id/branches", () => {
      return HttpResponse.json({
        data: [{ name: "main", protected: true }],
        meta: {},
      });
    }),
  );
}

describe("CreateSessionDialog", () => {
  let onOpenChange: (open: boolean) => void;

  beforeEach(() => {
    onOpenChange = vi.fn<(open: boolean) => void>();
    window.localStorage.clear();
    window.sessionStorage.clear();
    setMobileViewport(false);
  });

  it("renders dialog with title when open", () => {
    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText("New session")).toBeInTheDocument();
  });

  it("does not render dialog when closed", () => {
    renderWithProviders(
      <CreateSessionDialog open={false} onOpenChange={onOpenChange} />,
    );

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("renders textarea with placeholder", () => {
    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    expect(
      screen.getByPlaceholderText("Tell the agent what to do..."),
    ).toBeInTheDocument();
  });

  it("disables Start session button when textarea is empty", () => {
    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    expect(screen.getByRole("button", { name: "Start session" })).toBeDisabled();
  });

  it("enables Start session button when textarea has text", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    await user.type(
      screen.getByPlaceholderText("Tell the agent what to do..."),
      "Fix the login bug",
    );

    expect(screen.getByRole("button", { name: "Start session" })).toBeEnabled();
  });

  it("calls onOpenChange(false) on successful submission", async () => {
    const user = userEvent.setup();
    setupManualSessionHandler();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    await user.type(
      screen.getByPlaceholderText("Tell the agent what to do..."),
      "Fix the login bug",
    );

    await user.click(screen.getByRole("button", { name: "Start session" }));

    await waitFor(() => {
      expect(onOpenChange).toHaveBeenCalledWith(false);
    });
  });

  it("does not restore the submitted prompt after closing before the draft debounce fires", async () => {
    setupManualSessionHandler();

    const { rerender } = renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    const textarea = screen.getByPlaceholderText("Tell the agent what to do...");
    fireEvent.change(textarea, { target: { value: "Fix stale draft" } });

    fireEvent.click(screen.getByRole("button", { name: "Start session" }));

    await waitFor(() => {
      expect(onOpenChange).toHaveBeenCalledWith(false);
    });

    rerender(<CreateSessionDialog open={false} onOpenChange={onOpenChange} />);

    expect(window.sessionStorage.getItem(DRAFT_STORAGE_KEY)).toBeNull();

    rerender(<CreateSessionDialog open onOpenChange={onOpenChange} />);

    expect(screen.getByPlaceholderText<HTMLTextAreaElement>("Tell the agent what to do...").value).toBe("");
  });

  it("shows error message when session creation fails", async () => {
    const user = userEvent.setup();

    server.use(
      http.post("/api/v1/sessions/manual", () => {
        return HttpResponse.json(
          { error: { code: "INTERNAL", message: "Something went wrong" } },
          { status: 500 },
        );
      }),
    );

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    await user.type(
      screen.getByPlaceholderText("Tell the agent what to do..."),
      "Fix the login bug",
    );

    await user.click(screen.getByRole("button", { name: "Start session" }));

    await waitFor(() => {
      expect(screen.getByText(/Something went wrong/)).toBeInTheDocument();
    });
  });

  it("shows repo selector when repositories are available", async () => {
    setupRepoHandlers();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    // With multiple repos the first one is auto-selected, so the button shows its name
    await waitFor(() => {
      expect(screen.getByText("api-server")).toBeInTheDocument();
    });
  });

  it("shows model selector", () => {
    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    expect(screen.getByRole("combobox", { name: /Model/ })).toBeInTheDocument();
  });

  it("uses a mobile settings sheet instead of inline repo and model controls on small screens", async () => {
    const user = userEvent.setup();
    setMobileViewport(true);
    setupRepoHandlers();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    expect(await screen.findByRole("button", { name: "Session settings" })).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByText("api-server")).toBeInTheDocument();
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

  it("renders the mobile dialog as a full-screen scrollable composer", () => {
    setMobileViewport(true);

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    const dialog = screen.getByRole("dialog", { name: "New session" });
    const textarea = screen.getByRole("textbox", { name: "Session prompt" });

    expect(dialog).toHaveClass("inset-0");
    expect(dialog).toHaveClass("h-dvh");
    expect(dialog).toHaveClass("max-w-none");
    expect(dialog).toHaveClass("overflow-y-auto");
    expect(textarea).toHaveClass("max-sm:text-base");
    expect(textarea).toHaveClass("text-xs");
    expect(textarea).not.toHaveClass("text-base");
  });

  it("shows attachment button", () => {
    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    expect(
      screen.getByRole("button", { name: /Add files or photos/ }),
    ).toBeInTheDocument();
  });

  it("submits on Enter (without Shift)", async () => {
    const user = userEvent.setup();
    setupManualSessionHandler();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    const textarea = screen.getByPlaceholderText("Tell the agent what to do...");
    await user.type(textarea, "Do something");
    await user.keyboard("{Enter}");

    await waitFor(() => {
      expect(onOpenChange).toHaveBeenCalledWith(false);
    });
  });

  it("does not submit on Shift+Enter", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    const textarea = screen.getByPlaceholderText("Tell the agent what to do...");
    await user.type(textarea, "Do something");
    await user.keyboard("{Shift>}{Enter}{/Shift}");

    // Dialog should still be open, onOpenChange not called with false
    expect(onOpenChange).not.toHaveBeenCalledWith(false);
  });

  it("shows image URL input when Add image URL is clicked", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    // Open the attachment dropdown
    await user.click(screen.getByRole("button", { name: /Add files or photos/ }));
    expect(screen.getByTestId("add-image-url-link-icon")).toBeInTheDocument();
    // Click "Add image URL"
    await user.click(screen.getByText("Add image URL"));

    expect(screen.getByLabelText("Image URL")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add" })).toBeInTheDocument();
  });

  it("adds an image URL to attachments", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    // Open the attachment dropdown and show image URL input
    await user.click(screen.getByRole("button", { name: /Add files or photos/ }));
    await user.click(screen.getByText("Add image URL"));

    await user.type(screen.getByLabelText("Image URL"), "https://example.com/screenshot.png");
    await user.click(screen.getByRole("button", { name: "Add" }));

    // Should show the image as an attachment with a remove button
    expect(screen.getByAltText("screenshot.png")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Remove screenshot.png/ })).toBeInTheDocument();
  });

  it("allows starting a mobile dialog session with only an attachment", async () => {
    const user = userEvent.setup();
    setMobileViewport(true);
    setupManualSessionHandler();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    await user.click(await screen.findByRole("button", { name: "Add files or photos" }));
    await user.click(screen.getByRole("menuitem", { name: "Add image URL" }));
    await user.type(screen.getByLabelText("Image URL"), "https://example.com/mobile-attachment.png");
    await user.click(screen.getByRole("button", { name: "Add" }));

    const startButton = screen.getByRole("button", { name: "Start session" });
    expect(startButton).toBeEnabled();

    await user.click(startButton);

    await waitFor(() => {
      expect(onOpenChange).toHaveBeenCalledWith(false);
    });
  });

  it("opens an image lightbox from the dialog attachment thumbnail", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    await user.click(screen.getByRole("button", { name: /Add files or photos/ }));
    await user.click(screen.getByText("Add image URL"));
    await user.type(screen.getByLabelText("Image URL"), "https://example.com/screenshot.png");
    await user.click(screen.getByRole("button", { name: "Add" }));

    await user.click(screen.getByRole("button", { name: "Preview screenshot.png" }));

    expect(screen.getByRole("dialog", { name: "Image preview" })).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "screenshot.png" })).toBeInTheDocument();
  });

  it("removes an attachment when remove button is clicked", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    // Add an image URL attachment
    await user.click(screen.getByRole("button", { name: /Add files or photos/ }));
    await user.click(screen.getByText("Add image URL"));
    await user.type(screen.getByLabelText("Image URL"), "https://example.com/shot.png");
    await user.click(screen.getByRole("button", { name: "Add" }));

    // Verify it's there
    expect(screen.getByAltText("shot.png")).toBeInTheDocument();

    // Remove it
    await user.click(screen.getByRole("button", { name: /Remove shot.png/ }));

    // Should be gone
    expect(screen.queryByAltText("shot.png")).not.toBeInTheDocument();
  });

  it("shows selected repo name in repo button after selection", async () => {
    const user = userEvent.setup();
    setupRepoHandlers();

    server.use(
      http.get("/api/v1/repositories/:repoId/branches", () => {
        return HttpResponse.json({
          data: [
            { name: "main", protected: true },
            { name: "develop", protected: false },
          ],
          meta: {},
        });
      }),
    );

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    // First repo is auto-selected; switch to the second one
    await waitFor(() => {
      expect(screen.getByText("api-server")).toBeInTheDocument();
    });

    // Open repo dropdown and select the other repo
    await user.click(screen.getByText("api-server"));
    await user.click(screen.getByText("acme/web-app"));

    // Should show the new repo short name
    await waitFor(() => {
      expect(screen.getByText("web-app")).toBeInTheDocument();
    });
  });

  it("shows branch selector after selecting a repo", async () => {
    setupRepoHandlers();

    server.use(
      http.get("/api/v1/repositories/:repoId/branches", () => {
        return HttpResponse.json({
          data: [
            { name: "main", protected: true },
            { name: "feature-x", protected: false },
          ],
          meta: {},
        });
      }),
    );

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    // First repo is auto-selected, so branch selector should appear
    await waitFor(() => {
      expect(screen.getByText("api-server")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /Target branch/ })).toBeInTheDocument();
    });
  });

  it("keeps branch selection repo-backed when branch loading fails", async () => {
    setupRepoHandlers();

    server.use(
      http.get("/api/v1/repositories/:repoId/branches", () => {
        return HttpResponse.json(
          { error: { code: "INTERNAL", message: "Failed" } },
          { status: 500 },
        );
      }),
    );

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Target branch/ })).toBeInTheDocument();
    });
    expect(screen.queryByRole("textbox", { name: "Target branch" })).not.toBeInTheDocument();
  });

  it("does not add empty image URL", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    await user.click(screen.getByRole("button", { name: /Add files or photos/ }));
    await user.click(screen.getByText("Add image URL"));

    // Click Add without typing anything
    await user.click(screen.getByRole("button", { name: "Add" }));

    // No attachment area should appear
    expect(screen.queryByRole("button", { name: /Remove/ })).not.toBeInTheDocument();
  });

  it("shows non-image attachment as badge", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    // Add a non-image URL as attachment
    await user.click(screen.getByRole("button", { name: /Add files or photos/ }));
    await user.click(screen.getByText("Add image URL"));
    await user.type(screen.getByLabelText("Image URL"), "https://example.com/data.json");
    await user.click(screen.getByRole("button", { name: "Add" }));

    // Non-image URLs should render as a badge with filename, not as img
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
    expect(screen.getByText("data.json")).toBeInTheDocument();
  });

  it("submits with selected repo and model", async () => {
    // Radix UI DropdownMenu internally schedules state updates (Presence,
    // FocusScope, DismissableLayer) outside of React's act() scope.
    // This is a known limitation with React 19 + Radix.  Suppress the
    // expected act() warnings so they don't pollute test output.
    const origConsoleError = console.error;
    console.error = (...args: unknown[]) => {
      if (typeof args[0] === "string" && (
        args[0].includes("not wrapped in act") ||
        args[0].includes("was not awaited")
      )) return;
      origConsoleError(...args);
    };

    const user = userEvent.setup();
    setupRepoHandlers();
    setupManualSessionHandler();

    server.use(
      http.get("/api/v1/repositories/:repoId/branches", () => {
        return HttpResponse.json({
          data: [{ name: "main", protected: true }],
          meta: {},
        });
      }),
    );

    try {
      renderWithProviders(
        <CreateSessionDialog open onOpenChange={onOpenChange} />,
      );

      // Type a message
      await user.type(
        screen.getByPlaceholderText("Tell the agent what to do..."),
        "Fix the bug",
      );

      // First repo is auto-selected; wait for branch data to load
      await waitFor(() => {
        expect(screen.getByText("api-server")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: /Target branch/ })).toBeInTheDocument();
      });

      // Submit
      await user.click(screen.getByRole("button", { name: "Start session" }));

      await waitFor(() => {
        expect(onOpenChange).toHaveBeenCalledWith(false);
      });
    } finally {
      console.error = origConsoleError;
    }
  });

  it("does not submit a hidden default reasoning effort for unsupported agents", async () => {
    const user = userEvent.setup();
    let requestBody: Record<string, unknown> | null = null;

    server.use(
      http.get("/api/v1/auth/me", () => {
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
              },
            },
            created_at: "2026-01-01T00:00:00Z",
          },
        });
      }),
      // Gemini must be available in the model picker for this test to be able
      // to select "gemini-2.5-pro"; the composer hides agents the user has no
      // credentials for.
      http.get("/api/v1/settings/credentials/resolved", () => {
        return HttpResponse.json({
          data: [{ provider: "gemini", source: "personal" }],
        });
      }),
      http.post("/api/v1/sessions/manual", async ({ request }) => {
        requestBody = await request.json() as Record<string, unknown>;
        return HttpResponse.json({
          data: {
            id: "session-new-123",
            status: "pending",
            agent_type: "gemini_cli",
            created_at: "2026-04-09T00:00:00Z",
          },
        });
      }),
    );

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    await user.click(screen.getByRole("combobox", { name: /Model/i }));
    await user.click(screen.getByRole("option", { name: "gemini-2.5-pro" }));
    await user.type(
      screen.getByPlaceholderText("Tell the agent what to do..."),
      "Fix the login bug",
    );
    await user.click(screen.getByRole("button", { name: "Start session" }));

    await waitFor(() => {
      expect(onOpenChange).toHaveBeenCalledWith(false);
    });

    expect(requestBody).toMatchObject({
      message: "Fix the login bug",
      model: "gemini-2.5-pro",
      agent_type: "gemini_cli",
    });
    expect(requestBody).not.toHaveProperty("reasoning_effort");
  });
});
