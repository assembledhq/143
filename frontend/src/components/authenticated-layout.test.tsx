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
  it("shows Team and organization settings entries in the user menu", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <AuthenticatedLayout>
        <div>content</div>
      </AuthenticatedLayout>
    );

    await user.click(screen.getByRole("button", { name: /Alex Doe/ }));

    expect(await screen.findByRole("menuitem", { name: "Team" })).toBeInTheDocument();
    expect(await screen.findByRole("menuitem", { name: "Organization Settings" })).toBeInTheDocument();
  });

  it("routes to Team and organization settings from the user menu", async () => {
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
    await user.click(await screen.findByRole("menuitem", { name: "Organization Settings" }));

    expect(pushMock).toHaveBeenCalledWith("/settings");
  });
});
