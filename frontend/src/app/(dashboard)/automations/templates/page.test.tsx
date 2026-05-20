import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import AutomationTemplatesPage from "./page";

const currentUserRole = vi.hoisted(() => ({ value: "member" }));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    user: { role: currentUserRole.value },
    isLoading: false,
  }),
}));

describe("AutomationTemplatesPage", () => {
  it("renders a deeper template library with category browsing", () => {
    render(<AutomationTemplatesPage />);

    expect(screen.getByRole("heading", { name: "Automation templates" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Reliability" })).toBeInTheDocument();
    expect(screen.getByText("Find flaky tests")).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: /Use template/i }).length).toBeGreaterThan(0);
    expect(screen.getByText(/Browse examples and richer prompts/i)).toBeInTheDocument();
  });

  it("hides create links for builders", () => {
    currentUserRole.value = "builder";

    render(<AutomationTemplatesPage />);

    expect(screen.queryByRole("link", { name: "New automation" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Use template/i })).not.toBeInTheDocument();
  });
});
