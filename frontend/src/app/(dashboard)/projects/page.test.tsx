import { describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import ProjectsPage from "./page";

const currentUserRole = vi.hoisted(() => ({ value: "member" }));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    user: { role: currentUserRole.value },
    isLoading: false,
  }),
}));

describe("ProjectsPage", () => {
  it("shows the new-project action for members", () => {
    currentUserRole.value = "member";

    renderWithProviders(<ProjectsPage />);

    expect(screen.getByRole("link", { name: /new project/i })).toHaveAttribute("href", "/projects/new");
  });

  it("hides the new-project action from builders", () => {
    currentUserRole.value = "builder";

    renderWithProviders(<ProjectsPage />);

    expect(screen.queryByRole("link", { name: /new project/i })).not.toBeInTheDocument();
  });
});
