import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import DashboardLayout from "./layout";

vi.mock("@/components/authenticated-layout", () => ({
  AuthenticatedLayout: ({ children }: { children: React.ReactNode }) => (
    <div data-testid="authenticated-layout">{children}</div>
  ),
}));

describe("DashboardLayout", () => {
  it("does not wrap children in a page container", () => {
    render(
      <DashboardLayout>
        <div>Dashboard child content</div>
      </DashboardLayout>
    );

    expect(screen.getByTestId("authenticated-layout")).toBeInTheDocument();
    expect(screen.getByText("Dashboard child content")).toBeInTheDocument();
    expect(screen.queryByTestId("page-container")).not.toBeInTheDocument();
  });
});
