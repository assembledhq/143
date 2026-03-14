import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { AuthenticatedLayout } from "./authenticated-layout";

const { pushMock, replaceMock, logoutMock } = vi.hoisted(() => ({
  pushMock: vi.fn(),
  replaceMock: vi.fn(),
  logoutMock: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  usePathname: () => "/overview",
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

  it("shows all settings entries in the user menu", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    await user.click(screen.getByRole("button", { name: /Alex Doe/ }));

    expect(await screen.findByRole("menuitem", { name: "General" })).toBeInTheDocument();
    expect(await screen.findByRole("menuitem", { name: "Integrations" })).toBeInTheDocument();
    expect(await screen.findByRole("menuitem", { name: "Coding Agent" })).toBeInTheDocument();
    expect(await screen.findByRole("menuitem", { name: "Prioritization" })).toBeInTheDocument();
    expect(await screen.findByRole("menuitem", { name: "Team" })).toBeInTheDocument();
  });

  it("routes to settings pages from the user menu", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    await user.click(screen.getByRole("button", { name: /Alex Doe/ }));
    await user.click(await screen.findByRole("menuitem", { name: "Team" }));

    expect(pushMock).toHaveBeenCalledWith("/team");

    await user.click(screen.getByRole("button", { name: /Alex Doe/ }));
    await user.click(await screen.findByRole("menuitem", { name: "General" }));

    expect(pushMock).toHaveBeenCalledWith("/settings");

    await user.click(screen.getByRole("button", { name: /Alex Doe/ }));
    await user.click(await screen.findByRole("menuitem", { name: "Coding Agent" }));

    expect(pushMock).toHaveBeenCalledWith("/agent");
  });
});
