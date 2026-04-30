import React from "react";
import { describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import SessionsLayout from "./layout";

const sidebarLayoutMock = vi.fn(
  ({
    mobileShow,
    children,
  }: {
    mobileShow?: "sidebar" | "content";
    children: React.ReactNode;
  }) => (
    <div data-testid="sidebar-layout" data-mobile-show={mobileShow}>
      {children}
    </div>
  ),
);

vi.mock("next/navigation", () => ({
  usePathname: () => "/sessions",
}));

vi.mock("@/components/sidebar-layout", () => ({
  SidebarLayout: (props: {
    sidebar: React.ReactNode;
    children: React.ReactNode;
    mobileShow?: "sidebar" | "content";
  }) => sidebarLayoutMock(props),
}));

vi.mock("./session-sidebar", () => ({
  SessionSidebar: () => <div data-testid="session-sidebar" />,
}));

describe("SessionsLayout", () => {
  it("shows the content pane on mobile for the /sessions route", () => {
    renderWithProviders(
      <SessionsLayout>
        <div>Child content</div>
      </SessionsLayout>,
    );

    expect(screen.getByTestId("sidebar-layout")).toHaveAttribute("data-mobile-show", "content");
  });
});
