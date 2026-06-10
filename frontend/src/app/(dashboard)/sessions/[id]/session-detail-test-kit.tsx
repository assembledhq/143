// session-detail-test-kit.tsx — Shared fixtures for the SessionDetailPage
// test files (page-*.test.tsx). The suite was split out of a single ~9k-line
// page.test.tsx so vitest can spread the chunks across parallel workers; this
// module holds the setup every chunk reuses. vi.mock() calls cannot live here
// (vitest hoists them per test module), so each test file carries its own
// small mock preamble and then calls installSessionDetailPageTestHooks().
import { expect, vi, beforeAll, afterEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { fireEvent, render } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { NuqsTestingAdapter } from 'nuqs/adapters/testing';
import { server } from '@/test/mocks/server';
import { SessionDetailContent } from './session-detail-content';
import type { Session, SessionDiff, SingleResponse } from '@/lib/types';

export function sessionWithoutRawDiff(session: Session): Session {
  const copy = { ...session };
  delete copy.diff;
  delete copy.diff_history;
  return copy;
}

export function mockSessionDetailWithLazyDiff(session: Session) {
  server.use(
    http.get('/api/v1/sessions/:id', () => {
      return HttpResponse.json({ data: sessionWithoutRawDiff(session) } satisfies SingleResponse<Session>);
    }),
    http.get('/api/v1/sessions/:id/diff', () => {
      return HttpResponse.json({
        data: {
          session_id: session.id,
          diff: session.diff,
          diff_stats: session.diff_stats,
          diff_history: session.diff_history ?? [],
          diff_truncated: false,
          diff_history_truncated: false,
        },
      } satisfies SingleResponse<SessionDiff>);
    }),
  );
}

// Mock EventSource (not available in jsdom)
export class MockEventSource {
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
  private listeners = new Map<string, Array<(ev: MessageEvent) => void>>();
  constructor(url: string | URL) {
    this.url = String(url);
    MockEventSource.instances.push(this);
  }
  addEventListener = vi.fn((event: string, handler: EventListenerOrEventListenerObject) => {
    const fn = typeof handler === 'function'
      ? handler as (ev: MessageEvent) => void
      : (ev: MessageEvent) => handler.handleEvent(ev);
    this.listeners.set(event, [...(this.listeners.get(event) ?? []), fn]);
  });
  removeEventListener = vi.fn((event: string, handler: EventListenerOrEventListenerObject) => {
    const existing = this.listeners.get(event) ?? [];
    this.listeners.set(event, existing.filter((fn) => fn !== handler));
  });
  close = vi.fn();
  dispatchEvent = vi.fn(() => true);
  emit(event: string, data: unknown) {
    const message = { data: JSON.stringify(data) } as MessageEvent;
    for (const listener of this.listeners.get(event) ?? []) {
      listener(message);
    }
  }
}

export function setMobileViewport(matches: boolean) {
  Object.defineProperty(window, 'matchMedia', {
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

export function getChatScroller(container: HTMLElement): HTMLDivElement {
  const scroller = container.querySelector('div.flex-1.overflow-y-auto.space-y-2.p-4');
  expect(scroller).toBeInstanceOf(HTMLDivElement);
  return scroller as HTMLDivElement;
}

export function changeFieldValue(element: HTMLElement, value: string) {
  fireEvent.change(element, { target: { value } });
}

export function submitFieldWithEnter(element: HTMLElement) {
  fireEvent.keyDown(element, { key: 'Enter', code: 'Enter', charCode: 13 });
}

export function renderSessionDetailWithQueryClient(id: string, queryClient: QueryClient) {
  function Harness() {
    return (
      <NuqsTestingAdapter>
        <QueryClientProvider client={queryClient}>
          <SessionDetailContent id={id} />
        </QueryClientProvider>
      </NuqsTestingAdapter>
    );
  }

  return render(<Harness />);
}

export function installSessionDetailPageTestHooks({
  toast,
  routerPush,
}: {
  toast: { success: ReturnType<typeof vi.fn>; error: ReturnType<typeof vi.fn> };
  routerPush: ReturnType<typeof vi.fn>;
}) {
  beforeAll(() => {
    global.EventSource = MockEventSource as unknown as typeof EventSource;
    setMobileViewport(false);
  });

  afterEach(() => {
    MockEventSource.instances = [];
    toast.success.mockReset();
    toast.error.mockReset();
    routerPush.mockReset();
    vi.useRealTimers();
    window.localStorage.clear();
    vi.restoreAllMocks();
    setMobileViewport(false);
  });
}
