import React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
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

let mockPathname = "/sessions";

vi.mock("next/navigation", () => ({
  usePathname: () => mockPathname,
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
  beforeEach(() => {
    mockPathname = "/sessions";
  });

  it("shows the sidebar pane on mobile for the /sessions route", () => {
    renderWithProviders(
      <SessionsLayout>
        <div>Child content</div>
      </SessionsLayout>,
    );

    expect(screen.getByTestId("sidebar-layout")).toHaveAttribute("data-mobile-show", "sidebar");
  });

  it("shows the content pane on mobile for the /sessions/new route", () => {
    mockPathname = "/sessions/new";

    renderWithProviders(
      <SessionsLayout>
        <div>Child content</div>
      </SessionsLayout>,
    );

    expect(screen.getByTestId("sidebar-layout")).toHaveAttribute("data-mobile-show", "content");
  });

  it("shows the content pane on mobile for session detail routes", () => {
    mockPathname = "/sessions/session-123";

    renderWithProviders(
      <SessionsLayout>
        <div>Child content</div>
      </SessionsLayout>,
    );

    expect(screen.getByTestId("sidebar-layout")).toHaveAttribute("data-mobile-show", "content");
  });
});
