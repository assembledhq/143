import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, userEvent, waitFor, within } from '@/test/test-utils';
import { QueryClient } from '@tanstack/react-query';
import { act } from '@testing-library/react';
import { server } from '@/test/mocks/server';
import { mockSessions, mockMembers, mockPR, mockPRHealth } from '@/test/mocks/handlers';
import { CHANGESET_SPLIT_MIN_ADDITIONS, SessionDetailContent } from './session-detail-content';
import { queryKeys } from '@/lib/query-keys';
import { markProvisionalSessionDetail } from '@/lib/session-detail-cache';
import { SESSION_THREAD_STRIP_HEIGHT_CLASSNAME } from './session-detail-geometry';
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
    const frame = screen.getByTestId('session-detail-loading-skeleton');
    expect(frame).toBeInTheDocument();
    expect(frame).toHaveAttribute('data-session-transition', 'initial');
    expect(frame).toHaveAttribute('data-session-state', 'loading');
    expect(screen.queryByText('Loading session...')).not.toBeInTheDocument();
  });

  it('reconciles the cold-open skeleton into the loaded workspace in place', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    // Nothing seeded this open, so the skeleton starts cold rather than
    // provisional — the frame still has to survive the swap.
    const frame = screen.getByTestId('session-detail-loading-skeleton');
    expect(frame).toHaveAttribute('data-session-transition', 'initial');

    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.getByTestId('session-detail-frame')).toBe(frame);
    expect(frame).not.toHaveAttribute('data-session-state');
    expect(frame).not.toHaveAttribute('aria-busy');
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
        changesets: [],
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
    const transitionFrame = screen.getByTestId('session-detail-loading-skeleton');
    expect(transitionFrame).toHaveAttribute('data-session-transition', 'provisional');
    expect(transitionFrame).toHaveAttribute('data-session-state', 'loading');
    expect(transitionFrame).toHaveAttribute('aria-busy', 'true');
    // Metadata-first paint: the provisional row's title shows in the skeleton
    // headers (desktop and mobile) immediately, while the data-bearing
    // queries still wait for the authoritative payload.
    expect(screen.getAllByText('Provisional list title').length).toBeGreaterThanOrEqual(1);
    expect(screen.getByTestId('session-composer-loading')).toBeInTheDocument();
    expect(screen.queryByPlaceholderText('Send a follow-up message...')).not.toBeInTheDocument();
    expect(timelineRequests).toBe(0);
    expect(prRequests).toBe(0);

    releaseDetail();

    expect(await screen.findAllByText('Authoritative detail title')).not.toHaveLength(0);
    // The shared SessionDetailFrame reconciles in place: loading content is
    // replaced locally without replacing the workspace root.
    expect(screen.getByTestId('session-detail-frame')).toBe(transitionFrame);
  });

  it('keeps target metadata and actions gated when provisional detail fails, then retries in place', async () => {
    const user = userEvent.setup();
    const sessionId = 'session-abcdef12-3456-7890';
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: Infinity } },
    });
    queryClient.setQueryData(queryKeys.sessions.detail(sessionId), {
      data: markProvisionalSessionDetail({
        ...mockSessions[0],
        result_summary: 'Selected target session',
        threads: [],
        changesets: [],
      }),
    } satisfies SingleResponse<Session>);
    let releaseRetry = () => {};
    const retryBlocked = new Promise<void>((resolve) => {
      releaseRetry = resolve;
    });
    let requests = 0;
    let prRequests = 0;
    server.use(
      http.get(`/api/v1/sessions/${sessionId}`, async () => {
        requests += 1;
        if (requests === 1) {
          return HttpResponse.json(
            { error: { code: 'DETAIL_UNAVAILABLE', message: 'temporarily unavailable' } },
            { status: 503 },
          );
        }
        await retryBlocked;
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            result_summary: 'Recovered target session',
            threads: [],
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get(`/api/v1/sessions/${sessionId}/pr`, () => {
        prRequests += 1;
        return HttpResponse.json({ data: null });
      }),
    );

    renderSessionDetailWithQueryClient(sessionId, queryClient);

    expect(await screen.findByTestId('session-detail-transition-error')).toBeInTheDocument();
    expect(screen.getAllByText('Selected target session').length).toBeGreaterThanOrEqual(1);
    expect(screen.queryByPlaceholderText('Send a follow-up message...')).not.toBeInTheDocument();
    // A failed detail request must not un-gate the queries that wait for an
    // authoritative session — we never loaded the session they describe.
    expect(prRequests).toBe(0);

    const transitionFrame = screen.getByTestId('session-detail-loading-skeleton');
    // Nothing is in flight until the user retries, so the frame must not
    // announce itself as busy over the alert.
    expect(transitionFrame).not.toHaveAttribute('aria-busy');
    expect(transitionFrame).toHaveAttribute('data-session-state', 'error');
    // A seeded row still backs this failure, so the frame reports what it
    // is preserving, not just that it failed.
    expect(transitionFrame).toHaveAttribute('data-session-transition', 'provisional');
    await user.click(screen.getByRole('button', { name: 'Retry' }));

    // The query holds its error until the refetch settles, so the error stays
    // on screen. Without pending feedback the button would read as dead.
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Retry' })).toBeDisabled();
    });
    expect(screen.getByTestId('session-detail-transition-error')).toBeInTheDocument();
    expect(transitionFrame).toHaveAttribute('aria-busy', 'true');

    releaseRetry();

    expect(await screen.findAllByText('Recovered target session')).not.toHaveLength(0);
    expect(screen.getByTestId('session-detail-frame')).toBe(transitionFrame);
    expect(requests).toBe(2);
  });

  it('reports a cold detail failure as an error rather than an endless skeleton', async () => {
    const sessionId = 'session-abcdef12-3456-7890';
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: Infinity } },
    });
    server.use(
      http.get(`/api/v1/sessions/${sessionId}`, () =>
        HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'no such session' } },
          { status: 404 },
        ),
      ),
    );

    renderSessionDetailWithQueryClient(sessionId, queryClient);

    const alert = await screen.findByTestId('session-detail-transition-error');
    // Nothing was seeded, so the copy must not claim a preserved selection and
    // the shimmer chrome must not imply that content is still on its way.
    expect(alert).toHaveTextContent('could not be found');
    expect(alert).not.toHaveTextContent('preserved');
    expect(screen.queryByTestId('session-thread-strip-loading')).not.toBeInTheDocument();
    expect(screen.queryByTestId('session-composer-loading')).not.toBeInTheDocument();
    const coldFrame = screen.getByTestId('session-detail-loading-skeleton');
    expect(coldFrame).not.toHaveAttribute('aria-busy');
    // Nothing was seeded, so the frame reports the cold shape of the failure.
    expect(coldFrame).toHaveAttribute('data-session-state', 'error');
    expect(coldFrame).toHaveAttribute('data-session-transition', 'initial');
  });

  it('keeps the frame mounted and removes stale content when switching from loaded A to provisional B', async () => {
    const firstSession = {
      ...mockSessions[0],
      result_summary: 'Loaded session A',
      threads: [],
      changesets: [],
    };
    const secondSession = {
      ...mockSessions[1],
      result_summary: 'Selected session B',
      threads: [],
      changesets: [],
    };
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: Infinity } },
    });
    queryClient.setQueryData(queryKeys.sessions.detail(firstSession.id), {
      data: firstSession,
    } satisfies SingleResponse<Session>);
    queryClient.setQueryData(queryKeys.sessions.detail(secondSession.id), {
      data: markProvisionalSessionDetail(secondSession),
    } satisfies SingleResponse<Session>);

    let releaseSecondDetail = () => {};
    const secondDetailBlocked = new Promise<void>((resolve) => {
      releaseSecondDetail = resolve;
    });
    let secondTimelineRequests = 0;
    server.use(
      http.get(`/api/v1/sessions/${firstSession.id}`, () =>
        HttpResponse.json({ data: firstSession } satisfies SingleResponse<Session>),
      ),
      http.get(`/api/v1/sessions/${secondSession.id}`, async () => {
        await secondDetailBlocked;
        return HttpResponse.json({ data: secondSession } satisfies SingleResponse<Session>);
      }),
      http.get(`/api/v1/sessions/${secondSession.id}/timeline`, () => {
        secondTimelineRequests += 1;
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    const view = renderWithProviders(<SessionDetailContent id={firstSession.id} />, { queryClient });
    expect(await screen.findAllByText('Loaded session A')).not.toHaveLength(0);
    const frame = screen.getByTestId('session-detail-frame');
    expect(screen.getByPlaceholderText('Send a follow-up message...')).toBeInTheDocument();

    view.rerender(<SessionDetailContent id={secondSession.id} />);

    expect(screen.getByTestId('session-detail-loading-skeleton')).toBe(frame);
    expect(screen.getAllByText('Selected session B').length).toBeGreaterThanOrEqual(1);
    expect(screen.queryByText('Loaded session A')).not.toBeInTheDocument();
    expect(screen.queryByPlaceholderText('Send a follow-up message...')).not.toBeInTheDocument();
    expect(secondTimelineRequests).toBe(0);

    releaseSecondDetail();
    expect(await screen.findAllByText('Selected session B')).not.toHaveLength(0);
    expect(screen.getByTestId('session-detail-frame')).toBe(frame);
    expect(screen.getByTestId('session-thread-strip-empty')).toHaveClass(
      SESSION_THREAD_STRIP_HEIGHT_CLASSNAME,
    );
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

    const repoBranchRow = screen.getByTestId('session-overview-repo-branch');
    expect(repoBranchRow).toHaveTextContent('assembledhq/143 · 143/feature-session-details');
    expect(repoBranchRow).toHaveAttribute('title', 'assembledhq/143 · 143/feature-session-details');
    expect(repoBranchRow).toHaveClass('truncate');
    expect(repoBranchRow).not.toHaveTextContent(/feature-session-details\s*·/);

    const metadataRow = screen.getByTestId('session-overview-timing');
    expect(metadataRow).not.toHaveTextContent('assembledhq/143');
    expect(metadataRow).toHaveTextContent(/Completed/);
  });

  it('keeps issue-trigger provenance compact without redundant workflow copy', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            origin: 'issue_trigger',
          },
        } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={mockSessions[0].id} />);

    const context = await screen.findByTestId('session-overview-context');
    expect(within(context).getByText('Issue')).toBeInTheDocument();
    expect(context).toContainElement(screen.getByTestId('session-overview-repo-branch'));
    expect(context).toContainElement(screen.getByTestId('session-overview-timing'));
    expect(screen.queryByText('Created from issue intake')).not.toBeInTheDocument();
    expect(screen.queryByText('Started automatically from issue workflow')).not.toBeInTheDocument();
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

  it('hides the Overview review action when there are no changes to review', async () => {
    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);

    await screen.findByText('Could not reproduce the error in test environment');
    expect(screen.queryByRole('button', { name: 'Review & fix' })).not.toBeInTheDocument();
    expect(screen.queryByText('Review before creating a PR')).not.toBeInTheDocument();
    expect(within(screen.getByLabelText('Session detail actions')).queryByRole('button', { name: 'Review' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Code review' })).not.toBeInTheDocument();
  });

  it('shows a disabled review action before the split suggestion when no snapshot is available', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            snapshot_key: undefined,
            sandbox_state: 'none',
            diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
            diff_stats: { added: CHANGESET_SPLIT_MIN_ADDITIONS, removed: 1, files_changed: 1 },
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

    const reviewButton = await screen.findByRole('button', { name: 'Review & fix' });
    expect(reviewButton).toBeDisabled();
    expect(screen.getByText('Review before creating a PR')).toBeInTheDocument();
    expect(screen.getByText('Check the current diff and apply any fixes before publishing.')).toBeInTheDocument();
    expect(reviewButton).toHaveAttribute('title', 'A reusable sandbox snapshot is required before review');
    const reviewAction = reviewButton.closest<HTMLElement>('[data-slot="overview-action-control"]');
    expect(reviewButton).not.toHaveClass('w-full');
    const reviewTitle = screen.getByText('Review before creating a PR');
    const splitTitle = screen.getByText('Large change · 750 additions · 1 file');
    const reviewSuggestion = reviewTitle.closest('[data-slot="overview-action"]');
    const splitSuggestion = splitTitle.closest('[data-slot="overview-suggestion"]');
    // The action sits in the same quiet row as the copy rather than in a card
    // of its own, so keep the structure asserted instead of the spacing utilities.
    expect(reviewSuggestion).toContainElement(reviewAction);
    expect(reviewSuggestion?.closest('[data-slot="card"]')).toBeNull();
    // The row carries no accessible name of its own, so it stays a plain group
    // rather than adding a landmark that just repeats the visible title.
    expect(screen.queryByRole('region', { name: 'Review before creating a PR' })).not.toBeInTheDocument();
    expect(reviewSuggestion?.querySelector('[data-slot="overview-action-icon"] .lucide-scan-search')).toBeInTheDocument();
    expect(splitSuggestion?.querySelector('[data-slot="overview-suggestion-icon"] .lucide-git-branch')).toBeInTheDocument();
    expect(splitTitle.closest('[data-slot="card"]')).toBeNull();
    expect(reviewTitle.compareDocumentPosition(splitTitle) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(within(screen.getByLabelText('Session detail actions')).queryByRole('button', { name: 'Review' })).not.toBeInTheDocument();
  });

  it('orders the card-free PR details and Result sections before the quiet split suggestion', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            diff_stats: { added: CHANGESET_SPLIT_MIN_ADDITIONS, removed: 1, files_changed: 1 },
          },
        } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={mockSessions[0].id} />);

    const prDetailsSection = await screen.findByRole('region', { name: 'Pull request #42' });
    const resultSection = screen.getByTestId('session-result-section');
    const splitSuggestion = screen.getByRole('region', { name: 'Pull request size suggestion' });

    expect(prDetailsSection?.parentElement?.firstElementChild).toBe(prDetailsSection);
    expect(prDetailsSection.closest('[data-slot="card"]')).toBeNull();
    expect(resultSection.closest('[data-slot="card"]')).toBeNull();
    expect(resultSection).toHaveClass('border-t', 'pt-4');
    expect(prDetailsSection.compareDocumentPosition(resultSection) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(resultSection.compareDocumentPosition(splitSuggestion) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('keeps the PR health placeholder card-free and still divides Result while health loads', async () => {
    server.use(
      http.get('/api/v1/pull-requests/:id/health', () => new Promise(() => {})),
    );

    renderWithProviders(<SessionDetailContent id={mockSessions[0].id} />);

    const loadingSection = (await screen.findByText('Loading PR health...')).closest('[data-slot="pr-health-loading-section"]');
    expect(loadingSection).toBeInTheDocument();
    expect(loadingSection?.closest('[data-slot="card"]')).toBeNull();
    expect(screen.getByTestId('session-result-section')).toHaveClass('border-t', 'pt-4');
  });

  it('renders Result without a leading divider when no pull request section precedes it', async () => {
    server.use(
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json({ data: null });
      }),
    );

    renderWithProviders(<SessionDetailContent id={mockSessions[0].id} />);

    const resultSection = await screen.findByTestId('session-result-section');

    expect(screen.queryByRole('region', { name: /^Pull request #/ })).not.toBeInTheDocument();
    expect(resultSection.closest('[data-slot="card"]')).toBeNull();
    // Asserted separately: `.not.toHaveClass(a, b)` only requires one of the
    // two classes to be absent, so a single call would pass on a half-divider.
    expect(resultSection).not.toHaveClass('border-t');
    expect(resultSection).not.toHaveClass('pt-4');
  });

  it('shows a quiet inline status while the Overview review loop is running', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[1],
            status: 'completed',
            snapshot_key: 'snapshot-running-review',
            sandbox_state: 'snapshotted',
            diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
            diff_stats: { added: 1, removed: 1, files_changed: 1 },
          },
        } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/review-loops', () => {
        return HttpResponse.json({
          data: [{
            id: 'review-loop-running',
            org_id: 'org-1',
            session_id: 'session-98765432-abcd-ef01',
            status: 'running',
            source: 'manual',
            agent_type: 'claude_code',
            max_passes: 2,
            fix_mode: 'minimal',
            completed_passes: 0,
            review_required: false,
            started_at: '2026-02-17T07:12:00Z',
          }] as SessionReviewLoop[],
          meta: {},
        } satisfies ListResponse<SessionReviewLoop>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);

    const reviewStatus = await screen.findByText('Fixing with Claude Code');
    const statusRow = reviewStatus.closest('[data-slot="overview-review-status"]');

    expect(statusRow).not.toBeNull();
    expect(statusRow).toHaveAttribute('role', 'status');
    expect(statusRow).toHaveAttribute('aria-live', 'polite');
    expect(statusRow).toHaveAttribute('aria-atomic', 'true');
    expect(statusRow?.closest('[data-slot="card"]')).toBeNull();
    expect(statusRow?.querySelector('[data-slot="overview-review-status-control"]')).toBeNull();
    expect(statusRow?.querySelector('[data-slot="overview-review-status-icon"] .lucide-loader-circle')).toBeInTheDocument();
    expect(within(statusRow as HTMLElement).getByText('The review loop is checking the changes and applying fixes.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Review & fix' })).not.toBeInTheDocument();
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

    expect(await screen.findByRole('region', { name: 'Pull request #42' })).toBeInTheDocument();
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

    await user.click(await screen.findByRole('button', { name: 'Review & fix' }));

    const dialog = screen.getByRole('dialog', { name: 'Review' });
    expect(within(dialog).getByRole('combobox', { name: 'Review coding agent' })).toBeInTheDocument();
    expect(dialog.querySelector('.rounded-lg.border')).not.toBeInTheDocument();
    expect(dialog.querySelector('.lucide-clipboard-list')).not.toBeInTheDocument();
  });

  it('starts a manual review loop with three passes and minimal fixes by default', async () => {
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

    await user.click(await screen.findByRole('button', { name: 'Review & fix' }));
    expect(screen.getByRole('spinbutton', { name: 'Review passes' })).toHaveValue(3);
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

    await user.click(await screen.findByRole('button', { name: 'Review & fix' }));
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

    await user.click(await screen.findByRole('button', { name: 'Review & fix' }));

    expect(screen.queryByText('2 is the standard pass')).not.toBeInTheDocument();

    await user.click(await screen.findByRole('combobox', { name: 'Review coding agent' }));
    await user.click(await screen.findByRole('option', { name: 'Claude Code' }));
    await user.click(screen.getByRole('button', { name: 'Start review' }));

    await waitFor(() => {
      expect(postedBody).toEqual({ agent_type: 'claude_code', max_passes: 3, fix_mode: 'minimal' });
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

    await user.click(await screen.findByRole('button', { name: 'Review & fix' }));
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
    await user.click(within(detailSheet).getByRole('button', { name: 'Review & fix' }));
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
      expect(screen.queryByRole('button', { name: 'Review & fix' })).not.toBeInTheDocument();
    });
  });

  it('renders the session header title at the context-title size', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const headerTitle = await screen.findByRole('heading', {
      level: 1,
      name: 'Fixed TypeError by adding null check',
    });

    expect(headerTitle.className).toContain('text-base');
    expect(headerTitle.className).toContain('font-display');
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
            execution_brief: 'Quick null check fix',
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

  it('keeps the legacy full-page layout when the session has one pull request slot', async () => {
    const primaryChangesetID = '11111111-1111-4111-8111-111111111111';
    server.use(http.get('/api/v1/sessions/:id', () => HttpResponse.json({
      data: {
        ...mockSessions[0],
        changesets: [{
          id: primaryChangesetID, is_primary: true, order_index: 0, title: 'Foundation', summary: '',
          status: 'planned', target_branch: 'main', base_branch: 'main',
          created_at: '2026-07-11T00:00:00Z', updated_at: '2026-07-11T00:00:00Z',
        }],
      },
    })));

    renderWithProviders(<SessionDetailContent id={mockSessions[0].id} />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.queryByTestId('pull-request-list')).not.toBeInTheDocument();
    expect(screen.queryByTestId('selected-pull-request-panel')).not.toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Overview' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Changes' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Preview' })).toBeInTheDocument();
  });

  it('scopes the full PR details surface when a different pull request is selected', async () => {
    const primaryChangesetID = '11111111-1111-4111-8111-111111111111';
    const childChangesetID = '22222222-2222-4222-8222-222222222222';
    const primaryPR = { ...mockPR, id: 'pr-primary', changeset_id: primaryChangesetID, github_pr_number: 101, title: 'Foundation' };
    const childPR = {
      ...mockPR,
      id: 'pr-child',
      changeset_id: childChangesetID,
      github_pr_number: 102,
      github_pr_url: 'https://github.com/org/repo/pull/102',
      title: 'API integration',
      branch_name: '143/api',
      head_ref: '143/api',
      ci_status: 'failure' as const,
      review_status: 'changes_requested' as const,
    };
    const multiPRSession = {
      ...mockSessions[0],
      changesets: [
        {
          id: primaryChangesetID, is_primary: true, order_index: 0, title: 'Foundation', summary: 'Shared types',
          status: 'pr_open', target_branch: 'main', base_branch: 'main', working_branch: '143/foundation',
          pull_request: primaryPR, created_at: '2026-07-11T00:00:00Z', updated_at: '2026-07-11T00:00:00Z',
        },
        {
          id: childChangesetID, is_primary: false, order_index: 1, title: 'API integration', summary: 'Connect the API',
          status: 'pr_open', target_branch: 'main', base_branch: '143/foundation', working_branch: '143/api',
          stacked_on_changeset_id: primaryChangesetID, pull_request: childPR,
          created_at: '2026-07-11T00:00:00Z', updated_at: '2026-07-11T00:00:00Z',
        },
      ],
    };
    const requestedChangesets: string[] = [];
    const requestedHealthPRs: string[] = [];
    const requestedRepairPRs: string[] = [];
    server.use(
      http.get('/api/v1/sessions/:id', () => HttpResponse.json({ data: multiPRSession })),
      http.get('/api/v1/sessions/:id/pr', ({ request }) => {
        const selected = new URL(request.url).searchParams.get('changeset_id') ?? primaryChangesetID;
        requestedChangesets.push(selected);
        return HttpResponse.json({ data: selected === childChangesetID ? childPR : primaryPR });
      }),
      http.get('/api/v1/pull-requests/:id/health', ({ params }) => {
        requestedHealthPRs.push(String(params.id));
        return HttpResponse.json({
          data: params.id === childPR.id
            ? { ...mockPRHealth, pull_request_id: childPR.id, failing_test_count: 1, can_fix_tests: true, needs_agent_action: true }
            : { ...mockPRHealth, pull_request_id: primaryPR.id },
        });
      }),
      http.post('/api/v1/pull-requests/:id/repair/fix-tests', ({ params }) => {
        requestedRepairPRs.push(String(params.id));
        return HttpResponse.json({
          data: {
            session_id: multiPRSession.id,
            mode: 'resumed',
            reused_in_flight: false,
            head_sha: 'head-sha',
            base_sha: 'base-sha',
            health_version: 1,
            repair_action_type: 'fix_tests',
          },
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={multiPRSession.id} />);
    expect(await screen.findByTestId('pull-request-list')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /API integration/ }));

    await waitFor(() => {
      expect(within(screen.getByTestId('selected-pull-request-panel')).getByText('Connect the API')).toBeInTheDocument();
    });
    const selectedPanel = screen.getByTestId('selected-pull-request-panel');
    expect(within(selectedPanel).getByText('143/foundation')).toBeInTheDocument();
    expect(within(selectedPanel).getByText('changes requested')).toBeInTheDocument();
    expect(screen.getByTestId('branch-actions-unavailable')).toBeInTheDocument();
    await waitFor(() => expect(requestedChangesets).toContain(childChangesetID));
    await waitFor(() => expect(requestedHealthPRs).toContain(childPR.id));
    expect(screen.getByRole('link', { name: 'View PR' })).toHaveAttribute('href', childPR.github_pr_url);
    await user.click(await screen.findByRole('button', { name: 'Fix tests' }));
    await waitFor(() => expect(requestedRepairPRs).toEqual([childPR.id]));

    act(() => {
      window.history.replaceState({}, '', window.location.pathname);
      window.dispatchEvent(new PopStateEvent('popstate'));
    });
    await waitFor(() => {
      expect(within(screen.getByTestId('selected-pull-request-panel')).getByText('Shared types')).toBeInTheDocument();
    });
  }, 10_000);

  it('keeps every branch-backed action on the materialization boundary for a planned pull request slot', async () => {
    const primaryChangesetID = '33333333-3333-4333-8333-333333333333';
    const plannedChangesetID = '44444444-4444-4444-8444-444444444444';
    const session = {
      ...mockSessions[0],
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      changesets: [
        {
          id: primaryChangesetID, is_primary: true, order_index: 0, title: 'Foundation', summary: '',
          status: 'planned', target_branch: 'main', base_branch: 'main',
          created_at: '2026-07-11T00:00:00Z', updated_at: '2026-07-11T00:00:00Z',
        },
        {
          id: plannedChangesetID, is_primary: false, order_index: 1, title: 'API integration', summary: 'Not materialized yet',
          status: 'planned', target_branch: 'main', base_branch: 'main',
          created_at: '2026-07-11T00:00:00Z', updated_at: '2026-07-11T00:00:00Z',
        },
      ],
    };
    server.use(
      http.get('/api/v1/sessions/:id', () => HttpResponse.json({ data: session })),
      http.get('/api/v1/sessions/:id/pr', () => HttpResponse.json({ data: null })),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={session.id} />);
    await user.click(await screen.findByRole('button', { name: /API integration/ }));

    const selectedPanel = screen.getByTestId('selected-pull-request-panel');
    expect(within(selectedPanel).getByRole('button', { name: 'Create PR' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Review' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Push changes' })).not.toBeInTheDocument();

    await user.click(screen.getByTitle('View changes'));
    expect(await screen.findByText('Changes for this pull request will be available after its branch is materialized.')).toBeInTheDocument();
    expect(new URL(window.location.href).searchParams.get('review')).toBeNull();

    await user.click(screen.getByRole('tab', { name: 'Preview' }));
    expect(await screen.findByText('Preview for this pull request will be available after its branch is materialized.')).toBeInTheDocument();
  }, 10_000);
});
