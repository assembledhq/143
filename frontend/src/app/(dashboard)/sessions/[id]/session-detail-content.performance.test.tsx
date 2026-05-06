import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { mockSessions } from "@/test/mocks/handlers";
import type { ListResponse, Session, SessionTimelineEntry, SingleResponse } from "@/lib/types";

const { chatTimelineRenderState } = vi.hoisted(() => ({
  chatTimelineRenderState: { count: 0 },
}));

vi.mock("@/components/chat-timeline", () => ({
  ChatTimeline: ({ entries }: { entries: unknown[] }) => {
    chatTimelineRenderState.count += 1;
    return <div data-testid="chat-timeline-mock">{entries.length}</div>;
  },
}));

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

beforeAll(() => {
  global.EventSource = MockEventSource as unknown as typeof EventSource;
  setMobileViewport(false);
});

beforeEach(() => {
  chatTimelineRenderState.count = 0;
  MockEventSource.instances = [];
  window.localStorage.clear();
  setMobileViewport(false);
});

describe("SessionDetailContent performance", () => {
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
  });
});
