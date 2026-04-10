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
  let onOpenChange: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    onOpenChange = vi.fn();
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
});
