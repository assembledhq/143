import { act } from 'react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { NuqsTestingAdapter } from 'nuqs/adapters/testing';
import React from 'react';
import { parseAsString, useQueryState } from 'nuqs';
import { fireEvent, renderWithProviders, screen, waitFor } from '@/test/test-utils';
import userEvent from '@testing-library/user-event';
import { server } from '@/test/mocks/server';
import { SessionSidebar } from './session-sidebar';
import type { SessionDetail, SessionListItem } from '@/lib/types';

const { notifySuccess, notifyError } = vi.hoisted(() => ({
  notifySuccess: vi.fn(),
  notifyError: vi.fn(),
}));

vi.mock('@/lib/notify', () => ({
  notify: {
    success: notifySuccess,
    error: notifyError,
  },
}));

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

let mockPathname = '/sessions';
let mockSelectedSegment: string | null = null;
let mockSelectedSegments: string[] = [];

function mockSegmentsFromPathname() {
  if (mockSelectedSegments.length > 0) return mockSelectedSegments;
  if (mockSelectedSegment) return [mockSelectedSegment];
  const [, root, ...segments] = mockPathname.split('/');
  return root === 'sessions' ? segments.filter(Boolean) : [];
}
const mockRouterPush = vi.fn();
const mockRouterPrefetch = vi.fn();
const mockAuthState: {
  isAuthenticated: boolean;
  user: { id: string } | null;
  isLoading: boolean;
  logout: ReturnType<typeof vi.fn>;
} = {
  isAuthenticated: true,
  user: { id: 'user-1' },
  isLoading: false,
  logout: vi.fn(),
};

vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: mockRouterPush, prefetch: mockRouterPrefetch, replace: vi.fn(), back: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => mockPathname,
  useSelectedLayoutSegment: () => mockSelectedSegment,
  useSelectedLayoutSegments: () => mockSegmentsFromPathname(),
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: () => mockAuthState,
}));

const mockOptimisticSessions: { id: string; title: string; status: 'pending'; created_at: string; resolvedId?: string }[] = [];
const mockRemoveOptimisticSession = vi.fn();

vi.mock('@/contexts/optimistic-sessions', () => ({
  useOptimisticSessions: () => ({
    optimisticSessions: mockOptimisticSessions,
    addOptimisticSession: vi.fn(),
    removeOptimisticSession: mockRemoveOptimisticSession,
    markOptimisticResolved: vi.fn(),
  }),
  OptimisticSessionsProvider: ({ children }: { children: React.ReactNode }) => children,
}));

// Helper to create a session with overrides
function makeSession(overrides: Partial<SessionListItem> = {}): SessionListItem {
  return {
    id: 'sess-1',
    primary_issue_id: 'issue-1',
    org_id: 'org-1',
    agent_type: 'claude_code',
    status: 'completed',
    autonomy_level: 'full',
    token_mode: 'standard',
    current_turn: 0,
    sandbox_state: 'none',
    pr_creation_state: 'idle',
    created_at: '2026-02-17T07:00:00Z',
    started_at: '2026-02-17T07:00:00Z',
    completed_at: '2026-02-17T07:05:00Z',
    last_activity_at: '2026-02-17T07:05:00Z',
    result_summary: 'Test session',
    ...overrides,
  };
}

function serveSessions(sessions: SessionListItem[]) {
  server.use(
    http.get('/api/v1/sessions', () => {
      return HttpResponse.json({ data: sessions, meta: {} });
    }),
  );
}

function serveSessionDetail(session: SessionDetail) {
  server.use(
    http.get(`/api/v1/sessions/${session.id}`, () => {
      return HttpResponse.json({ data: session });
    }),
  );
}

function renderSidebarWithMutableSearchParams(initialSearchParams: Record<string, string>) {
  function ClearSearchParamsButton() {
    const [, setSearchParam] = useQueryState('search', parseAsString);
    return (
      <button type="button" onClick={() => void setSearchParam(null)}>
        Clear search params
      </button>
    );
  }

  function Harness() {
    const queryClient = new QueryClient({
      defaultOptions: {
        queries: {
          retry: false,
          gcTime: 0,
        },
      },
    });

    return (
      <NuqsTestingAdapter searchParams={initialSearchParams}>
        <ClearSearchParamsButton />
        <QueryClientProvider client={queryClient}>
          <SessionSidebar />
        </QueryClientProvider>
      </NuqsTestingAdapter>
    );
  }

  return renderWithProviders(<Harness />);
}

describe('SessionSidebar', () => {
  beforeEach(() => {
    mockPathname = '/sessions';
    mockSelectedSegment = null;
    mockSelectedSegments = [];
    mockRouterPush.mockReset();
    mockRouterPrefetch.mockReset();
    mockRemoveOptimisticSession.mockReset();
    mockOptimisticSessions.length = 0;
    mockAuthState.isAuthenticated = true;
    mockAuthState.user = { id: 'user-1' };
    mockAuthState.isLoading = false;
    mockAuthState.logout = vi.fn();
    notifySuccess.mockReset();
    notifyError.mockReset();
  });

  it('defaults the people scope to Mine', async () => {
    let capturedUserId: string | null = null;
    server.use(
      http.get('/api/v1/sessions', ({ request }) => {
        capturedUserId = new URL(request.url).searchParams.get('triggered_by_user_ids');
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    renderWithProviders(<SessionSidebar />);

    await screen.findByRole('button', { name: /Mine/ });
    expect(capturedUserId).toBe('user-1');
  });

  it('does not fetch sessions until the Mine scope can resolve the current user', async () => {
    mockAuthState.isAuthenticated = false;
    mockAuthState.user = null;
    mockAuthState.isLoading = true;

    let requestCount = 0;
    server.use(
      http.get('/api/v1/sessions', () => {
        requestCount += 1;
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get('/api/v1/sessions/counts', () => {
        requestCount += 1;
        return HttpResponse.json({ data: { all: 0, active: 0, archived: 0, cap: 0 } });
      }),
    );

    renderWithProviders(<SessionSidebar />);

    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(requestCount).toBe(0);
    expect(screen.getByText('Loading...')).toBeInTheDocument();
  });

  // -----------------------------------------------------------------------
  // Search filtering
  // -----------------------------------------------------------------------

  it('filters sessions by search input', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Alpha fix' }),
      makeSession({ id: 's2', result_summary: 'Beta update' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Alpha fix');

    const input = screen.getByPlaceholderText('Search sessions...');
    await userEvent.type(input, 'Beta');

    await waitFor(() => {
      expect(screen.queryByText('Alpha fix')).not.toBeInTheDocument();
    });
    expect(screen.getByText('Beta update')).toBeInTheDocument();
  });

  it('archives a session from the swipe action', async () => {
    let archiveCalls = 0;
    let resolveArchiveRefetch: (() => void) | undefined;
    server.use(
      http.get('/api/v1/sessions', () => {
        if (archiveCalls === 0) {
          return HttpResponse.json({
            data: [makeSession({ id: 's1', result_summary: 'Swipe me' })],
            meta: {},
          });
        }
        return new Promise((resolve) => {
          resolveArchiveRefetch = () => {
            resolve(HttpResponse.json({ data: [], meta: {} }));
          };
        });
      }),
      http.post('/api/v1/sessions/s1/archive', () => {
        archiveCalls += 1;
        return HttpResponse.json({ status: 'archived' });
      }),
    );

    renderWithProviders(<SessionSidebar />);
    const row = await screen.findByText('Swipe me');
    const surface = row.closest('[data-swipe-surface="true"]');
    expect(surface).not.toBeNull();

    fireEvent.touchStart(surface!, { touches: [{ clientX: 220, clientY: 24 }] });
    fireEvent.touchMove(surface!, { touches: [{ clientX: 120, clientY: 26 }] });
    fireEvent.touchEnd(surface!);

    fireEvent.click(screen.getAllByRole('button', { name: 'Archive session' })[0]);

    await waitFor(() => {
      expect(archiveCalls).toBe(1);
    });
    expect(screen.queryByText('Swipe me')).not.toBeInTheDocument();
    resolveArchiveRefetch?.();
  });

  it('optimistically removes an archived session before the archive request settles', async () => {
    let resolveArchive: (() => void) | undefined;
    server.use(
      http.get('/api/v1/sessions', () => {
        return HttpResponse.json({
          data: [makeSession({ id: 's1', result_summary: 'Immediate archive' })],
          meta: {},
        });
      }),
      http.post('/api/v1/sessions/s1/archive', () => {
        return new Promise((resolve) => {
          resolveArchive = () => resolve(HttpResponse.json({ status: 'archived' }));
        });
      }),
    );

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Immediate archive');

    await userEvent.click(screen.getByRole('button', { name: 'Archive session' }));

    await waitFor(() => {
      expect(screen.queryByText('Immediate archive')).not.toBeInTheDocument();
    });

    resolveArchive?.();
  });

  it('rolls back an optimistic archive when the archive request fails', async () => {
    server.use(
      http.get('/api/v1/sessions', () => {
        return HttpResponse.json({
          data: [makeSession({ id: 's1', result_summary: 'Archive rollback' })],
          meta: {},
        });
      }),
      http.post('/api/v1/sessions/s1/archive', () => {
        return HttpResponse.json(
          { error: { code: 'ARCHIVE_FAILED', message: 'Archive failed' } },
          { status: 500 },
        );
      }),
    );

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Archive rollback');

    await userEvent.click(screen.getByRole('button', { name: 'Archive session' }));

    await waitFor(() => {
      expect(screen.getByText('Archive rollback')).toBeInTheDocument();
    });
  });

  it('optimistically removes an archived session from loaded extra pages', async () => {
    server.use(
      http.get('/api/v1/sessions', ({ request }) => {
        const cursor = new URL(request.url).searchParams.get('cursor');
        if (cursor === 'page-2') {
          return HttpResponse.json({
            data: [makeSession({ id: 's2', result_summary: 'Second page archive' })],
            meta: {},
          });
        }
        return HttpResponse.json({
          data: [makeSession({ id: 's1', result_summary: 'First page session' })],
          meta: { next_cursor: 'page-2' },
        });
      }),
      http.post('/api/v1/sessions/s2/archive', () => {
        return HttpResponse.json({ status: 'archived' });
      }),
    );

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('First page session');
    await userEvent.click(screen.getByRole('button', { name: 'Show more' }));
    await screen.findByText('Second page archive');

    const archiveButtons = screen.getAllByRole('button', { name: 'Archive session' });
    await userEvent.click(archiveButtons[archiveButtons.length - 1]);

    await waitFor(() => {
      expect(screen.queryByText('Second page archive')).not.toBeInTheDocument();
    });
  });

  it('keeps capped session counts capped during optimistic archive updates', async () => {
    let resolveArchive: (() => void) | undefined;
    server.use(
      http.get('/api/v1/sessions', () => {
        return HttpResponse.json({
          data: [makeSession({ id: 's1', result_summary: 'Capped count archive', status: 'running' })],
          meta: {},
        });
      }),
      http.get('/api/v1/sessions/counts', () => {
        return HttpResponse.json({ data: { all: 100, active: 100, archived: 100, cap: 100 } });
      }),
      http.post('/api/v1/sessions/s1/archive', () => {
        return new Promise((resolve) => {
          resolveArchive = () => resolve(HttpResponse.json({ status: 'archived' }));
        });
      }),
    );

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Capped count archive');
    await screen.findAllByText('99+');

    await userEvent.click(screen.getByRole('button', { name: 'Archive session' }));

    await waitFor(() => {
      expect(screen.getAllByText('99+')).toHaveLength(3);
    });
    expect(screen.queryByText('99')).not.toBeInTheDocument();

    resolveArchive?.();
  });

  it('prefetches session detail when a session row is hovered or focused', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Prefetch me' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    const link = (await screen.findByText('Prefetch me')).closest('a');
    expect(link).not.toBeNull();

    fireEvent.mouseEnter(link!);
    fireEvent.focus(link!);

    expect(mockRouterPrefetch).toHaveBeenCalledWith('/sessions/s1');
  });

  it('prefetches the new-session route when the new-session affordance is hovered', async () => {
    serveSessions([]);

    renderWithProviders(<SessionSidebar />);
    const link = await screen.findByRole('link', { name: 'New session' });

    fireEvent.mouseEnter(link);

    expect(mockRouterPrefetch).toHaveBeenCalledWith('/sessions/new');
  });

  it('shows committed mobile archive feedback before removing the row while the backend request is pending', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let archiveCalls = 0;
    let archiveSettled = false;
    let resolveArchive: (() => void) | undefined;
    try {
      server.use(
        http.get('/api/v1/sessions', () => {
          if (archiveSettled) {
            return HttpResponse.json({ data: [], meta: {} });
          }
          return HttpResponse.json({
            data: [makeSession({ id: 's1', result_summary: 'Swipe pending' })],
            meta: {},
          });
        }),
        http.post('/api/v1/sessions/s1/archive', () => {
          archiveCalls += 1;
          return new Promise((resolve) => {
            resolveArchive = () => {
              archiveSettled = true;
              resolve(HttpResponse.json({ status: 'archived' }));
            };
          });
        }),
      );

      renderWithProviders(<SessionSidebar />);
      const row = await screen.findByText('Swipe pending');
      const surface = row.closest('[data-swipe-surface="true"]') as HTMLElement | null;
      expect(surface).not.toBeNull();
      const container = surface!.parentElement;
      expect(container).not.toBeNull();
      Object.defineProperty(container!, 'offsetWidth', {
        configurable: true,
        value: 390,
      });

      fireEvent.touchStart(surface!, { touches: [{ clientX: 320, clientY: 24 }] });
      fireEvent.touchMove(surface!, { touches: [{ clientX: 170, clientY: 26 }] });
      fireEvent.touchEnd(surface!);

      expect(container).toHaveAttribute('data-swipe-state', 'committed');
      expect(surface!.style.transform).toBe('translateX(-390px)');
      expect(container).toHaveTextContent('Archived');
      expect(screen.getByText('Swipe pending')).toBeInTheDocument();

      await waitFor(() => {
        expect(archiveCalls).toBe(1);
      });
      expect(notifySuccess).toHaveBeenCalledWith('Session archived', { duration: 2500 });
      expect(screen.queryByRole('button', { name: /undo/i })).not.toBeInTheDocument();

      await act(async () => {
        vi.advanceTimersByTime(500);
      });
      expect(container).toHaveAttribute('data-swipe-collapsing', 'true');
      expect(screen.getByText('Swipe pending')).toBeInTheDocument();

      await act(async () => {
        vi.advanceTimersByTime(250);
      });
      expect(screen.queryByText('Swipe pending')).not.toBeInTheDocument();

      await act(async () => { resolveArchive?.(); });
    } finally {
      vi.useRealTimers();
    }
  });

  it('keeps committed mobile archive feedback visible even when the backend responds before the animation completes', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let archiveSettled = false;
    let resolveArchive: (() => void) | undefined;
    try {
      server.use(
        http.get('/api/v1/sessions', () => {
          if (archiveSettled) {
            return HttpResponse.json({ data: [], meta: {} });
          }
          return HttpResponse.json({
            data: [makeSession({ id: 's1', result_summary: 'Fast swipe' })],
            meta: {},
          });
        }),
        http.post('/api/v1/sessions/s1/archive', () =>
          new Promise((resolve) => {
            resolveArchive = () => {
              archiveSettled = true;
              resolve(HttpResponse.json({ status: 'archived' }));
            };
          }),
        ),
      );

      renderWithProviders(<SessionSidebar />);
      const row = await screen.findByText('Fast swipe');
      const surface = row.closest('[data-swipe-surface="true"]') as HTMLElement | null;
      expect(surface).not.toBeNull();
      const container = surface!.parentElement;
      expect(container).not.toBeNull();
      Object.defineProperty(container!, 'offsetWidth', { configurable: true, value: 390 });

      fireEvent.touchStart(surface!, { touches: [{ clientX: 320, clientY: 24 }] });
      fireEvent.touchMove(surface!, { touches: [{ clientX: 170, clientY: 26 }] });
      fireEvent.touchEnd(surface!);

      // Simulate fast server: resolve before any animation timer fires
      await act(async () => { resolveArchive?.(); });

      // Session must remain visible even though the server already responded
      expect(screen.getByText('Fast swipe')).toBeInTheDocument();
      expect(container).toHaveAttribute('data-swipe-state', 'committed');
      expect(container).toHaveTextContent('Archived');

      await act(async () => { vi.advanceTimersByTime(500); });
      expect(container).toHaveAttribute('data-swipe-collapsing', 'true');
      expect(screen.getByText('Fast swipe')).toBeInTheDocument();

      await act(async () => { vi.advanceTimersByTime(250); });
      await waitFor(() => expect(screen.queryByText('Fast swipe')).not.toBeInTheDocument());
    } finally {
      vi.useRealTimers();
    }
  });

  it('keeps the desktop archive action de-emphasized until hover or focus', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Full-width session row' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Full-width session row');

    expect(screen.getByRole('button', { name: 'Archive session' })).toHaveClass(
      'md:opacity-0',
      'md:group-hover:opacity-100',
      'md:focus-visible:opacity-100',
    );
  });

  it('does not render a separate open-details icon for session rows', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'No extra open button' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('No extra open button');

    expect(screen.queryByRole('link', { name: 'Open session details for No extra open button' })).not.toBeInTheDocument();
  });

  it('uses the line tab variant for the session status filters', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Status filter tabs' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Status filter tabs');

    const tabList = screen.getByRole('tablist');
    expect(tabList).toHaveAttribute('data-variant', 'line');
    expect(tabList.className).toContain('justify-start');

    // The scroll/clip wrapper lives on the parent div so the active-tab
    // underline (positioned just below the trigger) isn't clipped.
    const scrollWrapper = tabList.parentElement;
    expect(scrollWrapper?.className).toContain('overflow-x-auto');
    expect(scrollWrapper?.className).toContain('overflow-y-hidden');
    expect(scrollWrapper?.className).toContain('pb-1');
  });

  it('navigates when the selected row shell is tapped', async () => {
    mockSelectedSegment = 's1';
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Selected session' }),
    ]);

    renderWithProviders(<SessionSidebar />);

    await screen.findByText('Selected session');
    const selectedLink = document.querySelector('a[aria-current="page"]') as HTMLAnchorElement | null;
    expect(selectedLink).not.toBeNull();
    if (!selectedLink) {
      throw new Error('expected selected session link to be present');
    }
    const selectedRow = selectedLink.parentElement;

    expect(selectedLink).toHaveAttribute('aria-current', 'page');
    expect(selectedRow).toHaveClass('rounded-xl', 'border', 'border-primary/25', 'bg-card', 'shadow-sm');

    fireEvent.click(selectedRow!);

    expect(mockRouterPush).toHaveBeenCalledWith('/sessions/s1');
  });

  it('uses direct session links without an intermediate opening state', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Slow session' }),
      makeSession({ id: 's2', result_summary: 'Other session' }),
    ]);

    renderWithProviders(<SessionSidebar />);

    const link = (await screen.findByText('Slow session')).closest('a');
    expect(link).not.toBeNull();

    await userEvent.click(link!);

    expect(link).not.toHaveAttribute('aria-busy');
    expect(screen.queryByText('Opening')).not.toBeInTheDocument();
    expect(screen.getByText('Slow session').closest('[role="option"]')).toHaveTextContent('Completed');
    expect(screen.getByText('Other session').closest('[role="option"]')).toHaveAttribute('aria-selected', 'false');
  });

  it('lets plain row links navigate through Next Link instead of imperative router push', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Native link session' }),
    ]);

    renderWithProviders(<SessionSidebar />);

    const link = (await screen.findByText('Native link session')).closest('a');
    expect(link).not.toBeNull();

    await userEvent.click(link!);

    expect(mockRouterPush).not.toHaveBeenCalled();
    expect(link).toHaveAttribute('href', '/sessions/s1');
  });

  it('does not hold a target row pending when switching from one selected session to another', async () => {
    mockSelectedSegment = 's1';
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Current session' }),
      makeSession({ id: 's2', result_summary: 'Next session' }),
    ]);

    renderWithProviders(<SessionSidebar />);

    const nextLink = (await screen.findByText('Next session')).closest('a');
    expect(nextLink).not.toBeNull();

    await userEvent.click(nextLink!);

    expect(nextLink).not.toHaveAttribute('aria-busy');
    expect(screen.queryByText('Opening')).not.toBeInTheDocument();
    expect(screen.getByText('Next session').closest('[role="option"]')).toHaveAttribute('aria-selected', 'false');
    expect(screen.getByText('Current session').closest('[role="option"]')).toHaveAttribute('aria-selected', 'true');
  });

  it('uses the same row padding frame for the new-session draft and normal sessions', async () => {
    mockPathname = '/sessions/new';
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Existing session' }),
    ]);

    renderWithProviders(<SessionSidebar />);

    const draftOption = await screen.findByRole('option', { name: 'New session draft' });
    const normalOption = (await screen.findByText('Existing session')).closest('[role="option"]');
    expect(normalOption).not.toBeNull();

    expect(draftOption).toHaveClass('flex', 'min-w-0', 'rounded-xl', 'border', 'p-1');
    expect(normalOption!).toHaveClass('flex', 'min-w-0', 'rounded-xl', 'border', 'p-1');

    const draftLink = draftOption.querySelector('a');
    const normalLink = normalOption!.querySelector('a');
    expect(draftLink).not.toBeNull();
    expect(normalLink).not.toBeNull();
    expect(draftLink!).toHaveClass('relative', 'block', 'min-w-0', 'flex-1', 'overflow-hidden', 'rounded-lg', 'px-3', 'py-2.5');
    expect(normalLink!).toHaveClass('relative', 'block', 'min-w-0', 'flex-1', 'overflow-hidden', 'rounded-lg', 'px-3', 'py-2.5');
  });

  it('clears search on Escape key', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Alpha fix' }),
      makeSession({ id: 's2', result_summary: 'Beta update' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Alpha fix');

    const input = screen.getByPlaceholderText('Search sessions...');
    await userEvent.type(input, 'Beta');
    await waitFor(() => {
      expect(screen.queryByText('Alpha fix')).not.toBeInTheDocument();
    });

    await userEvent.keyboard('{Escape}');
    await waitFor(() => {
      expect(screen.getByText('Alpha fix')).toBeInTheDocument();
    });
  });

  it('restores search from the URL and preserves it in session detail links', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Alpha fix' }),
      makeSession({ id: 's2', result_summary: 'Beta update' }),
    ]);

    renderWithProviders(<SessionSidebar />, {
      searchParams: { people: 'all', search: 'Beta' },
    });

    const input = await screen.findByPlaceholderText('Search sessions...');
    expect(input).toHaveValue('Beta');
    expect(screen.queryByText('Alpha fix')).not.toBeInTheDocument();
    expect((await screen.findByText('Beta update')).closest('a')).toHaveAttribute(
      'href',
      '/sessions/s2?people=all&search=Beta',
    );
  });

  it('clears the local search when navigation removes the search param', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Alpha fix' }),
      makeSession({ id: 's2', result_summary: 'Beta update' }),
    ]);

    renderSidebarWithMutableSearchParams({ search: 'Beta' });

    const input = await screen.findByPlaceholderText('Search sessions...');
    expect(input).toHaveValue('Beta');
    expect(screen.queryByText('Alpha fix')).not.toBeInTheDocument();
    expect(await screen.findByText('Beta update')).toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: 'Clear search params' }));

    await waitFor(() => {
      expect(input).toHaveValue('');
      expect(screen.getByText('Alpha fix')).toBeInTheDocument();
    });
  });

  it('opens a clicked session through its direct link', async () => {
    const session = makeSession({
      id: 's1',
      result_summary: 'Instant open session',
      status: 'running',
      diff_stats: { added: 12, removed: 4, files_changed: 3 },
    });
    serveSessions([session]);

    renderWithProviders(<SessionSidebar />);

    const link = (await screen.findByText('Instant open session')).closest('a');
    expect(link).not.toBeNull();
    expect(link).toHaveAttribute('href', '/sessions/s1');

    await userEvent.click(link!);

    expect(mockRouterPush).not.toHaveBeenCalled();
    expect(link).toHaveAttribute('href', '/sessions/s1');
  });

  // -----------------------------------------------------------------------
  // "No sessions match this filter" vs "No sessions yet"
  // -----------------------------------------------------------------------

  it('shows "No sessions match this filter" when search yields no results', async () => {
    serveSessions([makeSession({ id: 's1', result_summary: 'Only session' })]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Only session');

    const input = screen.getByPlaceholderText('Search sessions...');
    await userEvent.type(input, 'zzz-nonexistent');

    await waitFor(() => {
      expect(screen.getByText('No sessions match this filter.')).toBeInTheDocument();
    });
  });

  it('shows a GitHub setup notice when no repository integration is connected', async () => {
    serveSessions([]);
    server.use(
      http.get('/api/v1/integrations', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get('/api/v1/repositories', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    renderWithProviders(<SessionSidebar />);

    expect(await screen.findByText('GitHub setup required')).toBeInTheDocument();
    expect(
      screen.getByText(/connect github before creating sessions or projects/i),
    ).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open integrations' })).toHaveAttribute(
      'href',
      '/settings/integrations',
    );
  });

  // -----------------------------------------------------------------------
  // Optimistic session rows
  // -----------------------------------------------------------------------

  it('renders optimistic sessions when on "all" filter', async () => {
    serveSessions([]);
    mockOptimisticSessions.push({
      id: 'opt-1',
      title: 'Creating sandbox...',
      status: 'pending',
      created_at: new Date().toISOString(),
    });

    renderWithProviders(<SessionSidebar />);

    await waitFor(() => {
      expect(screen.getByText('Creating sandbox...')).toBeInTheDocument();
    });
    expect(screen.getByText('Pending')).toBeInTheDocument();
  });

  it('uses the same row padding frame for optimistic sessions and normal sessions', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Existing session' }),
    ]);
    mockOptimisticSessions.push({
      id: 'opt-1',
      title: 'Creating sandbox...',
      status: 'pending',
      created_at: new Date().toISOString(),
    });

    renderWithProviders(<SessionSidebar />);

    const optimisticOption = (await screen.findByText('Creating sandbox...')).closest('[role="option"]');
    const normalOption = (await screen.findByText('Existing session')).closest('[role="option"]');
    expect(optimisticOption).not.toBeNull();
    expect(normalOption).not.toBeNull();

    expect(optimisticOption!).toHaveClass('flex', 'min-w-0', 'rounded-xl', 'border', 'p-1');
    expect(normalOption!).toHaveClass('flex', 'min-w-0', 'rounded-xl', 'border', 'p-1');

    const optimisticSurface = optimisticOption!.querySelector('[data-session-row-surface="true"]');
    const normalSurface = normalOption!.querySelector('a');
    expect(optimisticSurface).not.toBeNull();
    expect(normalSurface).not.toBeNull();
    expect(optimisticSurface!).toHaveClass('relative', 'block', 'min-w-0', 'flex-1', 'overflow-hidden', 'rounded-lg', 'px-3', 'py-2.5');
    expect(normalSurface!).toHaveClass('relative', 'block', 'min-w-0', 'flex-1', 'overflow-hidden', 'rounded-lg', 'px-3', 'py-2.5');
  });

  it('hides a resolved optimistic row once its real session appears in the list', async () => {
    // Simulate the create flow: the optimistic has already been marked resolved
    // to real session id "s-real". The real row is served by the API.
    serveSessions([makeSession({ id: 's-real', result_summary: 'Real session' })]);
    mockOptimisticSessions.push({
      id: 'opt-1',
      title: 'Optimistic placeholder',
      status: 'pending',
      created_at: new Date().toISOString(),
      resolvedId: 's-real',
    });

    renderWithProviders(<SessionSidebar />);

    await screen.findByText('Real session');
    expect(screen.queryByText('Optimistic placeholder')).not.toBeInTheDocument();
  });

  it('keeps resolved optimistic ownership during the real-session handoff', async () => {
    serveSessions([makeSession({ id: 's-real', result_summary: 'Real session', status: 'pending' })]);
    mockOptimisticSessions.push({
      id: 'opt-1',
      title: 'Optimistic placeholder',
      status: 'pending',
      created_at: new Date().toISOString(),
      resolvedId: 's-real',
    });

    renderWithProviders(<SessionSidebar />);

    const realSessionLink = await screen.findByRole('link', { name: /Real session/ });
    expect(realSessionLink).toHaveAttribute('href', '/sessions/s-real');
    expect(screen.queryByText('Optimistic placeholder')).not.toBeInTheDocument();
    expect(screen.getAllByText('Real session')).toHaveLength(1);
    expect(mockRemoveOptimisticSession).not.toHaveBeenCalled();
  });

  it('shows the real session exactly once after the fallback timer removes the optimistic entry', async () => {
    serveSessions([makeSession({ id: 's-real', result_summary: 'Real session', status: 'pending' })]);
    mockOptimisticSessions.push({
      id: 'opt-1',
      title: 'Optimistic placeholder',
      status: 'pending',
      created_at: new Date().toISOString(),
      resolvedId: 's-real',
    });

    const { rerender } = renderWithProviders(<SessionSidebar />);

    // Resolved row is showing the real session.
    await screen.findByRole('link', { name: /Real session/ });
    expect(screen.getAllByText('Real session')).toHaveLength(1);

    // Simulate the fallback timer firing: the optimistic entry is removed from context.
    mockOptimisticSessions.length = 0;
    rerender(<SessionSidebar />);

    // Real session still appears exactly once — no duplication or disappearance
    // during the React key transition from optimistic.id → session.id.
    await waitFor(() => {
      expect(screen.getAllByText('Real session')).toHaveLength(1);
    });
    expect(screen.queryByText('Optimistic placeholder')).not.toBeInTheDocument();
  });

  it('keeps a resolved optimistic row visible when the real session is not yet in the list', async () => {
    serveSessions([]);
    mockOptimisticSessions.push({
      id: 'opt-1',
      title: 'Still waiting',
      status: 'pending',
      created_at: new Date().toISOString(),
      resolvedId: 's-not-yet-fetched',
    });

    renderWithProviders(<SessionSidebar />);

    await waitFor(() => {
      expect(screen.getByText('Still waiting')).toBeInTheDocument();
    });
  });

  // -----------------------------------------------------------------------
  // PR status badge variants
  // -----------------------------------------------------------------------

  it('shows PR badge with "Merged" status', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'pr_created',
        result_summary: 'PR session',
        pr_summary: { status: 'merged', ci_status: '', number: 1, url: '#' },
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('PR session');
    expect(screen.getByText('PR')).toBeInTheDocument();
    expect(screen.getByTitle('Merged')).toBeInTheDocument();
  });

  it('shows PR badge with "Closed" status even when CI previously passed', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'pr_created',
        result_summary: 'Closed PR session',
        pr_summary: { status: 'closed', ci_status: 'success', number: 5, url: '#' },
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Closed PR session');
    expect(screen.getByTitle('Closed')).toBeInTheDocument();
    expect(screen.queryByTitle('CI passed')).not.toBeInTheDocument();
  });

  it('shows PR badge with "CI passed" status', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'pr_created',
        result_summary: 'CI pass session',
        pr_summary: { status: 'open', ci_status: 'success', number: 2, url: '#' },
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('CI pass session');
    expect(screen.getByTitle('CI passed')).toBeInTheDocument();
  });

  it('shows PR badge with "CI failed" status', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'pr_created',
        result_summary: 'CI fail session',
        pr_summary: { status: 'open', ci_status: 'failure', number: 3, url: '#' },
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('CI fail session');
    expect(screen.getByTitle('CI failed')).toBeInTheDocument();
  });

  it('shows PR badge with "CI pending" status', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'pr_created',
        result_summary: 'CI pending session',
        pr_summary: { status: 'open', ci_status: 'pending', number: 4, url: '#' },
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('CI pending session');
    expect(screen.getByTitle('CI pending')).toBeInTheDocument();
  });

  // -----------------------------------------------------------------------
  // Diff stats badge
  // -----------------------------------------------------------------------

  it('shows diff stats badge when session has changes', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        result_summary: 'Diff session',
        diff_stats: { added: 10, removed: 3, files_changed: 2 },
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Diff session');
    // DiffStatsBadge renders +10 / -3 style content
    expect(screen.getByText('+10')).toBeInTheDocument();
  });

  it('does not show diff stats badge when added and removed are zero', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        result_summary: 'No diff session',
        diff_stats: { added: 0, removed: 0, files_changed: 0 },
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('No diff session');
    expect(screen.queryByText('+0')).not.toBeInTheDocument();
  });

  // -----------------------------------------------------------------------
  // Unread / working indicator
  // -----------------------------------------------------------------------

  it('shows animated dot for running sessions', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'running',
        result_summary: 'Running session',
        started_at: '2026-02-17T07:00:00Z',
        completed_at: undefined,
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Running session');
    expect(screen.getByText('Running')).toBeInTheDocument();
  });

  it('marks unread sessions when last_activity_at is after last_viewed_at', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'completed',
        result_summary: 'Unread session',
        last_activity_at: '2026-02-17T09:00:00Z',
        last_viewed_at: '2026-02-17T08:00:00Z',
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Unread session');
    // The unread session title gets "text-foreground" class
    const titleEl = screen.getByText('Unread session');
    expect(titleEl.className).toContain('text-foreground');
  });

  it('marks sessions with activity but no last_viewed_at as unread', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'completed',
        result_summary: 'Never viewed session',
        last_activity_at: '2026-02-17T09:00:00Z',
        // last_viewed_at is undefined -> unread
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Never viewed session');
    const titleEl = screen.getByText('Never viewed session');
    expect(titleEl.className).toContain('text-foreground');
  });

  // -----------------------------------------------------------------------
  // Failed session error message
  // -----------------------------------------------------------------------

  it('shows failure explanation for failed sessions', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'failed',
        result_summary: 'Failed session',
        failure_explanation: 'Could not connect to service',
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Failed session');
    expect(screen.getByText('Could not connect to service')).toBeInTheDocument();
  });

  it('shows error field when failure_explanation is absent', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        status: 'failed',
        result_summary: 'Error session',
        error: 'timeout exceeded',
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Error session');
    expect(screen.getByText('timeout exceeded')).toBeInTheDocument();
  });

  // -----------------------------------------------------------------------
  // PM badge
  // -----------------------------------------------------------------------

  it('shows PM badge for PM-triggered sessions without user', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        result_summary: 'PM session',
        pm_plan_id: 'plan-123',
        triggered_by_user_id: undefined,
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('PM session');
    expect(screen.getByText('PM')).toBeInTheDocument();
  });

  it('does not show PM badge when triggered_by_user_id is set', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        result_summary: 'User PM session',
        pm_plan_id: 'plan-123',
        triggered_by_user_id: 'user-1',
      }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('User PM session');
    expect(screen.queryByText('PM')).not.toBeInTheDocument();
  });

  // -----------------------------------------------------------------------
  // Selected session highlighting
  // -----------------------------------------------------------------------

  it('highlights the selected session', async () => {
    mockSelectedSegment = 's1';
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Selected session' }),
      makeSession({ id: 's2', result_summary: 'Other session' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Selected session');

    const selectedLink = screen.getByText('Selected session').closest('a');
    const unselectedLink = screen.getByText('Other session').closest('a');
    const selectedRow = selectedLink?.parentElement;
    expect(selectedLink?.className).toContain('bg-primary/5');
    expect(selectedLink?.className).toContain('md:bg-primary/5');
    expect(selectedLink?.className).toContain('border-transparent');
    expect(selectedLink?.className).toContain('shadow-none');
    expect(selectedLink?.className).toContain('ring-0');
    expect(selectedRow?.className).toContain('rounded-xl');
    expect(selectedRow?.className).toContain('border-primary/25');
    expect(selectedRow?.className).toContain('ring-1');
    expect(selectedRow?.className).toContain('ring-primary/10');
    expect(selectedRow?.className).toContain('shadow-sm');
    expect(selectedLink).toHaveAttribute('aria-current', 'page');
    expect(unselectedLink?.className).not.toContain('bg-primary/5');
  });

  it('reserves the same selected-shell layout for unselected rows', async () => {
    mockSelectedSegment = 's1';
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Selected session' }),
      makeSession({ id: 's2', result_summary: 'Other session' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Selected session');

    const selectedRow = screen.getByText('Selected session').closest('a')?.parentElement;
    const unselectedRow = screen.getByText('Other session').closest('a')?.parentElement;

    expect(selectedRow).toHaveClass('border', 'p-1');
    expect(selectedRow).toHaveClass('border-primary/25');
    expect(unselectedRow).toHaveClass('border', 'border-transparent', 'p-1');
  });

  it('highlights the selected session from the active layout segment', async () => {
    mockPathname = '/sessions/s1';
    mockSelectedSegment = 's1';
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Selected via pathname' }),
      makeSession({ id: 's2', result_summary: 'Other session' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Selected via pathname');

    const selectedLink = screen.getByText('Selected via pathname').closest('a');
    const selectedRow = selectedLink?.parentElement;
    expect(selectedLink?.className).toContain('bg-primary/5');
    expect(selectedLink?.className).toContain('md:bg-primary/5');
    expect(selectedLink?.className).toContain('border-transparent');
    expect(selectedLink?.className).toContain('shadow-none');
    expect(selectedLink?.className).toContain('ring-0');
    expect(selectedRow?.className).toContain('rounded-xl');
    expect(selectedRow?.className).toContain('border-primary/25');
    expect(selectedRow?.className).toContain('ring-1');
    expect(selectedRow?.className).toContain('ring-primary/10');
    expect(selectedRow?.className).toContain('shadow-sm');
    expect(selectedLink).toHaveAttribute('aria-current', 'page');
  });

  // -----------------------------------------------------------------------
  // Filter preservation in detail links
  // -----------------------------------------------------------------------

  it('preserves the user/status/repo filters in session detail links', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Linked session' }),
    ]);

    renderWithProviders(<SessionSidebar />, {
      searchParams: { people: 'all', status: 'active', repo: 'repo-1' },
    });
    await screen.findByText('Linked session');

    const link = screen.getByText('Linked session').closest('a');
    expect(link).toHaveAttribute(
      'href',
      '/sessions/s1?people=all&status=active&repo=repo-1',
    );
  });

  it('preserves search alongside the existing filters in session detail links', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Linked session' }),
    ]);

    renderWithProviders(<SessionSidebar />, {
      searchParams: { people: 'all', status: 'active', repo: 'repo-1', search: 'Linked' },
    });
    await screen.findByText('Linked session');

    const link = screen.getByText('Linked session').closest('a');
    expect(link).toHaveAttribute(
      'href',
      '/sessions/s1?people=all&status=active&repo=repo-1&search=Linked',
    );
  });

  it('uses the full row width for the session link and keeps the metadata pills horizontally scrollable', async () => {
    serveSessions([
      makeSession({
        id: 's1',
        result_summary: 'Overflow session',
        pm_plan_id: 'plan-123',
        triggered_by_user_id: undefined,
        linear_identifier_hint: 'ENG-1234',
        pr_summary: { status: 'merged', ci_status: '', number: 9, url: '#' },
        diff_stats: { added: 10, removed: 3, files_changed: 2 },
      }),
    ]);

    renderWithProviders(<SessionSidebar />, {
      searchParams: { people: 'all', status: 'active', repo: 'repo-1', search: 'Overflow' },
    });

    await screen.findByText('Overflow session');

    const sessionLink = screen.getByText('Overflow session').closest('a');
    expect(sessionLink).toHaveAttribute(
      'href',
      '/sessions/s1?people=all&status=active&repo=repo-1&search=Overflow',
    );

    const pillsScroller = screen.getByTestId('session-row-meta-scroll-s1');
    expect(pillsScroller.className).toContain('overflow-x-auto');
    expect(pillsScroller.className).toContain('scrollbar-hide');

    expect(screen.getByText('ENG-1234')).toBeInTheDocument();
    expect(screen.getByText('PM')).toBeInTheDocument();
    expect(screen.getByText('PR')).toBeInTheDocument();
    expect(screen.getByText('+10')).toBeInTheDocument();
  });

  it('preserves explicit people selections', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Member-scoped session' }),
    ]);

    renderWithProviders(<SessionSidebar />, {
      searchParams: { people: 'user-2,user-3' },
    });
    await screen.findByText('Member-scoped session');

    const link = screen.getByText('Member-scoped session').closest('a');
    expect(link).toHaveAttribute('href', '/sessions/s1?people=user-2%2Cuser-3');
  });

  it('shows in-flight PR creation and push statuses on session rows', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Opening a pull request', pr_creation_state: 'queued' }),
      makeSession({ id: 's2', result_summary: 'Updating an existing pull request', pr_push_state: 'pushing' }),
    ]);

    renderWithProviders(<SessionSidebar />);

    await screen.findByText('Opening a pull request');
    expect(screen.getByText('Creating PR')).toBeInTheDocument();
    expect(screen.getByText('Pushing changes')).toBeInTheDocument();
  });

  it('only serializes the filters that are actually set', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Status-only session' }),
    ]);

    renderWithProviders(<SessionSidebar />, {
      searchParams: { status: 'archived' },
    });
    await screen.findByText('Status-only session');

    const link = screen.getByText('Status-only session').closest('a');
    expect(link).toHaveAttribute('href', '/sessions/s1?status=archived');
  });

  it('omits the query suffix when no filters are active', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Plain session' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Plain session');

    const link = screen.getByText('Plain session').closest('a');
    expect(link).toHaveAttribute('href', '/sessions/s1');
  });

  it('preserves non-repo filters in the new session link', async () => {
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Linked session' }),
    ]);

    renderWithProviders(<SessionSidebar />, {
      searchParams: { people: 'all', status: 'active', repo: 'repo-1', search: 'Linked' },
    });
    const input = await screen.findByPlaceholderText('Search sessions...');
    expect(input).toHaveValue('Linked');

    expect(screen.getByRole('link', { name: 'New session' })).toHaveAttribute(
      'href',
      '/sessions/new?people=all&status=active&search=Linked',
    );
  });

  it('selects the new-session draft row without highlighting a saved session', async () => {
    mockPathname = '/sessions/new';
    mockSelectedSegment = 'new';
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Saved session below draft' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Saved session below draft');

    const listbox = screen.getByRole('listbox', { name: 'Sessions' });
    expect(listbox).toHaveAttribute(
      'aria-activedescendant',
      'session-sidebar-option-new-session',
    );

    const draftOption = screen.getByRole('option', { name: 'New session draft' });
    expect(draftOption).toHaveAttribute('aria-selected', 'true');
    expect(draftOption).toHaveClass('p-1');

    const savedOption = screen.getByText('Saved session below draft').closest('[role="option"]');
    expect(savedOption).toHaveAttribute('aria-selected', 'false');
    expect(savedOption?.className).not.toContain('ring-ring/20');
  });

  it('shows the open session as a contextual selected row when it is missing from the saved list', async () => {
    mockPathname = '/sessions/current-session';
    mockSelectedSegment = 'current-session';
    serveSessions([
      makeSession({ id: 's1', result_summary: 'First visible saved session' }),
      makeSession({ id: 's2', result_summary: 'Second visible saved session' }),
    ]);
    serveSessionDetail(makeSession({
      id: 'current-session',
      result_summary: 'Open session outside the list',
      status: 'running',
    }) as SessionDetail);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('First visible saved session');
    await screen.findByText('Open session outside the list');

    const listbox = screen.getByRole('listbox', { name: 'Sessions' });
    expect(listbox).toHaveAttribute(
      'aria-activedescendant',
      'session-sidebar-option-current-session',
    );

    const currentOption = screen.getByRole('option', { name: 'Open session outside the list' });
    expect(currentOption).toHaveAttribute('aria-selected', 'true');
    expect(currentOption).toHaveTextContent('Current');
    expect(currentOption).toHaveTextContent('Not in this list');

    const firstSavedOption = screen.getByText('First visible saved session').closest('[role="option"]');
    expect(firstSavedOption).toHaveAttribute('aria-selected', 'false');
    expect(firstSavedOption?.className).not.toContain('ring-ring/20');
  });

  it('does not render a contextual current-session row when the open session is already visible', async () => {
    mockPathname = '/sessions/current-session';
    mockSelectedSegment = 'current-session';
    serveSessions([
      makeSession({ id: 'current-session', result_summary: 'Visible current session' }),
      makeSession({ id: 's2', result_summary: 'Second visible saved session' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Visible current session');

    expect(screen.queryByText('Not in this list')).not.toBeInTheDocument();
    expect(screen.getAllByText('Visible current session')).toHaveLength(1);
    expect(screen.getByText('Visible current session').closest('[role="option"]')).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });

  it('pressing j from the context row moves focus to the first saved session, not the second', async () => {
    const user = userEvent.setup();
    mockPathname = '/sessions/current-session';
    mockSelectedSegment = 'current-session';
    serveSessions([
      makeSession({ id: 's1', result_summary: 'First saved session' }),
      makeSession({ id: 's2', result_summary: 'Second saved session' }),
    ]);
    serveSessionDetail(makeSession({
      id: 'current-session',
      result_summary: 'Open session outside the list',
      status: 'running',
    }) as SessionDetail);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Open session outside the list');
    await screen.findByText('First saved session');

    const listbox = screen.getByRole('listbox', { name: 'Sessions' });
    expect(listbox).toHaveAttribute('aria-activedescendant', 'session-sidebar-option-current-session');

    await user.keyboard('j');

    expect(listbox).toHaveAttribute('aria-activedescendant', 'session-sidebar-option-s1');
    expect(screen.getByText('First saved session').closest('[role="option"]')).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByText('Second saved session').closest('[role="option"]')).toHaveAttribute('aria-selected', 'false');
  });

  it('pressing k from the first saved session returns focus to the context row', async () => {
    const user = userEvent.setup();
    mockPathname = '/sessions/current-session';
    mockSelectedSegment = 'current-session';
    serveSessions([
      makeSession({ id: 's1', result_summary: 'First saved session' }),
      makeSession({ id: 's2', result_summary: 'Second saved session' }),
    ]);
    serveSessionDetail(makeSession({
      id: 'current-session',
      result_summary: 'Open session outside the list',
      status: 'running',
    }) as SessionDetail);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Open session outside the list');
    await screen.findByText('First saved session');

    await user.keyboard('j');

    const listbox = screen.getByRole('listbox', { name: 'Sessions' });
    expect(listbox).toHaveAttribute('aria-activedescendant', 'session-sidebar-option-s1');

    await user.keyboard('k');

    expect(listbox).toHaveAttribute('aria-activedescendant', 'session-sidebar-option-current-session');
    expect(screen.getByRole('option', { name: 'Open session outside the list' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByText('First saved session').closest('[role="option"]')).toHaveAttribute('aria-selected', 'false');
  });

  it('clears stale saved-session focus when navigating into the new-session draft', async () => {
    const user = userEvent.setup();
    serveSessions([
      makeSession({ id: 's1', result_summary: 'First saved session' }),
      makeSession({ id: 's2', result_summary: 'Second saved session' }),
    ]);

    const { rerender } = renderWithProviders(<SessionSidebar />);
    await screen.findByText('First saved session');

    await user.keyboard('j');
    expect(screen.getByText('Second saved session').closest('[role="option"]')).toHaveAttribute(
      'aria-selected',
      'true',
    );

    mockPathname = '/sessions/new';
    mockSelectedSegment = 'new';
    rerender(<SessionSidebar />);

    const listbox = screen.getByRole('listbox', { name: 'Sessions' });
    expect(listbox).toHaveAttribute(
      'aria-activedescendant',
      'session-sidebar-option-new-session',
    );
    expect(screen.getByRole('option', { name: 'New session draft' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(screen.getByText('Second saved session').closest('[role="option"]')).toHaveAttribute(
      'aria-selected',
      'false',
    );
  });

  it('removes draft selected styling after keyboard navigation moves to a saved session', async () => {
    const user = userEvent.setup();
    mockPathname = '/sessions/new';
    mockSelectedSegment = 'new';
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Keyboard-selected saved session' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Keyboard-selected saved session');

    await user.keyboard('j');

    const draftOption = screen.getByRole('option', { name: 'New session draft' });
    expect(draftOption).toHaveAttribute('aria-selected', 'false');
    expect(draftOption.querySelector('a')?.className).not.toContain('ring-primary/10');
    expect(screen.getByText('Keyboard-selected saved session').closest('[role="option"]')).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });

  it('defaults the people scope to Mine for session requests', async () => {
    const capturedPeople: string[] = [];
    server.use(
      http.get('*/api/v1/sessions', ({ request }) => {
        capturedPeople.push(new URL(request.url).searchParams.get('triggered_by_user_ids') ?? '');
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get('*/api/v1/sessions/counts', ({ request }) => {
        capturedPeople.push(new URL(request.url).searchParams.get('triggered_by_user_ids') ?? '');
        return HttpResponse.json({ data: { all: 0, active: 0, archived: 0, cap: 100 } });
      }),
    );

    renderWithProviders(<SessionSidebar />);
    await screen.findByRole('button', { name: /Mine/ });

    expect(capturedPeople).toContain('user-1');
  });

  it('supports roving keyboard navigation and opening the active session', async () => {
    const user = userEvent.setup();
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Alpha keyboard' }),
      makeSession({ id: 's2', result_summary: 'Beta keyboard' }),
      makeSession({ id: 's3', result_summary: 'Gamma keyboard' }),
      makeSession({ id: 's4', result_summary: 'Delta keyboard' }),
    ]);

    renderWithProviders(<SessionSidebar />);
    await screen.findByText('Alpha keyboard');

    await user.keyboard('j');
    const listbox = screen.getByRole('listbox', { name: 'Sessions' });
    expect(listbox).toBeInTheDocument();
    expect(listbox).toHaveAttribute('aria-activedescendant', 'session-sidebar-option-s2');
    expect(screen.getByText('Beta keyboard').closest('[role="option"]')).toHaveAttribute('aria-selected', 'true');

    // Pressing j once more — now that the list itself is focused — must
    // advance exactly one row. A single keystroke fires both the
    // React-delegated list onKeyDown and the document keydown listener; the
    // document handler has to bail when the event originated inside the list,
    // otherwise the cursor jumps two rows.
    await user.keyboard('j');
    expect(screen.getByText('Gamma keyboard').closest('[role="option"]')).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByText('Delta keyboard').closest('[role="option"]')).toHaveAttribute('aria-selected', 'false');

    await user.keyboard('{Enter}');
    expect(mockRouterPush).toHaveBeenCalledWith('/sessions/s3');
    expect(screen.queryByText('Opening')).not.toBeInTheDocument();
    expect(screen.getByText('Gamma keyboard').closest('a')).not.toHaveAttribute('aria-busy');
  });

  it('focuses search, starts a new session, and archives the active session by shortcut', async () => {
    const user = userEvent.setup();
    let archiveCalls = 0;
    serveSessions([
      makeSession({ id: 's1', result_summary: 'Archive by key' }),
    ]);
    server.use(
      http.post('/api/v1/sessions/s1/archive', () => {
        archiveCalls += 1;
        return HttpResponse.json({ status: 'archived' });
      }),
    );

    renderWithProviders(<SessionSidebar />, {
      searchParams: { repo: 'repo-1' },
    });
    await screen.findByText('Archive by key');

    await user.keyboard('/');
    expect(screen.getByPlaceholderText('Search sessions...')).toHaveFocus();

    await user.keyboard('{Escape}');
    await user.keyboard('n');
    expect(mockRouterPush).toHaveBeenCalledWith('/sessions/new');

    await user.keyboard('j');
    // Plain `a` is a no-op — archive requires Shift to avoid accidental fires.
    await user.keyboard('a');
    expect(archiveCalls).toBe(0);
    await user.keyboard('{Shift>}A{/Shift}');
    await waitFor(() => {
      expect(archiveCalls).toBe(1);
    });
  });

});
