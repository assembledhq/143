import React from "react";
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
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

const preloadSessionDetailContent = vi.hoisted(() => vi.fn());

vi.mock("./[id]/session-detail-page-client", () => ({
  preloadSessionDetailContent,
}));

describe("SessionsLayout", () => {
  // jsdom has no requestIdleCallback; provide one that runs synchronously so
  // the chunk warm-up path is exercised without waiting on the timeout
  // fallback. Stubbed for the whole file (and unstubbed only after all
  // per-test unmounts have run) so the effect cleanup's cancelIdleCallback
  // call never lands after the stub is gone — even when a test fails.
  beforeAll(() => {
    vi.stubGlobal("requestIdleCallback", (cb: IdleRequestCallback) => {
      cb({ didTimeout: false, timeRemaining: () => 50 });
      return 1;
    });
    vi.stubGlobal("cancelIdleCallback", () => {});
  });

  afterAll(() => {
    vi.unstubAllGlobals();
  });

  beforeEach(() => {
    mockPathname = "/sessions";
    preloadSessionDetailContent.mockClear();
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

  it("warms the session detail chunk during idle time after mount", () => {
    renderWithProviders(
      <SessionsLayout>
        <div>Child content</div>
      </SessionsLayout>,
    );

    expect(preloadSessionDetailContent).toHaveBeenCalled();
  });
});
