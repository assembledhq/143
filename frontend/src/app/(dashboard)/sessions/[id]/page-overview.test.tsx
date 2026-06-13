import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, userEvent, waitFor, within } from '@/test/test-utils';
import { QueryClient } from '@tanstack/react-query';
import { server } from '@/test/mocks/server';
import { mockSessions, mockMembers } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import { queryKeys } from '@/lib/query-keys';
import { markProvisionalSessionDetail } from '@/lib/session-detail-cache';
import type {
  ReviewLoopFixMode,
  Session,
  SessionReviewLoop,
  SessionThread,
  User,
  SingleResponse,
  ListResponse,
} from '@/lib/types';
import { installSessionDetailPageTestHooks, changeFieldValue, renderSessionDetailWithQueryClient } from './session-detail-test-kit';

const { toast } = vi.hoisted(() => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));
const { routerPush } = vi.hoisted(() => ({
  routerPush: vi.fn(),
}));

vi.mock('@/lib/notify', () => ({
  notify: toast,
}));

vi.mock('@/components/markdown', () => ({
  MarkdownContent: ({ content, className }: { content: string; className?: string }) => (
    <div className={className}>{content}</div>
  ),
}));

vi.mock('@/components/session-keyboard-help-overlay', () => ({
  SessionKeyboardHelpOverlay: ({ open }: { open: boolean }) => (
    open ? <div role="dialog" aria-label="Session keyboard shortcuts" /> : null
  ),
}));

vi.mock('next/navigation', () => ({
  useRouter: () => ({
    push: routerPush,
  }),
  useSearchParams: () => new URLSearchParams(),
}));

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

// Mock next/image to render a plain img
vi.mock('next/image', () => ({
  default: ({ src, alt, className, width, height }: { src: string; alt: string; className?: string; width?: number; height?: number }) => (
    <span data-next-image={src} aria-label={alt} className={className} data-width={width} data-height={height} />
  ),
}));

installSessionDetailPageTestHooks({ toast, routerPush });

describe('SessionDetailPage overview and review loop', () => {
  it('shows the session details skeleton initially', () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    expect(screen.getByTestId('session-detail-loading-skeleton')).toBeInTheDocument();
    expect(screen.queryByText('Loading session...')).not.toBeInTheDocument();
  });

  it('refetches authoritative detail immediately when provisional detail is cached as fresh', async () => {
    const sessionId = 'session-abcdef12-3456-7890';
    const queryClient = new QueryClient({
      defaultOptions: {
        queries: {
          retry: false,
          staleTime: 30_000,
          gcTime: Infinity,
        },
      },
    });
    let releaseDetail = () => {};
    const detailBlocked = new Promise<void>((resolve) => {
      releaseDetail = resolve;
    });
    queryClient.setQueryData(queryKeys.sessions.detail(sessionId), {
      data: markProvisionalSessionDetail({
        ...mockSessions[0],
        result_summary: 'Provisional list title',
        threads: [],
      }),
    } satisfies SingleResponse<Session>);
    let detailRequests = 0;
    let timelineRequests = 0;
    let prRequests = 0;
    server.use(
      http.get(`/api/v1/sessions/${sessionId}`, async () => {
        detailRequests += 1;
        await detailBlocked;
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            result_summary: 'Authoritative detail title',
            threads: [],
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get(`/api/v1/sessions/${sessionId}/timeline`, () => {
        timelineRequests += 1;
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get(`/api/v1/sessions/${sessionId}/pr`, () => {
        prRequests += 1;
        return HttpResponse.json({ data: null });
      }),
    );

    renderSessionDetailWithQueryClient(sessionId, queryClient);

    await waitFor(() => {
      expect(detailRequests).toBe(1);
    });
    expect(screen.getByTestId('session-detail-loading-skeleton')).toBeInTheDocument();
    // Metadata-first paint: the provisional row's title shows in the skeleton
    // headers (desktop and mobile) immediately, while the data-bearing
    // queries still wait for the authoritative payload.
    expect(screen.getAllByText('Provisional list title').length).toBeGreaterThanOrEqual(1);
    expect(timelineRequests).toBe(0);
    expect(prRequests).toBe(0);

    releaseDetail();

    expect(await screen.findAllByText('Authoritative detail title')).not.toHaveLength(0);
  });

  it('renders session with result summary as title', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const elements = await screen.findAllByText('Fixed TypeError by adding null check');
    expect(elements.length).toBeGreaterThanOrEqual(1);
  });

  it('protects the conversation workspace from collapsing on compact desktop widths', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.getByTestId('session-conversation-workspace')).toHaveClass('md:min-w-[440px]');
  });

  it('updates the browser tab title with the session title', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await waitFor(() => {
      expect(document.title).toBe('143 | Fixed TypeError by adding null check');
    });
  });

  it('shows agent type label', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getAllByText(/Claude Code/).length).toBeGreaterThanOrEqual(1);
  });

  it('shows the current repository and branch in the overview details', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.getByText('assembledhq/143 · 143/feature-session-details')).toBeInTheDocument();
  });

  it('renders the session Linear chip as an outbound link when only linear_identifier_hint is available', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            linked_issues: [],
            linear_identifier_hint: 'ENG-1234',
          },
        } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const link = await screen.findByRole('link', { name: 'ENG-1234' });
    expect(link).toHaveAttribute('href', 'https://linear.app/issue/ENG-1234');
    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
  });

  it('hides the Overview review readiness action when there are no changes to review', async () => {
    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);

    await screen.findByText('Could not reproduce the error in test environment');
    expect(screen.queryByRole('button', { name: 'Review' })).not.toBeInTheDocument();
    expect(screen.queryByText('Review and fix with a selected agent before creating a PR.')).not.toBeInTheDocument();
    expect(within(screen.getByLabelText('Session detail actions')).queryByRole('button', { name: 'Review' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Code review' })).not.toBeInTheDocument();
  });

  it('shows a disabled review action in the Overview readiness area when changes exist but no snapshot is available', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            snapshot_key: undefined,
            sandbox_state: 'none',
            diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
            diff_stats: { added: 1, removed: 1, files_changed: 1 },
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findByRole('button', { name: 'Review' })).toBeDisabled();
    expect(screen.getByText('Review and fix with a selected agent before creating a PR.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Review' })).toHaveAttribute('title', 'A reusable sandbox snapshot is required before review');
    expect(within(screen.getByLabelText('Session detail actions')).queryByRole('button', { name: 'Review' })).not.toBeInTheDocument();
  });

  it('moves the review action into PR health after a PR exists when a snapshot is available', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            snapshot_key: 'snapshot-post-pr-review',
            sandbox_state: 'snapshotted',
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/review-loops', () => {
        return HttpResponse.json({
          data: [] as SessionReviewLoop[],
          meta: {},
        } satisfies ListResponse<SessionReviewLoop>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findByText('PR health')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Review' })).toBeInTheDocument();
    expect(screen.queryByText('Review work')).not.toBeInTheDocument();
    expect(screen.queryByText('Review this work')).not.toBeInTheDocument();
    expect(within(screen.getByLabelText('Session detail actions')).queryByRole('button', { name: 'Review' })).not.toBeInTheDocument();
  });

  it('renders the review setup agent selector without a nested panel or clipboard icon', async () => {
    const user = userEvent.setup();

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[1],
            status: 'completed',
            snapshot_key: 'snapshot-manual-review',
            sandbox_state: 'snapshotted',
            diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
            diff_stats: { added: 1, removed: 1, files_changed: 1 },
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/review-loops', () => {
        return HttpResponse.json({
          data: [] as SessionReviewLoop[],
          meta: {},
        } satisfies ListResponse<SessionReviewLoop>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);

    await user.click(await screen.findByRole('button', { name: 'Review' }));

    const dialog = screen.getByRole('dialog', { name: 'Review' });
    expect(within(dialog).getByRole('combobox', { name: 'Review coding agent' })).toBeInTheDocument();
    expect(dialog.querySelector('.rounded-lg.border')).not.toBeInTheDocument();
    expect(dialog.querySelector('.lucide-clipboard-list')).not.toBeInTheDocument();
  });

  it('starts a manual review loop with the selected pass count and default minimal fix mode', async () => {
    const user = userEvent.setup();
    let postedBody: { max_passes: number; fix_mode?: ReviewLoopFixMode } | null = null;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[1],
            status: 'completed',
            snapshot_key: 'snapshot-manual-review',
            sandbox_state: 'snapshotted',
            diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
            diff_stats: { added: 1, removed: 1, files_changed: 1 },
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/review-loops', () => {
        return HttpResponse.json({
          data: [] as SessionReviewLoop[],
          meta: {},
        } satisfies ListResponse<SessionReviewLoop>);
      }),
      http.post('/api/v1/sessions/:id/review-loops', async ({ request, params }) => {
        const body = await request.json() as { max_passes: number; fix_mode?: ReviewLoopFixMode };
        postedBody = body;
        return HttpResponse.json({
          data: {
            id: 'review-loop-selected-passes',
            org_id: 'org-1',
            session_id: params.id as string,
            status: 'running',
            source: 'manual',
            agent_type: 'codex',
            max_passes: body.max_passes,
            fix_mode: body.fix_mode ?? 'minimal',
            completed_passes: 0,
            review_required: false,
            started_at: '2026-02-17T07:12:00Z',
          },
        } satisfies SingleResponse<SessionReviewLoop>, { status: 201 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);

    await user.click(await screen.findByRole('button', { name: 'Review' }));
    await user.click(await screen.findByRole('button', { name: 'Increase review passes' }));
    await user.click(screen.getByRole('button', { name: 'Start review' }));

    await waitFor(() => {
      expect(postedBody).toMatchObject({ max_passes: 3, fix_mode: 'minimal' });
    });
  });

  it('starts a manual review loop in exhaustive fix mode when selected', async () => {
    const user = userEvent.setup();
    let postedBody: { fix_mode?: ReviewLoopFixMode } | null = null;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[1],
            status: 'completed',
            snapshot_key: 'snapshot-manual-review',
            sandbox_state: 'snapshotted',
            diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
            diff_stats: { added: 1, removed: 1, files_changed: 1 },
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/review-loops', () => {
        return HttpResponse.json({
          data: [] as SessionReviewLoop[],
          meta: {},
        } satisfies ListResponse<SessionReviewLoop>);
      }),
      http.post('/api/v1/sessions/:id/review-loops', async ({ request, params }) => {
        postedBody = await request.json() as { fix_mode?: ReviewLoopFixMode };
        return HttpResponse.json({
          data: {
            id: 'review-loop-exhaustive',
            org_id: 'org-1',
            session_id: params.id as string,
            status: 'running',
            source: 'manual',
            agent_type: 'codex',
            max_passes: 2,
            fix_mode: postedBody.fix_mode ?? 'minimal',
            completed_passes: 0,
            review_required: false,
            started_at: '2026-02-17T07:12:00Z',
          },
        } satisfies SingleResponse<SessionReviewLoop>, { status: 201 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);

    await user.click(await screen.findByRole('button', { name: 'Review' }));
    const dialog = screen.getByRole('dialog', { name: 'Review' });
    expect(within(dialog).getByRole('radio', { name: 'Minimal fixes' })).toBeChecked();

    await user.click(within(dialog).getByRole('radio', { name: 'Fix every finding' }));
    await user.click(screen.getByRole('button', { name: 'Start review' }));

    await waitFor(() => {
      expect(postedBody).toMatchObject({ fix_mode: 'exhaustive' });
    });
  });

  it('lets the review loop use a coding agent different from the main session agent', async () => {
    const user = userEvent.setup();
    let postedBody: { agent_type?: string; max_passes: number; fix_mode?: ReviewLoopFixMode } | null = null;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[1],
            status: 'completed',
            agent_type: 'codex',
            snapshot_key: 'snapshot-manual-review',
            sandbox_state: 'snapshotted',
            diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
            diff_stats: { added: 1, removed: 1, files_changed: 1 },
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/review-loops', () => {
        return HttpResponse.json({
          data: [] as SessionReviewLoop[],
          meta: {},
        } satisfies ListResponse<SessionReviewLoop>);
      }),
      http.post('/api/v1/sessions/:id/review-loops', async ({ request, params }) => {
        postedBody = await request.json() as { agent_type?: string; max_passes: number; fix_mode?: ReviewLoopFixMode };
        return HttpResponse.json({
          data: {
            id: 'review-loop-selected-agent',
            org_id: 'org-1',
            session_id: params.id as string,
            status: 'running',
            source: 'manual',
            agent_type: postedBody.agent_type ?? 'codex',
            max_passes: postedBody.max_passes,
            fix_mode: postedBody.fix_mode ?? 'minimal',
            completed_passes: 0,
            review_required: false,
            started_at: '2026-02-17T07:12:00Z',
          },
        } satisfies SingleResponse<SessionReviewLoop>, { status: 201 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);

    await user.click(await screen.findByRole('button', { name: 'Review' }));

    expect(screen.queryByText('2 is the standard pass')).not.toBeInTheDocument();

    await user.click(await screen.findByRole('combobox', { name: 'Review coding agent' }));
    await user.click(await screen.findByRole('option', { name: 'Claude Code' }));
    await user.click(screen.getByRole('button', { name: 'Start review' }));

    await waitFor(() => {
      expect(postedBody).toEqual({ agent_type: 'claude_code', max_passes: 2, fix_mode: 'minimal' });
    });
  });

  it('opens the review loop in its returned agent tab', async () => {
    const user = userEvent.setup();
    const existingThread: SessionThread = {
      id: 'thread-main',
      session_id: 'session-98765432-abcd-ef01',
      org_id: 'org-1',
      agent_type: 'codex',
      label: 'Codex 1',
      status: 'completed',
      current_turn: 1,
      created_at: '2026-02-17T07:00:00Z',
      cost_cents: 0,
      pending_message_count: 0,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[1],
            status: 'completed',
            snapshot_key: 'snapshot-manual-review',
            sandbox_state: 'snapshotted',
            diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
            diff_stats: { added: 1, removed: 1, files_changed: 1 },
            threads: [existingThread],
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/review-loops', () => {
        return HttpResponse.json({
          data: [] as SessionReviewLoop[],
          meta: {},
        } satisfies ListResponse<SessionReviewLoop>);
      }),
      http.post('/api/v1/sessions/:id/review-loops', async ({ request, params }) => {
        const body = await request.json() as { max_passes: number };
        return HttpResponse.json({
          data: {
            id: 'review-loop-new-thread',
            org_id: 'org-1',
            session_id: params.id as string,
            thread_id: 'thread-review',
            status: 'running',
            source: 'manual',
            agent_type: 'codex',
            max_passes: body.max_passes,
            fix_mode: 'minimal',
            completed_passes: 0,
            review_required: false,
            started_at: '2026-02-17T07:12:00Z',
          },
        } satisfies SingleResponse<SessionReviewLoop>, { status: 201 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);

    expect(await screen.findByText('Codex 1')).toBeInTheDocument();

    await user.click(await screen.findByRole('button', { name: 'Review' }));
    await user.click(screen.getByRole('button', { name: 'Start review' }));

    const reviewTab = await screen.findByRole('tab', { name: /Codex Review/ });
    expect(reviewTab).toHaveAttribute('aria-selected', 'true');
  });

  it('starts a manual review loop from the mobile Overview sheet without relying on a popover', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));
    const user = userEvent.setup();
    let postCount = 0;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[1],
            status: 'completed',
            snapshot_key: 'snapshot-mobile-review',
            sandbox_state: 'snapshotted',
            diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
            diff_stats: { added: 1, removed: 1, files_changed: 1 },
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/review-loops', () => {
        return HttpResponse.json({
          data: [] as SessionReviewLoop[],
          meta: {},
        } satisfies ListResponse<SessionReviewLoop>);
      }),
      http.post('/api/v1/sessions/:id/review-loops', async ({ request, params }) => {
        postCount += 1;
        const body = await request.json() as { max_passes: number };
        return HttpResponse.json({
          data: {
            id: 'review-loop-mobile',
            org_id: 'org-1',
            session_id: params.id as string,
            status: 'running',
            source: 'manual',
            agent_type: 'codex',
            max_passes: body.max_passes,
            fix_mode: 'minimal',
            completed_passes: 0,
            review_required: false,
            started_at: '2026-02-17T07:12:00Z',
          },
        } satisfies SingleResponse<SessionReviewLoop>, { status: 201 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);

    await user.click(await screen.findByRole('button', { name: 'Open session details' }));
    const detailSheet = await screen.findByRole('dialog', { name: 'Session details' });
    await user.click(within(detailSheet).getByRole('button', { name: 'Review' }));
    await user.click(await screen.findByRole('button', { name: 'Start review' }));

    await waitFor(() => {
      expect(postCount).toBe(1);
    });
  });

  it('does not show a dedicated self-review button for viewers', async () => {
    server.use(
      http.get('/api/v1/auth/me', () => {
        return HttpResponse.json({
          data: {
            ...mockMembers[0],
            role: 'viewer',
          },
        } satisfies SingleResponse<User>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    await waitFor(() => {
      expect(screen.queryByRole('button', { name: 'Review' })).not.toBeInTheDocument();
    });
  });

  it('renders the session header title at text-sm size', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const headerTitle = await screen.findByRole('heading', {
      level: 1,
      name: 'Fixed TypeError by adding null check',
    });

    expect(headerTitle.className).toContain('text-sm');
    expect(headerTitle.className).not.toContain('text-xs');
  });

  it('lets the user edit the session title inline', async () => {
    const updatedTitle = 'Renamed session title';
    let currentTitle = 'Original editable title';
    const user = userEvent.setup();

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            title: currentTitle,
            result_summary: undefined,
          },
        } satisfies SingleResponse<Session>);
      }),
      http.patch('/api/v1/sessions/:id', async ({ request }) => {
        const body = await request.json() as { title: string };
        currentTitle = body.title;
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            title: currentTitle,
            result_summary: undefined,
          },
        } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findByRole('heading', { level: 1, name: currentTitle });
    await user.click(screen.getByRole('button', { name: 'Edit session title' }));

    const input = screen.getByDisplayValue(currentTitle);
    changeFieldValue(input, updatedTitle);
    await user.click(screen.getByRole('button', { name: 'Save title' }));

    await waitFor(() => {
      expect(screen.getByRole('heading', { level: 1, name: updatedTitle })).toBeInTheDocument();
    });
  }, 20000);

  it('shows a hover tooltip when Save title is disabled', async () => {
    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findByRole('heading', { level: 1, name: 'Fixed TypeError by adding null check' });
    await user.click(screen.getByRole('button', { name: 'Edit session title' }));

    const saveButton = screen.getByRole('button', { name: 'Save title' });
    expect(saveButton).toBeDisabled();

    await user.hover(saveButton.parentElement as HTMLElement);

    expect(await screen.findByRole('tooltip', { name: 'Enter a different title to save your changes.' })).toBeInTheDocument();
  });

  it('seeds the title editor from the same title shown in the header', async () => {
    const user = userEvent.setup();
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            title: undefined,
            pm_approach: 'Quick null check fix',
            result_summary: 'Fixed TypeError by adding null check',
          },
        } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findByRole('heading', { level: 1, name: 'Quick null check fix' });
    await user.click(screen.getByRole('button', { name: 'Edit session title' }));

    expect(screen.getByDisplayValue('Quick null check fix')).toBeInTheDocument();
  });

  it('shows overview tab with status in detail panel', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getAllByText('Completed').length).toBeGreaterThanOrEqual(1);
  });

  it('renders the desktop detail panel as an opaque surface above neighboring content', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const detailPanel = screen.getByTestId('session-detail-panel');

    expect(detailPanel).toHaveClass('relative');
    expect(detailPanel).toHaveClass('z-10');
    expect(detailPanel).toHaveClass('bg-background');
  });

  it('shows detail panel tabs for Overview and Changes', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByRole('tab', { name: 'Overview' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Changes' })).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: 'Validation' })).not.toBeInTheDocument();
  });

  it('uses the same desktop header border-box height for the conversation and detail panels', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.getByTestId('session-main-header')).toHaveClass('h-14');
    expect(screen.getByTestId('session-detail-header')).toHaveClass('h-14');
    expect(screen.getByTestId('session-detail-header-bar')).toHaveClass('h-14');
  });

  it('clips crowded session header metadata before it can overlap the detail toggle', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.getByTestId('session-header-summary')).toHaveClass('overflow-hidden');
    expect(screen.getByTestId('session-header-actions')).toHaveClass('shrink-0');
  });

  it('uses a dedicated mobile close button that does not compete with PR actions', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findAllByText('Fixed TypeError by adding null check');
    await user.click(screen.getByRole('button', { name: 'Open session details' }));

    // panelTabsEl is rendered both inline (desktop) and inside the Sheet
    // (mobile), so we scope to the dialog Radix opens for the sheet to
    // assert on the mobile-visible instance specifically.
    const sheet = await screen.findByRole('dialog');
    const closeBtn = within(sheet).getByRole('button', { name: 'Close details' });
    expect(closeBtn).toBeInTheDocument();
    const viewPRLink = within(sheet).getByRole('link', { name: 'View PR' });
    expect(viewPRLink).toBeInTheDocument();
    expect(viewPRLink.className).not.toContain('w-full');
    expect(within(sheet).queryByRole('button', { name: 'Close' })).not.toBeInTheDocument();

    await user.click(closeBtn);

    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    });
  });
});
