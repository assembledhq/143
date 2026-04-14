import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { CreateSessionDialog } from "./create-session-dialog";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";

vi.mock("next/navigation", () => ({
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
  useSearchParams: () => new URLSearchParams(),
}));

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
  );
}

describe("CreateSessionDialog", () => {
  let onOpenChange: (open: boolean) => void;

  beforeEach(() => {
    onOpenChange = vi.fn<(open: boolean) => void>();
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

  it("disables Create button when textarea is empty", () => {
    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    expect(screen.getByRole("button", { name: /Create/ })).toBeDisabled();
  });

  it("enables Create button when textarea has text", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    await user.type(
      screen.getByPlaceholderText("Tell the agent what to do..."),
      "Fix the login bug",
    );

    expect(screen.getByRole("button", { name: /Create/ })).toBeEnabled();
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

    await user.click(screen.getByRole("button", { name: /Create/ }));

    await waitFor(() => {
      expect(onOpenChange).toHaveBeenCalledWith(false);
    });
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

    await user.click(screen.getByRole("button", { name: /Create/ }));

    await waitFor(() => {
      expect(screen.getByText(/Something went wrong/)).toBeInTheDocument();
    });
  });

  it("shows repo selector when repositories are available", async () => {
    setupRepoHandlers();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Repo/ })).toBeInTheDocument();
    });
  });

  it("shows model selector", () => {
    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    expect(screen.getByRole("combobox", { name: /Model/ })).toBeInTheDocument();
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

    // Wait for repo selector to appear
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Repo/ })).toBeInTheDocument();
    });

    // Open repo dropdown and select a repo
    await user.click(screen.getByRole("button", { name: /Repo/ }));
    await user.click(screen.getByText("acme/api-server"));

    // Should show the repo short name
    await waitFor(() => {
      expect(screen.getByText("api-server")).toBeInTheDocument();
    });
  });

  it("shows branch selector after selecting a repo", async () => {
    const user = userEvent.setup();
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

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Repo/ })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: /Repo/ }));
    await user.click(screen.getByText("acme/api-server"));

    // Should show a branch selector button with default branch
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Target branch/ })).toBeInTheDocument();
    });
  });

  it("shows branch fallback input when branch fetch fails", async () => {
    setupRepoHandlers();

    server.use(
      http.get("/api/v1/repositories/:repoId/branches", () => {
        return HttpResponse.json(
          { error: { code: "INTERNAL", message: "Failed" } },
          { status: 500 },
        );
      }),
    );

    const user = userEvent.setup();

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Repo/ })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: /Repo/ }));
    await user.click(screen.getByText("acme/web-app"));

    // Should show a text input for branch instead of dropdown
    await waitFor(() => {
      expect(screen.getByLabelText("Target branch")).toBeInTheDocument();
    });
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

    renderWithProviders(
      <CreateSessionDialog open onOpenChange={onOpenChange} />,
    );

    // Type a message
    await user.type(
      screen.getByPlaceholderText("Tell the agent what to do..."),
      "Fix the bug",
    );

    // Wait for repo selector and select a repo
    const repoButton = await screen.findByRole("button", { name: /Repo/ });
    await user.click(repoButton);

    // Wait for dropdown to fully open and select repo
    const repoOption = await screen.findByText("acme/api-server");
    await user.click(repoOption);

    // Wait for dropdown to close, selection to settle, and branch data to load
    await waitFor(() => {
      expect(screen.getByText("api-server")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /Target branch/ })).toBeInTheDocument();
    });

    // Submit
    await user.click(screen.getByRole("button", { name: /Create/ }));

    await waitFor(() => {
      expect(onOpenChange).toHaveBeenCalledWith(false);
    });

    console.error = origConsoleError;
  });
});
