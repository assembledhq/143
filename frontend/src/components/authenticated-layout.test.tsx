import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { AuthenticatedLayout } from "./authenticated-layout";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";

const { replaceMock, logoutMock, useAuthMock } = vi.hoisted(() => ({
  replaceMock: vi.fn(),
  logoutMock: vi.fn(),
  useAuthMock: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  usePathname: () => "/autopilot",
  useRouter: () => ({
    push: vi.fn(),
    replace: replaceMock,
  }),
}));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: useAuthMock,
}));

const adminUser = {
  id: "user-1",
  name: "Alex Doe",
  email: "alex@example.com",
  role: "admin",
};

const memberUser = {
  id: "user-2",
  name: "Member User",
  email: "member@example.com",
  role: "member",
};

beforeEach(() => {
  useAuthMock.mockReturnValue({
    user: adminUser,
    isLoading: false,
    isAuthenticated: true,
    logout: logoutMock,
  });

  // The layout fetches proposal summary which isn't in the default handlers
  server.use(
    http.get("/api/v1/projects/proposals/summary", () => {
      return HttpResponse.json({ data: { count: 0 } });
    }),
  );
});

describe("AuthenticatedLayout", () => {
  it("shows projects in the primary navigation", () => {
    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    expect(screen.getByRole("link", { name: "Projects" })).toHaveAttribute("href", "/projects");
  });

  it("shows Autopilot in the primary navigation", () => {
    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    expect(screen.getByRole("link", { name: "Autopilot" })).toHaveAttribute("href", "/autopilot");
  });

  it("uses a full-width content area with generous padding", () => {
    const { container } = renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    const contentWrapper = container.querySelector("main > div:last-child");
    expect(contentWrapper).toHaveClass("max-w-none");
    expect(contentWrapper).toHaveClass("px-8");
    expect(contentWrapper).toHaveClass("py-6");
  });

  it("shows settings entries in the collapsible sidebar section", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    // Expand the settings section
    await user.click(screen.getByRole("button", { name: /Settings/ }));

    expect(screen.getByRole("link", { name: "Account" })).toHaveAttribute("href", "/settings/account");
    expect(screen.getByRole("link", { name: "General" })).toHaveAttribute("href", "/settings");
    expect(screen.getByRole("link", { name: "Integrations" })).toHaveAttribute("href", "/settings/integrations");
    expect(screen.getByRole("link", { name: "Coding agents" })).toHaveAttribute("href", "/settings/agent");
    expect(screen.getByRole("link", { name: "LLM" })).toHaveAttribute("href", "/settings/llm");
    expect(screen.getByRole("link", { name: "Autopilot settings" })).toHaveAttribute("href", "/settings/autopilot");
    expect(screen.getByRole("link", { name: "Evals" })).toHaveAttribute("href", "/settings/evals");
    expect(screen.getByRole("link", { name: "Team" })).toHaveAttribute("href", "/settings/team");
    expect(screen.getByRole("link", { name: "Audit log" })).toHaveAttribute("href", "/settings/audit-log");
  });

  it("shows only log out in the user menu", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    await user.click(screen.getByRole("button", { name: /Alex Doe/ }));

    expect(await screen.findByRole("menuitem", { name: "Log out" })).toBeInTheDocument();
  });

  it("hides audit log from non-admin users", async () => {
    useAuthMock.mockReturnValue({
      user: memberUser,
      isLoading: false,
      isAuthenticated: true,
      logout: logoutMock,
    });

    const user = userEvent.setup();

    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    // Expand the settings section
    await user.click(screen.getByRole("button", { name: /Settings/ }));

    expect(screen.getByRole("link", { name: "General" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Team" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Audit log" })).not.toBeInTheDocument();
  });

  it("does not show repo context switcher when org has only 1 repo", async () => {
    server.use(
      http.get("/api/v1/repositories/summary", () => {
        return HttpResponse.json({
          data: [
            {
              repository_id: "repo-1",
              full_name: "acme/api-server",
              active_session_count: 0,
              latest_session_status: null,
              active_project_count: 0,
            },
          ],
          meta: {},
        });
      })
    );

    const { container } = renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    // Wait for the query to settle, then verify the switcher is NOT rendered
    await waitFor(() => {
      expect(container.querySelector('[data-testid="repo-context-switcher"]')).not.toBeInTheDocument();
    });
  });

  // --- New nav header tests ---

  it("displays the organization name from settings", async () => {
    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    // The default mock returns "Test Org" from /api/v1/settings
    await waitFor(() => {
      expect(screen.getByText("Test Org")).toBeInTheDocument();
    });
  });

  it("shows org name icon with first letter", async () => {
    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    await waitFor(() => {
      expect(screen.getByText("T")).toBeInTheDocument();
    });
  });

  it("falls back to 143.dev when org name is not available", async () => {
    server.use(
      http.get("/api/v1/settings", () => {
        return HttpResponse.json({
          data: {
            id: "org-1",
            name: "",
            settings: {},
          },
        });
      }),
    );

    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    // Should initially show "143.dev" as the fallback before API responds
    expect(screen.getByText("143.dev")).toBeInTheDocument();
  });

  it("has a search button that opens the command palette", async () => {
    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    expect(screen.getByRole("button", { name: "Search" })).toBeInTheDocument();
  });

  it("has a new session button", () => {
    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    expect(screen.getByRole("button", { name: "New session" })).toBeInTheDocument();
  });

  it("opens create session dialog when new session button is clicked", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    await user.click(screen.getByRole("button", { name: "New session" }));

    await waitFor(() => {
      expect(screen.getByRole("dialog")).toBeInTheDocument();
      expect(screen.getByText("New session", { selector: "[data-slot='dialog-title']" })).toBeInTheDocument();
    });
  });

  it("org name links to /autopilot", async () => {
    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    await waitFor(() => {
      const orgLink = screen.getByText("Test Org").closest("a");
      expect(orgLink).toHaveAttribute("href", "/autopilot");
    });
  });
});
