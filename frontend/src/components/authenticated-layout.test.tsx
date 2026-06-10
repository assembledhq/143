import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent } from "@testing-library/react";
import { renderWithProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
import { AuthenticatedLayout, sessionDetailRouteId } from "./authenticated-layout";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";

const { replaceMock, logoutMock, useAuthMock } = vi.hoisted(() => ({
  replaceMock: vi.fn(),
  logoutMock: vi.fn(),
  useAuthMock: vi.fn(),
}));

let mockPathname = "/autopilot";

vi.mock("next/navigation", () => ({
  usePathname: () => mockPathname,
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
  mockPathname = "/autopilot";
  useAuthMock.mockReturnValue({
    user: adminUser,
    isLoading: false,
    isAuthenticated: true,
    logout: logoutMock,
  });

});

describe("AuthenticatedLayout", () => {
  it("hides projects from the primary navigation", () => {
    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    expect(screen.queryByRole("link", { name: "Projects" })).not.toBeInTheDocument();
  });

  it("shows Autopilot in the primary navigation", () => {
    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    expect(screen.getAllByRole("link", { name: "Autopilot" }).find((link) => link.getAttribute("href") === "/autopilot")).toBeDefined();
  });

  it("uses a slightly narrower default sidebar width", () => {
    const { container } = renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    const sidebar = container.querySelector("[data-testid='app-sidebar']");
    expect(sidebar).toHaveStyle({ "--app-sidebar-w": "236px" });
  });

  it("collapses the app sidebar to a slim rail between mobile and wide desktop", () => {
    const { container } = renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    const fullSidebar = container.querySelector("[data-testid='app-sidebar']");
    expect(fullSidebar).toHaveClass("hidden");
    expect(fullSidebar).toHaveClass("xl:flex");

    const compactRail = container.querySelector("[data-testid='app-sidebar-rail']");
    expect(compactRail).toHaveClass("hidden");
    expect(compactRail).toHaveClass("md:flex");
    expect(compactRail).toHaveClass("xl:hidden");
    expect(compactRail).toHaveClass("w-14");
  });

  it("renders compact rail Settings inside primary nav with matching sizing", () => {
    const { container } = renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    const compactRail = container.querySelector("[data-testid='app-sidebar-rail']");
    expect(compactRail).not.toBeNull();

    const quickActions = compactRail?.querySelector("[data-testid='app-sidebar-rail-quick-actions']");
    expect(quickActions).not.toBeNull();
    expect(within(quickActions as HTMLElement).queryByRole("link", { name: "Settings" })).toBeNull();
    expect(within(quickActions as HTMLElement).getByRole("button", { name: "Search" })).toHaveClass("h-7", "w-10");

    const primaryNav = compactRail?.querySelector("nav");
    expect(primaryNav).not.toBeNull();
    expect(primaryNav).toHaveClass("gap-0.5");

    const sessionsLink = within(primaryNav as HTMLElement).getByRole("link", { name: "Sessions" });
    expect(sessionsLink).toHaveClass("h-[30px]", "w-10");

    const settingsNavLink = within(primaryNav as HTMLElement).getByRole("link", { name: "Settings" });
    expect(settingsNavLink).toHaveClass("h-[30px]", "w-10");
    expect(settingsNavLink).not.toHaveClass("h-10");
    expect(settingsNavLink.querySelector("svg")).toHaveClass("h-4", "w-4");
  });

  it("keeps workspace and account actions reachable from the compact rail", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    await user.click(screen.getByRole("button", { name: "Open workspace menu" }));

    expect(await screen.findByText("Workspace")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Log out" })).toBeInTheDocument();
  });

  it("restores the app sidebar width from localStorage after mount", async () => {
    window.localStorage.setItem("143:app-sidebar-width", "280");

    const { container } = renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    const sidebar = container.querySelector("[data-testid='app-sidebar']");
    await waitFor(() => {
      expect(sidebar).toHaveStyle({ "--app-sidebar-w": "280px" });
    });
  });

  it("persists app sidebar resize and clamps it to the max", () => {
    const { container } = renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    const sidebar = container.querySelector("[data-testid='app-sidebar']");
    const handle = container.querySelector("[data-testid='app-sidebar-resize-handle']");
    expect(handle).not.toBeNull();

    fireEvent.pointerDown(handle!, { clientX: 100, pointerId: 1, button: 0 });
    fireEvent.pointerMove(document, { clientX: 220, pointerId: 1 });
    fireEvent.pointerUp(document, { pointerId: 1 });

    expect(sidebar).toHaveStyle({ "--app-sidebar-w": "300px" });
    expect(window.localStorage.getItem("143:app-sidebar-width")).toBe("300");
  });

  it("uses a full-width content area with responsive padding", () => {
    const { container } = renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    const contentWrapper = container.querySelector("main > div:last-child");
    expect(contentWrapper).toHaveClass("max-w-none");
    // Mobile-first: tight padding by default, wider on larger breakpoints.
    expect(contentWrapper).toHaveClass("px-4");
    expect(contentWrapper).toHaveClass("sm:px-6");
    expect(contentWrapper).toHaveClass("lg:px-10");
    expect(contentWrapper).toHaveClass("py-5");
    expect(contentWrapper).toHaveClass("sm:py-6");
  });

  it("content wrapper supports full-height children via flex-1 and min-h-0", () => {
    const { container } = renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    const contentWrapper = container.querySelector("main > div:last-child");
    expect(contentWrapper).toHaveClass("flex-1");
    expect(contentWrapper).toHaveClass("min-h-0");
  });

  it("pins the authenticated app shell to the visual viewport and contains overscroll", () => {
    const { container } = renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    const appShell = container.firstElementChild;
    expect(appShell).toHaveClass("fixed");
    expect(appShell).toHaveClass("inset-0");
    expect(appShell).toHaveClass("overflow-hidden");
    expect(appShell).toHaveClass("overscroll-none");

    const main = container.querySelector("main");
    expect(main).toHaveClass("overscroll-contain");
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
    expect(screen.getAllByRole("link", { name: "Autopilot" }).find((link) => link.getAttribute("href") === "/settings/autopilot")).toBeDefined();
    expect(screen.getByRole("link", { name: "Runtime" })).toHaveAttribute("href", "/settings/runtime");
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

  it("hides admin-only settings entries from non-admin users", async () => {
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

    // Members can see Team and the read-only pages (Integrations, Coding agents, Evals).
    expect(screen.getByRole("link", { name: "Team" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Integrations" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Coding agents" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Evals" })).toBeInTheDocument();

    // Admin-only entries are hidden.
    expect(screen.queryByRole("link", { name: "General" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "LLM" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Runtime" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Audit log" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Usage" })).not.toBeInTheDocument();
  });

  it("hides Autopilot settings from non-admin users", async () => {
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

    await user.click(screen.getByRole("button", { name: /Settings/ }));

    expect(screen.queryAllByRole("link", { name: "Autopilot" }).filter((link) => link.getAttribute("href") === "/settings/autopilot")).toHaveLength(0);
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

  it("displays the active organization name from memberships", async () => {
    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    // Default mock: /api/v1/auth/memberships returns "Test Org"
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

  it("has a search button that opens the command palette", async () => {
    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    // Rendered in both the desktop sidebar and the mobile top bar.
    expect(screen.getAllByRole("button", { name: "Search" }).length).toBeGreaterThan(0);
  });

  it("has a new session button", () => {
    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    expect(screen.getAllByRole("button", { name: "New session" }).length).toBeGreaterThan(0);
  });

  it("hides the global mobile header on session detail routes", () => {
    mockPathname = "/sessions/session-123";

    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    expect(screen.queryByRole("button", { name: "Open navigation menu" })).not.toBeInTheDocument();
  });

  it("opens create session dialog when new session button is clicked", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    // Click the first "New session" trigger (desktop sidebar); both triggers
    // wire up to the same handler.
    await user.click(screen.getAllByRole("button", { name: "New session" })[0]);

    await waitFor(() => {
      expect(screen.getByRole("dialog")).toBeInTheDocument();
      expect(screen.getByText("New session", { selector: "[data-slot='dialog-title']" })).toBeInTheDocument();
    });
  });

  it("org name is an org-switcher dropdown trigger", async () => {
    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    await waitFor(() => {
      const trigger = screen.getByTestId("org-switcher");
      expect(trigger).toHaveTextContent("Test Org");
      expect(trigger.getAttribute("aria-haspopup")).not.toBeNull();
    });
  });

  describe("mobile navigation drawer", () => {
    it("hamburger opens the drawer and reflects state via aria-expanded", async () => {
      const user = userEvent.setup();

      renderWithProviders(
        <AuthenticatedLayout>
          <div>content</div>
        </AuthenticatedLayout>
      );

      const hamburger = screen.getByRole("button", { name: "Open navigation menu" });
      expect(hamburger).toHaveAttribute("aria-expanded", "false");
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

      await user.click(hamburger);

      const drawer = await screen.findByRole("dialog");
      expect(drawer).toBeInTheDocument();
      expect(hamburger).toHaveAttribute("aria-expanded", "true");
      // The drawer exposes an accessible name for screen readers.
      expect(within(drawer).getByText("Navigation")).toBeInTheDocument();
    });

    it("closes the drawer when a primary nav link is tapped", async () => {
      const user = userEvent.setup();

      renderWithProviders(
        <AuthenticatedLayout>
          <div>content</div>
        </AuthenticatedLayout>
      );

      await user.click(screen.getByRole("button", { name: "Open navigation menu" }));
      const drawer = await screen.findByRole("dialog");

      // Click a nav link inside the drawer (scope with `within` to avoid
      // matching the desktop sidebar's duplicate link).
      await user.click(within(drawer).getByRole("link", { name: "Sessions" }));

      await waitFor(() => {
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      });
    });

    it("closes the drawer when a Settings sub-link is tapped", async () => {
      const user = userEvent.setup();

      renderWithProviders(
        <AuthenticatedLayout>
          <div>content</div>
        </AuthenticatedLayout>
      );

      await user.click(screen.getByRole("button", { name: "Open navigation menu" }));
      const drawer = await screen.findByRole("dialog");

      // Expand the Settings group inside the drawer, then tap a sub-link.
      // This verifies onNavigate threads through SidebarSettingsSection.
      await user.click(within(drawer).getByRole("button", { name: /Settings/ }));
      await user.click(within(drawer).getByRole("link", { name: "Account" }));

      await waitFor(() => {
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      });
    });

    it("uses a compact close control in the mobile drawer header", async () => {
      const user = userEvent.setup();

      renderWithProviders(
        <AuthenticatedLayout>
          <div>content</div>
        </AuthenticatedLayout>
      );

      await user.click(screen.getByRole("button", { name: "Open navigation menu" }));
      await screen.findByRole("dialog");

      const closeButton = screen.getByRole("button", { name: "Close navigation menu" });
      expect(closeButton).toHaveClass("h-9", "w-9");

      const closeIcon = closeButton.querySelector("svg");
      expect(closeIcon).toHaveClass("h-4", "w-4");
    });

    it("does not close the drawer on modifier-clicks (cmd/ctrl/middle)", async () => {
      const user = userEvent.setup();

      renderWithProviders(
        <AuthenticatedLayout>
          <div>content</div>
        </AuthenticatedLayout>
      );

      await user.click(screen.getByRole("button", { name: "Open navigation menu" }));
      const drawer = await screen.findByRole("dialog");

      // Cmd/Ctrl click opens the link in a new tab — current page is unchanged
      // for the user, so the drawer should stay put.
      await user.keyboard("{Meta>}");
      await user.click(within(drawer).getByRole("link", { name: "Sessions" }));
      await user.keyboard("{/Meta}");

      // Drawer is still in the DOM after a deliberate wait.
      await new Promise((r) => setTimeout(r, 50));
      expect(screen.queryByRole("dialog")).toBeInTheDocument();
    });
  });

  describe("sessionDetailRouteId", () => {
    const sessionId = "93cbcc8c-c12e-4bdf-8622-5865502c8977";

    it("extracts a uuid session id from a session detail path", () => {
      expect(sessionDetailRouteId(`/sessions/${sessionId}`)).toBe(sessionId);
    });

    it("returns null for everything that is not /sessions/<uuid>", () => {
      expect(sessionDetailRouteId("/autopilot")).toBeNull();
      expect(sessionDetailRouteId("/sessions")).toBeNull();
      expect(sessionDetailRouteId("/sessions/new")).toBeNull();
      expect(sessionDetailRouteId("/sessions/not-a-uuid")).toBeNull();
      expect(sessionDetailRouteId(`/sessions/${sessionId}/extra`)).toBeNull();
    });
  });

  describe("auth-gate session detail prefetch", () => {
    const sessionId = "93cbcc8c-c12e-4bdf-8622-5865502c8977";
    const loadingAuth = {
      user: null,
      isLoading: true,
      isFetching: true,
      isAuthenticated: false,
      isUnauthorized: false,
      isTransientError: false,
      refetchUser: vi.fn(),
      logout: logoutMock,
    };

    function trackSessionDetailRequests(): string[] {
      const requests: string[] = [];
      server.use(
        http.get("/api/v1/sessions/:id", ({ params }) => {
          requests.push(String(params.id));
          return HttpResponse.json({ data: { id: params.id, threads: [] } });
        })
      );
      return requests;
    }

    it("prefetches the session detail while /auth/me is still in flight", async () => {
      mockPathname = `/sessions/${sessionId}`;
      useAuthMock.mockReturnValue(loadingAuth);
      const requests = trackSessionDetailRequests();

      renderWithProviders(
        <AuthenticatedLayout>
          <div>content</div>
        </AuthenticatedLayout>
      );

      await waitFor(() => {
        expect(requests).toEqual([sessionId]);
      });
    });

    it("does not prefetch when auth has already settled", async () => {
      mockPathname = `/sessions/${sessionId}`;
      const requests = trackSessionDetailRequests();

      renderWithProviders(
        <AuthenticatedLayout>
          <div>content</div>
        </AuthenticatedLayout>
      );

      await new Promise((r) => setTimeout(r, 50));
      expect(requests).toEqual([]);
    });

    it("does not prefetch on non-session routes while auth loads", async () => {
      mockPathname = "/autopilot";
      useAuthMock.mockReturnValue(loadingAuth);
      const requests = trackSessionDetailRequests();

      renderWithProviders(
        <AuthenticatedLayout>
          <div>content</div>
        </AuthenticatedLayout>
      );

      await new Promise((r) => setTimeout(r, 50));
      expect(requests).toEqual([]);
    });
  });
});
