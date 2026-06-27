import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { act } from "@testing-library/react";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { mockSessions } from "@/test/mocks/handlers";
import type { ListResponse, Session, SessionTimelineEntry, SingleResponse } from "@/lib/types";

const { chatTimelineRenderState, codeReviewBarrelLoadState, recordReviewDiffViewRender, recordFileTreeRender } = vi.hoisted(() => ({
  chatTimelineRenderState: { count: 0 },
  codeReviewBarrelLoadState: { loads: 0 },
  recordReviewDiffViewRender: vi.fn(),
  recordFileTreeRender: vi.fn(),
}));

vi.mock("@/components/chat-timeline", () => ({
  ChatTimeline: ({ entries }: { entries: unknown[] }) => {
    chatTimelineRenderState.count += 1;
    return <div data-testid="chat-timeline-mock">{entries.length}</div>;
  },
}));

vi.mock("@/components/code-review/review-diff-view", async () => {
  const { memo } = await vi.importActual<typeof import("react")>("react");
  const ReviewDiffView = memo(function MockReviewDiffView({ files }: { files: unknown[] }) {
    recordReviewDiffViewRender(files.length);
    return <div data-testid="review-diff-view-mock">{files.length}</div>;
  });
  return { ReviewDiffView };
});

vi.mock("@/components/code-review", async (importOriginal) => {
  codeReviewBarrelLoadState.loads += 1;
  const actual = await importOriginal<typeof import("@/components/code-review")>();
  const { memo } = await vi.importActual<typeof import("react")>("react");
  const FileTree = memo(function MockFileTree({ files }: { files: unknown[] }) {
    recordFileTreeRender(files.length);
    return <div data-testid="file-tree-mock">{files.length}</div>;
  });
  return { ...actual, FileTree };
});

vi.mock("@/lib/notify", () => ({
  notify: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({
    push: vi.fn(),
  }),
  useSearchParams: () => new URLSearchParams(),
}));

vi.mock("next/link", () => ({
  default: ({ children, href, ...props }: React.ComponentProps<"a"> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

vi.mock("next/image", () => ({
  default: ({ src, alt, className, width, height }: { src: string; alt: string; className?: string; width?: number; height?: number }) => (
    <span data-next-image={src} aria-label={alt} className={className} data-width={width} data-height={height} />
  ),
}));

class MockEventSource {
  static instances: MockEventSource[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSED = 2;
  readyState = 0;
  url: string;
  withCredentials = false;
  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;

  constructor(url: string | URL) {
    this.url = String(url);
    MockEventSource.instances.push(this);
  }

  addEventListener = vi.fn();
  removeEventListener = vi.fn();
  close = vi.fn();
  dispatchEvent = vi.fn(() => true);
}

function setMobileViewport(matches: boolean) {
  Object.defineProperty(window, "matchMedia", {
    writable: true,
    configurable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: query === "(max-width: 767px)" ? matches : false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  });
}

beforeAll(async () => {
  await import("@/components/code-review/review-diff-view");
  global.EventSource = MockEventSource as unknown as typeof EventSource;
  setMobileViewport(false);
});

beforeEach(() => {
  chatTimelineRenderState.count = 0;
  codeReviewBarrelLoadState.loads = 0;
  recordReviewDiffViewRender.mockClear();
  recordFileTreeRender.mockClear();
  MockEventSource.instances = [];
  window.history.pushState(null, "", "/");
  window.localStorage.clear();
  setMobileViewport(false);
});

const reviewPerfDiff = [
  "diff --git a/src/app.ts b/src/app.ts",
  "--- a/src/app.ts",
  "+++ b/src/app.ts",
  "@@ -1 +1 @@",
  "-old",
  "+new",
  "diff --git a/src/utils.ts b/src/utils.ts",
  "--- a/src/utils.ts",
  "+++ b/src/utils.ts",
  "@@ -1 +1 @@",
  "-old",
  "+new",
  "",
].join("\n");

function installSessionWithDiffHandlers() {
  server.use(
    http.get("/api/v1/sessions/:id", () => {
      return HttpResponse.json({
        data: {
          ...mockSessions[0],
          primary_issue_id: undefined,
          sandbox_state: "ready",
          diff: undefined,
          diff_stats: { added: 2, removed: 2, files_changed: 2 },
          latest_diff_snapshot_id: "snapshot-1",
          diff_collected_at: "2026-02-17T07:05:00Z",
        },
      } satisfies SingleResponse<Session>);
    }),
    http.get("/api/v1/sessions/:id/timeline", () => {
      return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionTimelineEntry>);
    }),
    http.get("/api/v1/sessions/:id/diff", () => {
      return HttpResponse.json({
        data: {
          session_id: "session-abcdef12-3456-7890",
          diff: reviewPerfDiff,
          diff_stats: { added: 2, removed: 2, files_changed: 2 },
          diff_history: [],
          diff_truncated: false,
          diff_history_truncated: false,
        },
      });
    }),
  );
}

describe("SessionDetailContent performance", () => {
  it("does not load code review panel modules for the initial chat surface", async () => {
    const { SessionDetailContent } = await import("./session-detail-content");

    server.use(
      http.get("/api/v1/sessions/:id", () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            primary_issue_id: undefined,
            sandbox_state: "ready",
            diff: undefined,
            diff_stats: { added: 2, removed: 2, files_changed: 2 },
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get("/api/v1/sessions/:id/timeline", () => {
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findByPlaceholderText("Send a follow-up message...");
    expect(codeReviewBarrelLoadState.loads).toBe(0);
  });

  it("does not rerender the transcript while typing in the follow-up composer", async () => {
    const { SessionDetailContent } = await import("./session-detail-content");
    const user = userEvent.setup();

    server.use(
      http.get("/api/v1/sessions/:id", () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            primary_issue_id: undefined,
            sandbox_state: "ready",
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get("/api/v1/sessions/:id/timeline", () => {
        return HttpResponse.json({
          data: [
            {
              kind: "message",
              created_at: "2026-02-17T07:00:00Z",
              message: {
                id: 101,
                session_id: "session-abcdef12-3456-7890",
                org_id: "org-1",
                turn_number: 1,
                role: "assistant",
                content: "Initial transcript entry",
                created_at: "2026-02-17T07:00:00Z",
              },
            },
          ],
          meta: {},
        } satisfies ListResponse<SessionTimelineEntry>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const textarea = await screen.findByPlaceholderText("Send a follow-up message...");
    expect(await screen.findByTestId("chat-timeline-mock")).toBeInTheDocument();

    chatTimelineRenderState.count = 0;

    await user.type(textarea, "Lag");

    expect(textarea).toHaveValue("Lag");
    expect(chatTimelineRenderState.count).toBe(0);
  }, 30000);

  it("does not rerender the active review diff while typing in the follow-up composer", async () => {
    const { SessionDetailContent } = await import("./session-detail-content");
    const user = userEvent.setup();
    installSessionWithDiffHandlers();

    renderWithProviders(
      <SessionDetailContent id="session-abcdef12-3456-7890" />,
      { searchParams: { review: "active" } },
    );

    const textarea = await screen.findByPlaceholderText("Send a follow-up message...");
    expect(await screen.findByTestId("review-diff-view-mock")).toBeInTheDocument();

    recordReviewDiffViewRender.mockClear();

    await user.type(textarea, "Lag");

    expect(textarea).toHaveValue("Lag");
    expect(recordReviewDiffViewRender).not.toHaveBeenCalled();
  }, 30000);

  it("does not rerender the changes file tree while typing in the follow-up composer", async () => {
    const { SessionDetailContent } = await import("./session-detail-content");
    const user = userEvent.setup();
    installSessionWithDiffHandlers();

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const textarea = await screen.findByPlaceholderText("Send a follow-up message...");
    await user.click(screen.getByRole("tab", { name: /Changes/i }));
    expect(await screen.findByTestId("file-tree-mock")).toBeInTheDocument();

    recordFileTreeRender.mockClear();

    await user.type(textarea, "Lag");

    expect(textarea).toHaveValue("Lag");
    expect(recordFileTreeRender).not.toHaveBeenCalled();
  }, 30000);

  it("loads the raw diff only after the changes surface is opened", async () => {
    const { SessionDetailContent } = await import("./session-detail-content");
    const user = userEvent.setup();
    let diffRequestCount = 0;

    server.use(
      http.get("/api/v1/sessions/:id", () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            primary_issue_id: undefined,
            sandbox_state: "ready",
            diff: undefined,
            diff_stats: { added: 34083, removed: 176, files_changed: 3594 },
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get("/api/v1/sessions/:id/timeline", () => {
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
      http.get("/api/v1/sessions/:id/diff", () => {
        diffRequestCount += 1;
        return HttpResponse.json({
          data: {
            session_id: "session-abcdef12-3456-7890",
            diff: "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n",
            diff_stats: { added: 1, removed: 1, files_changed: 1 },
            diff_history: [],
            diff_truncated: false,
            diff_history_truncated: false,
          },
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findByPlaceholderText("Send a follow-up message...");
    expect(diffRequestCount).toBe(0);

    await user.click(screen.getByRole("tab", { name: /Changes/i }));

    await waitFor(() => {
      expect(diffRequestCount).toBe(1);
    });
  });

  it("starts loading the raw diff before session detail finishes for direct review links", async () => {
    const { SessionDetailContent } = await import("./session-detail-content");
    let releaseSession: () => void = () => {};
    const sessionGate = new Promise<void>((resolve) => {
      releaseSession = resolve;
    });
    let diffRequestCount = 0;

    server.use(
      http.get("/api/v1/sessions/:id", async () => {
        await sessionGate;
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            primary_issue_id: undefined,
            sandbox_state: "ready",
            diff: undefined,
            diff_stats: { added: 2, removed: 2, files_changed: 2 },
            latest_diff_snapshot_id: "snapshot-1",
            diff_collected_at: "2026-02-17T07:05:00Z",
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get("/api/v1/sessions/:id/timeline", () => {
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
      http.get("/api/v1/sessions/:id/diff", () => {
        diffRequestCount += 1;
        return HttpResponse.json({
          data: {
            session_id: "session-abcdef12-3456-7890",
            diff: reviewPerfDiff,
            diff_stats: { added: 2, removed: 2, files_changed: 2 },
            diff_history: [],
            diff_truncated: false,
            diff_history_truncated: false,
          },
        });
      }),
    );

    renderWithProviders(
      <SessionDetailContent id="session-abcdef12-3456-7890" />,
      { searchParams: { review: "active" } },
    );

    await waitFor(() => {
      expect(diffRequestCount).toBe(1);
    });

    releaseSession();
    expect(await screen.findByTestId("review-diff-view-mock")).toBeInTheDocument();
    expect(diffRequestCount).toBe(1);
  });

  it("does not refetch the diff when active detail polling only changes collection time", async () => {
    const { SessionDetailContent } = await import("./session-detail-content");
    let sessionRequestCount = 0;
    let diffRequestCount = 0;

    server.use(
      http.get("/api/v1/sessions/:id", () => {
        sessionRequestCount += 1;
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            primary_issue_id: undefined,
            status: "running",
            sandbox_state: "running",
            diff: undefined,
            diff_stats: { added: 2, removed: 2, files_changed: 2 },
            latest_diff_snapshot_id: "snapshot-1",
            diff_collected_at: `2026-02-17T07:05:0${Math.min(sessionRequestCount, 9)}Z`,
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get("/api/v1/sessions/:id/timeline", () => {
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
      http.get("/api/v1/sessions/:id/diff", () => {
        diffRequestCount += 1;
        return HttpResponse.json({
          data: {
            session_id: "session-abcdef12-3456-7890",
            diff: reviewPerfDiff,
            diff_stats: { added: 2, removed: 2, files_changed: 2 },
            diff_history: [],
            diff_truncated: false,
            diff_history_truncated: false,
          },
        });
      }),
    );

    renderWithProviders(
      <SessionDetailContent id="session-abcdef12-3456-7890" />,
      { searchParams: { review: "active" } },
    );

    await screen.findByTestId("review-diff-view-mock");
    await waitFor(() => {
      expect(sessionRequestCount).toBeGreaterThanOrEqual(2);
    });

    expect(diffRequestCount).toBe(1);
  });

  it("does not load the hidden transcript before direct review links render the diff", async () => {
    const { SessionDetailContent } = await import("./session-detail-content");
    let timelineRequestCount = 0;
    installSessionWithDiffHandlers();

    server.use(
      http.get("/api/v1/sessions/:id/timeline", () => {
        timelineRequestCount += 1;
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
    );

    renderWithProviders(
      <SessionDetailContent id="session-abcdef12-3456-7890" />,
      { searchParams: { review: "active" } },
    );

    expect(await screen.findByTestId("review-diff-view-mock")).toBeInTheDocument();
    expect(timelineRequestCount).toBe(0);
  });

  it("defers the hidden transcript after client-side navigation from chat to a direct review link", async () => {
    const { SessionDetailContent } = await import("./session-detail-content");
    const reviewSessionId = "session-review-next";
    let timelineRequestCount = 0;

    server.use(
      http.get("/api/v1/sessions/:id", ({ params }) => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            id: String(params.id),
            primary_issue_id: undefined,
            sandbox_state: "ready",
            diff: undefined,
            diff_stats: { added: 2, removed: 2, files_changed: 2 },
            latest_diff_snapshot_id: `snapshot-${String(params.id)}`,
            diff_collected_at: "2026-02-17T07:05:00Z",
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get("/api/v1/sessions/:id/timeline", () => {
        timelineRequestCount += 1;
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
      http.get("/api/v1/sessions/:id/diff", ({ params }) => {
        return HttpResponse.json({
          data: {
            session_id: String(params.id),
            diff: reviewPerfDiff,
            diff_stats: { added: 2, removed: 2, files_changed: 2 },
            diff_history: [],
            diff_truncated: false,
            diff_history_truncated: false,
          },
        });
      }),
    );

    const { rerender } = renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText("Send a follow-up message...");
    expect(timelineRequestCount).toBeGreaterThan(0);
    timelineRequestCount = 0;

    act(() => {
      window.history.pushState(null, "", `/sessions/${reviewSessionId}?review=active`);
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    rerender(<SessionDetailContent id={reviewSessionId} />);

    expect(await screen.findByTestId("review-diff-view-mock")).toBeInTheDocument();
    expect(timelineRequestCount).toBe(0);
  });

  it("shows a retryable error when the lazy diff request fails", async () => {
    const { SessionDetailContent } = await import("./session-detail-content");
    const user = userEvent.setup();

    server.use(
      http.get("/api/v1/sessions/:id", () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            primary_issue_id: undefined,
            sandbox_state: "ready",
            diff: undefined,
            diff_stats: { added: 1, removed: 1, files_changed: 1 },
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get("/api/v1/sessions/:id/timeline", () => {
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
      http.get("/api/v1/sessions/:id/diff", () => {
        return HttpResponse.json(
          { error: { code: "DIFF_LOAD_FAILED", message: "failed to load session diff" } },
          { status: 503 }
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findByPlaceholderText("Send a follow-up message...");
    await user.click(screen.getByRole("tab", { name: /Changes/i }));

    expect(await screen.findByText("Couldn't load changes")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });

  it("shows a truncation notice when the lazy diff response is capped", async () => {
    const { SessionDetailContent } = await import("./session-detail-content");
    const user = userEvent.setup();

    server.use(
      http.get("/api/v1/sessions/:id", () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            primary_issue_id: undefined,
            sandbox_state: "ready",
            diff: undefined,
            diff_stats: { added: 34083, removed: 176, files_changed: 3594 },
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get("/api/v1/sessions/:id/timeline", () => {
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
      http.get("/api/v1/sessions/:id/diff", () => {
        return HttpResponse.json({
          data: {
            session_id: "session-abcdef12-3456-7890",
            diff: "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n",
            diff_stats: { added: 34083, removed: 176, files_changed: 3594 },
            diff_history: [],
            diff_truncated: true,
            diff_history_truncated: true,
            diff_chars: 9000000,
            diff_max_chars: 2000000,
          },
        } satisfies SingleResponse<import("@/lib/types").SessionDiff>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findByPlaceholderText("Send a follow-up message...");
    await user.click(screen.getByRole("tab", { name: /Changes/i }));

    expect(await screen.findByText("Large diff truncated")).toBeInTheDocument();
    expect(screen.getByText(/showing the first/)).toBeInTheDocument();
  });

  it("does not describe raw diff truncation when only diff pass history is capped", async () => {
    const { SessionDetailContent } = await import("./session-detail-content");
    const user = userEvent.setup();

    server.use(
      http.get("/api/v1/sessions/:id", () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            primary_issue_id: undefined,
            sandbox_state: "ready",
            diff: undefined,
            diff_stats: { added: 4607, removed: 314, files_changed: 51 },
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get("/api/v1/sessions/:id/timeline", () => {
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionTimelineEntry>);
      }),
      http.get("/api/v1/sessions/:id/diff", () => {
        return HttpResponse.json({
          data: {
            session_id: "session-abcdef12-3456-7890",
            diff: "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n",
            diff_stats: { added: 4607, removed: 314, files_changed: 51 },
            diff_history: [],
            diff_truncated: false,
            diff_history_truncated: true,
            diff_chars: 259664,
            diff_history_bytes: 4194304,
            diff_max_chars: 2097152,
            diff_history_max_bytes: 2097152,
          },
        } satisfies SingleResponse<import("@/lib/types").SessionDiff>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findByPlaceholderText("Send a follow-up message...");
    await user.click(screen.getByRole("tab", { name: /Changes/i }));

    expect(await screen.findByText("Diff pass history truncated")).toBeInTheDocument();
    expect(screen.getByText("Diff pass history is too large to load for this view, so only the current diff is shown.")).toBeInTheDocument();
    expect(screen.queryByText("Large diff truncated")).not.toBeInTheDocument();
    expect(screen.queryByText(/showing the first 2,097,152 of 259,664 characters/)).not.toBeInTheDocument();
  });
});
