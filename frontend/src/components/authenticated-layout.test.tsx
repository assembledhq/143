import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { AuthenticatedLayout } from "./authenticated-layout";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";

const { pushMock, replaceMock, logoutMock } = vi.hoisted(() => ({
  pushMock: vi.fn(),
  replaceMock: vi.fn(),
  logoutMock: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  usePathname: () => "/autopilot",
  useRouter: () => ({
    push: pushMock,
    replace: replaceMock,
  }),
}));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    user: {
      id: "user-1",
      name: "Alex Doe",
      email: "alex@example.com",
      role: "admin",
    },
    isLoading: false,
    isAuthenticated: true,
    logout: logoutMock,
  }),
}));

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

    expect(screen.getByRole("link", { name: "General" })).toHaveAttribute("href", "/settings");
    expect(screen.getByRole("link", { name: "Integrations" })).toHaveAttribute("href", "/settings/integrations");
    expect(screen.getByRole("link", { name: "Coding agents" })).toHaveAttribute("href", "/settings/agent");
    expect(screen.getByRole("link", { name: "LLM" })).toHaveAttribute("href", "/settings/llm");
    expect(screen.getByRole("link", { name: "Autopilot" })).toHaveAttribute("href", "/settings/autopilot");
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
});
