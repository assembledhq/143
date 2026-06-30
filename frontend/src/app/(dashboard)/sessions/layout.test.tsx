import React from "react";
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, renderWithProviders, screen, waitFor } from "@/test/test-utils";
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
let mockSelectedSegment: string | null = null;
let mockSelectedSegments: string[] = [];
const mockRouterReplace = vi.hoisted(() => vi.fn());
const mockAuthUser = vi.hoisted<{ value: { role: string } | null }>(() => ({ value: null }));

function mockSegmentsFromPathname() {
  if (mockSelectedSegments.length > 0) return mockSelectedSegments;
  if (mockSelectedSegment) return [mockSelectedSegment];
  const [, root, ...segments] = mockPathname.split("/");
  return root === "sessions" ? segments.filter(Boolean) : [];
}

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: mockRouterReplace, back: vi.fn(), prefetch: vi.fn() }),
  usePathname: () => mockPathname,
  useSelectedLayoutSegment: () => mockSelectedSegment,
  useSelectedLayoutSegments: () => mockSegmentsFromPathname(),
}));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    user: mockAuthUser.value,
    isLoading: false,
    isFetching: false,
    isAuthenticated: Boolean(mockAuthUser.value),
    isUnauthorized: false,
    isTransientError: false,
    refetchUser: vi.fn(),
    logout: vi.fn(),
  }),
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
  SessionDetailPageClient: ({ id }: { id: string }) => {
    const [draft, setDraft] = React.useState("");
    return (
      <div data-testid="session-detail-page-client" data-session-id={id}>
        <label htmlFor={`detail-draft-${id}`}>Detail draft</label>
        <input
          id={`detail-draft-${id}`}
          aria-label="Detail draft"
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
        />
      </div>
    );
  },
  preloadSessionDetailContent,
}));

vi.mock("./new/manual-session-create-page-content", () => ({
  ManualSessionCreatePageContent: () => <div data-testid="manual-session-create-page" />,
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
    mockSelectedSegment = null;
    mockSelectedSegments = [];
    mockAuthUser.value = null;
    mockRouterReplace.mockClear();
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
    mockSelectedSegment = "new";
    mockSelectedSegments = ["new"];

    renderWithProviders(
      <SessionsLayout>
        <div>Child content</div>
      </SessionsLayout>,
    );

    expect(screen.getByTestId("sidebar-layout")).toHaveAttribute("data-mobile-show", "content");
  });

  it("shows the content pane on mobile for session detail routes", () => {
    mockPathname = "/sessions/session-123";
    mockSelectedSegment = "session-123";
    mockSelectedSegments = ["session-123"];

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

  it("defaults the bare /sessions route to the create-session content", () => {
    renderWithProviders(
      <SessionsLayout>
        <div>Legacy child content</div>
      </SessionsLayout>,
    );

    expect(screen.getByTestId("manual-session-create-page")).toBeInTheDocument();
    expect(screen.queryByText("Select a session")).not.toBeInTheDocument();
    expect(screen.queryByText("Legacy child content")).not.toBeInTheDocument();
  });

  it("redirects viewers away from create-session content", async () => {
    mockAuthUser.value = { role: "viewer" };
    mockPathname = "/sessions/new";
    mockSelectedSegment = "new";
    mockSelectedSegments = ["new"];

    renderWithProviders(
      <SessionsLayout>
        <div>Legacy child content</div>
      </SessionsLayout>,
    );

    expect(screen.getByText("Choose a seeded session")).toBeInTheDocument();
    expect(screen.queryByTestId("manual-session-create-page")).not.toBeInTheDocument();
    await waitFor(() => {
      expect(mockRouterReplace).toHaveBeenCalledWith("/demo");
    });
  });

  it("owns the create-session content on the /sessions/new route", () => {
    mockPathname = "/sessions/new";
    mockSelectedSegment = "new";
    mockSelectedSegments = ["new"];

    renderWithProviders(
      <SessionsLayout>
        <div>Legacy child content</div>
      </SessionsLayout>,
    );

    expect(screen.getByTestId("manual-session-create-page")).toBeInTheDocument();
    expect(screen.queryByText("Legacy child content")).not.toBeInTheDocument();
  });

  it("owns the selected session detail content and keys it by selected id", () => {
    mockPathname = "/sessions/session-123";
    mockSelectedSegment = "session-123";
    mockSelectedSegments = ["session-123"];

    const { rerender } = renderWithProviders(
      <SessionsLayout>
        <div>Legacy child content</div>
      </SessionsLayout>,
    );

    expect(screen.getByTestId("session-detail-page-client")).toHaveAttribute("data-session-id", "session-123");
    expect(screen.queryByText("Legacy child content")).not.toBeInTheDocument();

    mockPathname = "/sessions/session-456";
    mockSelectedSegment = "session-456";
    mockSelectedSegments = ["session-456"];
    rerender(
      <SessionsLayout>
        <div>Legacy child content</div>
      </SessionsLayout>,
    );

    expect(screen.getByTestId("session-detail-page-client")).toHaveAttribute("data-session-id", "session-456");
  });

  it("keeps the detail shell mounted when the selected session id changes", () => {
    mockPathname = "/sessions/session-123";
    mockSelectedSegment = "session-123";
    mockSelectedSegments = ["session-123"];

    const { rerender } = renderWithProviders(
      <SessionsLayout>
        <div>Legacy child content</div>
      </SessionsLayout>,
    );

    const draft = screen.getByLabelText("Detail draft");
    fireEvent.change(draft, { target: { value: "shell state survives" } });
    expect(screen.getByLabelText("Detail draft")).toHaveValue("shell state survives");

    mockPathname = "/sessions/session-456";
    mockSelectedSegment = "session-456";
    mockSelectedSegments = ["session-456"];
    rerender(
      <SessionsLayout>
        <div>Legacy child content</div>
      </SessionsLayout>,
    );

    expect(screen.getByTestId("session-detail-page-client")).toHaveAttribute("data-session-id", "session-456");
    expect(screen.getByLabelText("Detail draft")).toHaveValue("shell state survives");
  });

  it("replaces session detail with create content when navigating to /sessions/new", () => {
    mockPathname = "/sessions/session-123";
    mockSelectedSegment = "session-123";
    mockSelectedSegments = ["session-123"];

    const { rerender } = renderWithProviders(
      <SessionsLayout>
        <div>Legacy child content</div>
      </SessionsLayout>,
    );

    expect(screen.getByTestId("session-detail-page-client")).toBeInTheDocument();

    mockPathname = "/sessions/new";
    mockSelectedSegment = "new";
    mockSelectedSegments = ["new"];
    rerender(
      <SessionsLayout>
        <div>Legacy child content</div>
      </SessionsLayout>,
    );

    expect(screen.getByTestId("manual-session-create-page")).toBeInTheDocument();
    expect(screen.queryByTestId("session-detail-page-client")).not.toBeInTheDocument();
  });

  it("renders an unsupported route state for nested sessions routes", () => {
    mockPathname = "/sessions/session-123/diff";
    mockSelectedSegment = "session-123";
    mockSelectedSegments = ["session-123", "diff"];

    renderWithProviders(
      <SessionsLayout>
        <div>Legacy child content</div>
      </SessionsLayout>,
    );

    expect(screen.getByText("Unsupported sessions route")).toBeInTheDocument();
    expect(screen.queryByTestId("session-detail-page-client")).not.toBeInTheDocument();
  });
});
