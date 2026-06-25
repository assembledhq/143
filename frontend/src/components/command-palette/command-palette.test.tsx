import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { CommandPalette } from "./command-palette";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";

const pushMock = vi.fn();

vi.mock("next/navigation", () => ({
  usePathname: () => "/sessions",
  useRouter: () => ({ push: pushMock, replace: vi.fn() }),
}));

const logoutMock = vi.fn();

function renderPalette(overrides?: {
  open?: boolean;
  userRole?: string;
  searchParams?: Record<string, string>;
}) {
  const onOpenChange = vi.fn();
  return {
    onOpenChange,
    ...renderWithProviders(
      <CommandPalette
        open={overrides?.open ?? true}
        onOpenChange={onOpenChange}
        userRole={overrides?.userRole ?? "admin"}
        logout={logoutMock}
      />,
      { searchParams: overrides?.searchParams }
    ),
  };
}

describe("CommandPalette", () => {
  beforeEach(() => {
    pushMock.mockClear();
    logoutMock.mockClear();
    localStorage.clear();
  });

  it("renders static navigation items when open", async () => {
    renderPalette();
    expect(await screen.findByText("Sessions")).toBeInTheDocument();
    expect(screen.getByText("Projects")).toBeInTheDocument();
    expect(screen.getAllByText("Autopilot")).toHaveLength(2);
  });

  it("renders settings items", async () => {
    renderPalette();
    expect(await screen.findByText("General")).toBeInTheDocument();
    expect(screen.getByText("Integrations")).toBeInTheDocument();
    expect(screen.getByText("Team")).toBeInTheDocument();
  });

  it("renders quick action items", async () => {
    renderPalette();
    expect(await screen.findByText("New session")).toBeInTheDocument();
    expect(screen.getByText("New project")).toBeInTheDocument();
    expect(screen.getByText("Log out")).toBeInTheDocument();
  });

  it("starts a global new session without preserving ambient repo context", async () => {
    const user = userEvent.setup();
    renderPalette({ searchParams: { repo: "repo-1" } });

    const newSessionItem = await screen.findByText("New session");
    await user.click(newSessionItem);

    expect(pushMock).toHaveBeenCalledWith("/sessions/new");
  });

  it("excludes admin-only items for non-admin users", async () => {
    renderPalette({ userRole: "member" });
    await screen.findByText("Account");
    expect(screen.queryByText("General")).not.toBeInTheDocument();
    expect(screen.queryByText("Audit log")).not.toBeInTheDocument();
    expect(screen.getAllByText("Autopilot")).toHaveLength(1);
  });

  it("includes admin-only items for admin users", async () => {
    renderPalette({ userRole: "admin" });
    expect(await screen.findByText("Audit log")).toBeInTheDocument();
  });

  it("navigates to a page when a navigation item is selected", async () => {
    const user = userEvent.setup();
    renderPalette();

    const [autopilotItem] = await screen.findAllByText("Autopilot");
    await user.click(autopilotItem);

    expect(pushMock).toHaveBeenCalledWith("/autopilot");
  });

  it("calls logout when Log out is selected", async () => {
    const user = userEvent.setup();
    renderPalette();

    const logoutItem = await screen.findByText("Log out");
    await user.click(logoutItem);

    expect(logoutMock).toHaveBeenCalled();
  });

  it("shows 'Start manual session' when search has no results", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/sessions", () =>
        HttpResponse.json({ data: [], meta: {} })
      ),
      http.get("/api/v1/projects", () =>
        HttpResponse.json({ data: [], meta: {} })
      )
    );

    renderPalette();

    const input = screen.getByPlaceholderText("Type a command or search...");
    await user.type(input, "nonexistent-thing-xyz");

    await waitFor(() => {
      expect(
        screen.getByText(/Start manual session/i)
      ).toBeInTheDocument();
    });
  });

  it("shows dynamic session results when typing a search query", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/sessions", ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get("search")) {
          return HttpResponse.json({
            data: [
              { id: "sess-1", title: "Fix login bug", status: "completed", created_at: "2026-01-01T00:00:00Z", current_turn: 0, sandbox_state: "none", org_id: "org-1", primary_issue_id: null, agent_type: "codex", autonomy_level: "semi", token_mode: "normal" },
            ],
            meta: {},
          });
        }
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get("/api/v1/projects", () =>
        HttpResponse.json({ data: [], meta: {} })
      )
    );

    renderPalette();

    const input = screen.getByPlaceholderText("Type a command or search...");
    await user.type(input, "Fix login");

    await waitFor(() => {
      expect(screen.getByText("Fix login bug")).toBeInTheDocument();
    });
  });

  it("preserves repo context when opening a session result", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/sessions", ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get("search") === "Fix login") {
          return HttpResponse.json({
            data: [
              { id: "sess-1", title: "Fix login bug", status: "completed", created_at: "2026-01-01T00:00:00Z", current_turn: 0, sandbox_state: "none", org_id: "org-1", primary_issue_id: null, agent_type: "codex", autonomy_level: "semi", token_mode: "normal", repository_id: "repo-1" },
            ],
            meta: {},
          });
        }
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get("/api/v1/projects", () =>
        HttpResponse.json({ data: [], meta: {} })
      )
    );

    renderPalette({ searchParams: { repo: "repo-1" } });

    const input = screen.getByPlaceholderText("Type a command or search...");
    await user.type(input, "Fix login");

    const sessionItem = await screen.findByText("Fix login bug");
    await user.click(sessionItem);

    expect(pushMock).toHaveBeenCalledWith("/sessions/sess-1?repo=repo-1");
  });

  it("preserves repo context when opening a recent item", async () => {
    const user = userEvent.setup();
    localStorage.setItem(
      "143:command-palette:recents",
      JSON.stringify([
        {
          type: "session",
          id: "sess-1",
          label: "Fix login bug",
          href: "/sessions/sess-1",
          timestamp: Date.now(),
        },
      ])
    );

    renderPalette({ searchParams: { repo: "repo-1" } });

    const recentItem = await screen.findByText("Fix login bug");
    await user.click(recentItem);

    expect(pushMock).toHaveBeenCalledWith("/sessions/sess-1?repo=repo-1");
  });

  it("starts a manual session from the keyboard when search has no results", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/sessions", () =>
        HttpResponse.json({ data: [], meta: {} })
      ),
      http.get("/api/v1/projects", () =>
        HttpResponse.json({ data: [], meta: {} })
      )
    );

    renderPalette({ searchParams: { repo: "repo-1" } });

    const input = screen.getByPlaceholderText("Type a command or search...");
    await user.type(input, "nonexistent-thing-xyz");

    await screen.findByText(/Start manual session/i);
    await user.keyboard("[ArrowDown][Enter]");

    expect(pushMock).toHaveBeenCalledWith(
      "/sessions/new?prompt=nonexistent-thing-xyz"
    );
  });

  it("clears the query when the palette is reopened", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    const rendered = renderWithProviders(
      <CommandPalette
        open
        onOpenChange={onOpenChange}
        userRole="admin"
        logout={logoutMock}
      />
    );

    const input = screen.getByPlaceholderText("Type a command or search...");
    await user.type(input, "stale query");
    expect(input).toHaveValue("stale query");

    rendered.rerender(
      <CommandPalette
        open={false}
        onOpenChange={onOpenChange}
        userRole="admin"
        logout={logoutMock}
      />
    );
    rendered.rerender(
      <CommandPalette
        open
        onOpenChange={onOpenChange}
        userRole="admin"
        logout={logoutMock}
      />
    );

    expect(screen.getByPlaceholderText("Type a command or search...")).toHaveValue("");
  });

  it("shows repository switching when multiple repos exist", async () => {
    server.use(
      http.get("/api/v1/repositories/summary", () =>
        HttpResponse.json({
          data: [
            { repository_id: "repo-1", full_name: "acme/api", active_session_count: 2, latest_session_status: "running", active_project_count: 1 },
            { repository_id: "repo-2", full_name: "acme/web", active_session_count: 0, latest_session_status: null, active_project_count: 0 },
          ],
          meta: {},
        })
      )
    );

    renderPalette();

    await waitFor(() => {
      expect(screen.getByText("acme/api")).toBeInTheDocument();
      expect(screen.getByText("acme/web")).toBeInTheDocument();
    });
  });
});
